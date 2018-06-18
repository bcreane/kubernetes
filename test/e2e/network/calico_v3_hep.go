/*
Copyright 2018 The Kubernetes Authors.

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
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

/*
These tests use a hostNetworked  server pod to simulate network connectivity to the hostEndpoint.
This is made possible by the fact that hostNetworked pods are subjected to the policies of
a hostEndpoint.

The test flow is as follows:
1. Run a hostNetworked server pod.
2. Create a HEP on the node it lands on.
3. Create test network policy
4. Test the connectivity from clients on other nodes to the hostnetworked pod.
*/
// These tests must be Serial since they use hostEndpoint in a global fashion.
var _ = SIGDescribe("[Feature:CalicoPolicy-v3][Serial]", func() {
	f := framework.NewDefaultFramework("hep")
	var ctl *calico.Calicoctl

	var hepNodeName string
	var hepSvc *v1.Service
	const hepPort1 = 9090
	const hepPort2 = 9091

	// Applying a hep without any networkpolicy in place will block all inbound and outbound
	// connectivity, including the kubelet's connection to the apiserver, which can
	// disrupt core cluster functionality. This defaultAllow-egress policy
	// simplifies the test by ensuring that outbound connections are allowed.
	defaultAllow := strings.Join([]string{
		"apiVersion: projectcalico.org/v3",
		"kind: GlobalNetworkPolicy",
		"metadata:",
		"  name: heptest",
		"spec:",
		"  selector: heptest == \"heptest\"",
		"  egress:",
		"  - action: Allow",
	}, "\n")

	// avoidNodeCustomizer is used to ensure our client pod is not on the same node
	// as the hostEndpoint, and that it isn't on the master node.
	avoidNodeCustomizer := func(pod *v1.Pod) {
		pod.Spec.Affinity = &v1.Affinity{
			NodeAffinity: &v1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
					NodeSelectorTerms: []v1.NodeSelectorTerm{
						{
							MatchExpressions: []v1.NodeSelectorRequirement{
								{
									Key:      "kubernetes.io/hostname",
									Operator: v1.NodeSelectorOpNotIn,
									Values:   []string{hepNodeName},
								},
								{
									Key:      "node-role.kubernetes.io/master",
									Operator: v1.NodeSelectorOpDoesNotExist,
								},
							},
						},
					},
				},
			},
		}
	}

	Context("Host Endpoints", func() {
		BeforeEach(func() {
			ctl = calico.ConfigureCalicoctl(f)

			// Launch a hostNetworked server pod.
			var pod *v1.Pod
			pod, hepSvc = createHostNetworkedServerPodAndService(f, f.Namespace, "server", []int{hepPort1, hepPort2})
			framework.WaitForPodRunningInNamespace(f.ClientSet, pod)

			// Get the pod to find out its nodeName, as that information isn't available until
			// it is running.
			pod, err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).Get(pod.Name, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())

			// Note that Kubernetes node name is not strictly the same as the calico node name, and its the calico
			// node name that we need for the HEP. But we know these values will match for our rigs so in the interest
			// of keeping things simple, just use the k8s nodename.
			hepNodeName = pod.Spec.NodeName

			// Create our defaultAllow policy so we don't lock up the node.
			ctl.Apply(defaultAllow)

			// Create our hostEndpoint.
			setupHostEndPoint(ctl, hepNodeName, hepPort1, pod.Status.HostIP)
		})

		It("should block all inbound connections by default", func() {
			target1 := fmt.Sprintf("%s.%s:%d", hepSvc.Name, hepSvc.Namespace, hepPort1)
			target2 := fmt.Sprintf("%s.%s:%d", hepSvc.Name, hepSvc.Namespace, hepPort2)
			testCannotConnectX(f, f.Namespace, "client", hepSvc, target1, avoidNodeCustomizer)
			testCannotConnectX(f, f.Namespace, "client", hepSvc, target2, avoidNodeCustomizer)
		})

		It("should allow inbound connections with an allow-all", func() {
			// create a networkpolicy that allows all incoming connections.
			// assert client can ping hep
			policy := fmt.Sprintf(strings.Join([]string{
				"apiVersion: projectcalico.org/v3",
				"kind: GlobalNetworkPolicy",
				"metadata:",
				"  name: %[1]s",
				"spec:",
				"  selector: host-endpoint == \"%[1]s\"",
				"  ingress:",
				"  - action: Allow",
			}, "\n"), hepNodeName)
			ctl.Apply(policy)
			defer ctl.Delete(policy)

			target1 := fmt.Sprintf("%s.%s:%d", hepSvc.Name, hepSvc.Namespace, hepPort1)
			target2 := fmt.Sprintf("%s.%s:%d", hepSvc.Name, hepSvc.Namespace, hepPort2)
			testCanConnectX(f, f.Namespace, "client", hepSvc, target1, avoidNodeCustomizer, func() {})
			testCanConnectX(f, f.Namespace, "client", hepSvc, target2, avoidNodeCustomizer, func() {})
		})

		It("should allow connections only to the specified named port", func() {
			policy := fmt.Sprintf(strings.Join([]string{
				"apiVersion: projectcalico.org/v3",
				"kind: GlobalNetworkPolicy",
				"metadata:",
				"  name: %[1]s",
				"spec:",
				"  selector: host-endpoint == \"%[1]s\"",
				"  ingress:",
				"  - action: Allow",
				"    protocol: TCP",
				"    destination:",
				"      ports:",
				"      - hepport",
			}, "\n"), hepNodeName)
			ctl.Apply(policy)
			defer ctl.Delete(policy)

			target1 := fmt.Sprintf("%s.%s:%d", hepSvc.Name, hepSvc.Namespace, hepPort1)
			target2 := fmt.Sprintf("%s.%s:%d", hepSvc.Name, hepSvc.Namespace, hepPort2)
			testCanConnectX(f, f.Namespace, "client", hepSvc, target1, avoidNodeCustomizer, func() {})
			testCannotConnectX(f, f.Namespace, "client", hepSvc, target2, avoidNodeCustomizer)
		})

		AfterEach(func() {
			ctl.DeleteHE(hepNodeName)
			ctl.Delete(defaultAllow)
		})
	})
})

func setupHostEndPoint(ctl *calico.Calicoctl, nodeName string, port int, ip string) {
	hep := fmt.Sprintf(strings.Join([]string{
		"apiVersion: projectcalico.org/v3",
		"kind: HostEndpoint",
		"metadata:",
		"  name: %[1]s",
		"  labels:",
		"    host-endpoint: %[1]s",
		"    heptest: heptest",
		"spec:",
		"  node: %[1]s",
		"  expectedIPs:",
		"  - %[2]s",
		"  ports:",
		"  - name: hepport",
		"    port: %[3]d",
		"    protocol: TCP",
	}, "\n"), nodeName, ip, port)

	ctl.Apply(hep)
}
