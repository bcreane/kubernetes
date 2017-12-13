/*
Copyright (c) 2016-2017 Tigera, Inc. All rights reserved.

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
	"k8s.io/kubernetes/test/e2e/framework"

	"fmt"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/kubernetes/test/utils/calico"
)

/*
The following Network Policy tests verify that calico egress policy object definitions
are correctly enforced by a calico deployment.
*/

var _ = SIGDescribe("[Feature:CalicoPolicy-v3] egress policy", func() {
	var service *v1.Service
	var podServer *v1.Pod

	f := framework.NewDefaultFramework("network-policy")

	Context("Calico specific network policy", func() {
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

		It("should support lower order 'allow ingress' policy", func() {
			// Create deny-all policy
			By("Creating deny-all policy with kubectl, no client should be able to contact the server.")
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

			By("Creating calico allow ingress policy with lower order.")
			nsName := f.Namespace.Name
			policyName := "allow-ingress"
			policyStr := fmt.Sprintf("apiVersion: projectcalico.org/v3\n"+
				"kind: GlobalNetworkPolicy\n"+
				"metadata:\n"+
				"  name: %s\n"+
				"spec:\n"+
				"  selector: projectcalico.org/namespace == \"%s\" && pod-name == \"%s\"\n"+
				"  order: 500\n"+
				"  ingress:\n"+
				"  - action: Allow\n"+
				"    destination:\n"+
				"      selector: projectcalico.org/namespace == \"%s\"",
				policyName, nsName, podServer.Name, nsName)
			calicoctl.Apply(policyStr)
			defer calicoctl.DeleteGNP(policyName)

			By("Creating client which will be able to contact the server since lower order allow ingress rule created.")
			testCanConnect(f, f.Namespace, "client-can-connect", service, 80)

		})

		It("should support a 'deny egress' policy", func() {
			By("Creating calico egress policy which denies traffic within namespace.")
			nsName := f.Namespace.Name
			policyName := "deny-egress"
			policyStr := fmt.Sprintf("apiVersion: projectcalico.org/v3\n"+
				"kind: GlobalNetworkPolicy\n"+
				"metadata:\n"+
				"  name: %s\n"+
				"spec:\n"+
				"  selector: projectcalico.org/namespace == \"%s\"\n"+
				"  order: 500\n"+
				"  egress:\n"+
				"  - action: Deny\n"+
				"    destination:\n"+
				"      selector: projectcalico.org/namespace == \"%s\"",
				policyName, nsName, nsName)
			calicoctl.Apply(policyStr)
			defer calicoctl.DeleteGNP(policyName)

			By("Creating client which will not be able to contact the server since deny egress rule created.")
			testCannotConnect(f, f.Namespace, "client-cannot-connect", service, 80)
		})

		It("should enforce egress policy based on NamespaceSelector", func() {
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
			framework.Logf("Waiting for Server to come up.")
			err = framework.WaitForPodRunningInNamespace(f.ClientSet, podServerB)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupServerPodAndService(f, podServerB, serviceB)

			By("Creating client from namespace A which will be able to contact the server in namespace B since no policies are present.")
			By("Allow A -> B")
			testCanConnect(f, nsA, "client-can-connect", serviceB, 80) //allow A -> B
			By("Allow A -> A")
			testCanConnect(f, nsA, "client-can-connect", serviceA, 80) //allow A -> A
			By("Allow B -> A")
			testCanConnect(f, nsB, "client-can-connect", serviceA, 80) //allow B -> A
			By("Allow B -> B")
			testCanConnect(f, nsB, "client-can-connect", serviceB, 80) //allow B -> B

			By("Creating calico egress policy which denies traffic egress from namespace A to namespace B.")
			policyName := fmt.Sprintf("deny-egress-from-ns-%s-to-ns-%s", nsA.Name, nsB.Name)
			policyStr := fmt.Sprintf("apiVersion: projectcalico.org/v3\n"+
				"kind: GlobalNetworkPolicy\n"+
				"metadata:\n"+
				"  name: %s\n"+
				"spec:\n"+
				"  selector: projectcalico.org/namespace == \"%s\"\n"+
				"  order: 500\n"+
				"  egress:\n"+
				"  - action: Deny\n"+
				"    destination:\n"+
				"      selector: projectcalico.org/namespace == \"%s\"",
				policyName, nsA.Name, nsB.Name)
			calicoctl.Apply(policyStr)
			defer calicoctl.DeleteGNP(policyName)

			By("Creating client from namespace A which will not be able to contact the server in namespace A (default-deny), B (egress-deny) policy are present.")
			By("Deny A -> A")
			testCannotConnect(f, nsA, "client-cannot-connect", serviceA, 80) //deny A -> A
			By("Deny A -> B")
			testCannotConnect(f, nsA, "client-cannot-connect", serviceB, 80) //deny A -> B
			By("allow B -> B")
			testCanConnect(f, nsB, "client-can-connect", serviceB, 80) //allow B -> B

			By("Creating calico policy which allow traffic from namespace A to namespace A.")
			policyName = fmt.Sprintf("allow-egress-within-%s", nsA.Name)
			policyStr = fmt.Sprintf("apiVersion: projectcalico.org/v3\n"+
				"kind: GlobalNetworkPolicy\n"+
				"metadata:\n"+
				"  name: %s\n"+
				"spec:\n"+
				"  selector: projectcalico.org/namespace == \"%s\"\n"+
				"  order: 400\n"+
				"  egress:\n"+
				"  - action: Allow\n"+
				"    destination:\n"+
				"      notSelector: projectcalico.org/namespace == \"%s\"\n"+
				"  ingress:\n"+
				"  - action: Allow\n"+
				"    source:\n"+
				"      notSelector: projectcalico.org/namespace == \"%s\"",
				policyName, nsA.Name, nsB.Name, nsB.Name)
			calicoctl.Apply(policyStr)
			defer calicoctl.DeleteGNP(policyName)

			By("Creating client from namespace A which will not be able to contact the server in namespace B but allow to contact server in namespace A.")
			By("Allow A -> A")
			testCanConnect(f, nsA, "client-can-connect", serviceA, 80) //allow A -> A
			By("Deny A -> B")
			testCannotConnect(f, nsA, "client-cannot-connect", serviceB, 80) //deny A -> B
			By("Allow B -> B")
			testCanConnect(f, nsB, "client-can-connect", serviceB, 80) //allow B -> B
		})

		It("should enforce egress policy based on labelSelector and NamespaceSelector", func() {
			nsA := f.Namespace

			nsBName := f.BaseName + "-b"
			// The CreateNamespace helper uses the input name as a Name Generator, so the namespace itself
			// will have a different name than what we are setting as the value of ns-name.
			// This is fine as long as we don't try to match the label as nsB.Name in our policy.
			nsB, err := f.CreateNamespace(nsBName, map[string]string{
				"ns-name": nsBName,
			})
			Expect(err).NotTo(HaveOccurred())
			framework.Logf("Created a new namespace %s.", nsB.Name)

			By("Creating a simple server server-b in namespace A.")
			podServerAB, serviceAB := createServerPodAndService(f, nsA, "server-b", []int{80})
			framework.Logf("Waiting for Server to come up.")
			err = framework.WaitForPodRunningInNamespace(f.ClientSet, podServerAB)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupServerPodAndService(f, podServerAB, serviceAB)

			By("Creating a simple server server-b in namespace B.")
			podServerBB, serviceBB := createServerPodAndService(f, nsB, "server-b", []int{80})
			framework.Logf("Waiting for Server to come up.")
			err = framework.WaitForPodRunningInNamespace(f.ClientSet, podServerBB)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupServerPodAndService(f, podServerBB, serviceBB)

			By("Creating client from namespace A which will be able to contact the server in namespace B since no policies are present.")
			By("allow A.client-a -> A.server-b")
			testCanConnect(f, nsA, "client-a", serviceAB, 80) //allow A.client-a -> A.server-b
			By("allow A.client-a -> B.server-b")
			testCanConnect(f, nsA, "client-a", serviceBB, 80) //allow A.client-a -> B.server-b
			By("allow B.client-a -> A.server-b")
			testCanConnect(f, nsB, "client-a", serviceAB, 80) //allow B.client-a -> A.server-b

			By("Creating calico egress policy which denies traffic egress from client-a (namespace A) to service b (namespace B).")
			policyName := "deny-egress-from-nsa-client-a-to-nsb-svc-b"
			policyStr := fmt.Sprintf("apiVersion: projectcalico.org/v3\n"+
				"kind: GlobalNetworkPolicy\n"+
				"metadata:\n"+
				"  name: %s\n"+
				"spec:\n"+
				"  selector: projectcalico.org/namespace == \"%s\" && pod-name == \"client-a\"\n"+
				"  order: 500\n"+
				"  egress:\n"+
				"  - action: Deny\n"+
				"    destination:\n"+
				"      selector: projectcalico.org/namespace == \"%s\" && pod-name == \"server-b\"",
				policyName, nsA.Name, nsB.Name)
			calicoctl.Apply(policyStr)
			defer calicoctl.DeleteGNP(policyName)

			By("Creating calico egress policy to allow dns.")
			policyName = "allow-dns"
			policyStr = fmt.Sprintf("apiVersion: projectcalico.org/v3\n"+
				"kind: GlobalNetworkPolicy\n"+
				"metadata:\n"+
				"  name: %s\n"+
				"spec:\n"+
				"  selector: projectcalico.org/namespace == \"%s\" && pod-name == \"client-a\"\n"+
				"  order: 400\n"+
				"  egress:\n"+
				"  - action: Allow\n"+
				"    protocol: UDP\n"+
				"    destination:\n"+
				"      selector: projectcalico.org/namespace == \"kube-system\" && k8s-app == \"kube-dns\"\n"+
				"      ports: [53]",
				policyName, nsA.Name)
			calicoctl.Apply(policyStr)
			defer calicoctl.DeleteGNP(policyName)

			By("Creating client-a from namespace A which will not be able to contact the server in namespace A, B since egress deny policies are present.")
			By("deny A.client-a -> A.server-b")
			testCannotConnect(f, nsA, "client-a", serviceAB, 80) //deny A.client-a -> A.server-b
			By("deny A.client-a -> B.server-b")
			testCannotConnect(f, nsA, "client-a", serviceBB, 80) //deny A.client-a -> B.server-b
			By("allow A.client-b -> A.server-b")
			testCanConnect(f, nsA, "client-b", serviceAB, 80) //allow A.client-b -> A.server-b
			By("allow B.client-a -> A.server-b")
			testCanConnect(f, nsB, "client-a", serviceAB, 80) //allow B.client-a -> A.server-b

			By("Creating calico policy which allow traffic from A.client-a to B.server-b")
			policyName = fmt.Sprintf("allow-egress-within-%s", nsA.Name)
			policyStr = fmt.Sprintf("apiVersion: projectcalico.org/v3\n"+
				"kind: GlobalNetworkPolicy\n"+
				"metadata:\n"+
				"  name: %s\n"+
				"spec:\n"+
				"  selector: projectcalico.org/namespace == \"%s\" && pod-name == \"client-a\"\n"+
				"  order: 300\n"+
				"  egress:\n"+
				"  - action: Allow\n"+
				"    destination:\n"+
				"      selector: projectcalico.org/namespace == \"%s\" && pod-name == \"server-b\"\n"+
				"  ingress:\n"+
				"  - action: Allow",
				policyName, nsA.Name, nsB.Name)
			calicoctl.Apply(policyStr)
			defer calicoctl.DeleteGNP(policyName)

			By("Creating client-a from namespace A which will not be able to contact B.server-b but can contact A.server-b.")
			By("Deny A.client-a -> A.server-b")
			testCannotConnect(f, nsA, "client-a", serviceAB, 80) //deny A.client-a -> A.server-b
			By("allow A.client-a -> B.server-b")
			testCanConnect(f, nsA, "client-a", serviceBB, 80) //allow A.client-a -> B.server-b
			By("allow A.client-b -> A.server-b")
			testCanConnect(f, nsA, "client-b", serviceAB, 80) //allow A.client-b -> A.server-b
			By("allow B.client-a -> A.server-b")
			testCanConnect(f, nsB, "client-a", serviceAB, 80) //allow B.client-a -> A.server-b
		})

		It("should enforce egress policy based on portSelector and labelSelector and NamespaceSelector", func() {
			Skip("[turk] currently fails on all setups. Needs investigation before enabling")
			// Create Server with Service
			By("Creating a simple server.")
			podServerA, serviceA := createServerPodAndService(f, f.Namespace, "server-b", []int{80, 81, 82})
			framework.Logf("Waiting for Server to come up.")
			err := framework.WaitForPodRunningInNamespace(f.ClientSet, podServerA)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupServerPodAndService(f, podServerA, serviceA)

			nsA := f.Namespace
			nsBName := f.BaseName + "-b"
			// The CreateNamespace helper uses the input name as a Name Generator, so the namespace itself
			// will have a different name than what we are setting as the value of ns-name.
			// This is fine as long as we don't try to match the label as nsB.Name in our policy.
			nsB, err := f.CreateNamespace(nsBName, map[string]string{
				"ns-name": nsBName,
			})
			Expect(err).NotTo(HaveOccurred())
			framework.Logf("Created a new namespace %s.", nsB.Name)

			By("Creating a simple server server-b in namespace B.")
			podServerB, serviceB := createServerPodAndService(f, nsB, "server-b", []int{80, 81, 82})
			framework.Logf("Waiting for Server to come up.")
			err = framework.WaitForPodRunningInNamespace(f.ClientSet, podServerB)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupServerPodAndService(f, podServerB, serviceB)

			By("Creating client from namespace A which will be able to contact the server in namespace B since no policies are present.")
			By("allow A.client-a -> A.server-b.80")
			testCanConnect(f, nsA, "client-a", serviceA, 80) //allow A.client-a -> A.server-b.80
			By("allow A.client-a -> A.server-b.81")
			testCanConnect(f, nsA, "client-a", serviceA, 81) //allow A.client-a -> A.server-b.81
			By("allow A.client-a -> A.server-b.82")
			testCanConnect(f, nsA, "client-a", serviceA, 82) //allow A.client-a -> A.server-b.82

			By("allow A.client-a -> B.server-b.80")
			testCanConnect(f, nsA, "client-a", serviceB, 80) //allow A.client-a -> B.server-b.80
			By("allow A.client-a -> B.server-b.81")
			testCanConnect(f, nsA, "client-a", serviceB, 81) //allow A.client-a -> B.server-b.81
			By("allow A.client-a -> B.server-b.82")
			testCanConnect(f, nsA, "client-a", serviceB, 82) //allow A.client-a -> B.server-b.82

			By("allow B.Client-a -> A.server-b.80")
			testCanConnect(f, nsB, "client-a", serviceA, 80) //allow B.client-a -> A.server-b.80
			By("allow B.Client-a -> A.server-b.81")
			testCanConnect(f, nsB, "client-a", serviceA, 81) //allow B.client-a -> A.server-b.81
			By("allow B.Client-a -> A.server-b.82")
			testCanConnect(f, nsB, "client-a", serviceA, 82) //allow B.client-a -> A.server-b.82

			By("Creating calico egress policy which denies traffic egress from client A.client-a to B.server-b.80/81.")
			policyName := "deny-egress-from-nsa-client-a-to-nsb-svc-b"
			policyStr := fmt.Sprintf("apiVersion: projectcalico.org/v3\n"+
				"kind: GlobalNetworkPolicy\n"+
				"metadata:\n"+
				"  name: %s\n"+
				"spec:\n"+
				"  selector: projectcalico.org/namespace == \"%s\" && pod-name == \"client-a\"\n"+
				"  order: 500\n"+
				"  egress:\n"+
				"  - action: Deny\n"+
				"    protocol: TCP\n"+
				"    destination:\n"+
				"      selector: projectcalico.org/namespace == \"%s\" && pod-name == \"server-b\"\n"+
				"      ports: [80, 81]",
				policyName, nsA.Name, nsB.Name)
			calicoctl.Apply(policyStr)
			defer calicoctl.DeleteGNP(policyName)

			By("Creating calico egress policy to allow dns.")
			policyName = "allow-dns"
			policyStr = fmt.Sprintf("apiVersion: projectcalico.org/v3\n"+
				"kind: GlobalNetworkPolicy\n"+
				"metadata:\n"+
				"  name: %s\n"+
				"spec:\n"+
				"  selector: projectcalico.org/namespace == \"%s\" && pod-name == \"client-a\"\n"+
				"  order: 400\n"+
				"  egress:\n"+
				"  - action: Allow\n"+
				"    protocol: UDP\n"+
				"    destination:\n"+
				"      selector: projectcalico.org/namespace == \"kube-system\" && k8s-app == \"kube-dns\"\n"+
				"      ports: [53]",
				policyName, nsA.Name)
			calicoctl.Apply(policyStr)
			defer calicoctl.DeleteGNP(policyName)

			By("Creating client-a from namespace A which will not be able to contact the server in namespace A, B since egress deny policies are present.")
			By("deny A.Client-a -> B.server-b.80")
			testCannotConnect(f, nsA, "client-a", serviceB, 80) //deny A.client-a -> B.server-b.80
			By("deny A.Client-a -> B.server-b.81")
			testCannotConnect(f, nsA, "client-a", serviceB, 81) //deny A.client-a -> B.server-b.81
			By("deny A.Client-a -> B.server-b.82")
			testCannotConnect(f, nsA, "client-a", serviceB, 82) //deny A.client-a -> B.server-b.82

			By("allow A.Client-b -> B.server-b.80")
			testCanConnect(f, nsA, "client-b", serviceB, 80) //allow A.client-b -> B.server-b.80
			By("allow A.Client-b -> B.server-b.81")
			testCanConnect(f, nsA, "client-b", serviceB, 81) //allow A.client-b -> B.server-b.81
			By("allow A.Client-b -> B.server-b.82")
			testCanConnect(f, nsA, "client-b", serviceB, 82) //allow A.client-b -> B.server-b.82

			By("allow B.Client-a -> A.server-b.80")
			testCanConnect(f, nsB, "client-a", serviceA, 80) //allow B.client-a -> A.server-b.80
			By("allow B.Client-a -> A.server-b.81")
			testCanConnect(f, nsB, "client-a", serviceA, 81) //allow B.client-a -> A.server-b.81
			By("allow B.Client-a -> A.server-b.82")
			testCanConnect(f, nsB, "client-a", serviceA, 82) //allow B.client-a -> A.server-b.82

			By("Creating calico egress policy which allow traffic egress from A.client-a to B.server-b.81")
			policyName = fmt.Sprintf("allow-egress-within-%s", nsA.Name)
			policyStr = fmt.Sprintf("apiVersion: projectcalico.org/v3\n"+
				"kind: GlobalNetworkPolicy\n"+
				"metadata:\n"+
				"  name: %s\n"+
				"spec:\n"+
				"  selector: projectcalico.org/namespace == \"%s\" && pod-name == \"client-a\"\n"+
				"  order: 300\n"+
				"  egress:\n"+
				"  - action: Allow\n"+
				"    protocol: TCP\n"+
				"    destination:\n"+
				"      selector: projectcalico.org/namespace == \"%s\" && pod-name == \"server-b\"\n"+
				"      ports: [81, 82]",
				policyName, nsA.Name, nsB.Name)
			calicoctl.Apply(policyStr)
			defer calicoctl.DeleteGNP(policyName)

			By("Creating client-a from namespace A which will not be able to contact B.server-b.80 but can contact B.server-b.81/82 and A.server-b.")
			By("deny A.Client-a -> B.server-b.80")
			testCannotConnect(f, nsA, "client-a", serviceB, 80) //deny A.client-a -> B.server-b.80
			By("allow A.Client-a -> B.server-b.81")
			testCanConnect(f, nsA, "client-a", serviceB, 81) //allow A.client-a -> B.server-b.81
			By("allow A.Client-a -> B.server-b.82")
			testCanConnect(f, nsA, "client-a", serviceB, 82) //allow A.client-a -> B.server-b.82

			By("allow A.Client-b -> B.server-b.80")
			testCanConnect(f, nsA, "client-b", serviceB, 80) //allow A.client-b -> B.server-b.80
			By("allow A.Client-b -> B.server-b.81")
			testCanConnect(f, nsA, "client-b", serviceB, 81) //allow A.client-b -> B.server-b.81
			By("allow A.Client-b -> B.server-b.82")
			testCanConnect(f, nsA, "client-b", serviceB, 82) //allow A.client-b -> B.server-b.82

			By("allow B.Client-a -> A.server-b.80")
			testCanConnect(f, nsB, "client-a", serviceA, 80) //allow B.client-a -> A.server-b.80
			By("allow B.Client-a -> A.server-b.81")
			testCanConnect(f, nsB, "client-a", serviceA, 81) //allow B.client-a -> A.server-b.81
			By("allow B.Client-a -> A.server-b.82")
			testCanConnect(f, nsB, "client-a", serviceA, 82) //allow B.client-a -> A.server-b.82

		})
	})

})
