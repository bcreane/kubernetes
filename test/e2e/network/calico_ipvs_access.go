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

	"k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
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
		nodeNames, nodeIPs, _ = getNodesInfo(f, nodes, true)
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
				client       interface{}
				target       string
				applyLabels  map[string]string
				expectSNAT   bool
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

var _ = SIGDescribe("IPVSHostEndpoint", func() {

	// Test that Calico host endpoint policy still takes effect when kube-proxy is using IPVS
	// to implement k8s Service semantics.  We set up the following pods on two nodes:
	//
	// +-------------------+ +------------------+
	// |       node0       | |       node1      |
	// | +------+ +------+ | | +--------------+ |
	// | | pod0* || pod1 | | | | pod2 || pod3*| |
	// | +------+ +------+ | | +--------------+ |
	// +-------------------+ +------------------+
	// pod0 and pod3 are host networked pods.
	// Setup host endpoint "node0.eth0" for eth0 interface of node0.
	//
	// We set up services for all pods. Such service can be accessed via its clusterIP,
	// or its NodePort, or its externalIP.
	//
	// Host endpoint ingress policy test              AOF=false                 AOF=true
	// pod3* --> pod0* (local process)              policy apply              policy apply
	// pod2  --> clusterIP for service(pod0*)       policy apply              policy apply
	//       --> pod1 (local pod)                   policy not apply          policy apply
	//       --> node0 NodePort for service(pod1)   policy not apply          policy apply
	//       --> node0 NodePort for service(pod2)   policy not apply          policy apply
	//
	// Host endpoint egress policy test
	// pod1  --> pod2                               policy not apply          poicy apply
	//       --> clusterIP for service(pod2)        policy not apply          poicy apply
	//       --> node0 NodePort for service(pod2)   policy not apply          poicy apply
	//       --> external ip for service(pod2)      policy not apply          policy apply
	//
	// pod0*  --> pod2                              policy apply              poicy apply
	//       --> clusterIP for service(pod2)        policy apply              poicy apply
	//       --> node0 NodePort for service(pod2)   policy apply              poicy apply
	//       --> clusterIP for service(pod3*)       policy apply              poicy apply
	//       --> node0 NodePort for service(pod3*)  policy apply              poicy apply
	//
	// pod2  --> node0 NodePort for service(pod2)   policy not apply          policy apply
	//       --> node0 NodePort for service(pod1)   policy not apply          policy not apply
	//
	// For each pattern we follow the test procedure:
	//
	// - With no node0.eth0 host endpoint, target is accessible.
	//
	// - Create host endpoint for node0.eth0.
	//   Target is not accessible if destination pod is host networked for an ingress test.
	//   Target is not accessible if source pod is host networked for an egress test.
	//   Target is still accessible if none of above condition is met.
	//
	// - Add policy that applies to host endpoint to allow ingress or egress for access port.
	//   Target is accessible.
	//
	// - Add policy with lower order to deny ingress or egress for access port.
	//   Target is not accessible if "policy apply".
	//   Target is still accessible if "policy not apply".
	//
	// - Remove policies and host endpoint.

	f := framework.NewDefaultFramework("ipvs-hep")
	var (
		jig               *framework.ServiceTestJig
		nodeNames         []string
		nodeIPs           []string
		calicoNodeNames   []string
		hepNodeName       string
		hepCalicoNodeName string
		hepNodeIP         string
		nodeNameMap       map[int]string
		policyNames       []string
	)

	type hepTestConfig struct {
		// Any tweak needed to the target Service definition.
		svcTweak func(svc *v1.Service)
		// srcPod is the number of the destination pod, as per the diagram above.
		srcPod int
		// True if the destination pod should be host-networked.
		srcHostNetworked bool
		// dstPod is the number of the destination pod, as per the diagram above.
		dstPod int
		// True if the destination pod should be host-networked.
		dstHostNetworked bool
		// How to access the Service.
		accessType string
		// expect SNAT
		expectSNAT bool
	}

	type hepPolicyConfig struct {
		// Flag to indicate ingress or egress policy
		actionType string
		// Apply on forward flag.
		applyOnForward bool
		// Flag to indicate if policy applies to test setup.
		policyApply bool
	}

	var calicoctl *calico.Calicoctl

	addExternalIPClusterWide := func(svc *v1.Service) {
		svc.Spec.ExternalIPs = []string{nodeIPs[0]}
		svc.Spec.ExternalTrafficPolicy = v1.ServiceExternalTrafficPolicyTypeCluster
	}

	Context("IPVS Host Endpoint test", func() {

		BeforeEach(func() {
			/*
			   The following code tries to get config information for calicoctl from k8s ConfigMap.
			   A framework clientset is needed to access k8s configmap but it will only be created in the context of BeforeEach or IT.
			   Current solution is to use BeforeEach because this function is not a test case.
			   This will avoid complexity of creating a client by ourself.
			*/
			calicoctl = calico.ConfigureCalicoctl(f)

		})

		BeforeEach(func() {
			framework.Logf("BeforeEach for IPVS HostEndpoint")
			jig = framework.NewServiceTestJig(f.ClientSet, "ipvs-hep")
			nodes := jig.GetNodes(3)
			if len(nodes.Items) == 0 {
				framework.Skipf("No nodes exist, can't continue test.")
			}
			if len(nodes.Items) < 2 {
				framework.Skipf("Less than two schedulable nodes exist, can't continue test.")
			}

			// Need to avoid using master node to set up a host endpoint.
			nodeNames, nodeIPs, calicoNodeNames = getNodesInfo(f, nodes, false)

			Expect(len(nodeNames)).Should(BeNumerically(">=", 2))
			Expect(len(nodeIPs)).Should(BeNumerically(">=", 2))

			// Needs `iptables -P FORWARD ACCEPT`
			for _, node := range nodeNames {
				if !checkForwardAccept(f, node) {
					framework.Failf("FORWARD DROP, can't continue test. Please check your cluster setup.")
				}
			}

			nodeNameMap = map[int]string{0: nodeNames[0], 1: nodeNames[0], 2: nodeNames[1], 3: nodeNames[1]}

			// Host endpoint will set to node 0
			hepNodeName = nodeNames[0]
			hepCalicoNodeName = calicoNodeNames[0]
			hepNodeIP = nodeIPs[0]
			framework.Logf("hep node0 (%s %s), node1 (%s %s)", nodeNames[0], nodeIPs[0], nodeNames[1], nodeIPs[1])

			// Avoid using hepNode as calicoctl node.
			calicoctl.SetNodeToRun(nodeNames[1])

			// Log the server node's IP addresses.
			node, err := f.ClientSet.CoreV1().Nodes().Get(hepNodeName, metav1.GetOptions{})
			framework.ExpectNoError(err)
			for _, address := range node.Status.Addresses {
				framework.Logf("Host endpoint test node address: %#v", address)
			}

			// Prepare a policy names slice
			policyNames = []string{}
		})

		describeEgressTest := func(c hepTestConfig, policyConfigs []hepPolicyConfig) func() {
			return func() {
				var (
					client       *v1.Pod
					target       string
					action       string
					expectSNAT   bool
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

				if c.srcPod > 3 {
					panic("srcpod id bigger than 3")
				}
				if c.dstPod > 3 {
					panic("dstpod id bigger than 3")
				}

				getPolicyAction := func(p hepPolicyConfig, allowOrDeny string) string {
					if p.actionType == "ingress" {
						action = `
  ingress:
  - action: %s
    protocol: TCP
    destination:
      ports:
      - 8080
  egress:
  - action: Allow`
					} else if p.actionType == "egress" {
						action = `
  ingress:
  - action: Allow
  egress:
  - action: %s
    protocol: TCP
    destination:
      ports:
      - 8080`
					} else {
						panic("Unhandled actionType: " + p.actionType)
					}

					return strings.Trim(fmt.Sprintf(action, allowOrDeny), "\n")
				}

				BeforeEach(func() {
					// Setup destination service and pod.
					svcPort := 8080
					svcClusterIP, svcNodePort, dstPod := setupPodServiceOnNode(f, jig, nodeNameMap[c.dstPod], svcPort, c.svcTweak, c.dstHostNetworked)

					// Setup source client.
					src := &source{nodeNameMap[c.srcPod], "ipvs-hep-source", c.srcHostNetworked}
					client = createExecPodOrFail(f, src)

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
					} else if c.accessType == "NodePort" {
						expectSNAT = true
						target = fmt.Sprintf("%v:%v", hepNodeIP, svcNodePort)
					} else if c.accessType == "external IP" {
						expectSNAT = true
						target = fmt.Sprintf("%v:%v", hepNodeIP, svcPort)
					} else if c.accessType == "pod IP" {
						expectSNAT = false
						target = fmt.Sprintf("%v:%s", dstPod.Status.PodIP, "8080")
					} else {
						panic("Unhandled accessType: " + c.accessType)
					}
				})

				AfterEach(func() {
					By("Cleaning up policies and host endpoint")
					for _, name := range policyNames {
						calicoctl.DeleteGNP(name)
					}
					calicoctl.DeleteHE("host-ep")
					calicoctl.Cleanup()

					cleanupExecPodOrFail(f, client)
				})

				// Test each policy config
				for _, p := range policyConfigs {
					policy := p // make sure policy get correct value for IT.
					It("should correctly implement Host Endpoint NetworkPolicy", func() {
						if policy.applyOnForward {
							By("Start testing with ApplyOnForward=true")
						} else {
							By("Start testing with ApplyOnForward=false")
						}

						By("allowing connection with no HostEndpoint and NetworkPolicy")
						expectAccessAllowed()

						// We need allow following communications to kubelet port 10250.
						// -- "kubectl exec" if source pod is running on the node with a host endpoint policy.
						// -- "kubectl log" if calicoctl pod is running on the node with a host endpoint policy.
						// This policy should set before the creation of a host endpoint policy.
						By("allowing connection to kubelet port 10250 for kubectl exec/log")
						policyStr := fmt.Sprintf(`
apiVersion: projectcalico.org/v3
kind: GlobalNetworkPolicy
metadata:
  name: allow-kubectl-800
spec:
  applyOnForward: false
  selector: hep == "node0"
  order: 800
  ingress:
  - action: Allow
    protocol: TCP
    destination:
      ports:
      - %s
  egress:
  - action: Allow
    protocol: TCP
    source:
      ports:
      - %s
`,
							"10250", "10250")
						calicoctl.Apply(policyStr)
						policyNames = append(policyNames, "allow-kubectl-800")

						By("test connection by creating a HostEndpoint")
						hostEpStr := fmt.Sprintf(`
apiVersion: projectcalico.org/v3
kind: HostEndpoint
metadata:
  name: host-ep
  labels:
    hep: node0
spec:
  node: %s
  expectedIPs:
  - %s
`,
							hepCalicoNodeName, hepNodeIP)
						calicoctl.Apply(hostEpStr)

						if (c.dstHostNetworked && policy.actionType == "ingress") ||
							(c.srcHostNetworked && policy.actionType == "egress") {
							By("default deny by creating a HostEndpoint onto local host")
							expectAccessDenied()
						} else {
							By("no default deny by creating a HostEndpoint for forwarded traffic")
							expectAccessAllowed()
						}

						By("allowing traffic after installing a host endpoint allow policy")
						policyStr = fmt.Sprintf(`
apiVersion: projectcalico.org/v3
kind: GlobalNetworkPolicy
metadata:
  name: allow-500
spec:
  applyOnForward: %t
  selector: hep == "node0"
  order: 500
%s
`,
							policy.applyOnForward, getPolicyAction(policy, "Allow"))
						calicoctl.Apply(policyStr)
						policyNames = append(policyNames, "allow-500")

						expectAccessAllowed()

						By("testing connection after installing a host endpoint deny policy with lower order")
						policyStr = fmt.Sprintf(`
apiVersion: projectcalico.org/v3
kind: GlobalNetworkPolicy
metadata:
  name: deny-200
spec:
  applyOnForward: %t
  selector: hep == "node0"
  order: 200
%s
`,
							policy.applyOnForward, getPolicyAction(policy, "Deny"))
						calicoctl.Apply(policyStr)
						policyNames = append(policyNames, "deny-200")

						if policy.policyApply {
							By("policy apply. denying connection after installing a host endpoint deny policy with lower order")
							expectAccessDenied()
						} else {
							By("policy not apply. allowing connection after installing a host endpoint deny policy with lower order")
							expectAccessAllowed()
						}
					})
				}
			}
		}

		// ===== host endpoint ingress test =====
		// Needs `iptables -P FORWARD ACCEPT`

		Context("ingress-3-0: pod3 -> Pod IP -> pod0 [Feature:IPVSHep][Feature:IPVSHepIngress]",
			describeEgressTest(hepTestConfig{srcPod: 3, srcHostNetworked: true, dstPod: 0, dstHostNetworked: true, accessType: "pod IP"},
				[]hepPolicyConfig{{actionType: "ingress", applyOnForward: false, policyApply: true},
					{actionType: "ingress", applyOnForward: true, policyApply: true}}))

		Context("ingress-2C0: pod2 -> ClusterIP -> pod0 [Feature:IPVSHep][Feature:IPVSHepIngress]",
			describeEgressTest(hepTestConfig{srcPod: 2, dstPod: 0, dstHostNetworked: true, accessType: "cluster IP"},
				[]hepPolicyConfig{{actionType: "ingress", applyOnForward: false, policyApply: true},
					{actionType: "ingress", applyOnForward: true, policyApply: true}}))

		Context("ingress-2-1 : pod2 -> Pod IP -> pod1 [Feature:IPVSHep][Feature:IPVSHepIngress]",
			describeEgressTest(hepTestConfig{srcPod: 2, dstPod: 1, accessType: "pod IP"},
				[]hepPolicyConfig{{actionType: "ingress", applyOnForward: false, policyApply: false},
					{actionType: "ingress", applyOnForward: true, policyApply: true}}))

		Context("ingress-2N1 : pod2 -> NodePort -> pod1 [Feature:IPVSHep][Feature:IPVSHepIngress]",
			describeEgressTest(hepTestConfig{srcPod: 2, dstPod: 1, accessType: "NodePort"},
				[]hepPolicyConfig{{actionType: "ingress", applyOnForward: false, policyApply: false},
					{actionType: "ingress", applyOnForward: true, policyApply: true}}))

		Context("ingress-2N2 : pod2 -> NodePort -> pod2 [Feature:IPVSHep][Feature:IPVSHepIngress]",
			describeEgressTest(hepTestConfig{srcPod: 2, dstPod: 2, accessType: "NodePort"},
				[]hepPolicyConfig{{actionType: "ingress", applyOnForward: false, policyApply: false},
					{actionType: "ingress", applyOnForward: true, policyApply: true}}))

		// ===== host endpoint egress test =====
		// Needs `iptables -P FORWARD ACCEPT`

		Context("egress-1-2: pod1 -> Pod IP -> pod2 [Feature:IPVSHep][Feature:IPVSHepEgress]",
			describeEgressTest(hepTestConfig{srcPod: 1, dstPod: 2, accessType: "pod IP"},
				[]hepPolicyConfig{{actionType: "egress", applyOnForward: false, policyApply: false},
					{actionType: "egress", applyOnForward: true, policyApply: true}}))

		Context("egress-1C2: pod1 -> ClusterIP IP -> pod2 [Feature:IPVSHep][Feature:IPVSHepEgress]",
			describeEgressTest(hepTestConfig{srcPod: 1, dstPod: 2, accessType: "cluster IP"},
				[]hepPolicyConfig{{actionType: "egress", applyOnForward: false, policyApply: false},
					{actionType: "egress", applyOnForward: true, policyApply: true}}))

		Context("egress-1N2: pod1 -> NodePort IP -> pod2 [Feature:IPVSHep][Feature:IPVSHepEgress]",
			describeEgressTest(hepTestConfig{srcPod: 1, dstPod: 2, accessType: "NodePort"},
				[]hepPolicyConfig{{actionType: "egress", applyOnForward: false, policyApply: false},
					{actionType: "egress", applyOnForward: true, policyApply: true}}))

		// external ip currently not working because of issue https://github.com/projectcalico/felix/issues/1697
		// Disable external ip tests for now till we got this issue fixed.
		PContext("NotWorking egress-1E2: pod1 -> external IP -> pod2 [Feature:IPVSHep][Feature:IPVSHepEgress]",
			describeEgressTest(hepTestConfig{srcPod: 1, dstPod: 2, accessType: "external IP", svcTweak: addExternalIPClusterWide},
				[]hepPolicyConfig{{actionType: "egress", applyOnForward: false, policyApply: false},
					{actionType: "egress", applyOnForward: true, policyApply: true}}))

		Context("egress-0-2: pod0 -> Pod IP -> pod2 [Feature:IPVSHep][Feature:IPVSHepEgress]",
			describeEgressTest(hepTestConfig{srcPod: 0, srcHostNetworked: true, dstPod: 2, accessType: "pod IP"},
				[]hepPolicyConfig{{actionType: "egress", applyOnForward: false, policyApply: true},
					{actionType: "egress", applyOnForward: true, policyApply: true}}))

		Context("egress-0C2: pod0 -> ClusterIP IP -> pod2 [Feature:IPVSHep][Feature:IPVSHepEgress]",
			describeEgressTest(hepTestConfig{srcPod: 0, srcHostNetworked: true, dstPod: 2, accessType: "cluster IP"},
				[]hepPolicyConfig{{actionType: "egress", applyOnForward: false, policyApply: true},
					{actionType: "egress", applyOnForward: true, policyApply: true}}))

		Context("egress-0N2: pod0 -> NodePort IP -> pod2 [Feature:IPVSHep][Feature:IPVSHepEgress]",
			describeEgressTest(hepTestConfig{srcPod: 0, srcHostNetworked: true, dstPod: 2, accessType: "NodePort"},
				[]hepPolicyConfig{{actionType: "egress", applyOnForward: false, policyApply: true},
					{actionType: "egress", applyOnForward: true, policyApply: true}}))

		Context("egress-0C3: pod0 -> ClusterIP IP -> pod3 [Feature:IPVSHep][Feature:IPVSHepEgress]",
			describeEgressTest(hepTestConfig{srcPod: 0, srcHostNetworked: true, dstPod: 3, dstHostNetworked: true, accessType: "cluster IP"},
				[]hepPolicyConfig{{actionType: "egress", applyOnForward: false, policyApply: true},
					{actionType: "egress", applyOnForward: true, policyApply: true}}))

		Context("egress-0N3: pod0 -> NodePort IP -> pod3 [Feature:IPVSHep][Feature:IPVSHepEgress]",
			describeEgressTest(hepTestConfig{srcPod: 0, srcHostNetworked: true, dstPod: 3, dstHostNetworked: true, accessType: "NodePort"},
				[]hepPolicyConfig{{actionType: "egress", applyOnForward: false, policyApply: true},
					{actionType: "egress", applyOnForward: true, policyApply: true}}))

		Context("egress-2N2 : pod2 -> NodePort -> pod2 [Feature:IPVSHep][Feature:IPVSHepIngress]",
			describeEgressTest(hepTestConfig{srcPod: 2, dstPod: 2, accessType: "NodePort"},
				[]hepPolicyConfig{{actionType: "egress", applyOnForward: false, policyApply: false},
					{actionType: "egress", applyOnForward: true, policyApply: true}}))

		Context("egress-2N1 : pod2 -> NodePort -> pod1 [Feature:IPVSHep][Feature:IPVSHepIngress]",
			describeEgressTest(hepTestConfig{srcPod: 2, dstPod: 1, accessType: "NodePort"},
				[]hepPolicyConfig{{actionType: "egress", applyOnForward: false, policyApply: false},
					{actionType: "egress", applyOnForward: true, policyApply: false}}))
	})
})

var _ = SIGDescribe("IPVSIngress", func() {

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
		expectNoSnat = iota

		// With SNAT, and we expect networkpolicy to just work.
		expectSnatWorkingPolicy

		// With SNAT, and we do NOT expect networkpolicy to just work.
		// Cases with this are bugs that we need to fix, either in k8s or calico, because we want policy everywhere!
		expectSnatNoWorkingPolicy

		// For host -> local pod, we always expect the conntection to succeed.
		expectAlwaysAllowed
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

	f := framework.NewDefaultFramework("ipvs-ingress")

	Context("Workload ingress test", func() {

		BeforeEach(func() {
			jig = framework.NewServiceTestJig(f.ClientSet, "ipvs-ingress")
			nodes := jig.GetNodes(3)
			if len(nodes.Items) == 0 {
				framework.Skipf("No nodes exist, can't continue test.")
			}
			if len(nodes.Items) < 3 {
				framework.Skipf("Less than three schedulable nodes exist, can't continue test.")
			}
			nodeNames, nodeIPs, _ := getNodesInfo(f, nodes, true)
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

			if expectation == expectAlwaysAllowed {
				By("Always allowing traffic from both clients")
				testConnection(f, clientA, target, reachableWithSNAT)
				testConnection(f, clientB, target, reachableWithSNAT)
				return
			}

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

		It("1 Test accessing service ip from a pod on the same node with server pod [Feature:IPVSIngress][Feature:IPVSServiceIP]", func() {
			testIngressPolicy(ipvsTC.svcNodeName, false, ipvsTC.svcClusterIP, ipvsTC.svcPort, expectNoSnat)
		})

		It("2 Test accessing service ip from a pod running host network on the same node with server pod [Feature:IPVSIngress][Feature:IPVSServiceIP]", func() {
			testIngressPolicy(ipvsTC.svcNodeName, true, ipvsTC.svcClusterIP, ipvsTC.svcPort, expectAlwaysAllowed)
		})

		It("3 Test accessing service ip from a pod on a different node with server pod [Feature:IPVSIngress][Feature:IPVSServiceIP]", func() {
			testIngressPolicy(ipvsTC.node1Name, false, ipvsTC.svcClusterIP, ipvsTC.svcPort, expectNoSnat)
		})

		It("4 Test accessing service ip from a pod running host network on a different node with server pod [Feature:IPVSIngress][Feature:IPVSServiceIP]", func() {
			testIngressPolicy(ipvsTC.node1Name, true, ipvsTC.svcClusterIP, ipvsTC.svcPort, expectSnatNoWorkingPolicy)
		})

		It("5 Test accessing NodePort (Node running server pod) from a pod on the same node [Feature:IPVSIngress][Feature:IPVSNodePort]", func() {
			testIngressPolicy(ipvsTC.svcNodeName, false, ipvsTC.svcNodeIP, ipvsTC.svcNodePort, expectSnatWorkingPolicy)
		})
		// Nodeport should always SNAT, but when hostnetworked, SNAT changes IP to host's IP (i.e. no change)
		It("6 Test accessing NodePort (Node running server pod) from a pod running host network on the same node [Feature:IPVSIngress][Feature:IPVSNodePort]", func() {
			testIngressPolicy(ipvsTC.svcNodeName, true, ipvsTC.svcNodeIP, ipvsTC.svcNodePort, expectAlwaysAllowed)
		})

		It("7 Test accessing NodePort (Node running server pod) from a pod on a different node [Feature:IPVSIngress][Feature:IPVSNodePort]", func() {
			testIngressPolicy(ipvsTC.node1Name, false, ipvsTC.svcNodeIP, ipvsTC.svcNodePort, expectSnatNoWorkingPolicy)
		})

		// Nodeport should always SNAT, but when hostnetworked, SNAT changes IP to host's IP (i.e. no change)
		It("8 Test accessing NodePort (Node running server pod) from a pod running host network on a different node [Feature:IPVSIngress][Feature:IPVSNodePort]", func() {
			testIngressPolicy(ipvsTC.node1Name, true, ipvsTC.svcNodeIP, ipvsTC.svcNodePort, expectSnatNoWorkingPolicy)
		})

		It("9 Test accessing NodePort (Node not running server pod) from a pod on the same node [Feature:IPVSIngress][Feature:IPVSNodePort]", func() {
			testIngressPolicy(ipvsTC.node1Name, false, ipvsTC.node1Name, ipvsTC.svcNodePort, expectSnatNoWorkingPolicy)
		})

		// Nodeport should always SNAT, but when hostnetworked, SNAT changes IP to host's IP (i.e. no change)
		It("10 Test accessing NodePort (Node not running server pod) from a pod running host network on the same node [Feature:IPVSIngress][Feature:IPVSNodePort]", func() {
			testIngressPolicy(ipvsTC.node1Name, true, ipvsTC.node1IP, ipvsTC.svcNodePort, expectSnatNoWorkingPolicy)
		})

		It("11 Test accessing NodePort (Node not running server pod) from a pod on a third node [Feature:IPVSIngress][Feature:IPVSNodePort]", func() {
			testIngressPolicy(ipvsTC.node0Name, false, ipvsTC.node1Name, ipvsTC.svcNodePort, expectSnatNoWorkingPolicy)
		})

		It("12 Test accessing NodePort (Node not running server pod) from a pod running host network on a third node [Feature:IPVSIngress][Feature:IPVSNodePort]", func() {
			testIngressPolicy(ipvsTC.node0Name, true, ipvsTC.node1Name, ipvsTC.svcNodePort, expectSnatNoWorkingPolicy)
		})

		// TODO: Enable this test when we figure out what is wrong with accessing a NodePort using 127.0.0.1 with IPVS
		// Nodeport should always SNAT, but when hostnetworked, SNAT changes IP to host's IP (i.e. no change)
		// It("13 Test accessing NodePort (Node not running server pod) from a pod running host network on the same node using localhost [Feature:IPVSIngress][Feature:IPVSNodePort]", func() {
		// 	testIngressPolicy(ipvsTC.node1Name, true, "127.0.0.1", ipvsTC.svcNodePort, expectSnatNoWorkingPolicy)
		// })
	})

})

func getNodesInfo(f *framework.Framework, nodes *v1.NodeList, masterOK bool) ([]string, []string, []string) {
	// By default, Calico node name is host name, e.g. ip-10-0-0-108.
	// Kubernetes node name could be different (ip-10-0-0-108.us-west-2.compute.internal) if cloud provider is aws.
	var nodeNames, nodeIPs, calicoNodeNames []string
	for _, node := range nodes.Items {
		addrs := framework.GetNodeAddresses(&node, v1.NodeInternalIP)
		if len(addrs) == 0 {
			framework.Failf("node %s failed to report a valid ip address\n", node.Name)
		}

		if !masterOK && checkNodeIsMaster(f, addrs) {
			framework.Logf("Skip using master node %s", node.Name)
			continue
		}

		hostNames := framework.GetNodeAddresses(&node, v1.NodeHostName)
		if len(hostNames) == 0 {
			framework.Failf("node %s failed to report a valid host name\n", node.Name)
		}

		nodeNames = append(nodeNames, node.Name)
		nodeIPs = append(nodeIPs, addrs[0])
		calicoNodeNames = append(calicoNodeNames, hostNames[0])
	}
	return nodeNames, nodeIPs, calicoNodeNames
}

type source struct {
	node          string
	label         string
	hostNetworked bool
}

const (
	notReachable         = "unreachable"
	reachableWithoutSNAT = "reachable without SNAT"
	reachableWithSNAT    = "reachable with SNAT"
)

func createExecPodOrFail(f *framework.Framework, src *source) *v1.Pod {
	// Create a scratch pod to test the connection.
	framework.Logf("Creating an exec pod %s on node %s", src.label, src.node)
	execPodName := framework.CreateExecPodOrFail(f.ClientSet, f.Namespace.Name, src.label, func(pod *v1.Pod) {
		pod.ObjectMeta.Labels = map[string]string{"pod-name": src.label}
		pod.Spec.NodeName = src.node
		pod.Spec.HostNetwork = src.hostNetworked
	})

	var err error
	execPod, err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).Get(execPodName, metav1.GetOptions{})
	framework.ExpectNoError(err)

	return execPod

}

func cleanupExecPodOrFail(f *framework.Framework, pod *v1.Pod) {
	framework.Logf("Cleaning up the exec pod")
	err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).Delete(pod.Name, nil)
	Expect(err).NotTo(HaveOccurred())
}

func testConnection(f *framework.Framework, client interface{}, target string, reachability string) {
	var execPod *v1.Pod
	switch src := client.(type) {
	case *source:
		// Create a scratch pod to test the connection.
		execPod = createExecPodOrFail(f, src)
		defer cleanupExecPodOrFail(f, execPod)

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

func checkForwardAccept(f *framework.Framework, nodeName string) bool {
	// Get calico-node pod on the node.
	labelSelector := labels.SelectorFromSet(labels.Set(map[string]string{"k8s-app": "calico-node"}))
	fieldSelector := fields.SelectorFromSet(fields.Set{"spec.nodeName": nodeName})
	options := metav1.ListOptions{LabelSelector: labelSelector.String(), FieldSelector: fieldSelector.String()}

	pods, err := f.ClientSet.CoreV1().Pods("kube-system").List(options)
	Expect(err).NotTo(HaveOccurred())
	Expect(pods.Items).To(HaveLen(1), fmt.Sprintf("Failed to find calico/node pod on node %v when trying to check iptables policy", nodeName))
	pod := &pods.Items[0]

	// Check iptables rules
	framework.Logf("Run iptables command with pod %s on node %s", pod.Name, nodeName)

	cmd := "iptables -L | grep 'Chain FORWARD (policy'"
	out, err := framework.RunHostCmd("kube-system", pod.Name, cmd)
	Expect(err).NotTo(HaveOccurred())
	framework.Logf("Check FORWARD Chain. err %v out: %v", err, out)

	return !strings.Contains(out, "DROP")
}

func checkNodeIsMaster(f *framework.Framework, ips []string) bool {
	endpoints, err := f.ClientSet.CoreV1().Endpoints("default").Get("kubernetes", metav1.GetOptions{})
	if err != nil {
		framework.Failf("Get endpoints for service kubernetes failed (%s)", err)
	}
	if len(endpoints.Subsets) == 0 {
		framework.Failf("Endpoint has no subsets, cannot determine node addresses.")
	}

	hasIP := func(endpointIP string) bool {
		for _, ip := range ips {
			if ip == endpointIP {
				return true
			}
		}
		return false
	}

	for _, ss := range endpoints.Subsets {
		for _, e := range ss.Addresses {
			if hasIP(e.IP) {
				return true
			}
		}
	}

	return false
}
