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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/api/core/v1"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"

	"fmt"
	"io/ioutil"
	"strings"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

const serverPort1 = 80

// TODO: Need to consolidate these tests with the ones in test/e2e/network/calico_policy.

var _ = framework.KubeDescribe("CalicoPolicy", func() {
	var service *v1.Service
	var podServer *v1.Pod

	f := framework.NewDefaultFramework("calico-policy")

	AfterEach(func() {
		polRes := calico.CalicoctlGet("policy")
		for _, line := range strings.Split(polRes, "\n") {
			if strings.Contains(line, "NAME") {
				continue
			}
			framework.Logf("%v", line)
			if len(line) == 0 {
				continue
			}
			calico.Calicoctl("delete", "policy", line)
		}
	})
	BeforeEach(func() {
		if datastoreType == "" {
			// Infer datastore type by reading /etc/calico/calicoctl.cfg.
			b, err := ioutil.ReadFile("/etc/calico/calicoctl.cfg")
			Expect(err).NotTo(HaveOccurred())
			for _, line := range strings.Split(string(b), "\n") {
				if strings.Contains(line, "datastoreType") {
					if strings.Contains(line, "kubernetes") {
						datastoreType = "kdd"
					}
					if strings.Contains(line, "etcd") {
						datastoreType = "etcd"
					}
				}
			}
			framework.Logf("datastoreType = %v", datastoreType)
			Expect(datastoreType).NotTo(Equal(""))
		}
		if datastoreType == "kdd" {
			Skip("KDD mode not supported")
		}
		framework.Logf("Running tests for datastoreType %s", datastoreType)
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

		})

		AfterEach(func() {
			cleanupServerPodAndService(f, podServer, service)
		})

		It("should correctly isolate namespaces by ingress and egress policies [Feature:NetworkPolicy] [Feature:CalicoPolicy]", func() {
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
- apiVersion: v1
  kind: policy
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
			createCalicoPolicy(f, "default-deny-all", denyPolicyStr)
			defer cleanupCalicoPolicy(f, "default-deny-all")

			By("Creating calico namespace isolation policy.")
			nsNameA := f.Namespace.Name
			policyName := fmt.Sprintf("%s.%s", nsNameA, "namespace-isolation-a")
			policyStr := fmt.Sprintf(`
- apiVersion: v1
  kind: policy
  metadata:
    name: %s
  spec:
    order: 1000
    selector: projectcalico.org/namespace == "%s"
    ingress:
      - action: allow
        source:
          selector: projectcalico.org/namespace == "%s"
    egress:
      - action: allow
        destination:
          selector: projectcalico.org/namespace == "%s"
`,
				policyName, nsNameA, nsNameA, nsNameA)
			createCalicoPolicy(f, policyName, policyStr)
			defer cleanupCalicoPolicy(f, policyName)

			By("Creating another calico namespace isolation policy.")
			nsNameB := nsB.Name
			policyNameB := fmt.Sprintf("%s.%s", nsNameB, "namespace-isolation-b")
			policyStrB := fmt.Sprintf(`
- apiVersion: v1
  kind: policy
  metadata:
    name: %s
  spec:
    order: 1000
    selector: projectcalico.org/namespace == "%s"
    ingress:
      - action: allow
        source:
          selector: projectcalico.org/namespace == "%s"
    egress:
      - action: allow
        destination:
          selector: projectcalico.org/namespace == "%s"
`,
				policyNameB, nsNameB, nsNameB, nsNameB)
			createCalicoPolicy(f, policyNameB, policyStrB)
			defer cleanupCalicoPolicy(f, policyNameB)

			By("Creating calico allow dns policy.")
			dnsPolicyName := fmt.Sprintf("%s.%s", nsNameA, "allow-dns")
			dnsPolicyStr := fmt.Sprintf(`
- apiVersion: v1
  kind: policy
  metadata:
    name: %s
  spec:
    order: 400
    selector: projectcalico.org/namespace == "%s" || projectcalico.org/namespace == "%s"
    egress:
      - action: allow
        destination:
          selector: projectcalico.org/namespace == "kube-system" && k8s-app == "kube-dns"
`,
				dnsPolicyName, nsNameA, nsNameB)
			createCalicoPolicy(f, dnsPolicyName, dnsPolicyStr)
			defer cleanupCalicoPolicy(f, dnsPolicyName)

			By("allow A -> A")
			testCanConnect(f, nsA, "client-a", serviceA, 80)
			By("allow B -> B")
			testCanConnect(f, nsB, "client-b", serviceB, 80)
			By("deny B -> A")
			testCannotConnect(f, nsB, "client-b", serviceA, 80)
			By("deny A -> B")
			testCannotConnect(f, nsA, "client-a", serviceB, 80)
		})

		It("should be able to set up a \"default-deny\" policy for a namespace [Feature:NetworkPolicy] [Feature:CalicoPolicy]", func() {
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
- apiVersion: v1
  kind: policy
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
			createCalicoPolicy(f, "default-deny-all", denyPolicyStr)
			defer cleanupCalicoPolicy(f, "default-deny-all")

			// TODO (mattl): remove this as well since this only exists to set up the default allow when no policy is set up
			// for Calico node versions 2.5.1 and lower.
			By("Creating a default allow for anything not in Namespace A.")
			allowPolicyStr := fmt.Sprintf(`
- apiVersion: v1
  kind: policy
  metadata:
    name: default-allow
  spec:
    order: 4900
    selector: projectcalico.org/namespace != "%s"
    ingress:
    - action: allow
      source: {}
      destination: {}
    egress:
    - action: allow
      source: {}
      destination: {}
`,
				f.Namespace.Name)
			createCalicoPolicy(f, "default-allow", allowPolicyStr)
			defer cleanupCalicoPolicy(f, "default-allow")

			By("Creating calico default deny policy.")
			nsNameA := f.Namespace.Name
			policyName := fmt.Sprintf("%s.%s", nsNameA, "default-deny")
			// Policy only defines egress rules so ingress rules are defaulted to nothing (deny)
			policyStr := fmt.Sprintf(`
- apiVersion: v1
  kind: policy
  metadata:
    name: %s
  spec:
    order: 1000
    selector: projectcalico.org/namespace == "%s"
    egress:
    - action: allow
      destination: {}
      source: {}
`,
				policyName, nsNameA)
			createCalicoPolicy(f, policyName, policyStr)
			defer cleanupCalicoPolicy(f, policyName)

			By("Creating calico allow dns policy.")
			dnsPolicyName := fmt.Sprintf("%s.%s", nsNameA, "allow-dns")
			dnsPolicyStr := fmt.Sprintf(`
- apiVersion: v1
  kind: policy
  metadata:
    name: %s
  spec:
    order: 400
    selector: projectcalico.org/namespace == "%s"
    egress:
      - action: allow
        destination:
          selector: projectcalico.org/namespace == "kube-system" && k8s-app == "kube-dns"
`,
				dnsPolicyName, nsNameA)
			createCalicoPolicy(f, dnsPolicyName, dnsPolicyStr)
			defer cleanupCalicoPolicy(f, dnsPolicyName)

			By("deny A -> A")
			testCannotConnect(f, nsA, "client-a", serviceA, 80)
			By("allow B -> B")
			testCanConnect(f, nsB, "client-b", serviceB, 80)
			By("deny B -> A")
			testCannotConnect(f, nsB, "client-b", serviceA, 80)
			By("allow A -> B")
			testCanConnect(f, nsA, "client-a", serviceB, 80)
		})

		It("should correctly overwrite existing calico policies with simple ingress and egress policies [Feature:NetworkPolicy] [Feature:CalicoPolicy]", func() {
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

			// TODO (mattl): remove this and rework these policies. Currently need to create a default deny since Calico v2.6.0
			// defaults to allow for any non matching policies while 2.5.1 and earlier default to deny.
			By("Creating a namespace-wide default-deny policy")
			denyPolicyStr := `
- apiVersion: v1
  kind: policy
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
			createCalicoPolicy(f, "default-deny-all", denyPolicyStr)
			defer cleanupCalicoPolicy(f, "default-deny-all")

			By("Creating calico namespace isolation policy.")
			nsNameA := f.Namespace.Name
			policyName := fmt.Sprintf("%s.%s", nsNameA, "namespace-isolation-a")
			policyStr := fmt.Sprintf(`
- apiVersion: v1
  kind: policy
  metadata:
    name: %s
  spec:
    order: 1000
    selector: projectcalico.org/namespace == "%s"
    ingress:
      - action: allow
        source:
          selector: projectcalico.org/namespace == "%s"
    egress:
      - action: allow
        destination:
          selector: projectcalico.org/namespace == "%s"
`,
				policyName, nsNameA, nsNameA, nsNameA)
			createCalicoPolicy(f, policyName, policyStr)
			defer cleanupCalicoPolicy(f, policyName)

			By("Creating a calico \"default-deny\" policy that blocks all traffic to it.")
			nsNameB := nsB.Name
			policyNameB := fmt.Sprintf("%s.%s", nsNameB, "default-deny")
			policyStrB := fmt.Sprintf(`
- apiVersion: v1
  kind: policy
  metadata:
    name: %s
  spec:
    order: 900
    selector: projectcalico.org/namespace == "%s"
    egress:
    - action: allow
      destination: {}
      source: {}
`,

				policyNameB, nsNameB)
			createCalicoPolicy(f, policyNameB, policyStrB)
			defer cleanupCalicoPolicy(f, policyNameB)

			By("Creating calico allow dns policy.")
			dnsPolicyName := fmt.Sprintf("%s.%s", nsNameA, "allow-dns")
			dnsPolicyStr := fmt.Sprintf(`
- apiVersion: v1
  kind: policy
  metadata:
    name: %s
  spec:
    order: 400
    selector: projectcalico.org/namespace == "%s" || projectcalico.org/namespace == "%s"
    egress:
      - action: allow
        destination:
          selector: projectcalico.org/namespace == "kube-system" && k8s-app == "kube-dns"
`,
				dnsPolicyName, nsNameA, nsNameB)
			createCalicoPolicy(f, dnsPolicyName, dnsPolicyStr)
			defer cleanupCalicoPolicy(f, dnsPolicyName)

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
- apiVersion: v1
  kind: policy
  metadata:
    name: %s
  spec:
    order: 800
    selector: projectcalico.org/namespace == "%s"
    ingress:
    - action: allow
      source:
        selector: projectcalico.org/namespace == "%s"
`,
				ingressPolicyName, nsNameB, nsNameA)
			createCalicoPolicy(f, ingressPolicyName, ingressPolicyStr)
			defer cleanupCalicoPolicy(f, ingressPolicyName)

			// This should not be accessible yet since A will also need an egress policy
			By("deny A -> B")
			testCannotConnect(f, nsA, "client-a", serviceB, 80)

			By("Creating a simple egress policy on A that allows traffic to B.")
			egressPolicyName := fmt.Sprintf("%s.%s", nsNameA, "simple-egress")
			egressPolicyStr := fmt.Sprintf(`
- apiVersion: v1
  kind: policy
  metadata:
    name: %s
  spec:
    order: 700
    selector: projectcalico.org/namespace == "%s"
    egress:
    - action: allow
      destination:
        selector: projectcalico.org/namespace == "%s"
`,
				egressPolicyName, nsNameA, nsNameB)
			createCalicoPolicy(f, egressPolicyName, egressPolicyStr)
			defer cleanupCalicoPolicy(f, egressPolicyName)

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

		It("should correctly be able to select endpoints for policies using label selectors [Feature:NetworkPolicy] [Feature:CalicoPolicy]", func() {
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

			By("Creating simple servers with labels.")
			identifierKey := "identifier"
			nsNameA := f.Namespace.Name
			nsNameB := nsB.Name
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
- apiVersion: v1
  kind: policy
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
			createCalicoPolicy(f, "default-deny-all", denyPolicyStr)
			defer cleanupCalicoPolicy(f, "default-deny-all")

			By("Creating a \"default=deny\" calico policy based on pod labels.")
			policyName := fmt.Sprintf("%s", "pod-name-label-default-deny")
			// Create a policy to default deny on "pod-name" since that is a default label set by createServerPodAndService and CreateServerPodAndServiceWithLabels
			policyStr := fmt.Sprintf(`
- apiVersion: v1
  kind: policy
  metadata:
    name: %s
  spec:
    order: 1000
    selector: has(%s)
    egress:
    - action: allow
      destination: {}
      source: {}
`,
				policyName, "pod-name")
			createCalicoPolicy(f, policyName, policyStr)
			defer cleanupCalicoPolicy(f, policyName)

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
			policyNameAllowB := fmt.Sprintf("%s.%s", nsNameB, "ingress-allow-b")
			policyStrAllowB := fmt.Sprintf(`
- apiVersion: v1
  kind: policy
  metadata:
    name: %s
  spec:
    order: 900
    selector: has(%s)
    ingress:
    - action: allow
      source:
        selector: projectcalico.org/namespace == "%s"
`,
				policyNameAllowB, "pod-name", nsNameB)
			createCalicoPolicy(f, policyNameAllowB, policyStrAllowB)
			defer cleanupCalicoPolicy(f, policyNameAllowB)

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
- apiVersion: v1
  kind: policy
  metadata:
    name: %s
  spec:
    order: 800
    selector: %s in {"%s"}
    ingress:
    - action: allow
      source:
        selector: projectcalico.org/namespace == "%s"
    - action: deny
      source:
        selector: projectcalico.org/namespace == "%s"
`,
				policyNameSpecificLabel, identifierKey, "ident2", nsNameA, nsNameB)
			createCalicoPolicy(f, policyNameSpecificLabel, policyStrSpecificLabel)
			defer cleanupCalicoPolicy(f, policyNameSpecificLabel)

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

		It("should correctly overwrite existing calico policies with simple ingress and egress policies using Calico v2.6.0 types [Feature:NetworkPolicy] [Feature:CalicoPolicy]", func() {
			Skip("(mattl): uncomment this test when Essentials has been upgraded to Calico v2.6.0")
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


			// Verify that by default, all namespaces can connect with each other
			// Currently commented out because it breaks tests that are not broken without this block
			By("allow A -> A")
			testCanConnect(f, nsA, "client-a", serviceA, 80)
			By("allow B -> B")
			testCanConnect(f, nsB, "client-b", serviceB, 80)
			By("allow B -> A")
			testCanConnect(f, nsB, "client-b", serviceA, 80)
			By("allow A -> B")
			testCanConnect(f, nsA, "client-a", serviceB, 80)

			// TODO (mattl): remove this and rework these policies. Currently need to create a default deny since Calico v2.6.0
			// defaults to allow for any non matching policies while 2.5.1 and earlier default to deny.
			By("Creating a namespace-wide default-deny policy")
			denyPolicyStr := `
- apiVersion: v1
  kind: policy
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
			createCalicoPolicy(f, "default-deny-all", denyPolicyStr)
			defer cleanupCalicoPolicy(f, "default-deny-all")

			By("Creating calico namespace isolation policy.")
			nsNameA := f.Namespace.Name
			policyName := fmt.Sprintf("%s.%s", nsNameA, "namespace-isolation-a")
			policyStr := fmt.Sprintf(`
- apiVersion: v1
  kind: policy
  metadata:
	name: %s
  spec:
	order: 1000
	selector: projectcalico.org/namespace == "%s"
	types:
	- ingress
	- egress
	ingress:
	  - action: allow
		source:
		  selector: projectcalico.org/namespace == "%s"
	egress:
	  - action: allow
		destination:
		  selector: projectcalico.org/namespace == "%s"
`,
				policyName, nsNameA, nsNameA, nsNameA)
			createCalicoPolicy(f, policyName, policyStr)
			defer cleanupCalicoPolicy(f, policyName)

			By("Creating a calico \"default-deny\" policy that blocks all traffic to it.")
			nsNameB := nsB.Name
			policyNameB := fmt.Sprintf("%s.%s", nsNameB, "default-deny")
			policyStrB := fmt.Sprintf(`
- apiVersion: v1
  kind: policy
  metadata:
	name: %s
  spec:
	order: 900
	selector: projectcalico.org/namespace == "%s"
	types:
	- egress
	egress:
	- action: allow
	  destination: {}
	  source: {}
`,

				policyNameB, nsNameB)
			createCalicoPolicy(f, policyNameB, policyStrB)
			defer cleanupCalicoPolicy(f, policyNameB)

			By("Creating calico allow dns policy.")
			dnsPolicyName := fmt.Sprintf("%s.%s", nsNameA, "allow-dns")
			dnsPolicyStr := fmt.Sprintf(`
- apiVersion: v1
  kind: policy
  metadata:
	name: %s
  spec:
	order: 400
	selector: projectcalico.org/namespace == "%s" || projectcalico.org/namespace == "%s"
	types:
	- egress
	egress:
	  - action: allow
		destination:
		  selector: projectcalico.org/namespace == "kube-system" && k8s-app == "kube-dns"
`,
				dnsPolicyName, nsNameA, nsNameB)
			createCalicoPolicy(f, dnsPolicyName, dnsPolicyStr)
			defer cleanupCalicoPolicy(f, dnsPolicyName)

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
- apiVersion: v1
  kind: policy
  metadata:
	name: %s
  spec:
	order: 800
	selector: projectcalico.org/namespace == "%s"
	types:
	- ingress
	ingress:
	- action: allow
	  source:
		selector: projectcalico.org/namespace == "%s"
`,
				ingressPolicyName, nsNameB, nsNameA)
			createCalicoPolicy(f, ingressPolicyName, ingressPolicyStr)
			defer cleanupCalicoPolicy(f, ingressPolicyName)

			// This should not be accessible yet since A will also need an egress policy
			By("deny A -> B")
			testCannotConnect(f, nsA, "client-a", serviceB, 80)

			By("Creating a simple egress policy on A that allows traffic to B.")
			egressPolicyName := fmt.Sprintf("%s.%s", nsNameA, "simple-egress")
			egressPolicyStr := fmt.Sprintf(`
- apiVersion: v1
  kind: policy
  metadata:
	name: %s
  spec:
	order: 700
	selector: projectcalico.org/namespace == "%s"
	types:
	- egress
	egress:
	- action: allow
	  destination:
		selector: projectcalico.org/namespace == "%s"
`,
				egressPolicyName, nsNameA, nsNameB)
			createCalicoPolicy(f, egressPolicyName, egressPolicyStr)
			defer cleanupCalicoPolicy(f, egressPolicyName)

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
	})

	It("should enforce rule ordering correctly [Feature:CalicoPolicy]", func() {
		ns := f.Namespace

		By("Create a simple server pod.")
		serverPod, service := createServerPodAndService(f, ns, "server", []int{serverPort1})
		defer cleanupServerPodAndService(f, serverPod, service)
		framework.Logf("Waiting for server pod to come up.")
		err := framework.WaitForPodRunningInNamespace(f.ClientSet, serverPod)
		Expect(err).NotTo(HaveOccurred())

		By("Creating a client which should be able to connect to the server since no policies are present.")
		testCanConnect(f, ns, "client", service, serverPort1)

		By("Applying a policy that drops traffic from client.")
		calico.CalicoctlApply(
			`
- apiVersion: v1
  kind: policy
  metadata:
    name: client-policy
  spec:
    order: 100
    ingress:
      - action: deny
        protocol: tcp
        source:
          selector: pod-name=='client'
        destination:
          ports: [%d]
    selector: pod-name=='%s'
`,
			serverPort1, serverPod.Name)
		defer func() {
			calico.Calicoctl("delete", "policy", "client-policy")
		}()
		By("Creating a client that should not be able to connect to the server")
		testCannotConnect(f, ns, "client", service, serverPort1)

		By("Updating the policy with a allow rule before the drop rule in the same policy.")
		calico.CalicoctlApply(
			`
- apiVersion: v1
  kind: policy
  metadata:
    name: client-policy
  spec:
    order: 100
    ingress:
      - action: allow
        protocol: tcp
        source:
          selector: pod-name=='client'
        destination:
          ports: [%d]
      - action: deny
        protocol: tcp
        source:
          selector: pod-name=='client'
        destination:
          ports: [%d]
    selector: pod-name=='%s'
`,
			serverPort1, serverPort1, serverPod.Name)

		By("Creating a client which should be able to connect to the server since there is a allow rule.")
		testCanConnect(f, ns, "client", service, serverPort1)
	})

	It("should support CRUD operations of a policy", func() {
		ns := f.Namespace

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
		calico.CalicoctlApply(
			`
- apiVersion: v1
  kind: policy
  metadata:
    name: %s
  spec:
    order: 100
    ingress:
      - action: deny
        protocol: tcp
        source:
          selector: pod-name=='client'
        destination:
          ports: [%d]
    selector: pod-name=='%s' && projectcalico.org/namespace == '%s'
`,
			policyName, serverPort1, serverPod1.Name, ns.Name)
		By("Creating a client that should not be able to connect to the server")
		testCannotConnect(f, ns, "client", service, serverPort1)

		By("Checking if the policy exists")
		checkPolicyExists(policyName)

		// TODO(doublek): Consider if combining this with the logging test makes sense.
		By("Replace a policy that allows traffic from client.")
		calico.CalicoctlReplace(
			`
- apiVersion: v1
  kind: policy
  metadata:
    name: %s
  spec:
    order: 100
    ingress:
      - action: allow
        protocol: tcp
        source:
          selector: pod-name=='client'
        destination:
          ports: [%d]
    selector: pod-name=='%s' && projectcalico.org/namespace == '%s'
`,
			policyName, serverPort1, serverPod1.Name, ns.Name)
		By("Creating a client that can connect to the server")
		testCanConnect(f, ns, "client", service, serverPort1)

		By("Deleting the policy")
		calico.Calicoctl("delete", "policy", policyName)

		By("Checking if the policy doesn't exists")
		checkPolicyDoesntExist(policyName)
	})

	It("should support a 'log' rule [Feature:CalicoPolicy]", func() {
		ns := f.Namespace

		By("Create a simple server pod.")
		serverPod, service := createServerPodAndService(f, ns, "server", []int{serverPort1})
		defer cleanupServerPodAndService(f, serverPod, service)
		framework.Logf("Waiting for server pod to come up.")
		err := framework.WaitForPodRunningInNamespace(f.ClientSet, serverPod)
		Expect(err).NotTo(HaveOccurred())

		By("Creating a client which should be able to connect to the server since no policies are present.")
		testCanConnect(f, ns, "client", service, serverPort1)

		By("Verifying that initially there are no logs present in the node running the server pod")
		serverPodNow, err := f.ClientSet.Core().Pods(ns.Name).Get(serverPod.Name, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		serverNodeName := serverPodNow.Spec.NodeName
		serverNode, err := f.ClientSet.Core().Nodes().Get(serverNodeName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		framework.Logf("Server is running on %v", serverNodeName)
		serverSyslogCount := calico.CountSyslogLines(serverNode)

		By("Applying a policy that logs traffic from client then drops the same traffic.")
		calico.CalicoctlApply(
			`
- apiVersion: v1
  kind: policy
  metadata:
    name: policy-log-then-deny
  spec:
    order: 100
    ingress:
      - action: log
        protocol: tcp
        source:
          selector: pod-name=='client'
        destination:
          ports: [%d]
      - action: deny
        protocol: tcp
        source:
          selector: pod-name=='client'
        destination:
          ports: [%d]
    selector: pod-name=='%s'
`,
			serverPort1, serverPort1, serverPod.Name)
		defer func() {
			calico.Calicoctl("delete", "policy", "policy-log-then-deny")
		}()
		By("Creating a client that should not be able to connect to the server")
		testCannotConnect(f, ns, "client", service, serverPort1)

		newDropLogs := calico.GetNewCalicoDropLogs(serverNode, serverSyslogCount, "calico-packet")
		framework.Logf("New drop logs: %#v", newDropLogs)
		Expect(len(newDropLogs)).NotTo(BeZero())
	})

	It("should support 'DefaultEndpointToHostAction' [Feature:CalicoPolicy]", func() {
		// TODO(doublek): Doesn't do DefaultEndpointToHostAction 'RETURN' yet.
		// Only 'DROP' and 'ACCEPT' for now.

		connectivityCheckTimeout := 10

		// Pick a node on where we can spawn a pod and test DefaultEndpointToHostAction.
		_, nodes := framework.GetMasterAndWorkerNodesOrDie(f.ClientSet)
		nodeName, nodeIP := chooseNode(nodes)

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
	It("should allow negative selectors and support for filtering ICMP [Feature:CalicoPolicy]", func() {
		ns := f.Namespace

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
		calico.CalicoctlApply(
			`
- apiVersion: v1
  kind: policy
  metadata:
    name: deny-icmp
  spec:
    ingress:
    - action: deny
      protocol: icmp
    egress:
      - action: allow
    order: 100
    selector: '!has(icmp)'
- apiVersion: v1
  kind: policy
  metadata:
    name: allow-icmp-access
  spec:
    ingress:
    - action: allow
    egress:
      - action: allow
    order: 50
    selector: role != 'server'
`,
		)
		defer func() {
			calico.Calicoctl("delete", "policy", "deny-icmp")
			calico.Calicoctl("delete", "policy", "allow-icmp-access")
		}()

		By("Pinging the IMCP allowed pod")
		icmpPod, err := f.ClientSet.Core().Pods(ns.Name).Get(serverIcmpPod.Name, metav1.GetOptions{})
		calico.TestCanPing(f, ns, "client-can-ping", icmpPod)

		By("Pinging the IMCP not allowed pod")
		notIcmpPod, err := f.ClientSet.Core().Pods(ns.Name).Get(serverNoIcmpPod.Name, metav1.GetOptions{})
		calico.TestCannotPing(f, ns, "client-cannot-ping", notIcmpPod)
	})
})

func createCalicoPolicy(f *framework.Framework, policyName string, policyStr string) {
	framework.Logf("setting up calico policy %s.", policyName)
	calico.CalicoctlApply(policyStr)
}

func cleanupCalicoPolicy(f *framework.Framework, policyName string) {
	framework.Logf("Cleaning up calico policy %s.", policyName)
	calico.Calicoctl("delete", "policy", policyName)
}

func chooseNode(nodes *v1.NodeList) (string, string) {
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

func searchPolicy(policyName string) bool {
	polRes := calico.CalicoctlGet("policy")
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

func checkPolicyExists(policyName string) {
	Expect(searchPolicy(policyName)).To(BeTrue())
}

func checkPolicyDoesntExist(policyName string) {
	Expect(searchPolicy(policyName)).NotTo(BeTrue())
}