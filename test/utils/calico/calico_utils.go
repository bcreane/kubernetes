/*
Copyright (c) 2017 Tigera, Inc. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// This package makes public methods out of some of the utility methods for testing calico found at test/e2e/essentials.go
// Eventually these utilities should replace those and be used for any calico tests
package calico

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"golang.org/x/crypto/ssh"
	"k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labelutils "k8s.io/apimachinery/pkg/labels"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/generated"
	imageutils "k8s.io/kubernetes/test/utils/image"
	"k8s.io/kubernetes/test/utils/winctl"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

const (
	cmdTestPodName        = "cmd-test-container-pod"
	calicoctlManifestPath = "test/e2e/testing-manifests/calicoctl"
)

var (
	felixConfigNeeded       = true
	DatastoreType           = ""
	zeroGracePeriod   int64 = 0
	deleteImmediately       = &metav1.DeleteOptions{GracePeriodSeconds: &zeroGracePeriod}
	serviceCmd              = "/bin/sh -c 'while /bin/true; do echo foo | nc -l %d; done'"
	serverPort1             = 80
)

func ReadTestFileOrDie(file string, config ...interface{}) string {
	v := string(generated.ReadOrDie(path.Join(calicoctlManifestPath, file)))
	if len(config) == 1 {
		// A config object has been supplied. We can use this to substitute configuration into the file using
		// go templates.
		tmpl, err := template.New("temp").Parse(v)
		Expect(err).NotTo(HaveOccurred())
		substituted := new(bytes.Buffer)
		err = tmpl.Execute(substituted, config[0])
		Expect(err).NotTo(HaveOccurred())
		v = substituted.String()
	}
	return v
}

func CountSyslogLines(f *framework.Framework, node *v1.Node) int64 {
	pod, err := CreateLoggingPod(f, node)
	defer func() {
		By("Cleaning up the logging pod serving number of log lines.")
		if err := f.ClientSet.CoreV1().Pods(pod.Namespace).Delete(pod.Name, nil); err != nil {
			framework.Failf("unable to cleanup pod %v: %v", pod.Name, err)
		}
		/*
			// TODO: Commented this out because logging pods do not terminate quickly enough for this to pass
			err := framework.WaitForPodToDisappear(f.ClientSet, f.Namespace.Name, pod.Name, labelutils.Everything(), time.Second, wait.ForeverTestTimeout)
			if err != nil {
				framework.Failf("Failed to delete %s pod: %v", pod.Name, err)
			}
		*/
	}()
	framework.ExpectNoError(err)

	By("Counting the log lines from the logging pod")
	cmd := "journalctl --system | wc -l"
	output, err := framework.RunHostCmd(f.Namespace.Name, pod.Name, cmd)
	if err != nil {
		framework.Failf("failed executing cmd %v in %v/%v: %v", cmd, f.Namespace.Name, pod.Name, err)
	}
	framework.Logf("Number of log lines: %#v", output)

	// Convert the returned string line count to an int64
	words := strings.Trim(output, "\n")
	count, err := strconv.ParseInt(words, 10, 64)
	framework.ExpectNoError(err)
	return count
}

// Creates a host networked hostexec pod in the appropriate namespace and then run a kubectl exec command on that pod
// Cleanup exec pod.
func ExecuteCmdInPod(f *framework.Framework, cmd string) (string, error) {
	cmdTestContainerPod := framework.NewHostExecPodSpec(f.Namespace.Name, cmdTestPodName)
	defer func() {
		// Clean up the pod
		f.PodClient().Delete(cmdTestContainerPod.Name, metav1.NewDeleteOptions(0))
		err := framework.WaitForPodToDisappear(f.ClientSet, f.Namespace.Name, cmdTestContainerPod.Name, labelutils.Everything(), time.Second, wait.ForeverTestTimeout)
		if err != nil {
			framework.Failf("Failed to delete %s pod: %v", cmdTestContainerPod.Name, err)
		}
	}()
	_, stdout, err := executeCmdInPodWithCustomizer(f, cmd, cmdTestContainerPod, func(pod *v1.Pod) {})
	return stdout, err
}

// Creates a hostexec pod in the appropriate namespace with customizer and then run a kubectl exec command on that pod
// Do not cleanup exec pod, we may need to collect logs for it.
func ExecuteCmdInPodX(f *framework.Framework, cmd string, podCustomizer func(pod *v1.Pod)) (*v1.Pod, string, error) {
	cmdTestContainerPod := framework.NewHostExecPodSpec(f.Namespace.Name, cmdTestPodName)
	return executeCmdInPodWithCustomizer(f, cmd, cmdTestContainerPod, podCustomizer)
}

// Customize and create a pod in the appropriate namespace and then run a kubectl exec command on that pod.
// Note this function does not cleanup the pod.
func executeCmdInPodWithCustomizer(f *framework.Framework, cmd string, cmdTestContainerPod *v1.Pod, podCustomizer func(pod *v1.Pod)) (*v1.Pod, string, error) {
	podCustomizer(cmdTestContainerPod)
	f.PodClient().Create(cmdTestContainerPod)

	if err := f.WaitForPodRunning(cmdTestContainerPod.Name); err != nil {
		return nil, "", err
	}

	pod, err := f.PodClient().Get(cmdTestContainerPod.Name, metav1.GetOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("Failed to retrieve %s pod: %v", cmdTestContainerPod.Name, err)
	}
	framework.Logf("Created hostexec pod %s", pod.Name)

	stdout, err := framework.RunHostCmd(f.Namespace.Name, cmdTestContainerPod.Name, cmd)
	if err != nil {
		return pod, "", fmt.Errorf("failed executing cmd %v in %v/%v: %v", cmd, f.Namespace.Name, cmdTestContainerPod.Name, err)
	}
	return pod, stdout, err
}

func CreateLoggingPod(f *framework.Framework, node *v1.Node) (*v1.Pod, error) {
	podName := fmt.Sprintf("%s%s", "logging-", utilrand.String(5))

	volumes := []v1.Volume{
		{
			Name: "journald-run-log",
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: "/run/log",
				},
			},
		},
		{
			Name: "journald-var-log",
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: "/var/log",
				},
			},
		},
	}

	volumeMounts := []v1.VolumeMount{
		{
			Name:      "journald-run-log",
			MountPath: "/run/log",
		},
		{
			Name:      "journald-var-log",
			MountPath: "/var/log",
		},
	}

	containers := []v1.Container{
		{
			Name:         fmt.Sprintf("%s-container", podName),
			Image:        "ubuntu:16.04",
			VolumeMounts: volumeMounts,
			Command:      []string{"/bin/bash"},
			Args:         []string{"-c", "sleep 360000"},
		},
	}

	By(fmt.Sprintf("Creating a logging pod %s in namespace %s", podName, f.Namespace.Name))

	sv, err := f.ClientSet.Discovery().ServerVersion()
	if err != nil {
		return nil, err
	}
	// extract just the number part (sometimes we get strings like '10+')
	var getNum = regexp.MustCompile(`(\d+)\+?`)
	validMinor := getNum.FindStringSubmatch(sv.Minor)[1]
	minor, err := strconv.Atoi(validMinor)
	if err != nil {
		return nil, err
	}
	var nodeID string
	// We use ExternalID -which is getting deprecated-only for server side versions <= 10.
	if sv.Major == "1" && minor <= 10 {
		nodeID = node.Spec.ExternalID
	} else {
		nodeID = node.Name
	}

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: f.Namespace.Name,
			Labels: map[string]string{
				"pod-name":               podName,
				"kubernetes.io/hostname": nodeID,
			},
		},
		Spec: v1.PodSpec{
			Containers:    containers,
			Volumes:       volumes,
			RestartPolicy: v1.RestartPolicyNever,
			NodeSelector: map[string]string{
				"kubernetes.io/hostname": nodeID,
			},
		},
	}
	pod, err = f.ClientSet.CoreV1().Pods(f.Namespace.Name).Create(pod)
	if err != nil {
		return pod, err
	}
	framework.Logf("Created logging pod %v", pod.ObjectMeta.Name)

	err = f.WaitForPodRunning(pod.Name)
	if err != nil {
		return pod, err
	}

	// Get the pod again to get the assigned IP
	pod, err = f.PodClient().Get(pod.Name, metav1.GetOptions{})
	if err != nil {
		framework.Logf("Failed to retrieve %s pod: %v", pod.Name, err)
		return pod, err
	}

	return pod, nil
}

func commandExists(cmd string, node *v1.Node) bool {
	if _, err := framework.IssueSSHCommandWithResult(
		fmt.Sprintf("command -v %s", cmd),
		framework.TestContext.Provider,
		node); err != nil {
		return false
	}
	return true
}

func GetNewCalicoDropLogs(f *framework.Framework, node *v1.Node, since int64, logPfx string) (logs []string) {
	pod, err := CreateLoggingPod(f, node)
	defer func() {
		By(fmt.Sprintf("Cleaning up the logging pod serving %s log lines.", logPfx))
		if err := f.ClientSet.CoreV1().Pods(pod.Namespace).Delete(pod.Name, nil); err != nil {
			framework.Failf("unable to cleanup pod %v: %v", pod.Name, err)
		}
		/*
			// TODO: Commented this out because logging pods do not terminate quickly enough for this to pass
			err := framework.WaitForPodToDisappear(f.ClientSet, f.Namespace.Name, pod.Name, labelutils.Everything(), time.Second, wait.ForeverTestTimeout)
			if err != nil {
				framework.Failf("Failed to delete %s pod: %v", pod.Name, err)
			}
		*/
	}()

	By(fmt.Sprintf("Retrieving the %s log lines", logPfx))
	cmd := fmt.Sprintf("journalctl --system | tail -n +%d | grep %s || true", since+1, logPfx)
	output, err := framework.RunHostCmd(f.Namespace.Name, pod.Name, cmd)
	if err != nil {
		framework.Failf("failed executing cmd %v in %v/%v: %v", cmd, f.Namespace.Name, pod.Name, err)
	}
	if output == "" {
		logs = []string{}
	} else {
		logs = strings.Split(output, "\n")
	}
	return
}

func CalicoctlExec(args ...string) {
	cmd := exec.Command("calicoctl", args...)
	runCommandExpectNoError(cmd)
}

func CalicoctlGet(args ...string) string {
	c := append([]string{"calicoctl", "get"}, args...)
	cmd := exec.Command(c[0], c[1:]...)
	return runCommandExpectNoError(cmd)
}

func runCommandExpectNoError(cmd *exec.Cmd) string {
	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	framework.Logf("Running '%s %s'", cmd.Path, strings.Join(cmd.Args, " "))
	err := cmd.Run()
	framework.Logf("stdout: %v", stdout.String())
	framework.Logf("stderr: %v", stderr.String())
	Expect(err).NotTo(HaveOccurred())
	return stdout.String()
}

// Deprecated: Use containerized command: calicoctl.Apply().
func CalicoctlApply(yaml string, args ...interface{}) {
	cmd := exec.Command("calicoctl", "apply", "-f", "-")
	calicoctlCmdWithFile(cmd, yaml, args...)
}

// Deprecated: Use containerized command: calicoctl.Create().
func CalicoctlCreate(yaml string, args ...interface{}) {
	cmd := exec.Command("calicoctl", "create", "-f", "-")
	calicoctlCmdWithFile(cmd, yaml, args...)
}

// Deprecated: Use containerized command: calicoctl.Replace().
func CalicoctlReplace(yaml string, args ...interface{}) {
	cmd := exec.Command("calicoctl", "replace", "-f", "-")
	calicoctlCmdWithFile(cmd, yaml, args...)
}

func calicoctlCmdWithFile(cmd *exec.Cmd, yaml string, args ...interface{}) {
	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	stdin, err := cmd.StdinPipe()
	Expect(err).NotTo(HaveOccurred())
	err = cmd.Start()
	Expect(err).NotTo(HaveOccurred())
	_, err = stdin.Write([]byte(fmt.Sprintf(yaml, args...)))
	Expect(err).NotTo(HaveOccurred())
	err = stdin.Close()
	Expect(err).NotTo(HaveOccurred())

	framework.Logf("Running '%s %s'", cmd.Path, cmd.Args[1])
	err = cmd.Wait()
	framework.Logf("stdout: %v", stdout.String())
	framework.Logf("stderr: %v", stderr.String())
	Expect(err).NotTo(HaveOccurred())
}

func Calicoq(args ...string) (stdout string, stderr string, err error) {
	var stdoutBuf, stderrBuf bytes.Buffer

	cmd := exec.Command("calicoq", args...)
	cmd.Stdout, cmd.Stderr = &stdoutBuf, &stderrBuf

	framework.Logf("Running '%s %s'", cmd.Path, strings.Join(args, " "))
	err = cmd.Run()
	stdout = stdoutBuf.String()
	stderr = stderrBuf.String()
	framework.Logf("Stdout from calicoq: %v", stdout)
	framework.Logf("Stderr from calicoq: %v", stderr)
	return
}

func SetCalicoNodeEnvironment(clientset clientset.Interface, name string, value string) {
	_setCalicoNodeEnvironment(clientset, name, value, false)
}

func SetCalicoNodeEnvironmentWithRetry(clientset clientset.Interface, name string, value string) {
	_setCalicoNodeEnvironment(clientset, name, value, true)
}

func _setCalicoNodeEnvironment(clientset clientset.Interface, name string, value string, allowRetry bool) {
retry:
	ds, err := clientset.Extensions().DaemonSets("kube-system").Get("calico-node", metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())
	env := ds.Spec.Template.Spec.Containers[0].Env
	newEnv := []v1.EnvVar{}
	for _, envVar := range env {
		framework.Logf("%s=%s", envVar.Name, envVar.Value)
		if envVar.Name == name {
			if value == "" {
				// Omit this variable from the new environment.
			} else {
				newEnv = append(newEnv, v1.EnvVar{Name: name, Value: value})
				value = ""
			}
		} else {
			// Copy existing value to the new environment.
			newEnv = append(newEnv, envVar)
		}
	}

	// If we haven't already set a new value, do that now.
	if value != "" {
		newEnv = append(newEnv, v1.EnvVar{Name: name, Value: value})
	}

	ds.Spec.Template.Spec.Containers[0].Env = newEnv
	_, err = clientset.Extensions().DaemonSets("kube-system").Update(ds)
	if allowRetry {
		if err != nil {
			goto retry
		}
	} else {
		Expect(err).NotTo(HaveOccurred())
	}
}

func RestartCalicoNodePods(clientset clientset.Interface, specificNode string) {
	// Grab the number of pods in the kube-system namespace, so we know how many to expect.  Restarting
	// should not alter the number of pods.
	kubeSystemPodsList, err := clientset.CoreV1().Pods("kube-system").List(metav1.ListOptions{})
	Expect(err).ToNot(HaveOccurred())
	numKubeSys := len(kubeSystemPodsList.Items)
	calicoNodePodList, err := clientset.CoreV1().Pods("kube-system").List(metav1.ListOptions{
		LabelSelector: "k8s-app=calico-node",
	})
	Expect(err).NotTo(HaveOccurred())
	for _, calicoNodePod := range calicoNodePodList.Items {
		if specificNode == "" || calicoNodePod.Spec.NodeName == specificNode {
			clientset.CoreV1().Pods("kube-system").Delete(calicoNodePod.ObjectMeta.Name, deleteImmediately)
		}
	}
	framework.WaitForPodsRunningReady(clientset, "kube-system", int32(numKubeSys), 0, 5*time.Minute, map[string]string{})
}

func CreateServerPodWithLabels(f *framework.Framework, namespace *v1.Namespace, podName string, labels map[string]string, port int) *v1.Pod {
	var imageUrl string
	var podArgs []string
	var cmd string
	var nodeselector = map[string]string{}
	imagePull := v1.PullAlways
	if winctl.RunningWindowsTest() {
		imageUrl = "microsoft/powershell:nanoserver"
		podArgs = append(podArgs, "C:\\Program Files\\Powershell\\pwsh.exe", "-Command")
		cmd = fmt.Sprintf("while($true){Write-Host Hello ping test on %s ;Start-Sleep -Seconds 10}", port)
		nodeselector["beta.kubernetes.io/os"] = "windows"
		imagePull = v1.PullIfNotPresent
	} else {
		imageUrl = "gcr.io/google_containers/redis:e2e"
		podArgs = append(podArgs, "/bin/sh", "-c")
		cmd = fmt.Sprintf(serviceCmd, port)
		nodeselector["beta.kubernetes.io/os"] = "linux"
	}
	podArgs = append(podArgs, cmd)
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace.Name,
			Labels:    labels,
		},
		Spec: v1.PodSpec{
			NodeSelector: nodeselector,
			Containers: []v1.Container{
				{
					Name:            fmt.Sprintf("%s-container-%d", podName, port),
					Image:           imageUrl,
					Args:            podArgs,
					ImagePullPolicy: imagePull,
					Ports:           []v1.ContainerPort{{ContainerPort: int32(port)}},
				},
			},
		},
	}
	_, err := f.ClientSet.CoreV1().Pods(namespace.Name).Create(pod)
	Expect(err).NotTo(HaveOccurred())
	return pod
}

func CleanupServerPod(f *framework.Framework, pod *v1.Pod) {
	framework.Logf("CleanupServerPod")
	if err := f.ClientSet.CoreV1().Pods(pod.Namespace).Delete(pod.Name, nil); err != nil {
		framework.Failf("unable to cleanup pod %v: %v", pod.Name, err)
	}
}

func createPingClientPod(f *framework.Framework, namespace *v1.Namespace, podName string, targetPod *v1.Pod) *v1.Pod {
	var imageUrl string
	var podArgs []string
	var cmd string
	var nodeselector = map[string]string{}
	imagePull := v1.PullAlways
	if winctl.RunningWindowsTest() {
		imageUrl = "microsoft/powershell:nanoserver"
		podArgs = append(podArgs, "C:\\Program Files\\Powershell\\pwsh.exe", "-Command")
		cmd = fmt.Sprintf("ping -n 2 -w 10 %s", targetPod.Status.PodIP)
		nodeselector["beta.kubernetes.io/os"] = "windows"
		imagePull = v1.PullIfNotPresent
	} else {
		imageUrl = "gcr.io/google_containers/redis:e2e"
		podArgs = append(podArgs, "/bin/sh", "-c")
		cmd = fmt.Sprintf("ping -c 3 -W 2 -w 10 %s", targetPod.Status.PodIP)
		nodeselector["beta.kubernetes.io/os"] = "linux"
	}
	podArgs = append(podArgs, cmd)

	pod, err := f.ClientSet.CoreV1().Pods(namespace.Name).Create(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
				"pod-name": podName,
			},
		},
		Spec: v1.PodSpec{
			RestartPolicy: v1.RestartPolicyNever,
			NodeSelector:  nodeselector,
			Containers: []v1.Container{
				{
					Name:            fmt.Sprintf("%s-container", podName),
					Image:           imageUrl,
					Args:            podArgs,
					ImagePullPolicy: imagePull,
				},
			},
		},
	})
	Expect(err).NotTo(HaveOccurred())
	return pod
}

func TestCanPing(f *framework.Framework, ns *v1.Namespace, podName string, targetPod *v1.Pod) {
	framework.Logf("Creating client pod %s that should successfully connect to %s.", podName, targetPod.Status.PodIP)
	podClient := createPingClientPod(f, ns, podName, targetPod)
	defer func() {
		framework.Logf("Cleaning up the pod %s", podName)
		if err := f.ClientSet.CoreV1().Pods(ns.Name).Delete(podClient.Name, nil); err != nil {
			framework.Failf("unable to cleanup pod %v: %v", podClient.Name, err)
		}
	}()

	framework.Logf("Waiting for %s to complete.", podClient.Name)
	err := framework.WaitForPodNoLongerRunningInNamespace(f.ClientSet, podClient.Name, ns.Name)
	Expect(err).NotTo(HaveOccurred(), "Pod did not finish as expected.")

	framework.Logf("Waiting for %s to complete.", podClient.Name)
	err = framework.WaitForPodSuccessInNamespace(f.ClientSet, podClient.Name, ns.Name)
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("checking %s could communicate with server.", podClient.Name))
}

func TestCannotPing(f *framework.Framework, ns *v1.Namespace, podName string, targetPod *v1.Pod) {
	framework.Logf("Creating client pod %s that should successfully connect to %s.", podName, targetPod.Status.PodIP)
	podClient := createPingClientPod(f, ns, podName, targetPod)
	defer func() {
		framework.Logf("Cleaning up the pod %s", podName)
		if err := f.ClientSet.CoreV1().Pods(ns.Name).Delete(podClient.Name, nil); err != nil {
			framework.Failf("unable to cleanup pod %v: %v", podClient.Name, err)
		}
	}()

	framework.Logf("Waiting for %s to complete.", podClient.Name)
	err := framework.WaitForPodNoLongerRunningInNamespace(f.ClientSet, podClient.Name, ns.Name)
	Expect(err).NotTo(HaveOccurred(), "Pod did not finish as expected.")

	framework.Logf("Waiting for %s to complete.", podClient.Name)
	err = framework.WaitForPodSuccessInNamespace(f.ClientSet, podClient.Name, ns.Name)
	Expect(err).To(HaveOccurred(), fmt.Sprintf("checking %s could not communicate with server.", podClient.Name))
}

// Create a server pod with specified labels and a listening container for each port in ports[].
// Will also assign a pod label with key: "pod-name" and label set to the given podname for later use by the network
// policy.
func CreateServerPodAndServiceWithLabels(f *framework.Framework, namespace *v1.Namespace, podName string, ports []int, labels map[string]string) (*v1.Pod, *v1.Service) {

	// Because we have a variable amount of ports, we'll first loop through and generate our Containers for our pod,
	// and ServicePorts.for our Service.
	containers := []v1.Container{}
	servicePorts := []v1.ServicePort{}
	var imageUrl string
	var nodeselector = map[string]string{}
	imagePull := v1.PullAlways
	if winctl.RunningWindowsTest() {
		imageUrl = "caltigera/porter:first"
		nodeselector["beta.kubernetes.io/os"] = "windows"
		imagePull = v1.PullIfNotPresent
	} else {
		imageUrl = imageutils.GetE2EImage(imageutils.Porter)
		nodeselector["beta.kubernetes.io/os"] = "linux"
	}
	for _, port := range ports {
		// Build the containers for the server pod.
		containers = append(containers, v1.Container{
			Name:            fmt.Sprintf("%s-container-%d", podName, port),
			Image:           imageUrl,
			ImagePullPolicy: imagePull,
			Env: []v1.EnvVar{
				{
					Name:  fmt.Sprintf("SERVE_PORT_%d", port),
					Value: "foo",
				},
			},
			Ports: []v1.ContainerPort{
				{
					ContainerPort: int32(port),
					Name:          fmt.Sprintf("serve-%d", port),
				},
			},
			ReadinessProbe: &v1.Probe{
				Handler: v1.Handler{
					HTTPGet: &v1.HTTPGetAction{
						Path: "/",
						Port: intstr.IntOrString{
							IntVal: int32(port),
						},
						Scheme: v1.URISchemeHTTP,
					},
				},
			},
		})

		// Build the Service Ports for the service.
		servicePorts = append(servicePorts, v1.ServicePort{
			Name:       fmt.Sprintf("%s-%d", podName, port),
			Port:       int32(port),
			TargetPort: intstr.FromInt(port),
		})
	}

	newLabels := make(map[string]string)
	for k, v := range labels {
		newLabels[k] = v
	}
	newLabels["pod-name"] = podName

	By(fmt.Sprintf("Creating a server pod %s in namespace %s", podName, namespace.Name))
	pod, err := f.ClientSet.CoreV1().Pods(namespace.Name).Create(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   podName,
			Labels: newLabels,
		},
		Spec: v1.PodSpec{
			Containers:    containers,
			RestartPolicy: v1.RestartPolicyNever,
			NodeSelector:  nodeselector,
		},
	})
	Expect(err).NotTo(HaveOccurred())
	framework.Logf("Created pod %v", pod.ObjectMeta.Name)

	svcName := fmt.Sprintf("svc-%s", podName)
	By(fmt.Sprintf("Creating a service %s for pod %s in namespace %s", svcName, podName, namespace.Name))
	svc, err := f.ClientSet.CoreV1().Services(namespace.Name).Create(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: svcName,
		},
		Spec: v1.ServiceSpec{
			Ports: servicePorts,
			Selector: map[string]string{
				"pod-name": podName,
			},
		},
	})
	Expect(err).NotTo(HaveOccurred())
	framework.Logf("Created service %s", svc.Name)

	return pod, svc
}

func getConfigMap(f *framework.Framework, configNames []string) (*v1.ConfigMap, error) {
	for _, name := range configNames {
		if configMap, err := f.ClientSet.CoreV1().ConfigMaps(metav1.NamespaceSystem).Get(name, metav1.GetOptions{}); err == nil {
			return configMap, nil
		}
	}

	return nil, errors.New("Cannot get ConfigMap")
}

func GetCalicoConfigMapData(f *framework.Framework, cfgNames []string) (*map[string]string, error) {
	configMap, err := getConfigMap(f, cfgNames)
	if err != nil {
		framework.Logf("unable to get config map: %v", err)
		return nil, err
	}
	return &configMap.Data, nil

}

type CalicoctlOptions struct {
	Image string
}

type Calicoctl struct {
	opts           CalicoctlOptions
	datastore      string
	endPoints      string
	etcdCaFile     string
	etcdKeyFile    string
	etcdCertFile   string
	framework      *framework.Framework
	serviceAccount *v1.ServiceAccount
	role           *rbacv1.ClusterRole
	roleBinding    *rbacv1.ClusterRoleBinding
	env            map[string]string
	node           string
	nodeToAvoid    string
}

func ConfigureCalicoctl(f *framework.Framework, opts ...CalicoctlOptions) *Calicoctl {
	var ctl Calicoctl
	ctl.env = make(map[string]string)
	ctl.framework = f
	ctl.datastore = "kubernetes"
	ctl.endPoints = "unused"
	ctl.etcdCaFile = ""
	ctl.etcdKeyFile = ""
	ctl.etcdCertFile = ""
	if len(opts) == 1 {
		ctl.opts = opts[0]
	}
	cfg, err := GetCalicoConfigMapData(f, []string{"calico-config", "canal-config", "tigera-aws-config"})
	Expect(err).NotTo(HaveOccurred(), "Unable to get config map: %v", err)
	if v, ok := (*cfg)["etcd_endpoints"]; ok {
		ctl.datastore = "etcdv3"
		ctl.endPoints = v
		ctl.etcdCaFile = (*cfg)["etcd_ca"]
		ctl.etcdKeyFile = (*cfg)["etcd_key"]
		ctl.etcdCertFile = (*cfg)["etcd_cert"]
	}

	// The following resources are created for RBAC permissions for KDD tests. They do not affect etcd tests.
	sa := v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "calicoctl",
			Namespace:    f.Namespace.Name,
		},
	}
	saa, err := f.ClientSet.CoreV1().ServiceAccounts(f.Namespace.Name).Create(&sa)
	Expect(err).ShouldNot(HaveOccurred())
	ctl.serviceAccount = saa

	r := rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "calicoctl",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{
					"crd.projectcalico.org",
				},
				Resources: []string{
					// Allow access to all calico resources
					"*",
				},
				Verbs: []string{
					"create",
					"get",
					"list",
					"update",
					"delete",
				},
			},
			{
				APIGroups: []string{
					"extensions",
					"networking.k8s.io",
				},
				Resources: []string{
					"networkpolicies",
				},
				Verbs: []string{
					"get",
					"list",
					"watch",
				},
			},
			{
				APIGroups: []string{
					"",
				},
				Resources: []string{
					"namespaces",
					"serviceaccounts",
					"nodes",
				},
				Verbs: []string{
					"get",
					"list",
					"watch",
					"update",
				},
			},
			{
				APIGroups: []string{
					"",
				},
				Resources: []string{
					"pods",
				},
				Verbs: []string{
					"get",
					"list",
				},
			},
			{
				APIGroups: []string{
					"",
				},
				Resources: []string{
					"pods/status",
				},
				Verbs: []string{
					"update",
				},
			},
		},
	}
	rr, err := f.ClientSet.RbacV1().ClusterRoles().Create(&r)
	Expect(err).ShouldNot(HaveOccurred())
	ctl.role = rr

	rb := rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "calicoctl",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     rr.ObjectMeta.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      saa.ObjectMeta.Name,
				Namespace: f.Namespace.Name,
			},
		},
	}
	rbb, err := f.ClientSet.RbacV1().ClusterRoleBindings().Create(&rb)
	Expect(err).ShouldNot(HaveOccurred())
	ctl.roleBinding = rbb

	framework.Logf("Configured for datastoreType %s", ctl.datastore)
	return &ctl
}

// Use AvoidNode when calicoctl would not work if its pod was scheduled on some particular
// node.
//
// For example, if a test case has a HostEndpoint on a particular node, and the default
// policy that applies to that HostEndpoint is to deny outbound traffic, then:
//
// - The calicoctl _binary_ might still work if it was run directly on that node, because
//   we normally have a failsafe port open for outbound traffic to the etcd cluster.
//
// - However, running calicoctl _in a pod_ on that node will not work, because pod startup
//   also requires communications between kubelet on that node and the k8s API server, and
//   we do not have an open failsafe port for that.
//
// Therefore, because this file runs calicoctl in a pod, some tests will need to ensure
// that that pod is not on the node with the host endpoint.
func (c *Calicoctl) AvoidNode(nodeName string) {
	c.nodeToAvoid = nodeName
}

func (c *Calicoctl) Cleanup() {
	if c.datastore == "kubernetes" {
		c.framework.ClientSet.CoreV1().ServiceAccounts(c.framework.Namespace.Name).Delete(c.serviceAccount.Name, &metav1.DeleteOptions{})
		c.framework.ClientSet.RbacV1().ClusterRoles().Delete(c.role.Name, &metav1.DeleteOptions{})
		c.framework.ClientSet.RbacV1().ClusterRoleBindings().Delete(c.roleBinding.Name, &metav1.DeleteOptions{})
	}
}

func (c *Calicoctl) DatastoreType() string {
	return c.datastore
}

func (c *Calicoctl) Apply(yaml string, args ...string) {
	c.actionCtl(yaml, "apply", args...)
}

func (c *Calicoctl) ApplyWithError(yaml string, args ...string) error {
	_, err := c.actionCtlWithError(yaml, "apply", args...)
	return err
}

func (c *Calicoctl) Create(yaml string, args ...string) {
	c.actionCtl(yaml, "create", args...)
}

func (c *Calicoctl) CreateWithError(yaml string, args ...string) error {
	_, err := c.actionCtlWithError(yaml, "create", args...)
	return err
}

func (c *Calicoctl) Delete(yaml string, args ...string) {
	c.actionCtl(yaml, "delete", args...)
}

func (c *Calicoctl) Replace(yaml string, args ...string) {
	c.actionCtl(yaml, "replace", args...)
}

func (c *Calicoctl) ReplaceWithError(yaml string, args ...string) error {
	_, err := c.actionCtlWithError(yaml, "replace", args...)
	return err
}

func (c *Calicoctl) Get(args ...string) string {
	return c.execExpectNoError(append([]string{"get"}, args...)...)
}

func (c *Calicoctl) Exec(args ...string) string {
	return c.exec(args...)
}

func (c *Calicoctl) ExecReturnError(args ...string) (string, error) {
	return c.execReturnError(args...)
}

func (c *Calicoctl) DeleteHE(hostEndpointName string) {
	c.execExpectNoError("delete", "hostendpoint", hostEndpointName)
}

func (c *Calicoctl) DeleteGNP(policyName string) {
	c.execExpectNoError("delete", "globalnetworkpolicy", policyName)
}

func (c *Calicoctl) DeleteNP(namespace, policyName string) {
	c.execExpectNoError("delete", "networkpolicy", "-n", namespace, policyName)
}

func (c *Calicoctl) exec(args ...string) string {
	result, _ := c.executeCalicoctl("calicoctl", args...)
	return result
}

func (c *Calicoctl) execExpectNoError(args ...string) string {
	result, err := c.executeCalicoctl("calicoctl", args...)
	Expect(err).NotTo(HaveOccurred())
	return result
}

func (c *Calicoctl) execReturnError(args ...string) (string, error) {
	result, err := c.executeCalicoctl("calicoctl", args...)
	return result, err
}

func (c *Calicoctl) actionCtl(resYaml string, action string, args ...string) {
	logs, err := c.actionCtlWithError(resYaml, action, args...)
	if err != nil {
		framework.Failf("Error '%s'-ing calico resource: %s", action, logs)
	}
}

func (c *Calicoctl) actionCtlWithError(resYaml string, action string, args ...string) (string, error) {
	By("Setting args: " + strings.Join(args, " "))
	cmdString := fmt.Sprintf(
			"/calicoctl %s %s -f - <<EOF\n"+
			"%s\n"+
			"EOF\n",
		action, strings.Join(args, " "), resYaml,
	)
	logs, err := c.executeCalicoctl("/bin/sh", "-c", cmdString)
	return logs, err
}

func (c *Calicoctl) SetEnv(name, value string) {
	c.env[name] = value
}

func (c *Calicoctl) SetNodeToRun(nodeName string) {
	c.node = nodeName
}

func (c *Calicoctl) ApplyCNXLicense() {
	license := `apiVersion: projectcalico.org/v3
kind: LicenseKey
metadata:
  creationTimestamp: null
  name: default
spec:
  certificate: |
    -----BEGIN CERTIFICATE-----
    MIIFxjCCA66gAwIBAgIQVq3rz5D4nQF1fIgMEh71DzANBgkqhkiG9w0BAQsFADCB
    tTELMAkGA1UEBhMCVVMxEzARBgNVBAgTCkNhbGlmb3JuaWExFjAUBgNVBAcTDVNh
    biBGcmFuY2lzY28xFDASBgNVBAoTC1RpZ2VyYSwgSW5jMSIwIAYDVQQLDBlTZWN1
    cml0eSA8c2lydEB0aWdlcmEuaW8+MT8wPQYDVQQDEzZUaWdlcmEgRW50aXRsZW1l
    bnRzIEludGVybWVkaWF0ZSBDZXJ0aWZpY2F0ZSBBdXRob3JpdHkwHhcNMTgwNDA1
    MjEzMDI5WhcNMjAxMDA2MjEzMDI5WjCBnjELMAkGA1UEBhMCVVMxEzARBgNVBAgT
    CkNhbGlmb3JuaWExFjAUBgNVBAcTDVNhbiBGcmFuY2lzY28xFDASBgNVBAoTC1Rp
    Z2VyYSwgSW5jMSIwIAYDVQQLDBlTZWN1cml0eSA8c2lydEB0aWdlcmEuaW8+MSgw
    JgYDVQQDEx9UaWdlcmEgRW50aXRsZW1lbnRzIENlcnRpZmljYXRlMIIBojANBgkq
    hkiG9w0BAQEFAAOCAY8AMIIBigKCAYEAwg3LkeHTwMi651af/HEXi1tpM4K0LVqb
    5oUxX5b5jjgi+LHMPzMI6oU+NoGPHNqirhAQqK/k7W7r0oaMe1APWzaCAZpHiMxE
    MlsAXmLVUrKg/g+hgrqeije3JDQutnN9h5oZnsg1IneBArnE/AKIHH8XE79yMG49
    LaKpPGhpF8NoG2yoWFp2ekihSohvqKxa3m6pxoBVdwNxN0AfWxb60p2SF0lOi6B3
    hgK6+ILy08ZqXefiUs+GC1Af4qI1jRhPkjv3qv+H1aQVrq6BqKFXwWIlXCXF57CR
    hvUaTOG3fGtlVyiPE4+wi7QDo0cU/+Gx4mNzvmc6lRjz1c5yKxdYvgwXajSBx2pw
    kTP0iJxI64zv7u3BZEEII6ak9mgUU1CeGZ1KR2Xu80JiWHAYNOiUKCBYHNKDCUYl
    RBErYcAWz2mBpkKyP6hbH16GjXHTTdq5xENmRDHabpHw5o+21LkWBY25EaxjwcZa
    Y3qMIOllTZ2iRrXu7fSP6iDjtFCcE2bFAgMBAAGjZzBlMA4GA1UdDwEB/wQEAwIF
    oDATBgNVHSUEDDAKBggrBgEFBQcDAjAdBgNVHQ4EFgQUIY7LzqNTzgyTBE5efHb5
    kZ71BUEwHwYDVR0jBBgwFoAUxZA5kifzo4NniQfGKb+4wruTIFowDQYJKoZIhvcN
    AQELBQADggIBAAK207LaqMrnphF6CFQnkMLbskSpDZsKfqqNB52poRvUrNVUOB1w
    3dSEaBUjhFgUU6yzF+xnuH84XVbjD7qlM3YbdiKvJS9jrm71saCKMNc+b9HSeQAU
    DGY7GPb7Y/LG0GKYawYJcPpvRCNnDLsSVn5N4J1foWAWnxuQ6k57ymWwcddibYHD
    OPakOvO4beAnvax3+K5dqF0bh2Np79YolKdIgUVzf4KSBRN4ZE3AOKlBfiKUvWy6
    nRGvu8O/8VaI0vGaOdXvWA5b61H0o5cm50A88tTm2LHxTXynE3AYriHxsWBbRpoM
    oFnmDaQtGY67S6xGfQbwxrwCFd1l7rGsyBQ17cuusOvMNZEEWraLY/738yWKw3qX
    U7KBxdPWPIPd6iDzVjcZrS8AehUEfNQ5yd26gDgW+rZYJoAFYv0vydMEyoI53xXs
    cpY84qV37ZC8wYicugidg9cFtD+1E0nVgOLXPkHnmc7lIDHFiWQKfOieH+KoVCbb
    zdFu3rhW31ygphRmgszkHwApllCTBBMOqMaBpS8eHCnetOITvyB4Kiu1/nKvVxhY
    exit11KQv8F3kTIUQRm0qw00TSBjuQHKoG83yfimlQ8OazciT+aLpVaY8SOrrNnL
    IJ8dHgTpF9WWHxx04DDzqrT7Xq99F9RzDzM7dSizGxIxonoWcBjiF6n5
    -----END CERTIFICATE-----
  token: eyJhbGciOiJBMTI4R0NNS1ciLCJjdHkiOiJKV1QiLCJlbmMiOiJBMTI4R0NNIiwiaXYiOiJlaWNWbHlTbGxFMlAtQ25tIiwidGFnIjoiTk1KSHlRV2M1UWZ6M1dydHNCamxhZyIsInR5cCI6IkpXVCJ9.afBv55v15cFsaHqcsyDkfA.yBMyDIRFBtWxyNxI.Q18a_G6i2kiN0NsqtGSQjc0o2CrkdivRJFkpAlkYIttBAultPADLZmfgf0nzVqZkKAkOGSbIxjY5BgW59FEyaiEs8sL11HZqPB8l2eOqK4BSj5wx3yEhsFzQkD1pZZz8qVgE0Ml3SaSiGVhe4ADTiSsUBbU9JD_aRaa4m1QvS4IQiT_QuWxUtOi-LRXsvHURnkTs3K_WGu7_QW5RRHDGD_CP2kfTUMeSvcWSiT8vgrgPj5q4Zpz4XTWNT-u0sJraWu79tOqCu9YwKeDVMKgJ04sunGc9xsimkhUmOnwuiIEeR24GyL7I5FDrCUC6Oiif62o_ECaQA6NjHAFdq-LNCIb902tKD0BQ-q6AzUrjs21GNr9_oJZJXKL6m74UJULMVgxXZKze2IH9EXtQ0b2jHbi9-qyMp6Rc34Z4HtYmQPB3CRHjDTmzUpEXOsF-reYffRHLJY5DUk7fDfTnhBmUksYonuuGLKep1_YYAiAhkomj7mupFNVN5JnZx8P-v4cfAr4PZxF6Lw5utN5R1hArroYA1Z-2Et0LC6BbE6Q1j7_zmaBs2BEnNfWNn2LFBBOCHzax51ISz_DIcGSidsRDNE9vQDYhcb9MGqOtaCDAA5zHCArVxu2PiwJj6JNbdNB9nvLWlAqxUU4zJwNPFd9xQIR53RFNB0LHID-ab_H7_NFX0auolwSz5Fm14ID4SKvD7_1aqUJG9_WiEtNz9yZJL5vkspdSxnR59L4alUYErxSEWGmOIBvJPemftZBilH1Vmxt0MFyu7sxK_uEJ55OtxNXCfaa_MPp0Yhn9mjTeCSMH8dV6ahZuL8B85BHjFkqY_nLV5UKEvPcyflo4JLDAOvhTZ0bbqvheEx48FQPisSJoK5zY61FqK1tFrID5rdJQ4RMpe4Bix0Dy213hN08U1iNklHUgR-MMw2f4sfGouBm-3B-7P9bqwQlEVyKLkyBzOgWd0PADc0i5bdxCxoqL8AAehPTEGIk-lb2TKe71dCW47oZQwigRgbLHRJnYF9iVlFoXXf-MLH_edh5Gi2OD397MtuBvpGWS8KVjiyUYX-NhvOqgzqrRCH-7kRkmYBsL446hNzGYMjbxut488a2amVrsIuR4oerJnkSdK3o.MnNW4M-g2iiXOi1GVe5zaQ`
	c.Apply(license)
}

func (c *Calicoctl) executeCalicoctl(cmd string, args ...string) (string, error) {
	framework.Logf("Bringing up calicoctl pod to run: %s %s.", cmd, args)

	f := c.framework
	// By default, use the calicoctl image supplied through the command line args. If overridden in the
	// calicoctl configuration then use that.
	image := framework.TestContext.CalicoCtlImage
	if c.opts.Image != "" {
		image = c.opts.Image
	}
	env := []v1.EnvVar{
		{Name: "DATASTORE_TYPE", Value: c.datastore},
		{Name: "ETCD_ENDPOINTS", Value: c.endPoints},
		{Name: "ETCD_CA_CERT_FILE", Value: c.etcdCaFile},
		{Name: "ETCD_KEY_FILE", Value: c.etcdKeyFile},
		{Name: "ETCD_CERT_FILE", Value: c.etcdCertFile},
	}
	for name, value := range c.env {
		env = append(env, v1.EnvVar{Name: name, Value: value})
	}

	podName := fmt.Sprintf("%s%s", "calicoctl-", utilrand.String(5))
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
				"pod-name": "calicoctl",
			},
			Namespace: f.Namespace.Name,
		},
		Spec: v1.PodSpec{
			HostNetwork:   true,
			RestartPolicy: v1.RestartPolicyNever,
			Containers: []v1.Container{
				{
					Name:    "calicoctl-container",
					Image:   image,
					Command: []string{cmd},
					Args:    args,
					Env:     env,
				},
			},
			ServiceAccountName: c.serviceAccount.ObjectMeta.Name,
			//Since calico policy would be applied from master, hence made NodeSelector as linux
			NodeSelector: map[string]string{"beta.kubernetes.io/os": "linux"},
		},
	}
	if c.etcdCaFile != "" || c.etcdCertFile != "" || c.etcdKeyFile != ""  {
		framework.Logf("etcd is secured, adding certs to calicoctl pod")
		pod.Spec.Containers[0].VolumeMounts = []v1.VolumeMount{
			{
				Name:      "etcd-certs",
				MountPath: "/calico-secrets",
			},
		}
		pod.Spec.Volumes = []v1.Volume{
			{
				Name: "etcd-certs",
				VolumeSource: v1.VolumeSource{
					Secret: &v1.SecretVolumeSource{
						SecretName: "calico-etcd-secrets",
					},
				},
			},
		}
		// Check that etcd-certs exists in this namespace, copy it over from kube-system if not.
		_, err := f.ClientSet.CoreV1().Secrets(f.Namespace.Name).Get("calico-etcd-secrets", metav1.GetOptions{})
		if err != nil {
			originalSecret, err := f.ClientSet.CoreV1().Secrets("kube-system").Get("calico-etcd-secrets", metav1.GetOptions{})
			if err != nil {
				framework.Failf("unable to find secret %v in ns %v: %v", originalSecret.Name, originalSecret.Namespace, err)
			}
			// Copy the bits we want out of the old secret
			modifiedSecret := &v1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: originalSecret.Name,
					Namespace: f.Namespace.Name,
				},
				Type: originalSecret.Type,
				Data: originalSecret.Data,
			}
			newSecret, err := f.ClientSet.CoreV1().Secrets(f.Namespace.Name).Create(modifiedSecret)
			if err != nil {
				framework.Failf("unable to create secret %v in ns %v: %v", newSecret.Name, newSecret.Namespace, err)
			}
		}
	}

	if c.node != "" {
		framework.Logf("calicoctl will be running on node %s.", c.node)
		pod.Spec.NodeName = c.node
	}

	if c.nodeToAvoid != "" {
		framework.Logf("calicoctl will avoid running on node %v", c.nodeToAvoid)
		pod.Spec.Affinity = &v1.Affinity{
			NodeAffinity: &v1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
					NodeSelectorTerms: []v1.NodeSelectorTerm{
						{
							MatchExpressions: []v1.NodeSelectorRequirement{
								{
									Key:      "kubernetes.io/hostname",
									Operator: v1.NodeSelectorOpNotIn,
									Values:   []string{c.nodeToAvoid},
								},
							},
						},
					},
				},
			},
		}
	}

	podClient, err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).Create(pod)
	Expect(err).NotTo(HaveOccurred())
	defer func() {
		if err := f.ClientSet.CoreV1().Pods(podClient.Namespace).Delete(podClient.Name, nil); err != nil {
			framework.Failf("unable to cleanup pod %v: %v", podClient.Name, err)
		}
	}()

	err = framework.WaitTimeoutForPodNoLongerRunningInNamespace(f.ClientSet, podClient.Name, f.Namespace.Name, 6*time.Minute)
	if err != nil {
		framework.Logf("calicoctl pod %v got error %v, %#", podClient, err)
	}
	Expect(err).NotTo(HaveOccurred(), "Pod did not finish as expected.")

	exeErr := framework.WaitForPodSuccessInNamespace(f.ClientSet, podClient.Name, f.Namespace.Name)

	// Collect pod logs regardless of execution result.
	logs, logErr := framework.GetPodLogs(f.ClientSet, f.Namespace.Name, podClient.Name, "calicoctl-container")
	if logErr != nil {
		framework.Failf("Error getting container logs: %s", logErr)
	}
	framework.Logf("Getting current log for calicoctl: %s", logs)

	return logs, exeErr
}

func LogCalicoDiagsForNode(f *framework.Framework, nodeName string) error {
	// Find the calico-node pod on the target node.
	calicoNodePodList, err := f.ClientSet.CoreV1().Pods("kube-system").List(metav1.ListOptions{
		LabelSelector: "k8s-app=calico-node",
	})
	if err != nil {
		return err
	}
	for _, calicoNodePod := range calicoNodePodList.Items {
		if calicoNodePod.Spec.NodeName == nodeName {
			// Get some diags from the calico-node pod and log them out directly here.
			for _, cmd := range []string{
				"ip route",
				"ipset save",
				"iptables-save -c",
				"/sbin/versions",
			} {
				out, err := framework.RunHostCmd("kube-system", calicoNodePod.Name, cmd)
				framework.Logf("err %v out:\n%v", err, out)
			}

			// Also get the calico-node container logs.  These will be big, so we write
			// them to a uniquely named file.
			fileName := "./" + calicoNodePod.Name + "_" + time.Now().Format(time.RFC850)
			outFile, err := os.Create(fileName)
			if err != nil {
				return err
			}
			defer outFile.Close()

			logCmd := framework.KubectlCmd(
				"logs",
				"-n",
				"kube-system",
				"-c",
				"calico-node",
				calicoNodePod.Name,
			)
			logCmd.Stdout = outFile
			err = logCmd.Start()
			if err != nil {
				return err
			}
			err = logCmd.Wait()
			if err != nil {
				return err
			}
			framework.Logf("calico-node diags saved to %v", fileName)
		}
	}
	return nil
}

func GetPodNow(f *framework.Framework, podName string) *v1.Pod {
	podNow, err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).Get(podName, metav1.GetOptions{})
	framework.ExpectNoError(err)
	framework.Logf("Pod is on %v, IP %v", podNow.Spec.NodeName, podNow.Status.PodIP)
	framework.Logf("Full pod detail = %#v", podNow)
	return podNow
}

func LogCalicoDiagsForPodNode(f *framework.Framework, podName string) error {
	podNow := GetPodNow(f, podName)
	return LogCalicoDiagsForNode(f, podNow.Spec.NodeName)
}

func RunSSHCommand(cmd, host, user string, timeout time.Duration) (stdout, stderr string, rc int, err error) {
	signer, err := framework.GetSigner(framework.TestContext.Provider)
	if err != nil {
		return "", "", 1, fmt.Errorf("error getting signer for provider %s: '%v'", framework.TestContext.Provider, err)
	}

	config := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         timeout,
	}

	client, err := ssh.Dial("tcp", host, config)
	if err != nil {
		return "", "", 1, fmt.Errorf("error getting SSH client to host %s: '%v'", host, err)
	}

	// Run the command
	framework.Logf("Executing '%s' on %v", cmd, client.RemoteAddr())
	session, err := client.NewSession()
	if err != nil {
		return "", "", 0, fmt.Errorf("error creating session to host %s: '%v'", client.RemoteAddr(), err)
	}
	defer session.Close()

	// Run the command.
	code := 0
	var bout, berr bytes.Buffer

	session.Stdout, session.Stderr = &bout, &berr
	err = session.Run(cmd)
	if err != nil {
		// Check whether the command failed to run or didn't complete.
		if exiterr, ok := err.(*ssh.ExitError); ok {
			// If we got an ExitError and the exit code is nonzero, we'll
			// consider the SSH itself successful (just that the command run
			// errored on the host).
			if code = exiterr.ExitStatus(); code != 0 {
				err = nil
			}
		} else {
			// Some other kind of error happened (e.g. an IOError); consider the
			// SSH unsuccessful.
			err = fmt.Errorf("failed running `%s` on %s: '%v'", cmd, client.RemoteAddr(), err)
		}
	}
	return bout.String(), berr.String(), code, err
}
