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
	"os/exec"
	"strconv"
	"strings"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	imageutils "k8s.io/kubernetes/test/utils/image"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var (
	felixConfigNeeded       = true
	DatastoreType           = ""
	zeroGracePeriod   int64 = 0
	deleteImmediately       = &metav1.DeleteOptions{GracePeriodSeconds: &zeroGracePeriod}
	serviceCmd              = "/bin/sh -c 'while /bin/true; do echo foo | nc -l %d; done'"
	serverPort1             = 80
)

func countSyslogLines(node *v1.Node) (count int64) {
	result, err := framework.IssueSSHCommandWithResult(
		"wc -l /var/log/syslog",
		framework.TestContext.Provider,
		node)
	framework.ExpectNoError(err)
	words := strings.Split(result.Stdout, " ")
	count, err = strconv.ParseInt(words[0], 10, 64)
	framework.ExpectNoError(err)
	return
}

func countSystemJournalLines(node *v1.Node) (count int64) {
	result, err := framework.IssueSSHCommandWithResult(
		"journalctl --system | wc -l",
		framework.TestContext.Provider,
		node)
	framework.ExpectNoError(err)
	words := strings.Trim(result.Stdout, "\n")
	count, err = strconv.ParseInt(words, 10, 64)
	framework.ExpectNoError(err)
	return
}

func CountSyslogLines(node *v1.Node) int64 {
	if commandExists("journalctl", node) {
		return countSystemJournalLines(node)
	} else {
		return countSyslogLines(node)
	}
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

func GetNewCalicoDropLogs(node *v1.Node, since int64, logPfx string) (logs []string) {
	var cmd string
	if commandExists("journalctl", node) {
		cmd = fmt.Sprintf("journalctl --system | tail -n +%d | grep %s || true", since+1, logPfx)
	} else {
		cmd = fmt.Sprintf("tail -n +%d /var/log/syslog | grep %s || true", since+1, logPfx)
	}
	return getNewCalicoDropLogs(cmd, node)
}

func getNewCalicoDropLogs(cmd string, node *v1.Node) (logs []string) {
	result, err := framework.IssueSSHCommandWithResult(
		cmd,
		framework.TestContext.Provider,
		node)
	framework.ExpectNoError(err)
	if result.Stdout == "" {
		logs = []string{}
	} else {
		logs = strings.Split(result.Stdout, "\n")
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
	calicoNodePodList, err := clientset.CoreV1().Pods("kube-system").List(metav1.ListOptions{
		LabelSelector: "k8s-app=calico-node",
	})
	Expect(err).NotTo(HaveOccurred())
	for _, calicoNodePod := range calicoNodePodList.Items {
		if specificNode == "" || calicoNodePod.Spec.NodeName == specificNode {
			clientset.CoreV1().Pods("kube-system").Delete(calicoNodePod.ObjectMeta.Name, deleteImmediately)
			framework.WaitForPodNameRunningInNamespace(clientset, calicoNodePod.Spec.NodeName, "kube-system")
		}
	}
}

func CreateServerPodWithLabels(f *framework.Framework, namespace *v1.Namespace, podName string, labels map[string]string, port int) *v1.Pod {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace.Name,
			Labels:    labels,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  fmt.Sprintf("%s-container-%d", podName, port),
					Image: "gcr.io/google_containers/redis:e2e",
					Args: []string{
						"/bin/sh",
						"-c",
						fmt.Sprintf(serviceCmd, port),
					},
					Ports: []v1.ContainerPort{{ContainerPort: int32(port)}},
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
	pod, err := f.ClientSet.CoreV1().Pods(namespace.Name).Create(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
				"pod-name": podName,
			},
		},
		Spec: v1.PodSpec{
			RestartPolicy: v1.RestartPolicyNever,
			Containers: []v1.Container{
				{
					Name:  fmt.Sprintf("%s-container", podName),
					Image: "gcr.io/google_containers/redis:e2e",
					Args: []string{
						"/bin/sh",
						"-c",
						fmt.Sprintf("ping -c 3 -W 2 -w 10 %s", targetPod.Status.PodIP),
					},
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
	for _, port := range ports {
		// Build the containers for the server pod.
		containers = append(containers, v1.Container{
			Name:  fmt.Sprintf("%s-container-%d", podName, port),
			Image: imageutils.GetE2EImage(imageutils.Porter),
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

type Calicoctl struct {
	datastore string
	endPoints string
	framework *framework.Framework
}

func ConfigureCalicoctl(f *framework.Framework) *Calicoctl {
	var ctl Calicoctl
	ctl.framework = f
	ctl.datastore = "kubernetes"
	ctl.endPoints = "unused"
	cfg, err := GetCalicoConfigMapData(f, []string{"calico-config", "canal-config"})
	Expect(err).NotTo(HaveOccurred(), "Unable to get config map: %v", err)
	if v, ok := (*cfg)["etcd_endpoints"]; ok {
		ctl.datastore = "etcdv3"
		ctl.endPoints = v
	}

	framework.Logf("Configured for datastoreType %s", ctl.datastore)
	return &ctl
}

func (c *Calicoctl) DatastoreType() string {
	return c.datastore
}

func (c *Calicoctl) Apply(yaml string, args ...interface{}) {
	c.actionCtl(fmt.Sprintf(yaml, args...), "apply")
}

func (c *Calicoctl) Create(yaml string, args ...interface{}) {
	c.actionCtl(fmt.Sprintf(yaml, args...), "create")
}

func (c *Calicoctl) Delete(yaml string, args ...interface{}) {
	c.actionCtl(fmt.Sprintf(yaml, args...), "delete")
}

func (c *Calicoctl) Replace(yaml string, args ...interface{}) {
	c.actionCtl(fmt.Sprintf(yaml, args...), "replace")
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

func (c *Calicoctl) actionCtl(resYaml string, action string) {
	resourceArgs := fmt.Sprintf("echo '%s' | tee /$HOME/e2e-test-resource.yaml ; /calicoctl %s -f /$HOME/e2e-test-resource.yaml", resYaml, action)
	logs, err := c.executeCalicoctl("/bin/sh", "-c", resourceArgs)
	if err != nil {
		framework.Failf("Error '%s'-ing calico resource: %s", action, logs)
	}
}

func (c *Calicoctl) executeCalicoctl(cmd string, args ...string) (string, error) {
	framework.Logf("Bringing up calicoctl pod to run: %s %s.", cmd, args)

	f := c.framework
	podClient, err := f.ClientSet.CoreV1().Pods("kube-system").Create(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "calicoctl",
			Labels: map[string]string{
				"pod-name": "calicoctl",
			},
		},
		Spec: v1.PodSpec{
			HostNetwork:   true,
			RestartPolicy: v1.RestartPolicyNever,
			Containers: []v1.Container{
				{
					Name:    "calicoctl-container",
					Image:   framework.TestContext.CalicoCtlImage,
					Command: []string{cmd},
					Args:    args,
					Env: []v1.EnvVar{
						{Name: "DATASTORE_TYPE", Value: c.datastore},
						{Name: "ETCD_ENDPOINTS", Value: c.endPoints},
					},
				},
			},
		},
	})

	Expect(err).NotTo(HaveOccurred())
	defer func() {
		if err := f.ClientSet.CoreV1().Pods(podClient.Namespace).Delete(podClient.Name, nil); err != nil {
			framework.Failf("unable to cleanup pod %v: %v", podClient.Name, err)
		}
	}()

	err = framework.WaitForPodNoLongerRunningInNamespace(f.ClientSet, podClient.Name, "kube-system")
	Expect(err).NotTo(HaveOccurred(), "Pod did not finish as expected.")

	exeErr := framework.WaitForPodSuccessInNamespace(f.ClientSet, podClient.Name, "kube-system")

	// Collect pod logs regardless of execution result.
	logs, logErr := framework.GetPodLogs(f.ClientSet, "kube-system", podClient.Name, fmt.Sprintf("%s-container", podClient.Name))
	if logErr != nil {
		framework.Failf("Error getting container logs: %s", logErr)
	}
	framework.Logf("Getting current log for calicoctl: %s", logs)

	return logs, exeErr
}

func LogCalicoDiagsForNode(f *framework.Framework, nodeName string) {
	node, err := f.ClientSet.CoreV1().Nodes().Get(nodeName, metav1.GetOptions{})
	framework.ExpectNoError(err)

	// For the following operations to work, you need to run the e2e.test binary with
	// '--provider local', and to have an unencrypted SSH key in ~/.ssh/id_rsa on the
	// machine/account where you are running that binary, that is able to access the other nodes
	// in the cluster.
	//
	// (Probably other provider setups would work too, but I have not researched those.)
	framework.IssueSSHCommand("sudo ip route", framework.TestContext.Provider, node)
	framework.IssueSSHCommand("sudo ipset save", framework.TestContext.Provider, node)
	framework.IssueSSHCommand("sudo iptables-save -c -t filter", framework.TestContext.Provider, node)
}

func GetPodNow(f *framework.Framework, podName string) *v1.Pod {
	podNow, err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).Get(podName, metav1.GetOptions{})
	framework.ExpectNoError(err)
	framework.Logf("Pod is on %v, IP %v", podNow.Spec.NodeName, podNow.Status.PodIP)
	framework.Logf("Full pod detail = %#v", podNow)
	return podNow
}

func LogCalicoDiagsForPodNode(f *framework.Framework, podName string) {
	podNow := GetPodNow(f, podName)
	LogCalicoDiagsForNode(f, podNow.Spec.NodeName)
}
