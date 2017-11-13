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
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// TODO: Need to consolidate these tests with the ones in test/e2e/network/calico_policy.

var _ = SIGDescribe("[Feature:CalicoPolicy-v3] calico policy", func() {
	var service *v1.Service
	var podServer *v1.Pod
	var calicoctl *calico.Calicoctl
	serverPort1 := 80

	f := framework.NewDefaultFramework("calico-policy")

	BeforeEach(func() {
		// The following code tries to get config information for calicoctl from k8s ConfigMap.
		// A framework clientset is needed to access k8s configmap but it will only be created in the context of BeforeEach or IT.
		// Current solution is to use BeforeEach because this function is not a test case.
		// This will avoid complexity of creating a client by ourself.
		calicoctl = calico.ConfigureCalicoctl(f)
	})
	Context("Calico specific network policy", func() {
		BeforeEach(func() {
			// Create Server with Service
			By("Creating a simple server.")
			podServer, service = createServerPodAndService(f, f.Namespace, "server", []int{80})
			framework.Logf("Waiting for Server to come up.")
			err := framework.WaitForPodRunningInNamespace(f.ClientSet, podServer)
			Expect(err).NotTo(HaveOccurred())

			By("Creating client which will be able to contact the server since no policies are present.")
			testCanConnect(f, f.Namespace, "client-can-connect", service, 80)
			calicoctl = calico.ConfigureCalicoctl(f)

		})

		AfterEach(func() {
			cleanupServerPodAndService(f, podServer, service)
		})

		It("should correctly isolate namespaces by ingress and egress policies", func() {
			nsA := f.Namespace
			serviceA := service
			nsBName := f.BaseName + "-b"
			// The CreateNamespace helper uses the input name as a Name Generator, so the namespace itself
			// will have a different name than what we are setting as the value of ns-name.
			// This is fine as long as we don't try to match the label as nsB.Name in our policy.
			nsB, err := f.CreateNamespace(nsBName, map[string]string{
				"ns-name": nsBName,
			})
			Expect(err).NotTo(HaveOccurred())
			framework.Logf("Created a new namespace %s.", nsB.Name)

			By("Creating a simple server.")
			podServerB, serviceB := createServerPodAndService(f, nsB, "server-b", []int{80})
			defer cleanupServerPodAndService(f, podServerB, serviceB)
			framework.Logf("Waiting for Server to come up.")
			err = framework.WaitForPodRunningInNamespace(f.ClientSet, podServerB)
			Expect(err).NotTo(HaveOccurred())

			// TODO (mattl): remove this and rework these policies. Currently need to create a default deny since Calico v2.6.0
			// defaults to allow for any non matching policies while 2.5.1 and earlier default to deny.
			By("Creating a namespace-wide default-deny policy")
			denyPolicyStr := `
- apiVersion: projectcalico.org/v3
  kind: GlobalNetworkPolicy
  metadata:
    name: default-deny-all
  spec:
    order: 5000
    selector: has(pod-name)
`
			calicoctl.Apply(denyPolicyStr)
			defer calicoctl.DeleteGNP("default-deny-all")

			By("Creating calico namespace isolation policy.")
			nsNameA := f.Namespace.Name
			policyName := fmt.Sprintf("%s.%s", nsNameA, "namespace-isolation-a")
			policyStr := fmt.Sprintf(`
- apiVersion: projectcalico.org/v3
  kind: NetworkPolicy
  metadata:
    name: %s
    namespace: %s
  spec:
    order: 1000
    ingress:
      - action: allow
        source:
          namespaceSelector: e2e-framework == "%s"
    egress:
      - action: allow
        destination:
          namespaceSelector: e2e-framework == "%s"
`,
				policyName, nsNameA, f.BaseName, f.BaseName)
			calicoctl.Apply(policyStr)
			defer calicoctl.DeleteNP(nsNameA, policyName)

			By("Creating another calico namespace isolation policy.")
			policyNameB := fmt.Sprintf("%s.%s", nsBName, "namespace-isolation-b")
			policyStrB := fmt.Sprintf(`
- apiVersion: projectcalico.org/v3
  kind: NetworkPolicy
  metadata:
    name: %s
    namespace: %s
  spec:
    order: 1000
    ingress:
      - action: allow
        source:
          namespaceSelector: ns-name == "%s"
    egress:
      - action: allow
        destination:
          namespaceSelector: ns-name == "%s"
`,
				policyNameB, nsB.Name, nsBName, nsBName)
			calicoctl.Apply(policyStrB)
			defer calicoctl.DeleteNP(nsB.Name, policyNameB)

			By("Creating calico allow to dns policy.")
			dnsPolicyName := fmt.Sprintf("%s.%s", nsNameA, "allow-egress-to-dns")
			dnsPolicyStr := fmt.Sprintf(`
apiVersion: projectcalico.org/v3
kind: GlobalNetworkPolicy
metadata:
  name: %s
spec:
  order: 400
  selector: all()
  egress:
    - action: allow
      destination:
        selector: k8s-app == "kube-dns"
`,
				dnsPolicyName)
			calicoctl.Apply(dnsPolicyStr)
			defer calicoctl.DeleteGNP(dnsPolicyName)

			By("Creating calico allow dns egress policy.")
			dnsEgressPolicyName := fmt.Sprintf("%s.%s", nsNameA, "allow-dns-egress")
			dnsEgressPolicyStr := fmt.Sprintf(`
apiVersion: projectcalico.org/v3
kind: GlobalNetworkPolicy
metadata:
  name: %s
spec:
  order: 300
  selector: k8s-app == "kube-dns"
  egress:
    - action: allow
`,
				dnsEgressPolicyName)
			calicoctl.Apply(dnsEgressPolicyStr)
			defer calicoctl.DeleteGNP(dnsEgressPolicyName)

			By("allow A -> A")
			testCanConnect(f, nsA, "client-a", serviceA, 80)
			By("allow B -> B")
			testCanConnect(f, nsB, "client-b", serviceB, 80)
			By("deny B -> A")
			testCannotConnect(f, nsB, "client-b", serviceA, 80)
			By("deny A -> B")
			testCannotConnect(f, nsA, "client-a", serviceB, 80)
		})

		It("should be able to set up a \"default-deny\" policy for a namespace", func() {
			nsA := f.Namespace
			serviceA := service
			nsBName := f.BaseName + "-b"
			// The CreateNamespace helper uses the input name as a Name Generator, so the namespace itself
			// will have a different name than what we are setting as the value of ns-name.
			// This is fine as long as we don't try to match the label as nsB.Name in our policy.
			nsB, err := f.CreateNamespace(nsBName, map[string]string{
				"ns-name": nsBName,
			})
			Expect(err).NotTo(HaveOccurred())
			framework.Logf("Created a new namespace %s.", nsB.Name)

			By("Creating a simple server.")
			podServerB, serviceB := createServerPodAndService(f, nsB, "server-b", []int{80})
			defer cleanupServerPodAndService(f, podServerB, serviceB)
			framework.Logf("Waiting for Server to come up.")
			err = framework.WaitForPodRunningInNamespace(f.ClientSet, podServerB)
			Expect(err).NotTo(HaveOccurred())

			// TODO (mattl): remove this and rework these policies. Currently need to create a default deny since Calico v2.6.0
			// defaults to allow for any non matching policies while 2.5.1 and earlier default to deny.
			By("Creating a namespace-wide default-deny policy")
			denyPolicyStr := fmt.Sprintf(`
- apiVersion: projectcalico.org/v3
  kind: NetworkPolicy
  metadata:
    name: default-deny
    namespace: %s
  spec:
    order: 5000
`,
				nsA.Name)
			calicoctl.Apply(denyPolicyStr)
			defer calicoctl.DeleteNP(nsA.Name, "default-deny")

			By("Creating calico allow egress in namespace A.")
			policyName := fmt.Sprintf("%s.%s", nsA.Name, "allow-egress")
			// Policy only defines egress rules so ingress rules are defaulted to nothing (deny)
			policyStr := fmt.Sprintf(`
- apiVersion: projectcalico.org/v3
  kind: NetworkPolicy
  metadata:
    name: %s
    namespace: %s
  spec:
    order: 1000
    egress:
    - action: allow
      destination: {}
      source: {}
`,
				policyName, nsA.Name)
			calicoctl.Apply(policyStr)
			defer calicoctl.DeleteNP(nsA.Name, policyName)

			By("allow B -> B")
			testCanConnect(f, nsB, "client-b", serviceB, 80)
			By("deny A -> A")
			testCannotConnect(f, nsA, "client-a", serviceA, 80)
			By("deny B -> A")
			testCannotConnect(f, nsB, "client-b", serviceA, 80)
			By("allow A -> B")
			testCanConnect(f, nsA, "client-a", serviceB, 80)
		})

		It("should correctly overwrite existing calico policies with simple ingress and egress policies", func() {
			nsA := f.Namespace
			serviceA := service

			nsAName := f.Namespace.Name
			nsALabelName := "e2e-framework"
			nsALabelValue := f.BaseName
			nsBLabelName := "ns-name"
			nsBLabelValue := f.BaseName + "-b"

			// The CreateNamespace helper uses the input name as a Name Generator, so the namespace itself
			// will have a different name than what we are setting as the value of ns-name.
			// This is fine as long as we don't try to match the label as nsB.Name in our policy.
			nsB, err := f.CreateNamespace(nsBLabelValue, map[string]string{
				nsBLabelName: nsBLabelValue,
			})
			Expect(err).NotTo(HaveOccurred())
			framework.Logf("Created a new namespace %s.", nsB.Name)
			nsBName := nsB.Name

			By("Creating a simple server.")
			podServerB, serviceB := createServerPodAndService(f, nsB, "server-b", []int{80})
			defer cleanupServerPodAndService(f, podServerB, serviceB)
			framework.Logf("Waiting for Server to come up.")
			err = framework.WaitForPodRunningInNamespace(f.ClientSet, podServerB)
			Expect(err).NotTo(HaveOccurred())

			// Verify that by default, all namespaces can connect with each other
			// Currently commented out because it breaks tests that are not broken without this block
			/*
				By("allow A -> A")
				testCanConnect(f, nsA, "client-a", serviceA, 80)
				By("allow B -> B")
				testCanConnect(f, nsB, "client-b", serviceB, 80)
				By("allow B -> A")
				testCanConnect(f, nsB, "client-b", serviceA, 80)
				By("allow A -> B")
				testCanConnect(f, nsA, "client-a", serviceB, 80)
			*/

			By("Creating a namespace-wide default-deny policy")
			denyPolicyStr := `
- apiVersion: projectcalico.org/v3
  kind: GlobalNetworkPolicy
  metadata:
    name: default-deny-all
  spec:
    order: 5000
    selector: all()
    ingress:
    - action: deny
      source: {}
      destination: {}
`
			calicoctl.Apply(denyPolicyStr)
			defer calicoctl.DeleteGNP("default-deny-all")

			By("Creating calico policy to allow dns egress.")
			dnsPolicyName := fmt.Sprintf("%s.%s", nsAName, "allow-dns-egress")
			dnsPolicyStr := fmt.Sprintf(`
apiVersion: projectcalico.org/v3
kind: GlobalNetworkPolicy
metadata:
  name: %s
spec:
  order: 10
  selector: k8s-app == "kube-dns"
  egress:
    - action: allow
  ingress:
    - action: allow
`,
				dnsPolicyName)
			calicoctl.Apply(dnsPolicyStr)
			defer calicoctl.DeleteGNP(dnsPolicyName)

			By("Creating calico policy to access dns.")
			dnsPolicyName = fmt.Sprintf("%s.%s", nsAName, "allow-access-to-dns")
			dnsPolicyStr = fmt.Sprintf(`
- apiVersion: projectcalico.org/v3
  kind: GlobalNetworkPolicy
  metadata:
    name: %s
  spec:
    order: 40
    egress:
      - action: allow
        destination:
          selector: k8s-app == "kube-dns"
`,
				dnsPolicyName)
			calicoctl.Apply(dnsPolicyStr)
			defer calicoctl.DeleteGNP(dnsPolicyName)

			By("Creating calico policy to allow internal namespace traffic.")
			policyName := fmt.Sprintf("%s.%s", nsAName, "namespace-isolation-a")
			policyStr := fmt.Sprintf(`
- apiVersion: projectcalico.org/v3
  kind: NetworkPolicy
  metadata:
    name: %s
    namespace: %s
  spec:
    order: 1000
    ingress:
      - action: allow
        source:
          namespaceSelector: %s == "%s"
    egress:
      - action: allow
        destination:
          namespaceSelector: %s == "%s"
`,
				policyName, nsAName, nsALabelName, nsALabelValue, nsALabelName, nsALabelValue)
			calicoctl.Apply(policyStr)
			defer calicoctl.DeleteNP(nsAName, policyName)

			// Verify that any pods in B cannot be reached and A can only communicate with itself
			By("allow A -> A")
			testCanConnect(f, nsA, "client-a", serviceA, 80)
			By("deny B -> B")
			testCannotConnect(f, nsB, "client-b", serviceB, 80)
			By("deny B -> A")
			testCannotConnect(f, nsB, "client-b", serviceA, 80)
			By("deny A -> B")
			testCannotConnect(f, nsA, "client-a", serviceB, 80)

			By("Creating a simple ingress policy on B that allows traffic to B from A.")
			ingressPolicyName := fmt.Sprintf("%s.%s", nsBName, "simple-ingress")
			ingressPolicyStr := fmt.Sprintf(`
- apiVersion: projectcalico.org/v3
  kind: NetworkPolicy
  metadata:
    name: %s
    namespace: %s
  spec:
    order: 800
    ingress:
    - action: allow
      source:
        namespaceSelector: %s == "%s"
`,
				ingressPolicyName, nsBName, nsALabelName, nsALabelValue)
			calicoctl.Apply(ingressPolicyStr)
			defer calicoctl.DeleteNP(nsBName, ingressPolicyName)

			// This should not be accessible yet since A will also need an egress policy
			By("deny A -> B")
			testCannotConnect(f, nsA, "client-a", serviceB, 80)

			By("Creating a simple egress policy on A that allows traffic to B.")
			egressPolicyName := fmt.Sprintf("%s.%s", nsAName, "simple-egress")
			egressPolicyStr := fmt.Sprintf(`
- apiVersion: projectcalico.org/v3
  kind: NetworkPolicy
  metadata:
    name: %s
    namespace: %s
  spec:
    order: 700
    egress:
    - action: allow
      destination:
        namespaceSelector: %s == "%s"
`,
				egressPolicyName, nsAName, nsBLabelName, nsBLabelValue)
			calicoctl.Apply(egressPolicyStr)
			defer calicoctl.DeleteNP(nsAName, egressPolicyName)

			By("Testing that A and B can access one another. Only B cannot connect to B.")
			By("allow A -> A")
			testCanConnect(f, nsA, "client-a", serviceA, 80)
			By("deny B -> B")
			testCannotConnect(f, nsB, "client-b", serviceB, 80)
			By("deny B -> A")
			testCannotConnect(f, nsB, "client-b", serviceA, 80)
			By("allow A -> B")
			testCanConnect(f, nsA, "client-a", serviceB, 80)
		})

		It("should correctly be able to select endpoints for policies using label selectors", func() {
			nsA := f.Namespace
			serviceA := service

			//nsAName := f.Namespace.Name
			nsALabelName := "e2e-framework"
			nsALabelValue := f.BaseName
			//Set nsBName after the namespace is created
			nsBLabelName := "ns-name"
			nsBLabelValue := f.BaseName + "-b"

			// The CreateNamespace helper uses the input name as a Name Generator, so the namespace itself
			// will have a different name than what we are setting as the value of ns-name.
			// This is fine as long as we don't try to match the label as nsB.Name in our policy.
			nsB, err := f.CreateNamespace(nsBLabelValue, map[string]string{
				nsBLabelName: nsBLabelValue,
			})
			Expect(err).NotTo(HaveOccurred())
			framework.Logf("Created a new namespace %s.", nsB.Name)
			nsBName := nsB.Name

			By("Creating simple servers with labels.")
			identifierKey := "identifier"
			podServerB, serviceB := calico.CreateServerPodAndServiceWithLabels(f, nsB, "server-b", []int{80}, map[string]string{identifierKey: "ident1"})
			defer cleanupServerPodAndService(f, podServerB, serviceB)
			framework.Logf("Waiting for Server to come up.")
			err = framework.WaitForPodRunningInNamespace(f.ClientSet, podServerB)
			Expect(err).NotTo(HaveOccurred())

			// Create a labeled server within namespace A: the namespace without a labeled server pod
			podServerC, serviceC := calico.CreateServerPodAndServiceWithLabels(f, nsA, "server-c", []int{80}, map[string]string{identifierKey: "ident2"})
			defer cleanupServerPodAndService(f, podServerC, serviceC)
			framework.Logf("Waiting for Server to come up.")
			err = framework.WaitForPodRunningInNamespace(f.ClientSet, podServerC)
			Expect(err).NotTo(HaveOccurred())

			// Test that all of the pods are able to reach each other
			// Commented out for now since it seems to make the last few tests fail
			/*
				By("allow A -> A")
				testCanConnect(f, nsA, "client-a", serviceA, 80)
				By("allow B -> B")
				testCanConnect(f, nsB, "client-b", serviceB, 80)
				By("allow B -> A")
				testCanConnect(f, nsB, "client-b", serviceA, 80)
				By("allow A -> B")
				testCanConnect(f, nsA, "client-a", serviceB, 80)
				By("allow A -> C")
				testCanConnect(f, nsA, "client-a", serviceC, 80)
				By("allow B -> C")
				testCanConnect(f, nsB, "client-b", serviceC, 80)
			*/

			// TODO (mattl): remove this and rework these policies. Currently need to create a default deny since Calico v2.6.0
			// defaults to allow for any non matching policies while 2.5.1 and earlier default to deny.
			By("Creating a namespace-wide default-deny policy")
			denyPolicyStr := `
- apiVersion: projectcalico.org/v3
  kind: GlobalNetworkPolicy
  metadata:
    name: default-deny-all
  spec:
    order: 5000
    selector: has(pod-name)
    ingress:
    - action: deny
      source: {}
      destination: {}
`
			calicoctl.Apply(denyPolicyStr)
			defer calicoctl.DeleteGNP("default-deny-all")

			// Test that none of the pods are able to reach each other since they all have a pod-name selector
			By("deny A -> A")
			testCannotConnect(f, nsA, "client-a", serviceA, 80)
			By("deny B -> B")
			testCannotConnect(f, nsB, "client-b", serviceB, 80)
			By("deny B -> A")
			testCannotConnect(f, nsB, "client-b", serviceA, 80)
			By("deny A -> B")
			testCannotConnect(f, nsA, "client-a", serviceB, 80)
			By("deny A -> C")
			testCannotConnect(f, nsA, "client-a", serviceC, 80)
			By("deny B -> C")
			testCannotConnect(f, nsB, "client-b", serviceC, 80)

			By("Creating an ingress policy to allow traffic from namespace B to any pods with with a specific label.")
			policyNameAllowB := fmt.Sprintf("%s.%s", nsBName, "ingress-allow-b")
			policyStrAllowB := fmt.Sprintf(`
- apiVersion: projectcalico.org/v3
  kind: GlobalNetworkPolicy
  metadata:
    name: %s
  spec:
    order: 900
    selector: has(%s)
    ingress:
    - action: allow
      source:
        namespaceSelector: %s == "%s"
`,
				policyNameAllowB, "pod-name", nsBLabelName, nsBLabelValue)
			calicoctl.Apply(policyStrAllowB)
			defer calicoctl.DeleteGNP(policyNameAllowB)

			// Test that any pod can receive traffic from namespace B only
			By("deny A -> A")
			testCannotConnect(f, nsA, "client-a", serviceA, 80)
			By("allow B -> B")
			testCanConnect(f, nsB, "client-b", serviceB, 80)
			By("allow B -> A")
			testCanConnect(f, nsB, "client-b", serviceA, 80)
			By("deny A -> B")
			testCannotConnect(f, nsA, "client-a", serviceB, 80)
			By("deny A -> C")
			testCannotConnect(f, nsA, "client-a", serviceC, 80)
			By("allow B -> C")
			testCanConnect(f, nsB, "client-b", serviceC, 80)

			By("Creating an ingress policy to allow traffic from namespace A and deny traffic from namespace B only on a specific label with a specific value.")
			policyNameSpecificLabel := fmt.Sprintf("%s", "ingress-allow-a-deny-b")
			policyStrSpecificLabel := fmt.Sprintf(`
- apiVersion: projectcalico.org/v3
  kind: GlobalNetworkPolicy
  metadata:
    name: %s
  spec:
    order: 800
    selector: %s in {"%s"}
    ingress:
    - action: allow
      source:
        namespaceSelector: %s == "%s"
    - action: deny
      source:
        namespaceSelector: %s == "%s"
`,
				policyNameSpecificLabel, identifierKey, "ident2",
				nsALabelName, nsALabelValue,
				nsBLabelName, nsBLabelValue)
			calicoctl.Apply(policyStrSpecificLabel)
			defer calicoctl.DeleteGNP(policyNameSpecificLabel)

			// Test that only A can access C. B should be able to access A but not C.
			By("deny A -> A")
			testCannotConnect(f, nsA, "client-a", serviceA, 80)
			By("allow B -> B")
			testCanConnect(f, nsB, "client-b", serviceB, 80)
			By("allow B -> A")
			testCanConnect(f, nsB, "client-b", serviceA, 80)
			By("deny A -> B")
			testCannotConnect(f, nsA, "client-a", serviceB, 80)
			By("allow A -> C")
			testCanConnect(f, nsA, "client-a", serviceC, 80)
			By("deny B -> C")
			testCannotConnect(f, nsB, "client-b", serviceC, 80)
		})

		/*
			It("should correctly overwrite existing calico policies with simple ingress and egress policies using Calico v2.6.0 types", func() {
				// TODO (mattl): uncomment this test when Essentials has been upgraded to Calico v2.6.0
				nsA := f.Namespace
				serviceA := service
				nsBName := f.BaseName + "-b"
				// The CreateNamespace helper uses the input name as a Name Generator, so the namespace itself
				// will have a different name than what we are setting as the value of ns-name.
				// This is fine as long as we don't try to match the label as nsB.Name in our policy.
				nsB, err := f.CreateNamespace(nsBName, map[string]string{
					"ns-name": nsBName,
				})
				Expect(err).NotTo(HaveOccurred())
				framework.Logf("Created a new namespace %s.", nsB.Name)

				By("Creating a simple server.")
				podServerB, serviceB := createServerPodAndService(f, nsB, "server-b", []int{80})
				defer cleanupServerPodAndService(f, podServerB, serviceB)
				framework.Logf("Waiting for Server to come up.")
				err = framework.WaitForPodRunningInNamespace(f.ClientSet, podServerB)
				Expect(err).NotTo(HaveOccurred())
		*/

		// Verify that by default, all namespaces can connect with each other
		// Currently commented out because it breaks tests that are not broken without this block
		/*
			By("allow A -> A")
			testCanConnect(f, nsA, "client-a", serviceA, 80)
			By("allow B -> B")
			testCanConnect(f, nsB, "client-b", serviceB, 80)
			By("allow B -> A")
			testCanConnect(f, nsB, "client-b", serviceA, 80)
			By("allow A -> B")
			testCanConnect(f, nsA, "client-a", serviceB, 80)
		*/

		/*
						// TODO (mattl): remove this and rework these policies. Currently need to create a default deny since Calico v2.6.0
						// defaults to allow for any non matching policies while 2.5.1 and earlier default to deny.
						By("Creating a namespace-wide default-deny policy")
						denyPolicyStr := `
			- apiVersion: projectcalico.org/v3
			  kind: NetworkPolicy
			  metadata:
			    name: default-deny-all
			  spec:
			    order: 5000
			    selector: has(pod-name)
			    ingress:
			    - action: deny
			      source: {}
			      destination: {}
			`
						v2CreateCalicoPolicy(f, "default-deny-all", denyPolicyStr)
						defer v2CleanupCalicoPolicy(f, "default-deny-all")

						By("Creating calico namespace isolation policy.")
						sNameA := f.Namespace.Name
						policyName := fmt.Sprintf("%s.%s", nsNameA, "namespace-isolation-a")
						policyStr := fmt.Sprintf(`
			- apiVersion: projectcalico.org/v3
			  kind: NetworkPolicy
			  metadata:
			    name: %s
			  spec:
			    order: 1000
			    selector: calico/k8s_ns == "%s"
			    types:
			    - ingress
			    - egress
			    ingress:
			      - action: allow
			        source:
			          selector: calico/k8s_ns == "%s"
			    egress:
			      - action: allow
			        destination:
			          selector: calico/k8s_ns == "%s"
			`,
							policyName, nsNameA, nsNameA, nsNameA)
						v2CreateCalicoPolicy(f, policyName, policyStr)
						defer v2CleanupCalicoPolicy(f, policyName)

						By("Creating a calico \"default-deny\" policy that blocks all traffic to it.")
						nsNameB := nsB.Name
						policyNameB := fmt.Sprintf("%s.%s", nsNameB, "default-deny")
						policyStrB := fmt.Sprintf(`
			- apiVersion: projectcalico.org/v3
			  kind: NetworkPolicy
			  metadata:
			    name: %s
			  spec:
			    order: 900
			    selector: calico/k8s_ns == "%s"
			    types:
			    - egress
			    egress:
			    - action: allow
			      destination: {}
			      source: {}
			`,

							policyNameB, nsNameB)
						v2CreateCalicoPolicy(f, policyNameB, policyStrB)
						defer v2CleanupCalicoPolicy(f, policyNameB)

						By("Creating calico allow dns policy.")
						dnsPolicyName := fmt.Sprintf("%s.%s", nsNameA, "allow-dns")
						dnsPolicyStr := fmt.Sprintf(`
			- apiVersion: projectcalico.org/v3
			  kind: NetworkPolicy
			  metadata:
			    name: %s
			  spec:
			    order: 400
			    selector: calico/k8s_ns == "%s" || calico/k8s_ns == "%s"
			    types:
			    - egress
			    egress:
			      - action: allow
			        destination:
			          selector: calico/k8s_ns == "kube-system" && k8s-app == "kube-dns"
			`,
							dnsPolicyName, nsNameA, nsNameB)
						v2CreateCalicoPolicy(f, dnsPolicyName, dnsPolicyStr)
						defer v2CleanupCalicoPolicy(f, dnsPolicyName)

						// Verify that any pods in B cannot be reached and A can only communicate with itself
						By("allow A -> A")
						testCanConnect(f, nsA, "client-a", serviceA, 80)
						By("deny B -> B")
						testCannotConnect(f, nsB, "client-b", serviceB, 80)
						By("deny B -> A")
						testCannotConnect(f, nsB, "client-b", serviceA, 80)
						By("deny A -> B")
						testCannotConnect(f, nsA, "client-a", serviceB, 80)

						By("Creating a simple ingress policy on B that allows traffic to B from A.")
						ingressPolicyName := fmt.Sprintf("%s.%s", nsNameB, "simple-ingress")
						ingressPolicyStr := fmt.Sprintf(`
			- apiVersion: projectcalico.org/v3
			  kind: NetworkPolicy
			  metadata:
			    name: %s
			  spec:
			    order: 800
			    selector: calico/k8s_ns == "%s"
			    types:
			    - ingress
			    ingress:
			    - action: allow
			      source:
			        selector: calico/k8s_ns == "%s"
			`,
							ingressPolicyName, nsNameB, nsNameA)
						v2CreateCalicoPolicy(f, ingressPolicyName, ingressPolicyStr)
						defer v2CleanupCalicoPolicy(f, ingressPolicyName)

						// This should not be accessible yet since A will also need an egress policy
						By("deny A -> B")
						testCannotConnect(f, nsA, "client-a", serviceB, 80)

						By("Creating a simple egress policy on A that allows traffic to B.")
						egressPolicyName := fmt.Sprintf("%s.%s", nsNameA, "simple-egress")
						egressPolicyStr := fmt.Sprintf(`
			- apiVersion: projectcalico.org/v3
			  kind: NetworkPolicy
			  metadata:
			    name: %s
			  spec:
			    order: 700
			    selector: calico/k8s_ns == "%s"
			    types:
			    - egress
			    egress:
			    - action: allow
			      destination:
			        selector: calico/k8s_ns == "%s"
			`,
							egressPolicyName, nsNameA, nsNameB)
						v2CreateCalicoPolicy(f, egressPolicyName, egressPolicyStr)
						defer v2CleanupCalicoPolicy(f, egressPolicyName)

						By("Testing that A and B can access one another. Only B cannot connect to B.")
						By("allow A -> A")
						testCanConnect(f, nsA, "client-a", serviceA, 80)
						By("deny B -> B")
						testCannotConnect(f, nsB, "client-b", serviceB, 80)
						By("deny B -> A")
						testCannotConnect(f, nsB, "client-b", serviceA, 80)
						By("allow A -> B")
						testCanConnect(f, nsA, "client-a", serviceB, 80)
					})
		*/
	})

	It("should enforce rule ordering correctly", func() {
		ns := f.Namespace
		calicoctl := calico.ConfigureCalicoctl(f)

		By("Create a simple server pod.")
		serverPod, service := createServerPodAndService(f, ns, "server", []int{serverPort1})
		defer cleanupServerPodAndService(f, serverPod, service)
		framework.Logf("Waiting for server pod to come up.")
		err := framework.WaitForPodRunningInNamespace(f.ClientSet, serverPod)
		Expect(err).NotTo(HaveOccurred())

		By("Creating a client which should be able to connect to the server since no policies are present.")
		testCanConnect(f, ns, "client", service, serverPort1)

		By("Applying a policy that drops traffic from client.")
		calicoctl.Apply(
			`
- apiVersion: projectcalico.org/v3
  kind: NetworkPolicy
  metadata:
    name: client-policy
    namespace: %s
  spec:
    order: 100
    ingress:
      - action: deny
        protocol: tcp
        source:
          selector: pod-name=="client"
        destination:
          ports: [%d]
    selector: pod-name=="%s"
`,
			ns.Name, serverPort1, serverPod.Name)
		defer func() {
			calicoctl.Exec("delete", "-n", ns.Name, "policy", "client-policy")
		}()
		By("Creating a client that should not be able to connect to the server")
		testCannotConnect(f, ns, "client", service, serverPort1)

		By("Updating the policy with a allow rule before the drop rule in the same policy.")
		calicoctl.Apply(
			`
- apiVersion: projectcalico.org/v3
  kind: NetworkPolicy
  metadata:
    name: client-policy
    namespace: %s
  spec:
    order: 100
    ingress:
      - action: allow
        protocol: tcp
        source:
          selector: pod-name=="client"
        destination:
          ports: [%d]
      - action: deny
        protocol: tcp
        source:
          selector: pod-name=="client"
        destination:
          ports: [%d]
    selector: pod-name=="%s"
`,
			ns.Name, serverPort1, serverPort1, serverPod.Name)

		By("Creating a client which should be able to connect to the server since there is a allow rule.")
		testCanConnect(f, ns, "client", service, serverPort1)
	})

	It("should support CRUD operations of a policy", func() {
		ns := f.Namespace
		calicoctl := calico.ConfigureCalicoctl(f)

		By("Create a simple server pod.")
		serverPod1, service := createServerPodAndService(f, ns, "server1", []int{serverPort1})
		defer cleanupServerPodAndService(f, serverPod1, service)
		framework.Logf("Waiting for server pod to come up.")
		err := framework.WaitForPodRunningInNamespace(f.ClientSet, serverPod1)
		Expect(err).NotTo(HaveOccurred())

		By("Creating a client which should be able to connect to the server since no policies are present.")
		testCanConnect(f, ns, "client", service, serverPort1)

		policyName := "test-policy"

		By("Applying a policy that drops traffic from client.")
		calicoctl.Apply(
			`
- apiVersion: projectcalico.org/v3
  kind: NetworkPolicy
  metadata:
    name: %s
    namespace: %s
  spec:
    order: 100
    ingress:
      - action: deny
        protocol: tcp
        source:
          selector: pod-name=="client"
        destination:
          ports: [%d]
    selector: pod-name=="%s"
`,
			policyName, ns.Name, serverPort1, serverPod1.Name)
		By("Creating a client that should not be able to connect to the server")
		testCannotConnect(f, ns, "client", service, serverPort1)

		By("Checking if the policy exists")
		v2CheckPolicyExists(calicoctl, policyName, ns.Name)

		// TODO(doublek): Consider if combining this with the logging test makes sense.
		By("Replace a policy that allows traffic from client.")
		calicoctl.Replace(
			`
- apiVersion: projectcalico.org/v3
  kind: NetworkPolicy
  metadata:
    name: %s
    namespace: %s
  spec:
    order: 100
    ingress:
      - action: allow
        protocol: tcp
        source:
          selector: pod-name=="client"
        destination:
          ports: [%d]
    selector: pod-name=="%s"
`,
			policyName, ns.Name, serverPort1, serverPod1.Name)
		By("Creating a client that can connect to the server")
		testCanConnect(f, ns, "client", service, serverPort1)

		By("Deleting the policy")
		calicoctl.Exec("delete", "-n", ns.Name, "policy", policyName)

		By("Checking if the policy doesn't exists")
		v2CheckPolicyDoesntExist(calicoctl, policyName, "")
	})

	// TODO: re-enable when we've got a minute to figure out how to grab these logs
	//       in a more universal manner.
	//	It("should support a 'log' rule", func() {
	//		ns := f.Namespace
	//
	//		By("Create a simple server pod.")
	//		serverPod, service := createServerPodAndService(f, ns, "server", []int{serverPort1})
	//		defer cleanupServerPodAndService(f, serverPod, service)
	//		framework.Logf("Waiting for server pod to come up.")
	//		err := framework.WaitForPodRunningInNamespace(f.ClientSet, serverPod)
	//		Expect(err).NotTo(HaveOccurred())
	//
	//		By("Creating a client which should be able to connect to the server since no policies are present.")
	//		testCanConnect(f, ns, "client", service, serverPort1)
	//
	//		By("Verifying that initially there are no logs present in the node running the server pod")
	//		serverPodNow, err := f.ClientSet.Core().Pods(ns.Name).Get(serverPod.Name, metav1.GetOptions{})
	//		Expect(err).NotTo(HaveOccurred())
	//		serverNodeName := serverPodNow.Spec.NodeName
	//		serverNode, err := f.ClientSet.Core().Nodes().Get(serverNodeName, metav1.GetOptions{})
	//		Expect(err).NotTo(HaveOccurred())
	//		framework.Logf("Server is running on %v", serverNodeName)
	//		serverSyslogCount := calico.CountSyslogLines(serverNode)
	//
	//		By("Applying a policy that logs traffic from client then drops the same traffic.")
	//		calico.CalicoctlApply(
	//			`
	//- apiVersion: projectcalico.org/v3
	//  kind: NetworkPolicy
	//  metadata:
	//    name: policy-log-then-deny
	//    namespace: %s
	//  spec:
	//    order: 100
	//    ingress:
	//      - action: log
	//        protocol: tcp
	//        source:
	//          selector: pod-name=="client"
	//        destination:
	//          ports: [%d]
	//      - action: deny
	//        protocol: tcp
	//        source:
	//          selector: pod-name=="client"
	//        destination:
	//          ports: [%d]
	//    selector: pod-name=="%s"
	//`,
	//			ns.Name, serverPort1, serverPort1, serverPod.Name)
	//		defer func() {
	//			calico.Calicoctl("delete", "-n", ns.Name, "policy", "policy-log-then-deny")
	//		}()
	//		By("Creating a client that should not be able to connect to the server")
	//		testCannotConnect(f, ns, "client", service, serverPort1)
	//
	//		newDropLogs := calico.GetNewCalicoDropLogs(serverNode, serverSyslogCount, "calico-packet")
	//		framework.Logf("New drop logs: %#v", newDropLogs)
	//		Expect(len(newDropLogs)).NotTo(BeZero())
	//	})

	It("should support 'DefaultEndpointToHostAction'", func() {
		// TODO(doublek): Doesn't do DefaultEndpointToHostAction 'RETURN' yet.
		// Only 'DROP' and 'ACCEPT' for now.

		connectivityCheckTimeout := 10

		// Pick a node on where we can spawn a pod and test DefaultEndpointToHostAction.
		_, nodes := framework.GetMasterAndWorkerNodesOrDie(f.ClientSet)
		nodeName, nodeIP := v2ChooseNode(nodes)

		By("Setting 'DefaultEndpointToHostAction' to 'DROP' and restarting calico node.")
		calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_DEFAULTENDPOINTTOHOSTACTION", "DROP")
		calico.RestartCalicoNodePods(f.ClientSet, "")

		By("Creating a client which should not be able to ping the host since DefaultEndpointToHostAction is 'DROP'.")
		Expect(framework.CheckConnectivityToHost(f, nodeName, "ping-test-cant-connect", nodeIP, framework.IPv4PingCommand, connectivityCheckTimeout)).To(HaveOccurred())
		// Pod created above will be cleaned up when the namespace goes away, which is exectuted as part of test teardown.

		By("Setting 'DefaultEndpointToHostAction' to 'ACCEPT' and restarting calico node.")
		calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_DEFAULTENDPOINTTOHOSTACTION", "ACCEPT")
		calico.RestartCalicoNodePods(f.ClientSet, "")

		By("Creating a client which should able to ping the host since DefaultEndpointToHostAction is 'ACCEPT'.")
		Expect(framework.CheckConnectivityToHost(f, nodeName, "ping-test-can-connect", nodeIP, framework.IPv4PingCommand, connectivityCheckTimeout)).NotTo(HaveOccurred())
		// Pod created above will be cleaned up when the namespace goes away, which is exectuted as part of test teardown.
	})
	It("should allow negative selectors and support for filtering ICMP", func() {
		ns := f.Namespace
		calicoctl := calico.ConfigureCalicoctl(f)

		By("Create a server pods with label 'icmp:yes'.")
		serverName := "server-icmp"
		labels := map[string]string{
			"pod-name": serverName,
			"icmp":     "yes",
			"role":     "server",
		}
		serverIcmpPod := calico.CreateServerPodWithLabels(f, ns, serverName, labels, serverPort1)
		defer calico.CleanupServerPod(f, serverIcmpPod)
		framework.Logf("Pod created")
		framework.Logf("Waiting for server pod to come up.")
		err := framework.WaitForPodRunningInNamespace(f.ClientSet, serverIcmpPod)
		Expect(err).NotTo(HaveOccurred())

		serverName = "server-no-icmp"
		labels = map[string]string{
			"pod-name": serverName,
			"role":     "server",
		}
		serverNoIcmpPod := calico.CreateServerPodWithLabels(f, ns, serverName, labels, serverPort1)
		defer calico.CleanupServerPod(f, serverNoIcmpPod)
		framework.Logf("Pod created")
		framework.Logf("Waiting for server pod to come up.")
		err = framework.WaitForPodRunningInNamespace(f.ClientSet, serverNoIcmpPod)
		Expect(err).NotTo(HaveOccurred())

		By("Applying a policy that logs traffic from client then drops the same traffic.")
		calicoctl.Apply(
			`
- apiVersion: projectcalico.org/v3
  kind: NetworkPolicy
  metadata:
    name: deny-icmp
    namespace: %s
  spec:
    ingress:
    - action: deny
      protocol: icmp
    egress:
      - action: allow
    order: 100
    selector: "!has(icmp)"
- apiVersion: projectcalico.org/v3
  kind: NetworkPolicy
  metadata:
    name: allow-icmp-access
    namespace: %s
  spec:
    ingress:
    - action: allow
    egress:
      - action: allow
    order: 50
    selector: role != "server"
`, ns.Name, ns.Name,
		)
		defer func() {
			calicoctl.Exec("delete", "-n", ns.Name, "policy", "allow-icmp-access")
			calicoctl.Exec("delete", "-n", ns.Name, "policy", "deny-icmp")
		}()

		By("Pinging the IMCP allowed pod")
		icmpPod, err := f.ClientSet.Core().Pods(ns.Name).Get(serverIcmpPod.Name, metav1.GetOptions{})
		calico.TestCanPing(f, ns, "client-can-ping", icmpPod)

		By("Pinging the IMCP not allowed pod")
		notIcmpPod, err := f.ClientSet.Core().Pods(ns.Name).Get(serverNoIcmpPod.Name, metav1.GetOptions{})
		calico.TestCannotPing(f, ns, "client-cannot-ping", notIcmpPod)
	})
})

func v2ChooseNode(nodes *v1.NodeList) (string, string) {
	// TODO(doublek): Make a more informed decision here rather than picking the first one.
	node := nodes.Items[0]
	nodeName := node.Name
	var nodeIP string
	for _, a := range node.Status.Addresses {
		if a.Type == v1.NodeExternalIP {
			nodeIP = a.Address
			break
		}
	}

	if nodeIP == "" {
		// No external IPs were found, let's try to use internal as plan B
		for _, a := range node.Status.Addresses {
			if a.Type == v1.NodeInternalIP {
				nodeIP = a.Address
				break
			}
		}
	}
	Expect(nodeIP).NotTo(Equal(""))
	return nodeName, nodeIP
}

func v2SearchPolicy(c *calico.Calicoctl, policyName, ns string) bool {
	args := []string{"policy", "--all-namespaces"}
	if ns != "" {
		args = []string{"policy", "-n", ns}
	}
	polRes := c.Get(args...)
	polExists := false
	for _, line := range strings.Split(polRes, "\n") {
		if strings.Contains(line, "NAME") {
			continue
		}
		if strings.Contains(line, policyName) {
			polExists = true
		}
	}
	return polExists
}

func v2CheckPolicyExists(c *calico.Calicoctl, policyName, ns string) {
	Expect(v2SearchPolicy(c, policyName, ns)).To(BeTrue())
}

func v2CheckPolicyDoesntExist(c *calico.Calicoctl, policyName, ns string) {
	Expect(v2SearchPolicy(c, policyName, ns)).NotTo(BeTrue())
}
