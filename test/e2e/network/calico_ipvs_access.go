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
	"strings"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/kubernetes/test/e2e/framework"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = SIGDescribe("IPVSAccess", func() {

	// Test different access patterns from a pod to a service.
	//  +--------+     +------------+     +--------+
	//  | server |<----| service IP |<----| client |
	//  +--------+     +------------+     +--------+
	//                                  (a) client node == server node
	//                                  (b) client node != server node
	//  +--------+     +-----------+  	         +--------+
	//  | server |<----| node port |<----------------| client |
	//  +--------+     +-----------+                 +--------+
	//          (c) NodePort node == server node     (c1) client node == server node
	//                                               (c2) client node != server node
	//
	//          (d) NodePort node != server node     (d1) client node == server node
	//                                               (d2) client node == NodePort node
	//                                               (d3) client node != server node and
	//                                                    client node != NodePort node

	const canConnect = true
	const hostNetwork = true // An indication that a client pod is running in host network namespace.
	const noSnat = true      // An indication that packets are going from a client pod to a server pod without SNAT.
	var nsName string
	var proxyMode string
	var jig *framework.ServiceTestJig

	// The ipvs test requires three schedulable nodes. Back end server pod is running on node3.
	type IPVSTestConfig struct {
		node1Name    string
		node1IP      string
		node2Name    string
		node2IP      string
		svcNodeName  string
		svcNodeIP    string
		svcClusterIP string
		svcPort      int
		svcNodePort  int
	}
	var ipvsTC IPVSTestConfig

	f := framework.NewDefaultFramework("ipvs-access")

	Context("Workload ingress test", func() {

		BeforeEach(func() {
			nsName = f.Namespace.Name

			if proxyMode == "" {
				var err error
				if proxyMode, err = framework.ProxyMode(f); err != nil {
					framework.Failf("Couldn't detect KubeProxy mode.")
				}
				framework.Logf("kube-proxy is running in %s mode.", proxyMode)
			}

			if proxyMode != "ipvs" {
				framework.Skipf("IPVS access test requires kube-proxy running in ipvs mode.")
			}

			jig = framework.NewServiceTestJig(f.ClientSet, "ipvs-access")
			nodes := jig.GetNodes(3)
			if len(nodes.Items) == 0 {
				framework.Skipf("No nodes exist, can't continue test.")
			}
			if len(nodes.Items) < 3 {
				framework.Skipf("Less than three schedulable nodes exist, can't continue test.")
			}
			nodeNames, nodeIPs := getNodesInfo(nodes)
			Expect(len(nodeNames)).To(Equal(3))
			Expect(len(nodeIPs)).To(Equal(3))

			svcPort := 8080
			svcClusterIP, svcNodePort := setupPodSeviceOnNode(f, nsName, jig, nodeNames[2], svcPort)

			ipvsTC = IPVSTestConfig{
				node1Name:    nodeNames[0],
				node1IP:      nodeIPs[0],
				node2Name:    nodeNames[1],
				node2IP:      nodeIPs[1],
				svcNodeName:  nodeNames[2],
				svcNodeIP:    nodeIPs[2],
				svcClusterIP: svcClusterIP,
				svcPort:      svcPort,
				svcNodePort:  svcNodePort,
			}

			framework.Logf("IPVS test config %#v", ipvsTC)

		})

		AfterEach(func() {
			cleanupPodService(f, nsName, jig)
		})

		doServiceAccessTestWithPolicy := func(serverLabels map[string]string, nodeName string, destIP string, destPort int, useHostNetwork bool, withNoSnat bool) {
			By("Traffic is allowed from pod 'client-a' and 'client-b'.")
			execSourceIPTestWithExpect(f, nsName, "client-a", nodeName, destIP, destPort, useHostNetwork, withNoSnat, canConnect)
			execSourceIPTestWithExpect(f, nsName, "client-b", nodeName, destIP, destPort, useHostNetwork, withNoSnat, canConnect)

			By("Creating a network policy for the server which allows traffic from pod 'client-b'.")
			policy := &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "allow-client-b-via-pod-selector",
				},
				Spec: networkingv1.NetworkPolicySpec{
					// Apply this policy to the Server
					PodSelector: metav1.LabelSelector{
						MatchLabels: serverLabels,
					},
					// Allow traffic only from client-b
					Ingress: []networkingv1.NetworkPolicyIngressRule{{
						From: []networkingv1.NetworkPolicyPeer{{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"pod-name": "client-b",
								},
							},
						}},
					}},
				},
			}

			policy, err := f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).Create(policy)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupNetworkPolicy(f, policy)

			By("Traffic is not allowed from pod 'client-a'.")
			execSourceIPTestWithExpect(f, nsName, "client-a", nodeName, destIP, destPort, useHostNetwork, withNoSnat, !canConnect)
			if !useHostNetwork {
				if withNoSnat {
					By("Non host network pods and no snat. Traffic is allowed from pod 'client-b'.")
					execSourceIPTestWithExpect(f, nsName, "client-b", nodeName, destIP, destPort, useHostNetwork, withNoSnat, canConnect)
				} else {
					// SNAT: There will be couple of cases that a packet could be SNATed, for instance,
					// the node port cases if the node port node is different from both the server node
					// and the client node. When there is an SNAT, it means that the NetworkPolicy that we
					// configure in this test will not be effective in allowing client-b to access the
					// server, because the source IP seen on the server node is not the same as the source
					// IP that the NetworkPolicy allows.
					By("Non host network pods with snat. Traffic is not allowed from pod 'client-b'.")
					execSourceIPTestWithExpect(f, nsName, "client-b", nodeName, destIP, destPort, useHostNetwork, withNoSnat, !canConnect)
				}
			} else {
				// Host-networked client: If the client is host-networked, the source IP
				// for an attempted connection to the server will not be the client's pod
				// IP, so again the NetworkPolicy will be ineffective.
				By("Host network pods. Traffic is not allowed from both 'client-a' and 'client-b'.")
				execSourceIPTestWithExpect(f, nsName, "client-b", nodeName, destIP, destPort, useHostNetwork, withNoSnat, !canConnect)
			}

		}

		It("Test accessing service ip from a pod on the same node with server pod [Feature:IPVSAccess][Feature:IPVSServiceIP]", func() {
			doServiceAccessTestWithPolicy(jig.Labels, ipvsTC.svcNodeName, ipvsTC.svcClusterIP, ipvsTC.svcPort, !hostNetwork, noSnat)
		})

		It("Test accessing service ip from a pod running host network on the same node with server pod [Feature:IPVSAccess][Feature:IPVSServiceIP]", func() {
			doServiceAccessTestWithPolicy(jig.Labels, ipvsTC.svcNodeName, ipvsTC.svcClusterIP, ipvsTC.svcPort, hostNetwork, noSnat)
		})

		It("Test accessing service ip from a pod on a different node with server pod [Feature:IPVSAccess][Feature:IPVSServiceIP]", func() {
			doServiceAccessTestWithPolicy(jig.Labels, ipvsTC.node2Name, ipvsTC.svcClusterIP, ipvsTC.svcPort, !hostNetwork, noSnat)
		})

		It("Test accessing service ip from a pod running host network on a different node with server pod [Feature:IPVSAccess][Feature:IPVSServiceIP]", func() {
			doServiceAccessTestWithPolicy(jig.Labels, ipvsTC.node2Name, ipvsTC.svcClusterIP, ipvsTC.svcPort, hostNetwork, noSnat)
		})

		It("Test accessing NodePort (Node running server pod) from a pod on the same node [Feature:IPVSAccess][Feature:IPVSNodePort]", func() {
			doServiceAccessTestWithPolicy(jig.Labels, ipvsTC.svcNodeName, ipvsTC.svcNodeIP, ipvsTC.svcNodePort, !hostNetwork, noSnat)
		})

		It("Test accessing NodePort (Node running server pod) from a pod running host network on the same node [Feature:IPVSAccess][Feature:IPVSNodePort]", func() {
			doServiceAccessTestWithPolicy(jig.Labels, ipvsTC.svcNodeName, ipvsTC.svcNodeIP, ipvsTC.svcNodePort, hostNetwork, noSnat)
		})

		It("Test accessing NodePort (Node running server pod) from a pod on a different node [Feature:IPVSAccess][Feature:IPVSNodePort]", func() {
			doServiceAccessTestWithPolicy(jig.Labels, ipvsTC.node2Name, ipvsTC.svcNodeIP, ipvsTC.svcNodePort, !hostNetwork, !noSnat)
		})

		It("Test accessing NodePort (Node running server pod) from a pod running host network on a different node [Feature:IPVSAccess][Feature:IPVSNodePort]", func() {
			doServiceAccessTestWithPolicy(jig.Labels, ipvsTC.node2Name, ipvsTC.svcNodeIP, ipvsTC.svcNodePort, hostNetwork, noSnat)
		})

		It("Test accessing NodePort (Node not running server pod) from a pod on the same node [Feature:IPVSAccess][Feature:IPVSNodePort]", func() {
			doServiceAccessTestWithPolicy(jig.Labels, ipvsTC.node2Name, ipvsTC.node2Name, ipvsTC.svcNodePort, !hostNetwork, noSnat)
		})

		It("Test accessing NodePort (Node not running server pod) from a pod running host network on the same node [Feature:IPVSAccess][Feature:IPVSNodePort]", func() {
			doServiceAccessTestWithPolicy(jig.Labels, ipvsTC.node2Name, ipvsTC.node2Name, ipvsTC.svcNodePort, hostNetwork, noSnat)
		})

		/* The following test cases won't work because of kubernetes ipvs issue https://github.com/kubernetes/kubernetes/issues/53393
		It("Test accessing NodePort (Node not running server pod) from a pod on a third node [Feature:IPVSAccess][Feature:IPVSNodePort]", func() {
			doServiceAccessTestWithPolicy(jig.Labels, ipvsTC.node1Name, ipvsTC.node2Name, ipvsTC.svcNodePort, !hostNetwork, noSnat)
		})

		It("Test accessing NodePort (Node not running server pod) from a pod running host network on a third node [Feature:IPVSAccess][Feature:IPVSNodePort]", func() {
			doServiceAccessTestWithPolicy(jig.Labels, ipvsTC.node1Name, ipvsTC.node2Name, ipvsTC.svcNodePort, hostNetwork, noSnat)
		})
		*/

	})

})

func getNodesInfo(nodes *v1.NodeList) ([]string, []string) {
	nodeNames := []string{}
	nodeIPs := []string{}
	for _, node := range nodes.Items {
		nodeNames = append(nodeNames, node.Name)
		addrs := framework.GetNodeAddresses(&node, v1.NodeInternalIP)
		if len(addrs) == 0 {
			framework.Failf("node %s failed to report a valid ip address\n", node.Name)
		}
		nodeIPs = append(nodeIPs, addrs[0])
	}
	return nodeNames, nodeIPs
}

func execSourceIPTestWithExpect(f *framework.Framework, nsName, podName, nodeName, serviceIP string, servicePort int, useHostNetwork bool, withNoSnat bool, canConnect bool) {
	framework.Logf("Creating an exec pod %s on node %s, expect can connect to be %t", podName, nodeName, canConnect)
	execPodName := framework.CreateExecPodOrFail(f.ClientSet, nsName, fmt.Sprintf("execpod-sourceip-%s", nodeName), func(pod *v1.Pod) {
		pod.ObjectMeta.Labels = map[string]string{"pod-name": podName}
		pod.Spec.NodeName = nodeName
		pod.Spec.HostNetwork = useHostNetwork
	})

	defer func() {
		framework.Logf("Cleaning up the exec pod")
		err := f.ClientSet.Core().Pods(nsName).Delete(execPodName, nil)
		Expect(err).NotTo(HaveOccurred())
	}()
	execPod, err := f.ClientSet.Core().Pods(nsName).Get(execPodName, metav1.GetOptions{})
	framework.ExpectNoError(err)

	var stdout string
	framework.Logf("Waiting for wget %s:%d to be successful.", serviceIP, servicePort)
	cmd := fmt.Sprintf("for i in $(seq 1 5); do wget -T 2 %s:%d -O - | grep client_address && exit 0 || sleep 1; done; exit 1", serviceIP, servicePort)
	stdout, err = framework.RunHostCmd(execPod.Namespace, execPod.Name, cmd)
	framework.Logf("PodIP %s, output %s.", execPod.Status.PodIP, stdout)
	if err != nil {
		framework.Logf("Can not connect.Wget got err: %v.", err)
		Expect(canConnect).To(BeFalse())
		return
	}
	Expect(canConnect).To(BeTrue())

	// The stdout return from RunHostCmd seems to come with "\n", so TrimSpace is needed.
	// Desired stdout in this format: client_address=x.x.x.x
	outputs := strings.Split(strings.TrimSpace(stdout), "=")
	if len(outputs) != 2 {
		// Fail the test if output format is unexpected.
		framework.Failf("exec pod returned unexpected stdout format: [%s]\n", stdout)
	}
	sourceIP := outputs[1]

	if !useHostNetwork && withNoSnat {
		// Verify source ip if exec pod is not running in host network namespace
		// and has not been snatted. With exec pod running in host network namespace
		// and the destination ip is a virtual ip (service ip), exec pod ip may be
		// different with the real source ip. For instance, If the host happens to
		// have a local ip 10.x.x.x which is closer to service ip 10.100.x.x than
		// pod id 192.168.x.x, this 10.x.x.x may be used by kernel as source ip.
		Expect(execPod.Status.PodIP).To(Equal(sourceIP))
	}
}

func setupPodSeviceOnNode(f *framework.Framework, nsName string, jig *framework.ServiceTestJig, nodeName string, svcPort int) (string, int) {
	serviceName := jig.Name
	By("creating a TCP service " + serviceName + " in namespace " + nsName + ".")
	svc := jig.CreateTCPServiceWithPort(nsName, func(svc *v1.Service) {
		svc.Spec.Type = v1.ServiceTypeNodePort

	}, int32(svcPort))
	jig.SanityCheckService(svc, v1.ServiceTypeNodePort)
	svcIP := svc.Spec.ClusterIP
	svcNodePort := int(svc.Spec.Ports[0].NodePort)

	podName := jig.Name
	By("Creating a backend server pod " + podName + " which echoes back source ip.")
	jig.LaunchEchoserverPodOnNode(f, nodeName, podName)

	// Waiting for service to expose endpoint.
	framework.ValidateEndpointsOrFail(f.ClientSet, nsName, serviceName, framework.PortsByPodName{podName: {svcPort}})

	return svcIP, svcNodePort
}

func cleanupPodService(f *framework.Framework, nsName string, jig *framework.ServiceTestJig) {
	By("Cleaning up echo service and backend pod.")
	err := f.ClientSet.Core().Services(nsName).Delete(jig.Name, nil)
	Expect(err).NotTo(HaveOccurred())
	err = f.ClientSet.Core().Pods(nsName).Delete(jig.Name, nil)
	Expect(err).NotTo(HaveOccurred())
}
