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

package network

import (
	"fmt"

	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"

	"k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/apimachinery/pkg/util/wait"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

const DikastesContainerName = "dikastes"
const ProxyContainerName = "istio-proxy"
const IstioNamespace = "istio-system"

var _ = SIGDescribe("[Feature:CalicoPolicy-ALP] calico application layer policy", func() {
	var calicoctl *calico.Calicoctl

	f := framework.NewDefaultFramework("calico-alp")

	BeforeEach(func() {
		var err error

		// See if Istio is installed. If not, then skip these tests so we don't cause spurious failures on non-Istio
		// test environments.
		_, err = f.ClientSet.CoreV1().Namespaces().Get(IstioNamespace, metav1.GetOptions{})
		if err != nil {
			framework.Skipf("Istio not installed. ALP tests not supported.")
		}

		// Namespace for the test, labeled so that Istio Sidecar Injector will add the Dikastes & Envoy sidecars.
		f.Namespace.Labels["istio-injection"] = "enabled"
		f.Namespace, err = f.ClientSet.CoreV1().Namespaces().Update(f.Namespace)
		Expect(err).ToNot(HaveOccurred())
	})

	BeforeEach(func() {
		// The following code tries to get config information for calicoctl from k8s ConfigMap.
		// A framework clientset is needed to access k8s configmap but it will only be created in the context of BeforeEach or IT.
		// Current solution is to use BeforeEach because this function is not a test case.
		// This will avoid complexity of creating a client by ourself.
		calicoctl = calico.ConfigureCalicoctl(f)
		calicoctl.SetEnv("ALPHA_FEATURES", "serviceaccounts,httprules")
	})

	Context("with service running", func(){
		var podServer *v1.Pod
		var service *v1.Service

		BeforeEach(func() {
			// Create Server with Service
			By("Creating a simple server.")
			podServer, service = createIstioServerPodAndService(f, f.Namespace, "server", []int{80})
			framework.Logf("Waiting for Server to come up.")
			err := framework.WaitForPodRunningInNamespace(f.ClientSet, podServer)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("with no policy", func(){

			It("should allow pod with default service account to connect", func(){
				By("Creating client which will be able to contact the server since no policies are present.")
				testIstioCanConnect(f, f.Namespace, "default-can-connect", service, 80, podServer, nil)
			})
		})

		Context("with GlobalNetworkPolicy selecting \"can-connect\" service account", func(){

			BeforeEach(func(){
				gnp := `
- apiVersion: projectcalico.org/v3
  kind: GlobalNetworkPolicy
  metadata:
    name: svc-acct-can-connect
  spec:
    selector: pod-name == "server"
    ingress:
      - action: Allow
        source:
          serviceAccounts:
            names: ["can-connect"]
    egress:
      - action: Allow
`
				calicoctl.Apply(gnp)
			})

			AfterEach(func(){
				calicoctl.DeleteGNP("svc-acct-can-connect")
			})

			It("should allow \"can-connect\" pod to connect", func(){
				By("creating \"can-connect\" service account")
				sa := createServiceAccount(f, "can-connect", f.Namespace.Name, map[string]string{"can-connect": "true"})
				defer deleteServiceAccount(f, sa)

				By("testing connectivity with pod using \"can-connect\" service account")
				testIstioCanConnect(f, f.Namespace, "pod-can-connect", service, 80, podServer, sa)
			})

			It("should not allow \"cannot-connect\" pod to connect", func(){
				By("creating \"cannot-connect\" service account")
				sa := createServiceAccount(f, "cannot-connect", f.Namespace.Name, map[string]string{"can-connect": "false"})
				defer deleteServiceAccount(f, sa)

				By("testing connectivity with pod using \"cannot-connect\" service account")
				testIstioCannotConnect(f, f.Namespace, "pod-cannot-connect", service, 80, podServer, sa)
			})
		})
	})
})


// createIstioServerPodAndService works just like createServerPodAndService(), but with some Istio specific tweaks.
func createIstioServerPodAndService(f *framework.Framework, namespace *v1.Namespace, podName string, ports []int) (*v1.Pod, *v1.Service) {
	return createServerPodAndServiceX(f, namespace, podName, ports,
		func(pod *v1.Pod) {
			oldContainers := pod.Spec.Containers
			pod.Spec.Containers = []v1.Container{}
			for _, container := range oldContainers {
				// Strip out readiness probe because Istio doesn't support HTTP health probes when in mTLS mode.
				container.ReadinessProbe = nil
				pod.Spec.Containers = append(pod.Spec.Containers, container)
			}
		},
		func(svc *v1.Service){
			for _, port := range svc.Spec.Ports {
				// Istio requires service ports to be named <protocol>[-<suffix>]
				port.Name = fmt.Sprintf("http-%d", port.Port)
			}
		},
	)
}

// testIstioCanConnect works like testCanConnect(), but takes the target Pod for diagnostics, and an optional Service
// Account for the probe pod.
func testIstioCanConnect(f *framework.Framework, ns *v1.Namespace, podName string, service *v1.Service, targetPort int, targetPod *v1.Pod, account *v1.ServiceAccount, ) {
	testIstioCanConnectX(f, ns, podName, service, targetPort, targetPod, func(pod *v1.Pod){
		if account != nil {
			pod.Spec.ServiceAccountName = account.Name
		}
	})
}

// testIstioCanConnectX works like testCanConnectX(), but has Istio specific tweaks and diagnostics.
func testIstioCanConnectX(f *framework.Framework, ns *v1.Namespace, podName string, service *v1.Service, targetPort int, targetPod *v1.Pod, podCustomizer func(pod *v1.Pod)) {
	By(fmt.Sprintf("Creating client pod %s that should successfully connect to %s.", podName, service.Name))
	podClient := createNetworkClientPodX(f, ns, podName, service, targetPort, podCustomizer)
	containerName := podClient.Spec.Containers[0].Name
	defer func() {
		By(fmt.Sprintf("Cleaning up the pod %s", podName))
		if err := f.ClientSet.CoreV1().Pods(ns.Name).Delete(podClient.Name, nil); err != nil {
			framework.Failf("unable to cleanup pod %v: %v", podClient.Name, err)
		}
	}()

	// Istio injects proxy sidecars into the pod, and these sidecars do not exit when the main probe container finishes.
	// So, we can't use WaitForPodSuccessInNamespace to wait for the probe to finish. Instead, we use
	// WaitForContainerSuccess which just waits for a specific container in the pod to finish.
	framework.Logf("Waiting for %s to complete.", podClient.Name)
	err := WaitForContainerSuccess(f.ClientSet, podClient, containerName)
	if err != nil {
		framework.Logf("Client container was not successful %v", err)
		diags := getProbeAndTargetDiags(f, targetPod, ns, podName, containerName)

		framework.Failf("Pod %s should be able to connect to service %s, but was not able to connect.%s",
			podName, service.Name, diags)

		// Dump debug information for the test namespace.
		framework.DumpDebugInfo(f.ClientSet, f.Namespace.Name)
	}
}

// testIstioCannotConnect works like testCannotConnect(), but the target pod for diagnostics and an optional service
// account.
func testIstioCannotConnect(f *framework.Framework, ns *v1.Namespace, podName string, service *v1.Service, targetPort int, targetPod *v1.Pod, account *v1.ServiceAccount) {
	testIstioCannotConnectX(f, ns, podName, service, targetPort, targetPod, func(pod *v1.Pod) {
		if account != nil {
			pod.Spec.ServiceAccountName = account.Name
		}
	})
}


// testIstioCannotConnectX works like testCannotConnectX(), but has Istio specific tweaks.
func testIstioCannotConnectX(f *framework.Framework, ns *v1.Namespace, podName string, service *v1.Service, targetPort int, targetPod *v1.Pod, podCustomizer func(pod *v1.Pod)) {
	By(fmt.Sprintf("Creating client pod %s that should not be able to connect to %s.", podName, service.Name))
	podClient := createNetworkClientPodX(f, ns, podName, service, targetPort, podCustomizer)
	containerName := podClient.Spec.Containers[0].Name
	defer func() {
		By(fmt.Sprintf("Cleaning up the pod %s", podName))
		if err := f.ClientSet.CoreV1().Pods(ns.Name).Delete(podClient.Name, nil); err != nil {
			framework.Failf("unable to cleanup pod %v: %v", podClient.Name, err)
		}
	}()

	// Istio injects proxy sidecars into the pod, and these sidecars do not exit when the main probe container finishes.
	// So, we can't use WaitForPodSuccessInNamespace to wait for the probe to finish. Instead, we use
	// WaitForContainerSuccess which just waits for a specific container in the pod to finish.
	framework.Logf("Waiting for %s to complete.", podClient.Name)
	err := WaitForContainerSuccess(f.ClientSet, podClient, containerName)

	// We expect an error here since it's a cannot connect test.
	// Dump debug information if the error was nil.
	if err == nil {
		// Get logs from the target, both Dikastes and the proxy (Envoy)
		diags := getProbeAndTargetDiags(f, targetPod, ns, podName, containerName)

		framework.Failf("Pod %s should not be able to connect to service %s, but was able to connect.%s",
			podName, service.Name, diags)

		// Dump debug information for the test namespace.
		framework.DumpDebugInfo(f.ClientSet, f.Namespace.Name)
	}
}

// WaitForContainerSuccess waits for a container in a pod to terminate successfully (Exit code 0), and returns an error
// if the container terminates unsuccessfully.
func WaitForContainerSuccess(c clientset.Interface, p *v1.Pod, containerName string) error {
	return wait.PollImmediate(framework.Poll, framework.DefaultPodDeletionTimeout, containerSuccess(c, p.Name, p.Namespace, containerName) )
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

func getProbeAndTargetDiags(f *framework.Framework, targetPod *v1.Pod, ns *v1.Namespace, podName string, containerName string) (string) {
	// Get logs from the target, both Dikastes and the proxy (Envoy)
	dikastesLogs, logErr := framework.GetPodLogs(f.ClientSet, targetPod.Namespace, targetPod.Name, DikastesContainerName)
	if logErr != nil {
		framework.Logf("Error getting dikastes container logs: %s", logErr)
	}
	proxyLogs, logErr := framework.GetPodLogs(f.ClientSet, targetPod.Namespace, targetPod.Name, ProxyContainerName)
	if logErr != nil {
		framework.Logf("Error getting dikastes container logs: %s", logErr)
	}
	logs, logErr := framework.GetPreviousPodLogs(f.ClientSet, ns.Name, podName, containerName)
	if logErr != nil {
		framework.Logf("Error getting probe container logs: %s", logErr)
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

Target Dikastes Logs:
%s

Target Proxy Logs:
%s

Current NetworkPolicies:
	%v

Pods:
	%v

`, logs, dikastesLogs, proxyLogs, policies.Items, pods)
}

func createServiceAccount(f *framework.Framework, name, namespace string, labels map[string]string) *v1.ServiceAccount {
	sa := &v1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels:labels}}
	sa, err := f.ClientSet.CoreV1().ServiceAccounts(namespace).Create(sa)
	Expect(err).ToNot(HaveOccurred())
	return sa
}

func deleteServiceAccount(f *framework.Framework, sa *v1.ServiceAccount) {
	err := f.ClientSet.CoreV1().ServiceAccounts(sa.Namespace).Delete(sa.Name, &metav1.DeleteOptions{})
	Expect(err).ToNot(HaveOccurred())
}