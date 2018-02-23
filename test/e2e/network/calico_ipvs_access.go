/*
Copyright (c) 2017-2018 Tigera, Inc. All rights reserved.
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

	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"
)

var _ = SIGDescribe("IPVSEgress", func() {

	// Test that Calico workload egress policy still takes effect when kube-proxy is using IPVS
	// to implement k8s Service semantics.  We set up the following pods on two nodes:
	//
	// +-------------------+ +----------+
	// |       node0       | |  node1   |
	// | +------+ +------+ | | +------+ |
	// | | pod0 | | pod1 | | | | pod2 | |
	// | +------+ +------+ | | +------+ |
	// +-------------------+ +----------+
	//
	// For each pod we set up a NodePort Service with an externalIP - i.e. where the Service
	// only forwards to that particular pod.  Such a Service can be accessed via its clusterIP,
	// or its NodePort, or its externalIP.  For tests using the externalIP, the target Service's
	// externalTrafficPolicy is set to either Local or Cluster: this affects the detail of k8s
	// iptables processing when forwarding to that Service.
	//
	// Then we test the following access patterns, with and without egress policy for pod0:
	//
	// +------+
	// | pod0 |----+---> clusterIP for Service(pod0)
	// +------+    |
	//             +---> clusterIP for Service(pod1)
	//             |
	//             +---> clusterIP for Service(pod2)
	//             |
	//             +---> node0:<NodePort for Service(pod0)>
	//             |
	//             +---> node1:<NodePort for Service(pod0)>
	//             |
	//             +---> node0:<NodePort for Service(pod1)>
	//             |
	//             +---> node1:<NodePort for Service(pod1)>
	//             |
	//             +---> node0:<NodePort for Service(pod2)>
	//             |
	//             +---> node1:<NodePort for Service(pod2)>
	//             |
	//             +---> externalIP for Service(pod0,externalTrafficPolicy=Local)
	//             |
	//             +---> externalIP for Service(pod0,externalTrafficPolicy=Cluster)
	//             |
	//             +---> externalIP for Service(pod1,externalTrafficPolicy=Local)
	//             |
	//             +---> externalIP for Service(pod1,externalTrafficPolicy=Cluster)
	//             |
	//             +---> externalIP for Service(pod2,externalTrafficPolicy=Cluster)
	//             |
	//             +---> a host-networked pod on node0
	//             |
	//             +---> a host-networked pod on node1
	//
	// For each pattern we follow the test procedure:
	//
	// - With no pod0 egress policy, pod0 can access its target.
	//
	// - Add default-deny egress policy applying to pod0.  Now pod0 cannot access its target.
	//
	// - Add policy that applies to pod0 and allows egress to the target pod, identified by a
	//   label that is specific to that pod.  Now pod0 can access its target again.
	//
	// - Remove target-specific allow policy.  Now pod0 cannot access its target.
	//
	// - Remove default-deny egress policy.  Now pod0 can access its target again.

	f := framework.NewDefaultFramework("ipvs-egress")
	var (
		jig       *framework.ServiceTestJig
		nodeNames []string
		nodeIPs   []string
	)

	BeforeEach(func() {
		framework.Logf("BeforeEach for IPVS Egress")
		jig = framework.NewServiceTestJig(f.ClientSet, "ipvs-egress")
		nodes := jig.GetNodes(2)
		if len(nodes.Items) == 0 {
			framework.Skipf("No nodes exist, can't continue test.")
		}
		if len(nodes.Items) < 2 {
			framework.Skipf("Less than two schedulable nodes exist, can't continue test.")
		}
		nodeNames, nodeIPs = getNodesInfo(nodes)
		Expect(len(nodeNames)).To(Equal(2))
		Expect(len(nodeIPs)).To(Equal(2))
	})

	type egressTest struct {
		// Any tweak needed to the target Service definition.
		svcTweak func(svc *v1.Service)
		// dstPod is the number of the destination pod, as per the diagram above.
		dstPod int
		// True if the destination pod should be host-networked.
		dstHostNetworked bool
		// How to access the Service.
		accessType string
	}

	describeEgressTest := func(c egressTest) func() {
		return func() {
			var (
				client      interface{}
				target      string
				applyLabels map[string]string
				expectSNAT  bool
				reachability string
			)

			expectAccessAllowed := func() {
				if expectSNAT {
					reachability = reachableWithSNAT
				} else {
					reachability = reachableWithoutSNAT
				}
				testConnection(f, client, target, reachability)
			}

			expectAccessDenied := func() {
				testConnection(f, client, target, notReachable)
			}

			BeforeEach(func() {
				// Whenever we connect back to the same pod, we need SNAT or the pod will drop the
				// packet as a martian.
				expectSNAT = c.dstPod == 0

				// We only start up a single target pod, but we place it either on node[0] or node[1] depending
				// on the test.
				var node string
				if c.dstPod <= 1 {
					// Pods 0 and 1 belong on the first node.
					node = nodeNames[0]
				} else {
					// Pod 2 is on the second node.
					node = nodeNames[1]
				}
				svcPort := 8080
				svcClusterIP, svcNodePort, dstPod := setupPodServiceOnNode(f, jig, node, svcPort, c.svcTweak, c.dstHostNetworked)

				// Figure out the correct target to pass to wget, depending on the destination and type of test.
				// We may also flip the expectSNAT flag here if the scenario requires it.
				if c.accessType == "cluster IP" {
					target = fmt.Sprintf("%v:%v", svcClusterIP, svcPort)
					if c.dstHostNetworked {
						// TODO If the destination pod is host networked, then, by the time Calico
						// decides whether to do "NAT-outgoing", we'll see the destination as
						// outside the cluster.
						expectSNAT = true
					}
				} else if c.accessType == "node0 NodePort" {
					expectSNAT = true
					target = fmt.Sprintf("%v:%v", nodeIPs[0], svcNodePort)
				} else if c.accessType == "node1 NodePort" {
					expectSNAT = true
					target = fmt.Sprintf("%v:%v", nodeIPs[1], svcNodePort)
				} else if c.accessType == "external IP" {
					expectSNAT = true
					target = fmt.Sprintf("%v:%v", nodeIPs[0], svcPort)
				} else {
					panic("Unhandled accessType: " + c.accessType)
				}

				if c.dstPod == 0 {
					// We're doing a loopback test where the pod accesses itself via a service of some kind.
					// Use the target pod we just created as the source pod.
					applyLabels = jig.Labels
					client = dstPod
				} else {
					// We're not doing a loopback test so we need to make a pod0 too.
					client = jig.LaunchEchoserverPodOnNode(f, nodeNames[0], "ipvs-egress-source", false)
					applyLabels = map[string]string{"pod-name": "ipvs-egress-source"}
				}
			})

			It("should correctly implement NetworkPolicy", func() {
				By("allowing connection with no NetworkPolicy")
				expectAccessAllowed()

				By("denying traffic after installing a default-deny policy")
				defaultDenyPolicy := &networkingv1.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "egress-default-deny",
					},
					Spec: networkingv1.NetworkPolicySpec{
						// Apply this policy to the source (pod0).
						PodSelector: metav1.LabelSelector{
							MatchLabels: applyLabels,
						},
						// Say that it's an egress policy, but don't allow any
						// destinations; that has the effect of being a default deny.
						PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
					},
				}
				defaultDenyPolicy, err := f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).Create(defaultDenyPolicy)
				Expect(err).NotTo(HaveOccurred())
				expectAccessDenied()

				if c.dstHostNetworked {
					By("Skipping policy tests for host networked target - not implemented")
					return
				}
				if c.accessType == "node1 NodePort" {
					By("Skipping policy tests for remote node port - NAT happens on other node")
					return
				}
				if c.accessType == "external IP" {
					By("Skipping policy tests for external IP - we fail to detect the forwarding")
					return
				}

				By("allowing traffic after installing a target-specific policy")
				// Configure policy for pod0 to allow egress to specific target.
				targetAccessPolicy := &networkingv1.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "egress-target-access",
					},
					Spec: networkingv1.NetworkPolicySpec{
						// Apply this policy to the source (pod0).
						PodSelector: metav1.LabelSelector{
							MatchLabels: applyLabels,
						},
						// Say that it's an egress policy.
						PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
						// Allow traffic only to the target service.
						Egress: []networkingv1.NetworkPolicyEgressRule{{
							To: []networkingv1.NetworkPolicyPeer{{
								PodSelector: &metav1.LabelSelector{
									MatchLabels: jig.Labels,
								},
							}},
						}},
					},
				}

				targetAccessPolicy, err = f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).Create(targetAccessPolicy)
				Expect(err).NotTo(HaveOccurred())
				expectAccessAllowed()

				By("denying traffic after removing the target-specific policy")
				cleanupNetworkPolicy(f, targetAccessPolicy)
				targetAccessPolicy = nil
				expectAccessDenied()

				By("allowing traffic after removing the default-deny policy")
				cleanupNetworkPolicy(f, defaultDenyPolicy)
				defaultDenyPolicy = nil
				expectAccessAllowed()
			})
		}
	}

	addExternalIPLocalOnly := func(svc *v1.Service) {
		svc.Spec.ExternalIPs = []string{nodeIPs[0]}
		svc.Spec.ExternalTrafficPolicy = v1.ServiceExternalTrafficPolicyTypeLocal
	}

	setLocalOnly := func(svc *v1.Service) {
		svc.Spec.ExternalTrafficPolicy = v1.ServiceExternalTrafficPolicyTypeLocal
	}

	addExternalIPClusterWide := func(svc *v1.Service) {
		svc.Spec.ExternalIPs = []string{nodeIPs[0]}
		svc.Spec.ExternalTrafficPolicy = v1.ServiceExternalTrafficPolicyTypeCluster
	}

	// ===== Mainline: access services via the cluster IP =====

	Context("scenario-0C0: pod0 -> service clusterIP -> pod0",
		describeEgressTest(egressTest{dstPod: 0, accessType: "cluster IP"}))

	Context("scenario-0C1: pod0 -> service clusterIP -> pod1 (on same node)",
		describeEgressTest(egressTest{dstPod: 1, accessType: "cluster IP"}))

	Context("scenario-0C2: pod0 -> service clusterIP -> pod2 (on other node)",
		describeEgressTest(egressTest{dstPod: 2, accessType: "cluster IP"}))

	// ===== Access via NodePorts =====

	Context("scenario-0N00: pod0 -> node0 NodePort -> pod0",
		describeEgressTest(egressTest{dstPod: 0, accessType: "node0 NodePort"}))

	Context("scenario-0L00: pod0 -> node0 NodePort local-only -> pod0",
		describeEgressTest(egressTest{dstPod: 0, accessType: "node0 NodePort", svcTweak: setLocalOnly}))

	// Needs `iptables -P FORWARD ACCEPT`
	Context("scenario-0N10: pod0 -> node1 NodePort -> pod0",
		describeEgressTest(egressTest{dstPod: 0, accessType: "node1 NodePort"}))

	// Needs `iptables -P FORWARD ACCEPT`
	Context("scenario-0N11: pod0 -> node1 NodePort -> pod1 (hairpin back to same node)",
		describeEgressTest(egressTest{dstPod: 1, accessType: "node1 NodePort"}))

	// Needs `iptables -P FORWARD ACCEPT`
	Context("scenario-0N01: pod0 -> node0 NodePort -> pod1 (pod on same node)",
		describeEgressTest(egressTest{dstPod: 1, accessType: "node0 NodePort"}))

	Context("scenario-0L01: pod0 -> node0 NodePort local-only -> pod1",
		describeEgressTest(egressTest{dstPod: 1, accessType: "node0 NodePort", svcTweak: setLocalOnly}))

	Context("scenario-0N02: pod0 -> node0 NodePort -> pod2 (pod on other node)",
		describeEgressTest(egressTest{dstPod: 2, accessType: "node0 NodePort"}))

	Context("scenario-0N12: pod0 -> node1 NodePort -> pod2 (pod on other node)",
		describeEgressTest(egressTest{dstPod: 2, accessType: "node1 NodePort"}))

	Context("scenario-0L12: pod0 -> node1 NodePort local-only -> pod2",
		describeEgressTest(egressTest{dstPod: 2, accessType: "node1 NodePort", svcTweak: setLocalOnly}))

	// ===== Access via external IPs =====
	// BUG: Calico currently fails to detect externalIP as a forwarding destination so egress policy not applied.

	Context("scenario-0EL1: pod0 -> externalIP local-only -> pod1 (on same node)",
		describeEgressTest(egressTest{dstPod: 1, accessType: "external IP", svcTweak: addExternalIPLocalOnly}))

	Context("scenario-0EC1: pod0 -> externalIP externalTrafficPolicy=Cluster -> pod1 (on same node)",
		describeEgressTest(egressTest{dstPod: 1, accessType: "external IP", svcTweak: addExternalIPClusterWide}))

	Context("scenario-0EC2: pod0 -> externalIP externalTrafficPolicy=Cluster -> pod2 on other node)",
		describeEgressTest(egressTest{dstPod: 2, accessType: "external IP", svcTweak: addExternalIPClusterWide}))

	Context("scenario-0EL0: pod0 -> externalIP local-only -> pod0",
		describeEgressTest(egressTest{dstPod: 0, accessType: "external IP", svcTweak: addExternalIPLocalOnly}))

	Context("scenario-0EC0: pod0 -> externalIP externalTrafficPolicy=Cluster -> pod0",
		describeEgressTest(egressTest{dstPod: 0, accessType: "external IP", svcTweak: addExternalIPClusterWide}))

	// ===== Access to host-networked pods =====

	// Issues with port sharing, can fail to bind if another host pod is running
	// Fails at default deny stage.
	// BUG: Calico detects as forwarded so skips egress policy
	PContext("scenario-0H1: pod0 -> clusterIP -> host-networked pod1 (on same node)",
		describeEgressTest(egressTest{dstPod: 1, dstHostNetworked: true, accessType: "cluster IP"}))

	Context("scenario-0H2: pod0 -> clusterIP -> host-networked pod2 (on other node)",
		describeEgressTest(egressTest{dstPod: 2, dstHostNetworked: true, accessType: "cluster IP"}))

})

var _ = SIGDescribe("IPVSAccess", func() {

	// Test different access patterns from a pod to a service.
	//  +--------+     +------------+     +--------+
	//  | server |<----| service IP |<----| client |
	//  +--------+     +------------+     +--------+
	//                                  (a) client node == server node
	//                                  (b) client node != server node
	//  +--------+     +-----------+                 +--------+
	//  | server |<----| node port |<----------------| client |
	//  +--------+     +-----------+                 +--------+
	//          (c) NodePort node == server node     (c1) client node == server node
	//                                               (c2) client node != server node
	//
	//          (d) NodePort node != server node     (d1) client node == server node
	//                                               (d2) client node == NodePort node
	//                                               (d3) client node != server node and
	//                                                    client node != NodePort node

	var jig *framework.ServiceTestJig
	const (
		// With no SNAT, we expect networkpolicy to just work.
		expectNoSnat              = iota

		// With SNAT, and we expect networkpolicy to just work.
		expectSnatWorkingPolicy   = iota

		// With SNAT, and we do NOT expect networkpolicy to just work.
		// Cases with this are bugs that we need to fix, either in k8s or calico, because we want policy everywhere!
		expectSnatNoWorkingPolicy = iota
	)

	// The ipvs test requires three schedulable nodes. Back end server pod is running on node3.
	type IPVSTestConfig struct {
		node0Name    string
		node0IP      string
		node1Name    string
		node1IP      string
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
			svcClusterIP, svcNodePort, _ := setupPodServiceOnNode(f, jig, nodeNames[2], svcPort, nil, false)

			ipvsTC = IPVSTestConfig{
				node0Name:    nodeNames[0],
				node0IP:      nodeIPs[0],
				node1Name:    nodeNames[1],
				node1IP:      nodeIPs[1],
				svcNodeName:  nodeNames[2],
				svcNodeIP:    nodeIPs[2],
				svcClusterIP: svcClusterIP,
				svcPort:      svcPort,
				svcNodePort:  svcNodePort,
			}

			framework.Logf("IPVS test config %#v", ipvsTC)

		})

		AfterEach(func() {
			cleanupPodService(f, jig)
		})

		testIngressPolicy := func(srcNode string, srcHostNetworked bool, destIP string, destPort int, expectation int) {
			target := fmt.Sprintf("%v:%v", destIP, destPort)
			clientA := &source{srcNode, "client-a", srcHostNetworked}
			clientB := &source{srcNode, "client-b", srcHostNetworked}

			By("Traffic is allowed from pod 'client-a' and 'client-b'.")
			if expectation == expectNoSnat {
				testConnection(f, clientA, target, reachableWithoutSNAT)
				testConnection(f, clientB, target, reachableWithoutSNAT)
			} else {
				testConnection(f, clientA, target, reachableWithSNAT)
				testConnection(f, clientB, target, reachableWithSNAT)
			}

			By("Creating a network policy for the server which allows traffic from pod 'client-b'.")
			policy := &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "allow-client-b-via-pod-selector",
				},
				Spec: networkingv1.NetworkPolicySpec{
					// Apply this policy to the Server
					PodSelector: metav1.LabelSelector{
						MatchLabels: jig.Labels,
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
			testConnection(f, clientA, target, notReachable)
			switch expectation {
			case expectNoSnat:
				By("Not expecting SNAT. Traffic is allowed from pod 'client-b'.")
				testConnection(f, clientB, target, reachableWithoutSNAT)
			case expectSnatWorkingPolicy:
				By("Expecting SNAT, and policy working. Traffic is allowed from pod 'client-b'.")
				testConnection(f, clientB, target, reachableWithSNAT)
			case expectSnatNoWorkingPolicy:
				By("Expecting SNAT, and policy not working: Traffic is not allowed from pod 'client-b'.")
				// Network policy is not working for these, do NOT expect the allow policy to work, so connection fails
				testConnection(f, clientB, target, notReachable)
			}
		}

		It("1 Test accessing service ip from a pod on the same node with server pod [Feature:IPVSAccess][Feature:IPVSServiceIP]", func() {
			testIngressPolicy(ipvsTC.svcNodeName, false, ipvsTC.svcClusterIP, ipvsTC.svcPort, expectNoSnat)
		})

		It("2 Test accessing service ip from a pod running host network on the same node with server pod [Feature:IPVSAccess][Feature:IPVSServiceIP]", func() {
			testIngressPolicy(ipvsTC.svcNodeName, true, ipvsTC.svcClusterIP, ipvsTC.svcPort, expectSnatNoWorkingPolicy)
		})

		It("3 Test accessing service ip from a pod on a different node with server pod [Feature:IPVSAccess][Feature:IPVSServiceIP]", func() {
			testIngressPolicy(ipvsTC.node1Name, false, ipvsTC.svcClusterIP, ipvsTC.svcPort, expectNoSnat)
		})

		It("4 Test accessing service ip from a pod running host network on a different node with server pod [Feature:IPVSAccess][Feature:IPVSServiceIP]", func() {
			testIngressPolicy(ipvsTC.node1Name, true, ipvsTC.svcClusterIP, ipvsTC.svcPort, expectSnatNoWorkingPolicy)
		})

		It("5 Test accessing NodePort (Node running server pod) from a pod on the same node [Feature:IPVSAccess][Feature:IPVSNodePort]", func() {
			testIngressPolicy(ipvsTC.svcNodeName, false, ipvsTC.svcNodeIP, ipvsTC.svcNodePort, expectSnatWorkingPolicy)
		})
		// Nodeport should always SNAT, but when hostnetworked, SNAT changes IP to host's IP (i.e. no change)
		It("6 Test accessing NodePort (Node running server pod) from a pod running host network on the same node [Feature:IPVSAccess][Feature:IPVSNodePort]", func() {
			testIngressPolicy(ipvsTC.svcNodeName, true, ipvsTC.svcNodeIP, ipvsTC.svcNodePort, expectSnatNoWorkingPolicy)
		})

		It("7 Test accessing NodePort (Node running server pod) from a pod on a different node [Feature:IPVSAccess][Feature:IPVSNodePort]", func() {
			testIngressPolicy(ipvsTC.node1Name, false, ipvsTC.svcNodeIP, ipvsTC.svcNodePort, expectSnatNoWorkingPolicy)
		})

		// Nodeport should always SNAT, but when hostnetworked, SNAT changes IP to host's IP (i.e. no change)
		It("8 Test accessing NodePort (Node running server pod) from a pod running host network on a different node [Feature:IPVSAccess][Feature:IPVSNodePort]", func() {
			testIngressPolicy(ipvsTC.node1Name, true, ipvsTC.svcNodeIP, ipvsTC.svcNodePort, expectSnatNoWorkingPolicy)
		})

		It("9 Test accessing NodePort (Node not running server pod) from a pod on the same node [Feature:IPVSAccess][Feature:IPVSNodePort]", func() {
			testIngressPolicy(ipvsTC.node1Name, false, ipvsTC.node1Name, ipvsTC.svcNodePort, expectSnatNoWorkingPolicy)
		})

		// Nodeport should always SNAT, but when hostnetworked, SNAT changes IP to host's IP (i.e. no change)
		It("10 Test accessing NodePort (Node not running server pod) from a pod running host network on the same node [Feature:IPVSAccess][Feature:IPVSNodePort]", func() {
			testIngressPolicy(ipvsTC.node1Name, true, ipvsTC.node1Name, ipvsTC.svcNodePort, expectSnatNoWorkingPolicy)
		})

		It("11 Test accessing NodePort (Node not running server pod) from a pod on a third node [Feature:IPVSAccess][Feature:IPVSNodePort]", func() {
			testIngressPolicy(ipvsTC.node0Name, false, ipvsTC.node1Name, ipvsTC.svcNodePort, expectSnatNoWorkingPolicy)
		})

		It("12 Test accessing NodePort (Node not running server pod) from a pod running host network on a third node [Feature:IPVSAccess][Feature:IPVSNodePort]", func() {
			testIngressPolicy(ipvsTC.node0Name, true, ipvsTC.node1Name, ipvsTC.svcNodePort, expectSnatNoWorkingPolicy)
		})

	})

})

func getNodesInfo(nodes *v1.NodeList) ([]string, []string) {
	var nodeNames []string
	var nodeIPs []string
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

type source struct {
	node          string
	label         string
	hostNetworked bool
}

const (
	notReachable = "unreachable"
	reachableWithoutSNAT = "reachable without SNAT"
	reachableWithSNAT = "reachable with SNAT"
)

func testConnection(f *framework.Framework, client interface{}, target string, reachability string) {
	var execPod *v1.Pod
	switch src := client.(type) {
	case *source:
		// Create a scratch pod to test the connection.
		framework.Logf("Creating an exec pod %s on node %s, expect pod to be %s", src.label, src.node, reachability)
		execPodName := framework.CreateExecPodOrFail(f.ClientSet, f.Namespace.Name, fmt.Sprintf("execpod-sourceip-%s", src.node), func(pod *v1.Pod) {
			pod.ObjectMeta.Labels = map[string]string{"pod-name": src.label}
			pod.Spec.NodeName = src.node
			pod.Spec.HostNetwork = src.hostNetworked
		})

		defer func() {
			framework.Logf("Cleaning up the exec pod")
			err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).Delete(execPodName, nil)
			Expect(err).NotTo(HaveOccurred())
		}()
		var err error
		execPod, err = f.ClientSet.CoreV1().Pods(f.Namespace.Name).Get(execPodName, metav1.GetOptions{})
		framework.ExpectNoError(err)
	case *v1.Pod:
		// Use the pod that the caller has given us.
		execPod = src
	default:
		panic("Unhandled client type")
	}

	completedAttempts := 0
	startTime := time.Now()
	reason := "<unknown>"
	for completedAttempts <= 1 || time.Since(startTime) < 30*time.Second {
		if completedAttempts > 0 {
			framework.Logf("Retrying connectivity check...")
			time.Sleep(time.Second)
		}
		// First, do the connectivity check.
		framework.Logf("Checking connectivity with 'wget %v'", target)
		cmd := fmt.Sprintf("wget -T 2 %v -O - | grep client_address && exit 0 || exit 1", target)
		stdout, err := framework.RunKubectl(
			"exec",
			fmt.Sprintf("--namespace=%v", execPod.Namespace),
			execPod.Name, "-c", "exec",
			"--",
			"/bin/sh", "-c", cmd)
		framework.Logf("wget finished: PodIP %s, output %s.", execPod.Status.PodIP, stdout)
		completedAttempts++

		// Then, figure out what the result means...
		if reachability != notReachable {
			if err != nil {
				// Expected a connection but it failed.
				reason = "Failure: Connection unexpectedly failed."
				framework.Logf(reason)
				continue
			}

			// Desired stdout in this format: "client_address=x.x.x.x\n"
			outputs := strings.Split(strings.TrimSpace(stdout), "=")
			if len(outputs) != 2 {
				reason = fmt.Sprintf("exec pod returned unexpected stdout format: [%s]\n", stdout)
				framework.Logf(reason)
				continue
			}
			if !execPod.Spec.HostNetwork && reachability == reachableWithoutSNAT {
				// Verify observed source IP if exec pod is not running in host network namespace
				// and we don't expect any SNAT in the data path.  With exec pod running in host
				// network namespace and the destination IP is a virtual IP (service IP), the source
				// IP that the destination sees may be different from the exec pod IP.  For
				// instance, If the host happens to have a local IP 10.x.x.x which is closer to
				// service IP 10.100.x.x than pod IP 192.168.x.x, this 10.x.x.x may be used by
				// kernel as source IP.
				sourceIP := outputs[1]
				if sourceIP != execPod.Status.PodIP {
					reason = "Failure: the server saw incorrect source IP, pod IP was unexpectedly SNATed."
					framework.Logf(reason)
					// We allow retries for this because there seems to be a race in kube-proxy's programming
					// that sometimes results in connectivity before NAT is in place.
					continue
				}
			}

			return // Success!
		} else {
			if err == nil {
				reason = "Failure: Connection unexpectedly suceeded."
				framework.Logf(reason)
				continue
			}

			return // Success!
		}
	}
	calico.MaybeWaitForInvestigation()
	Fail("Failed to establish expected connectivity after retries: " + reason)
}

func setupPodServiceOnNode(f *framework.Framework, jig *framework.ServiceTestJig,
	nodeName string,
	svcPort int,
	tweak func(svc *v1.Service),
	dstHostNetworked bool,
) (string, int, *v1.Pod) {
	serviceName := jig.Name
	By("creating a TCP service " + serviceName + " in namespace " + f.Namespace.Name + ".")
	svc := jig.CreateTCPServiceWithPort(f.Namespace.Name, func(svc *v1.Service) {
		svc.Spec.Type = v1.ServiceTypeNodePort
		if tweak != nil {
			tweak(svc)
		}

	}, int32(svcPort))
	jig.SanityCheckService(svc, v1.ServiceTypeNodePort)
	svcIP := svc.Spec.ClusterIP
	svcNodePort := int(svc.Spec.Ports[0].NodePort)

	podName := jig.Name
	By("Creating a backend server pod " + podName + " which echoes back source ip.")
	pod := jig.LaunchEchoserverPodOnNode(f, nodeName, podName, dstHostNetworked)

	// Waiting for service to expose endpoint.
	framework.ValidateEndpointsOrFail(f.ClientSet, f.Namespace.Name, serviceName, framework.PortsByPodName{podName: {svcPort}})

	return svcIP, svcNodePort, pod
}

func cleanupPodService(f *framework.Framework, jig *framework.ServiceTestJig) {
	By("Cleaning up echo service and backend pod.")
	err := f.ClientSet.CoreV1().Services(f.Namespace.Name).Delete(jig.Name, nil)
	Expect(err).NotTo(HaveOccurred())
	err = f.ClientSet.CoreV1().Pods(f.Namespace.Name).Delete(jig.Name, nil)
	Expect(err).NotTo(HaveOccurred())
}
