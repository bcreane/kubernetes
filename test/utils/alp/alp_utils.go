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

package alp

import (
	"fmt"
	"strings"
	"time"

	apierrs "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/kubernetes/test/e2e/framework"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"

	. "github.com/onsi/gomega"
)

const (
	DikastesContainerName = "dikastes"
	ProxyContainerName    = "istio-proxy"
	IstioNamespace        = "istio-system"
	PilotDiscoveryPort    = 15003
	NumberOfRetries       = 50
)

func CheckIstioInstall(f *framework.Framework) (bool, error) {
	_, err := f.ClientSet.CoreV1().Namespaces().Get(IstioNamespace, metav1.GetOptions{})
	if apierrs.IsNotFound(err) {
		return false, nil // not installed
	}
	if err != nil {
		framework.Logf("Checking istio install failed with error: %s.", err)
		return false, err // with error
	}
	return true, nil // installed.
}

func EnableIstioInjectionForNamespace(f *framework.Framework, ns *v1.Namespace) {
	// Namespace for the test, labeled so that Istio Sidecar Injector will add the Dikastes & Envoy sidecars.
	ns.Labels["istio-injection"] = "enabled"
	_, err := f.ClientSet.CoreV1().Namespaces().Update(ns)
	Expect(err).ToNot(HaveOccurred())

	// Wait until injection took place
	framework.Logf("Verifying istio injector is working.")
	checkPodFunc := func(f *framework.Framework, ns *v1.Namespace) bool {
		execPodName := framework.CreateExecPodOrFail(f.ClientSet, ns.Name, "alpexec-", nil)
		pod, err := f.ClientSet.CoreV1().Pods(ns.Name).Get(execPodName, metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())

		defer func() {
			framework.DeletePodOrFail(f.ClientSet, ns.Name, execPodName)
			WaitForPodNotFoundInNamespace(f, ns, execPodName)
		}()

		return checkPodSideCars(pod)
	}

	startTime := time.Now()
	for time.Since(startTime) < 30*time.Second {
		if checkPodFunc(f, ns) {
			return
		}

		framework.Logf("Retrying sidecar validation check...")
		time.Sleep(2 * time.Second)

	}

	// pod injection failed.
	framework.Failf("istio sidecars failed to be injected for namespace %s", ns.Name)
}

func GetProbeAndTargetDiags(f *framework.Framework, targetPod *v1.Pod, ns *v1.Namespace, podName string, containerName string) string {
	// Get logs from the target, both Dikastes and the proxy (Envoy)
	dikastesLogs, logErr := framework.GetPodLogs(f.ClientSet, targetPod.Namespace, targetPod.Name, DikastesContainerName)
	if logErr != nil {
		framework.Logf("Error getting dikastes container logs: %s", logErr)
	}
	probeLogs, logErr := framework.GetPreviousPodLogs(f.ClientSet, ns.Name, podName, containerName)
	if logErr != nil {
		framework.Logf("Error getting probe container logs: %s", logErr)
	}

	// Envoy proxy flushes HTTP logs every 10 seconds, so wait before attempting to get the logs so we'll see any
	// requests generated by the test.
	time.Sleep(10 * time.Second)
	proxyLogs, logErr := framework.GetPodLogs(f.ClientSet, targetPod.Namespace, targetPod.Name, ProxyContainerName)
	if logErr != nil {
		framework.Logf("Error getting target proxy container logs: %s", logErr)
	}
	probeProxyLogs, logErr := framework.GetPodLogs(f.ClientSet, ns.Name, podName, ProxyContainerName)
	if logErr != nil {
		framework.Logf("Error getting probe proxy container logs: %s", logErr)
	}

	// Collect current NetworkPolicies applied in the test namespace.
	policies, err := f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).List(metav1.ListOptions{})
	if err != nil {
		framework.Logf("error getting current NetworkPolicies for %s namespace: %s", f.Namespace.Name, err)
	}
	// Collect the list of pods running in the test namespace.
	podsInNS, err := framework.GetPodsInNamespace(f.ClientSet, f.Namespace.Name, map[string]string{})
	if err != nil {
		framework.Logf("error getting pods for %s namespace: %s", f.Namespace.Name, err)
	}
	pods := []string{}
	for _, p := range podsInNS {
		pods = append(pods, fmt.Sprintf("Pod: %s, Status: %s\n", p.Name, p.Status.String()))
	}

	return fmt.Sprintf(`
Probe Logs:
%s

Probe Proxy Logs:
%s

Target Dikastes Logs:
%s

Target Proxy Logs:
%s

Current NetworkPolicies:
	%v

Pods:
	%v

`, probeLogs, probeProxyLogs, dikastesLogs, proxyLogs, policies.Items, pods)
}

func GetIstioDiags(f *framework.Framework) string {
	istioPods, err := f.ClientSet.CoreV1().Pods(IstioNamespace).List(metav1.ListOptions{})
	if err != nil {
		framework.Logf("error getting pods for %s namespace: %s", f.Namespace.Name, err)
	}
	cOut := []string{}
	out := []string{"istio-system status:"}
	for _, p := range istioPods.Items {
		out = append(out, fmt.Sprintf("%-40s  %-8s", p.Name, p.Status.Phase))
		for _, ics := range p.Status.InitContainerStatuses {
			out = append(out, fmt.Sprintf("  init  %-40s  %s", ics.Name, containerStateString(&ics.State)))
			l, err := framework.GetPodLogs(f.ClientSet, IstioNamespace, p.Name, ics.Name)
			if err != nil {
				framework.Logf("Error getting %s %s logs: %s", p.Name, ics.Name, err)
			}
			cOut = append(cOut, fmt.Sprintf("%s/%s logs:\n%s\n\n", p.Name, ics.Name, l))
		}
		for _, cs := range p.Status.ContainerStatuses {
			out = append(out, fmt.Sprintf("        %-40s  %s", cs.Name, containerStateString(&cs.State)))
			l, err := framework.GetPodLogs(f.ClientSet, IstioNamespace, p.Name, cs.Name)
			if err != nil {
				framework.Logf("Error getting %s %s logs: %s", p.Name, cs.Name, err)
			}
			cOut = append(cOut, fmt.Sprintf("%s/%s logs:\n%s", p.Name, cs.Name, l))
		}
	}
	return strings.Join(out, "\n") + "\n\n" + strings.Join(cOut, "\n\n")
}

func containerStateString(state *v1.ContainerState) string {
	if state.Waiting != nil {
		return "Waiting - " + state.Waiting.Reason
	}
	if state.Running != nil {
		return "Running"
	}
	if state.Terminated != nil {
		return fmt.Sprintf("Terminated(%d)", state.Terminated.ExitCode)
	}
	return "UNKNOWN"
}

func WrapPodCustomizerIncreaseRetries(podCustomizer func(pod *v1.Pod)) func(pod *v1.Pod) {
	return func(pod *v1.Pod) {
		podCustomizer(pod)
		// Increase retries because Istio pods can sometimes take a while to connect to services
		pod.Spec.Containers[0].Args[2] = strings.Replace(pod.Spec.Containers[0].Args[2],
			"$(seq 1 5)", fmt.Sprintf("$(seq 1 %d)", NumberOfRetries), 1)
	}
}

func CreateServiceAccount(f *framework.Framework, name, namespace string, labels map[string]string) *v1.ServiceAccount {
	sa := &v1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels}}
	sa, err := f.ClientSet.CoreV1().ServiceAccounts(namespace).Create(sa)
	Expect(err).ToNot(HaveOccurred())
	return sa
}

func DeleteServiceAccount(f *framework.Framework, sa *v1.ServiceAccount) {
	err := f.ClientSet.CoreV1().ServiceAccounts(sa.Namespace).Delete(sa.Name, &metav1.DeleteOptions{})
	Expect(err).ToNot(HaveOccurred())
}

// WaitForPodNotFound waits for the pod to be completely terminated (not "Get-able") in a namespace.
func WaitForPodNotFoundInNamespace(f *framework.Framework, ns *v1.Namespace, podName string) error {
	return wait.PollImmediate(framework.Poll, framework.DefaultPodDeletionTimeout, func() (bool, error) {
		_, err := f.ClientSet.CoreV1().Pods(ns.Name).Get(podName, metav1.GetOptions{})
		if apierrs.IsNotFound(err) {
			return true, nil // done
		}
		if err != nil {
			return true, err // stop wait with error
		}
		return false, nil
	})
}

// WaitForContainerSuccess waits for a container in a pod to terminate successfully (Exit code 0), and returns an error
// if the container terminates unsuccessfully.
func WaitForContainerSuccess(c clientset.Interface, p *v1.Pod, containerName string) error {
	return wait.PollImmediate(framework.Poll, 10*time.Minute, containerSuccess(c, p.Name, p.Namespace, containerName))
}

// containerSuccess constructs a wait.ConditionFunc that checks if a container in a pod has terminated successfully
// (Exit code 0).
func containerSuccess(c clientset.Interface, podName, namespace, containerName string) wait.ConditionFunc {
	return func() (bool, error) {
		pod, err := c.CoreV1().Pods(namespace).Get(podName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Name != containerName {
				continue
			}
			if cs.State.Terminated == nil {
				return false, nil
			}
			t := cs.State.Terminated
			if t.Reason == "Completed" && t.ExitCode == 0 {
				return true, nil
			} else {
				return true, fmt.Errorf("container unsuccessfully terminated reason: %s, exit code: %d", t.Reason, t.ExitCode)
			}
		}
		return false, nil
	}
}

func VerifyContainersForPod(pod *v1.Pod) {
	if !checkPodSideCars(pod) {
		framework.Failf("Pod does not have valid istio side cars")
	}
}

func checkPodSideCars(pod *v1.Pod) bool {
	initContainers := ""
	for _, c := range pod.Spec.InitContainers {
		initContainers += c.Name + " "
	}

	containers := ""
	for _, c := range pod.Spec.Containers {
		containers += c.Name + " "
	}
	framework.Logf("pod <%s> got init containers <%s>, containers <%s>.", pod.Name, initContainers, containers)

	if !strings.Contains(containers, ProxyContainerName) || !strings.Contains(containers, DikastesContainerName) {
		return false
	}

	return true
}
