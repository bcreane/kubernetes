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

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"
	"k8s.io/kubernetes/test/utils/winctl"

	. "github.com/onsi/ginkgo"
)

const (
	allowAll = `
  ingress:
  - action: Allow
  egress:
  - action: Allow
`
	denyAll = `
  ingress:
  - action: Deny
  egress:
  - action: Deny
`
	noneAll = `
  types: [Ingress, Egress]
`
)

var _ = framework.KubeDescribe("[Feature:CalicoPolicy-v3] policy ordering", func() {
	var service *v1.Service
	var podServer *v1.Pod
	var serverNodeName string
	var serverNodeIPs []string
	var hostNetworkedServer bool

	f := framework.NewDefaultFramework("calico-v3-policy-ordering")

	JustBeforeEach(func() {
		// Create a server pod.
		By("Creating a simple server")
		if hostNetworkedServer {
			podServer, service = createHostNetworkedServerPodAndService(f, f.Namespace, "server", []int{80})
		} else {
			podServer, service = createServerPodAndService(f, f.Namespace, "server", []int{80})
		}
		framework.Logf("Waiting for Server to come up.")
		err := framework.WaitForPodRunningInNamespace(f.ClientSet, podServer)
		framework.ExpectNoError(err)
		podServer = calico.GetPodNow(f, podServer.Name)
		serverNodeName = podServer.Spec.NodeName

		testCanConnect(f, f.Namespace, "client-can-connect-80", service, 80)
		// Discover the server node's IP addresses.
		node, err := f.ClientSet.CoreV1().Nodes().Get(serverNodeName, metav1.GetOptions{})
		framework.ExpectNoError(err)
		serverNodeIPs = []string{}
		for _, address := range node.Status.Addresses {
			framework.Logf("Server node address: %#v", address)
			if address.Type == v1.NodeExternalIP || address.Type == v1.NodeInternalIP {
				serverNodeIPs = append(serverNodeIPs, address.Address)
			}
		}
	})

	// For the tests where the server is host-networked, we use the following closure to modify
	// the client pod spec so that the client runs on a different node from the server.  Then
	// traffic between the client and server is forced to go through the server node's host
	// endpoint where we are applying policy.
	setNodeAffinity := func(pod *v1.Pod) {
		pod.Spec.Affinity = &v1.Affinity{
			NodeAffinity: &v1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
					NodeSelectorTerms: []v1.NodeSelectorTerm{
						{
							MatchExpressions: []v1.NodeSelectorRequirement{
								{
									Key:      "kubernetes.io/hostname",
									Operator: v1.NodeSelectorOpNotIn,
									Values:   []string{serverNodeName},
								},
							},
						},
					},
				},
			},
		}
	}

	logServerDiags := func() {
		// Collect/log Calico diags for the server node.
		logErr := calico.LogCalicoDiagsForNode(f, serverNodeName)
		if logErr != nil {
			framework.Logf("Error getting Calico diags: %v", logErr)
		}

	}

	expectConnection := func() {
		var podCustomizer func(pod *v1.Pod)
		if hostNetworkedServer {
			podCustomizer = setNodeAffinity
		}
		target := fmt.Sprintf("%s:%d", service.Spec.ClusterIP, 80)
		//This is a hack for windows to use PodIP instead of Service's ClusterIP
		if winctl.RunningWindowsTest() {
			podTarget, serviceTarget := winctl.GetTarget(f, service, 80)
			//check connectivity with podIP
			fmt.Printf("Checking connectivity with podIP :%s \n",podTarget)
			testCanConnectX(f, f.Namespace, "client-can-connect", service, podTarget, podCustomizer, logServerDiags)
			//assign service ClusterIP to check connectivity
			target = serviceTarget
			fmt.Printf("Checking connectivity with serviceIP :%s \n",target)
		}
		testCanConnectX(f, f.Namespace, "client-can-connect", service, target, podCustomizer, logServerDiags)
	}
	expectNoConnection := func() {
		var podCustomizer func(pod *v1.Pod)
		if hostNetworkedServer {
			podCustomizer = setNodeAffinity
		}
		target := fmt.Sprintf("%s:%d", service.Spec.ClusterIP, 80)
		//This is a hack for windows to use PodIP instead of Service's ClusterIP
		if winctl.RunningWindowsTest() {
			podTarget, serviceTarget := winctl.GetTarget(f, service, 80)
			//check connectivity with podIP
			fmt.Printf("Checking connectivity with podIP :%s \n",podTarget)
			testCannotConnectX(f, f.Namespace, "client-can-connect", service, podTarget, podCustomizer)
			//assign service ClusterIP to check connectivity
			target = serviceTarget
			fmt.Printf("Checking connectivity with serviceIP :%s \n",target)
		}
		testCannotConnectX(f, f.Namespace, "client-can-connect", service, target, podCustomizer)
	}

	It("should be contactable [Feature:WindowsPolicy]", expectConnection)

	var (
		names           []string
		orders          []int
		policies        []string
		policiesApplied []string
	)

	Context("with policies", func() {

		var calicoctl *calico.Calicoctl

		BeforeEach(func() {
			/*
			   The following code tries to get config information for calicoctl from k8s ConfigMap.
			   A framework clientset is needed to access k8s configmap but it will only be created in the context of BeforeEach or IT.
			   Current solution is to use BeforeEach because this function is not a test case.
			   This will avoid complexity of creating a client by ourself.
			*/
			calicoctl = calico.ConfigureCalicoctl(f)

		})

		JustBeforeEach(func() {
			By("Configuring policies")
			selector := `pod-name == "` + podServer.Name + `"`
			if hostNetworkedServer {
				selector = `police-me == "true"`
				calicoctl.AvoidNode(serverNodeName)
			}
			policiesApplied = []string{}
			for ii := range policies {
				policyStr := fmt.Sprintf(`
apiVersion: projectcalico.org/v3
kind: GlobalNetworkPolicy
metadata:
  name: %s
spec:
  selector: %s
  order: %d
%s
`,
					names[ii], selector, orders[ii], policies[ii])
				calicoctl.Apply(policyStr)
				policiesApplied = append(policiesApplied, names[ii])
			}
			if hostNetworkedServer {
				hostEpStr := fmt.Sprintf(`
apiVersion: projectcalico.org/v3
kind: HostEndpoint
metadata:
  name: server-host-ep
  labels:
    police-me: true
spec:
  node: %s
  expectedIPs:
`,
					serverNodeName)
				for _, ip := range serverNodeIPs {
					hostEpStr = hostEpStr + "  - " + ip + "\n"
				}
				calicoctl.Apply(hostEpStr)
			}
		})

		AfterEach(func() {
			By("Cleaning up policies")
			if hostNetworkedServer {
				calicoctl.DeleteHE("server-host-ep")
			}
			for _, policyName := range policiesApplied {
				calicoctl.DeleteGNP(policyName)
			}
			policiesApplied = []string{}
			calicoctl.Cleanup()
		})

		definePolicyContentTests := func() {

			Context("allowAll, denyAll, denyAll", func() {
				BeforeEach(func() {
					policies = []string{allowAll, denyAll, denyAll}
				})
				It("should be contactable [Feature:WindowsPolicy]", expectConnection)
			})

			Context("denyAll, denyAll, denyAll", func() {
				BeforeEach(func() {
					policies = []string{denyAll, denyAll, denyAll}
				})
				It("should not be contactable [Feature:WindowsPolicy]", expectNoConnection)
			})

			Context("denyAll, allowAll, allowAll", func() {
				BeforeEach(func() {
					policies = []string{denyAll, allowAll, allowAll}
				})
				It("should not be contactable [Feature:WindowsPolicy]", expectNoConnection)
			})

			Context("noneAll, allowAll, allowAll", func() {
				BeforeEach(func() {
					policies = []string{noneAll, allowAll, allowAll}
				})
				It("should be contactable [Feature:WindowsPolicy]", expectConnection)
			})

			Context("noneAll, denyAll, allowAll", func() {
				BeforeEach(func() {
					policies = []string{noneAll, denyAll, allowAll}
				})
				It("should not be contactable [Feature:WindowsPolicy]", expectNoConnection)
			})
		}

		Context("ordering by explicit policy order field", func() {

			BeforeEach(func() {
				hostNetworkedServer = false
				names = []string{"pol-c", "pol-b", "pol-a"}
				orders = []int{1, 2, 3}
			})

			definePolicyContentTests()
		})

		Context("ordering by policy name as tie-breaker", func() {

			BeforeEach(func() {
				hostNetworkedServer = false
				names = []string{"pol-1", "pol-2", "pol-3"}
				orders = []int{1, 1, 1}
			})

			definePolicyContentTests()
		})

		Context("with host network and endpoint, and explicit policy orders", func() {

			BeforeEach(func() {
				if calicoctl.DatastoreType() == "kubernetes" {
					// Can't configure host endpoints with Kubernetes as the data store.
					Skip("Test is not possible with Kubernetes as the data store")
				}
				if winctl.RunningWindowsTest() {
					// windows node does not support host endpoints and host networked pod.
					Skip("Test is not possible with windows nodes")
				}

				hostNetworkedServer = true
				names = []string{"pol-c", "pol-b", "pol-a"}
				orders = []int{1, 2, 3}
			})

			definePolicyContentTests()
		})

	})
})
