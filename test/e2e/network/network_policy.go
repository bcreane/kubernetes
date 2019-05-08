/*
Copyright 2016 The Kubernetes Authors.

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
	"k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilversion "k8s.io/kubernetes/pkg/util/version"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"
	imageutils "k8s.io/kubernetes/test/utils/image"
	"k8s.io/kubernetes/test/utils/winctl"

	"fmt"
	"os"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

/*
The following Network Policy tests verify that policy object definitions
are correctly enforced by a networking plugin. It accomplishes this by launching
a simple netcat server, and two clients with different
attributes. Each test case creates a network policy which should only allow
connections from one of the clients. The test then asserts that the clients
failed or successfully connected as expected.
*/

var egressVersion = utilversion.MustParseSemantic("v1.8.0")

var _ = SIGDescribe("[Feature:NetworkPolicy]", func() {
	var service *v1.Service
	var podServer *v1.Pod
	f := framework.NewDefaultFramework("network-policy")

	Context("NetworkPolicy between server and client", func() {
		BeforeEach(func() {
			By("Creating a simple server that serves on port 80 and 81.")
			podServer, service = createServerPodAndService(f, f.Namespace, "server", []int{80, 81})

			By("Waiting for pod ready", func() {
				err := f.WaitForPodReady(podServer.Name)
				Expect(err).NotTo(HaveOccurred())
			})

			// Create pods, which should be able to communicate with the server on port 80 and 81.
			By("Testing pods can connect to both ports when no policy is present.")
			testCanConnect(f, f.Namespace, "client-can-connect-80", service, 80)
			testCanConnect(f, f.Namespace, "client-can-connect-81", service, 81)
		})

		AfterEach(func() {
			cleanupServerPodAndService(f, podServer, service)
		})

		It("should support a 'default-deny' policy [Feature:WindowsPolicy]", func() {
			policy := &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "deny-all",
				},
				Spec: networkingv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{},
					Ingress:     []networkingv1.NetworkPolicyIngressRule{},
				},
			}

			policy, err := f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).Create(policy)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupNetworkPolicy(f, policy)

			// Create a pod with name 'client-cannot-connect', which will attempt to communicate with the server,
			// but should not be able to now that isolation is on.
			testCannotConnect(f, f.Namespace, "client-cannot-connect", service, 80)
		})

		It("should enforce policy based on PodSelector [Feature:WindowsPolicy]", func() {
			By("Creating a network policy for the server which allows traffic from the pod 'client-a'.")
			policy := &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "allow-client-a-via-pod-selector",
				},
				Spec: networkingv1.NetworkPolicySpec{
					// Apply this policy to the Server
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"pod-name": podServer.Name,
						},
					},
					// Allow traffic only from client-a
					Ingress: []networkingv1.NetworkPolicyIngressRule{{
						From: []networkingv1.NetworkPolicyPeer{{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"pod-name": "client-a",
								},
							},
						}},
					}},
				},
			}

			policy, err := f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).Create(policy)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupNetworkPolicy(f, policy)

			By("Creating client-a which should be able to contact the server.", func() {
				testCanConnect(f, f.Namespace, "client-a", service, 80)
			})
			By("Creating client-b which should not be able to contact the server.", func() {
				testCannotConnect(f, f.Namespace, "client-b", service, 80)
			})
		})

		It("should enforce policy based on NamespaceSelector [Feature:WindowsPolicy]", func() {
			nsA := f.Namespace
			nsBName := f.BaseName + "-b"
			// The CreateNamespace helper uses the input name as a Name Generator, so the namespace itself
			// will have a different name than what we are setting as the value of ns-name.
			// This is fine as long as we don't try to match the label as nsB.Name in our policy.
			nsB, err := f.CreateNamespace(nsBName, map[string]string{
				"ns-name": nsBName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Create Server with Service in NS-B
			framework.Logf("Waiting for server to come up.")
			err = framework.WaitForPodRunningInNamespace(f.ClientSet, podServer)
			Expect(err).NotTo(HaveOccurred())

			// Create Policy for that service that allows traffic only via namespace B
			By("Creating a network policy for the server which allows traffic from namespace-b.")
			policy := &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "allow-ns-b-via-namespace-selector",
				},
				Spec: networkingv1.NetworkPolicySpec{
					// Apply to server
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"pod-name": podServer.Name,
						},
					},
					// Allow traffic only from NS-B
					Ingress: []networkingv1.NetworkPolicyIngressRule{{
						From: []networkingv1.NetworkPolicyPeer{{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"ns-name": nsBName,
								},
							},
						}},
					}},
				},
			}
			policy, err = f.ClientSet.NetworkingV1().NetworkPolicies(nsA.Name).Create(policy)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupNetworkPolicy(f, policy)

			testCannotConnect(f, nsA, "client-a", service, 80)
			testCanConnect(f, nsB, "client-b", service, 80)
		})

		It("should enforce policy based on Ports [Feature:WindowsPolicy]", func() {
			By("Creating a network policy for the Service which allows traffic only to one port.")
			policy := &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "allow-ingress-on-port-81",
				},
				Spec: networkingv1.NetworkPolicySpec{
					// Apply to server
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"pod-name": podServer.Name,
						},
					},
					// Allow traffic only to one port.
					Ingress: []networkingv1.NetworkPolicyIngressRule{{
						Ports: []networkingv1.NetworkPolicyPort{{
							Port: &intstr.IntOrString{IntVal: 81},
						}},
					}},
				},
			}
			policy, err := f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).Create(policy)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupNetworkPolicy(f, policy)

			By("Testing pods can connect only to the port allowed by the policy.")
			testCannotConnect(f, f.Namespace, "client-a", service, 80)
			testCanConnect(f, f.Namespace, "client-b", service, 81)
		})

		It("should enforce multiple, stacked policies with overlapping podSelectors [Feature:WindowsPolicy]", func() {
			By("Creating a network policy for the Service which allows traffic only to one port.")
			policy := &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "allow-ingress-on-port-80",
				},
				Spec: networkingv1.NetworkPolicySpec{
					// Apply to server
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"pod-name": podServer.Name,
						},
					},
					// Allow traffic only to one port.
					Ingress: []networkingv1.NetworkPolicyIngressRule{{
						Ports: []networkingv1.NetworkPolicyPort{{
							Port: &intstr.IntOrString{IntVal: 80},
						}},
					}},
				},
			}
			policy, err := f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).Create(policy)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupNetworkPolicy(f, policy)

			By("Creating a network policy for the Service which allows traffic only to another port.")
			policy2 := &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "allow-ingress-on-port-81",
				},
				Spec: networkingv1.NetworkPolicySpec{
					// Apply to server
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"pod-name": podServer.Name,
						},
					},
					// Allow traffic only to one port.
					Ingress: []networkingv1.NetworkPolicyIngressRule{{
						Ports: []networkingv1.NetworkPolicyPort{{
							Port: &intstr.IntOrString{IntVal: 81},
						}},
					}},
				},
			}
			policy2, err = f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).Create(policy2)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupNetworkPolicy(f, policy2)

			By("Testing pods can connect to both ports when both policies are present.")
			testCanConnect(f, f.Namespace, "client-a", service, 80)
			testCanConnect(f, f.Namespace, "client-b", service, 81)
		})

		It("should support allow-all policy [Feature:WindowsPolicy]", func() {
			By("Creating a network policy which allows all traffic.")
			policy := &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "allow-all",
				},
				Spec: networkingv1.NetworkPolicySpec{
					// Allow all traffic
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{},
					},
					Ingress: []networkingv1.NetworkPolicyIngressRule{{}},
				},
			}
			policy, err := f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).Create(policy)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupNetworkPolicy(f, policy)

			By("Testing pods can connect to both ports when an 'allow-all' policy is present.")
			testCanConnect(f, f.Namespace, "client-a", service, 80)
			testCanConnect(f, f.Namespace, "client-b", service, 81)
		})

		It("should allow ingress access on one named port", func() {
			policy := &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "allow-client-a-via-named-port-ingress-rule",
				},
				Spec: networkingv1.NetworkPolicySpec{
					// Apply this policy to the Server
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"pod-name": podServer.Name,
						},
					},
					// Allow traffic to only one named port: "serve-80".
					Ingress: []networkingv1.NetworkPolicyIngressRule{{
						Ports: []networkingv1.NetworkPolicyPort{{
							Port: &intstr.IntOrString{Type: intstr.String, StrVal: "serve-80"},
						}},
					}},
				},
			}

			policy, err := f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).Create(policy)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupNetworkPolicy(f, policy)

			By("Creating client-a which should be able to contact the server.", func() {
				testCanConnect(f, f.Namespace, "client-a", service, 80)
			})
			By("Creating client-b which should not be able to contact the server on port 81.", func() {
				testCannotConnect(f, f.Namespace, "client-b", service, 81)
			})
		})

		It("should allow egress access on one named port [Feature:NetworkPolicy]", func() {
			framework.SkipUnlessServerVersionGTE(egressVersion, f.ClientSet.Discovery())
			clientPodName := "client-a"
			protocolUDP := v1.ProtocolUDP
			policy := &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "allow-client-a-via-named-port-egress-rule",
				},
				Spec: networkingv1.NetworkPolicySpec{
					// Apply this policy to client-a
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"pod-name": clientPodName,
						},
					},
					// Allow traffic to only one named port: "serve-80".
					Egress: []networkingv1.NetworkPolicyEgressRule{{
						Ports: []networkingv1.NetworkPolicyPort{
							{
								Port: &intstr.IntOrString{Type: intstr.String, StrVal: "serve-80"},
							},
							// Allow DNS look-ups
							{
								Protocol: &protocolUDP,
								Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: 53},
							},
						},
					}},
				},
			}

			policy, err := f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).Create(policy)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupNetworkPolicy(f, policy)

			By("Creating client-a which should be able to contact the server.", func() {
				testCanConnect(f, f.Namespace, clientPodName, service, 80)
			})
			By("Creating client-a which should not be able to contact the server on port 81.", func() {
				testCannotConnect(f, f.Namespace, clientPodName, service, 81)
			})
		})
	})
})

func testCanConnect(f *framework.Framework, ns *v1.Namespace, podName string, service *v1.Service, targetPort int) {
	target := fmt.Sprintf("%s.%s:%d", service.Name, service.Namespace, targetPort)
	// This is a hack for windows to use Service's ClusterIP,instead of DNS name.
	if winctl.RunningWindowsTest() {
		podTarget, serviceTarget := winctl.GetTarget(f, service, targetPort)
		if os.Getenv("WINDOWS_OS") == "1903" {
			// use Service ClusterIP to check connectivity with server
			fmt.Printf("Checking connectivity with serviceIP: %s\n", serviceTarget)
			testCanConnectX(f, ns, podName, service, serviceTarget, func(pod *v1.Pod) {}, func() {})
		}
		// assign podIP to check connectivity
		target = podTarget
		fmt.Printf("Checking connectivity with podIP: %s\n", target)
	}
	testCanConnectX(f, ns, podName, service, target, func(pod *v1.Pod) {}, func() {})
}
func testCanConnectX(f *framework.Framework, ns *v1.Namespace, podName string, service *v1.Service, target string, podCustomizer func(pod *v1.Pod), onFailure func()) {
	By(fmt.Sprintf("Creating client pod with base name %s that should successfully connect to %s.", podName, service.Name))
	podClient := createNetworkClientPodX(f, ns, podName, target, podCustomizer)
	defer func() {
		By(fmt.Sprintf("Cleaning up the pod %s", podClient.Name))
		if err := f.ClientSet.CoreV1().Pods(ns.Name).Delete(podClient.Name, nil); err != nil {
			framework.Failf("unable to cleanup pod %v: %v", podClient.Name, err)
		}
	}()

	framework.Logf("Waiting for %s to complete.", podClient.Name)
	err := framework.WaitForPodNoLongerRunningInNamespace(f.ClientSet, podClient.Name, ns.Name)
	if err != nil {
		framework.Logf("Pod did not FINISH as expected, skipping to diags collection: %v", err)
	}
	if err == nil {
		framework.Logf("Waiting for %s to report success.", podClient.Name)
		err = framework.WaitForPodSuccessInNamespace(f.ClientSet, podClient.Name, ns.Name)
	}
	if err != nil {
		defer framework.MaybeWaitForInvestigation()

		framework.Logf("FAIL: Pod %s should be able to connect to service %s, but was not able to connect.", podClient.Name, service.Name)
		By("Collecting diagnostics after failure")

		// Run caller's failure hook first.
		onFailure()

		// Collect/log Calico diags.
		logErr := calico.LogCalicoDiagsForPodNode(f, ns.Name, podClient.Name)
		if logErr != nil {
			framework.Logf("Error getting Calico diags: %v", logErr)
		}

		// Collect pod logs when we see a failure.
		logs, logErr := framework.GetPodLogs(f.ClientSet, ns.Name, podClient.Name, fmt.Sprintf("%s-container", podName))
		if logErr != nil {
			framework.Failf("Error getting container logs: %s", logErr)
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

		framework.Failf("Pod %s should be able to connect to service %s, but was not able to connect.\nPod logs:\n%s\n\n Current NetworkPolicies:\n\t%v\n\n Pods:\n\t%v\n\n", podClient.Name, service.Name, logs, policies.Items, pods)

		// Dump debug information for the test namespace.
		framework.DumpDebugInfo(f.ClientSet, f.Namespace.Name)
	}
}

func testCannotConnect(f *framework.Framework, ns *v1.Namespace, podName string, service *v1.Service, targetPort int) {
	target := fmt.Sprintf("%s.%s:%d", service.Name, service.Namespace, targetPort)
	// This is a hack for windows to use Service's ClusterIP,instead of DNS name
	if winctl.RunningWindowsTest() {
		podTarget, serviceTarget := winctl.GetTarget(f, service, targetPort)
		if os.Getenv("WINDOWS_OS") == "1903" {
			// use Service ClusterIP to check connectivity with server
			fmt.Printf("Checking connectivity with serviceIP :%s \n", serviceTarget)
			testCannotConnectX(f, ns, podName, service, serviceTarget, func(pod *v1.Pod) {})
		}
		// assign podIP to check connectivity
		target = podTarget
		fmt.Printf("Checking connectivity with podIP :%s \n", target)
	}
	testCannotConnectX(f, ns, podName, service, target, func(pod *v1.Pod) {})
}
func testCannotConnectX(f *framework.Framework, ns *v1.Namespace, podName string, service *v1.Service, target string, podCustomizer func(pod *v1.Pod)) {
	By(fmt.Sprintf("Creating client pod with base name %s that should not be able to connect to %s.", podName, service.Name))
	podClient := createNetworkClientPodX(f, ns, podName, target, podCustomizer)
	defer func() {
		By(fmt.Sprintf("Cleaning up the pod %s", podClient.Name))
		if err := f.ClientSet.CoreV1().Pods(ns.Name).Delete(podClient.Name, nil); err != nil {
			framework.Failf("unable to cleanup pod %v: %v", podClient.Name, err)
		}
	}()

	framework.Logf("Waiting for %s to complete.", podClient.Name)
	err := framework.WaitForPodSuccessInNamespace(f.ClientSet, podClient.Name, ns.Name)

	// We expect an error here since it's a cannot connect test.
	// Dump debug information if the error was nil.
	if err == nil {
		defer framework.MaybeWaitForInvestigation()

		framework.Logf("FAIL: Pod %s should not be able to connect to service %s, but was able to connect.", podClient.Name, service.Name)
		By("Collecting diagnostics after failure")

		// Collect pod logs when we see a failure.
		logs, logErr := framework.GetPodLogs(f.ClientSet, ns.Name, podClient.Name, fmt.Sprintf("%s-container", podName))
		if logErr != nil {
			framework.Failf("Error getting container logs: %s", logErr)
		}

		// Collect current NetworkPolicies applied in the test namespace.
		policies, err := f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).List(metav1.ListOptions{})
		if err != nil {
			framework.Logf("error getting current NetworkPolicies for %s namespace: %s", f.Namespace.Name, err)
		}

		// Collect current NetworkPolicies applied in the pod namespace.
		policies2, err := f.ClientSet.NetworkingV1().NetworkPolicies(ns.Name).List(metav1.ListOptions{})
		if err != nil {
			framework.Logf("error getting current NetworkPolicies for %s namespace: %s", ns.Name, err)
		}

		// Collect the list of pods running in the test namespace.
		podsInNS, err := framework.GetPodsInNamespace(f.ClientSet, ns.Name, map[string]string{})
		if err != nil {
			framework.Logf("error getting pods for %s namespace: %s", f.Namespace.Name, err)
		}

		pods := []string{}
		for _, p := range podsInNS {
			pods = append(pods, fmt.Sprintf("Pod: %s, Status: %s\n", p.Name, p.Status.String()))
		}

		framework.Failf("Pod %s should not be able to connect to service %s, but was able to connect.\n"+
			"Pod logs:\n%s\n\n Current NetworkPolicies:\n\t%v\n\nCurrent NetworkPolicies (pod NS):\n\t%v\n\n Pods:\n\t %v\n\n",
			podClient.Name, service.Name, logs, policies.Items, policies2.Items, pods)

		// Dump debug information for the test namespace.
		framework.DumpDebugInfo(f.ClientSet, f.Namespace.Name)
	}
}

// Create a server pod with a listening container for each port in ports[].
// Will also assign a pod label with key: "pod-name" and label set to the given podname for later use by the network
// policy.
func createServerPodAndService(f *framework.Framework, namespace *v1.Namespace, podName string, ports []int) (*v1.Pod, *v1.Service) {
	return createServerPodAndServiceX(f, namespace, podName, ports, func(pod *v1.Pod) {}, func(_ *v1.Service) {})
}
func createHostNetworkedServerPodAndService(f *framework.Framework, namespace *v1.Namespace, podName string, ports []int) (*v1.Pod, *v1.Service) {
	return createServerPodAndServiceX(f, namespace, podName, ports, func(pod *v1.Pod) {
		pod.Spec.HostNetwork = true
	}, func(_ *v1.Service) {})
}
func createServerPodAndServiceX(f *framework.Framework, namespace *v1.Namespace, podName string, ports []int, podCustomizer func(pod *v1.Pod), serviceCustomizer func(svc *v1.Service)) (*v1.Pod, *v1.Service) {
	// Because we have a variable amount of ports, we'll first loop through and generate our Containers for our pod,
	// and ServicePorts.for our Service.
	var imageUrl string
	containers := []v1.Container{}
	servicePorts := []v1.ServicePort{}
	var nodeselector = map[string]string{}
	imagePull := v1.PullAlways
	if winctl.RunningWindowsTest() {
		imageUrl = winctl.GetPorterImage()
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

	// Windows 1903 vxlan has an issue on connections between windows node to windows pod.
	// Turn readiness off if that is the case.
	if winctl.RunningWindowsTest() && winctl.DisableReadiness() {
		framework.Logf("Do not enable readiness check for windows vxlan")
		for i, _ := range containers {
			containers[i].ReadinessProbe = nil
		}
	}

	By(fmt.Sprintf("Creating a server pod %s in namespace %s", podName, namespace.Name))
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
				"pod-name": podName,
			},
		},
		Spec: v1.PodSpec{
			Containers:    containers,
			RestartPolicy: v1.RestartPolicyNever,
			NodeSelector:  nodeselector,
		},
	}
	// Allow customization of the pod spec before creation.
	if podCustomizer != nil {
		podCustomizer(pod)
	}
	pod, err := f.ClientSet.CoreV1().Pods(namespace.Name).Create(pod)
	Expect(err).NotTo(HaveOccurred())
	framework.Logf("Created pod %v", pod.ObjectMeta.Name)

	svcName := fmt.Sprintf("svc-%s", podName)
	By(fmt.Sprintf("Creating a service %s for pod %s in namespace %s", svcName, podName, namespace.Name))
	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: svcName,
		},
		Spec: v1.ServiceSpec{
			Ports: servicePorts,
			Selector: map[string]string{
				"pod-name": podName,
			},
		},
	}
	serviceCustomizer(svc)
	svc, err = f.ClientSet.CoreV1().Services(namespace.Name).Create(svc)
	Expect(err).NotTo(HaveOccurred())
	framework.Logf("Created service %s", svc.Name)

	return pod, svc
}

func cleanupServerPodAndService(f *framework.Framework, pod *v1.Pod, service *v1.Service) {
	By("Cleaning up the server.")
	if err := f.ClientSet.CoreV1().Pods(pod.Namespace).Delete(pod.Name, nil); err != nil {
		framework.Failf("unable to cleanup pod %v: %v", pod.Name, err)
	}
	//This is a hack again to clear map created for Servicename and endpointIP
	//Clean up winctl service endpoint map here
	if winctl.RunningWindowsTest() {
		By("Cleaning up the ServiceEndpointIP map.")
		winctl.CleanupServiceEndpointMap()
	}
	By("Cleaning up the server's service.")
	if err := f.ClientSet.CoreV1().Services(service.Namespace).Delete(service.Name, nil); err != nil {
		framework.Failf("unable to cleanup svc %v: %v", service.Name, err)
	}
}

// Create a client pod which will attempt a netcat to the provided service, on the specified port.
// This client will attempt a one-shot connection, then die, without restarting the pod.
// Test can then be asserted based on whether the pod quit with an error or not.
func createNetworkClientPod(f *framework.Framework, namespace *v1.Namespace, podName string, targetService *v1.Service, targetPort int, expectFail bool) *v1.Pod {
	target := fmt.Sprintf("%s.%s:%d", targetService.Name, targetService.Namespace, targetPort)
	return createNetworkClientPodX(f, namespace, podName, target, func(pod *v1.Pod) {})
}
func createNetworkClientPodX(f *framework.Framework, namespace *v1.Namespace, podNameBase string, target string, podCustomizer func(pod *v1.Pod)) *v1.Pod {
	var imageUrl, commandStr string
	var podArgs []string
	var cmd string
	var nodeselector = map[string]string{}

	// Randomize pod names to avoid clashes with previous tests.
	podName := calico.GenerateRandomName(podNameBase)

	imagePull := v1.PullAlways
	if winctl.RunningWindowsTest() {
		imageUrl, commandStr = winctl.GetClientImageAndCommand()
		podArgs = append(podArgs, commandStr, "-Command")
		cmd = fmt.Sprintf("$sb={Invoke-WebRequest %s -UseBasicParsing -TimeoutSec 3 -DisableKeepAlive}; "+
			"For ($i=0; $i -lt 5; $i++) { sleep 5; "+
			"try {& $sb} catch { echo failed loop $i ; continue }; exit 0 ; }; exit 1", target)
		nodeselector["beta.kubernetes.io/os"] = "windows"
		imagePull = v1.PullIfNotPresent
	} else {
		imageUrl = "busybox"
		podArgs = append(podArgs, "/bin/sh", "-c")
		cmd = fmt.Sprintf("for i in $(seq 1 5); do wget -T 5 %s -O - && exit 0 || sleep 1; done; cat /etc/resolv.conf; exit 1", target)
		nodeselector["beta.kubernetes.io/os"] = "linux"
	}
	podArgs = append(podArgs, cmd)
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
				"pod-name": podNameBase,
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
	}
	if podCustomizer != nil {
		podCustomizer(pod)
	}
	var err error
	pod, err = f.ClientSet.CoreV1().Pods(namespace.Name).Create(pod)

	Expect(err).NotTo(HaveOccurred())

	return pod
}

func cleanupNetworkPolicy(f *framework.Framework, policy *networkingv1.NetworkPolicy) {
	By("Cleaning up the policy.")
	if err := f.ClientSet.NetworkingV1().NetworkPolicies(policy.Namespace).Delete(policy.Name, nil); err != nil {
		framework.Failf("unable to cleanup policy %v: %v", policy.Name, err)
	}
}
