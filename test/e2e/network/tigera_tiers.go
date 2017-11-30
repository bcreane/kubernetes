// Copyright (c) 2017 Tigera, Inc. All rights reserved.

package network

import (
	"k8s.io/api/core/v1"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"fmt"
	"strings"
)

// Because tiers are not namespaced, these tests cannot be run in parallel.
var _ = framework.KubeDescribe("[Feature:CNX-v3][Serial] tiers", func() {
	const clientName = "client"
	var service *v1.Service
	var podServer *v1.Pod
	var calicoctl *calico.Calicoctl
	f := framework.NewDefaultFramework("calico-policy")

	Context("Tiered policy tests", func() {
		pDefaultDeny := &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name: "deny-all",
			},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{},
				Ingress:     []networkingv1.NetworkPolicyIngressRule{},
			},
		}

		BeforeEach(func() {
			// The following code tries to get config information for calicoctl from k8s ConfigMap.
			// A framework clientset is needed to access k8s configmap but it will only
			// be created in the context of BeforeEach or IT.
			// Current solution is to use BeforeEach because this function is not a test case.
			// This will avoid complexity of creating a client by yourself.
			calicoctl = calico.ConfigureCalicoctl(f)

			// Create a default-deny policy. We don't bother to explicitly delete this after since it will get wiped when the
			// namespace is deleted.
			By("Creating a default-deny policy.")
			_, err := f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).Create(pDefaultDeny)
			Expect(err).ToNot(HaveOccurred())

			By("Creating a target server.")
			podServer, service = createServerPodAndService(f, f.Namespace, "server", []int{80})
			err = framework.WaitForPodRunningInNamespace(f.ClientSet, podServer)
			Expect(err).NotTo(HaveOccurred())

			By("Testing server pod should not be accessible with default deny.")
			testCannotConnect(f, f.Namespace, clientName, service, 80)

			By("Creating tier0.")
			result, err := calicoctl.ExecReturnError("delete", "tier", "t0", "--skip-not-exists")
			if err != nil {
				framework.Failf("Error deleting calico Tier 't0': %s", result)
			}

			calicoctl.Create(newTier("t0", 98))
		})

		AfterEach(func() {
			if podServer != nil && service != nil {
				cleanupServerPodAndService(f, podServer, service)
				podServer = nil
				service = nil
			}
			calicoctl = calico.ConfigureCalicoctl(f)
			result, err := calicoctl.ExecReturnError("delete", "tier", "t0", "--skip-not-exists")
			if err != nil {
				framework.Failf("Error deleting calico Tier 't0': %s", result)
			}
		})

		Describe("take precedence over the default tier", func() {
			Specify("when it has a policy that allows traffic", func() {
				By("Creating a policy in tier0 with rules to allows traffic.")
				p := fmt.Sprintf(strings.Join([]string{"",
					"apiVersion: projectcalico.org/v3",
					"kind: NetworkPolicy",
					"metadata:",
					"  name: t0.test-policy",
					"  namespace: %s",
					"spec:",
					"  order: 100",
					"  selector: pod-name == \"server\"",
					"  tier: t0",
					"  ingress:",
					"   - action: Allow",
				}, "\n"), f.Namespace.Name)
				calicoctl.Create(p)
				defer calicoctl.Delete(p)

				By("Testing server pod should be accessible.")
				testCanConnect(f, f.Namespace, clientName, service, 80)
			})

			It("can 'Pass' to the default tier", func() {
				// Use a lower order here
				By("Creating a policy in tier0 with rules to pass to next tier.")
				p := fmt.Sprintf(strings.Join([]string{"",
					"apiVersion: projectcalico.org/v3",
					"kind: NetworkPolicy",
					"metadata:",
					"  name: t0.test-policy",
					"  namespace: %s",
					"spec:",
					"  order: 50",
					"  selector: pod-name == \"server\"",
					"  tier: t0",
					"  ingress:",
					"   - action: Pass",
				}, "\n"), f.Namespace.Name)
				calicoctl.Create(p)
				defer calicoctl.Delete(p)

				By("Testing server pod should not be accessible with default deny.")
				testCannotConnect(f, f.Namespace, clientName, service, 80)

				By("Creating a policy in default tier with rules to allow traffic.")
				p2 := fmt.Sprintf(strings.Join([]string{"",
					"apiVersion: projectcalico.org/v3",
					"kind: NetworkPolicy",
					"metadata:",
					"  name: test-policy",
					"  namespace: %s",
					"spec:",
					"  order: 100",
					"  selector: pod-name == \"server\"",
					"  ingress:",
					"   - action: Allow",
				}, "\n"), f.Namespace.Name)
				calicoctl.Create(p2)
				defer calicoctl.Delete(p2)

				By("Testing server pod should be accessible.")
				testCanConnect(f, f.Namespace, clientName, service, 80)
			})
		})

		Context("when interacting with a second tier", func() {
			BeforeEach(func() {
				By("Creating tier1 with higher order number than tier0")
				calicoctl.Exec("delete", "tier", "t1")
				calicoctl.Create(newTier("t1", 99))
			})

			AfterEach(func() {
				calicoctl = calico.ConfigureCalicoctl(f)
				calicoctl.Exec("delete", "tier", "t1")
			})

			It("can explicitly pass traffic", func() {
				By("Creating a policy in tier0 with rules to pass to next tier.")
				p := fmt.Sprintf(strings.Join([]string{"",
					"apiVersion: projectcalico.org/v3",
					"kind: NetworkPolicy",
					"metadata:",
					"  name: t0.test-pass",
					"  namespace: %s",
					"spec:",
					"  selector: pod-name == \"server\"",
					"  tier: t0",
					"  ingress:",
					"   - action: Pass",
				}, "\n"), f.Namespace.Name)
				calicoctl.Create(p)
				defer calicoctl.Delete(p)

				By("Testing server pod should not be accessible with default deny.")
				testCannotConnect(f, f.Namespace, clientName, service, 80)

				By("Creating a policy tier1 with rules to allow traffic.")
				p2 := fmt.Sprintf(strings.Join([]string{"",
					"apiVersion: projectcalico.org/v3",
					"kind: NetworkPolicy",
					"metadata:",
					"  name: t1.test-allow",
					"  namespace: %s",
					"spec:",
					"  selector: pod-name == \"server\"",
					"  tier: t1",
					"  ingress:",
					"   - action: Allow",
				}, "\n"), f.Namespace.Name)
				calicoctl.Create(p2)
				defer calicoctl.Delete(p2)

				By("Testing server pod should be accessible.")
				testCanConnect(f, f.Namespace, clientName, service, 80)
			})

			Context("when it has a policy that applies to an endpoint but doesn't allow or drop the traffic", func() {
				It("defaults to deny", func() {
					By("Creating a policy in default tier with rules to log traffic without allowing it.")
					p := fmt.Sprintf(strings.Join([]string{"",
						"apiVersion: projectcalico.org/v3",
						"kind: NetworkPolicy",
						"metadata:",
						"  name: t0.test-pass",
						"  namespace: %s",
						"spec:",
						"  selector: pod-name == \"server\"",
						"  tier: t0",
						"  ingress:",
						"   - action: Log",
					}, "\n"), f.Namespace.Name)
					calicoctl.Create(p)
					defer calicoctl.Delete(p)

					// Create an allow rule in the second tier so that we know it didn't just
					// hit the default deny rule.
					By("Creating a policy in tier1 with rules to allow traffic.")
					p2 := fmt.Sprintf(strings.Join([]string{"",
						"apiVersion: projectcalico.org/v3",
						"kind: NetworkPolicy",
						"metadata:",
						"  name: t1.test-allow",
						"  namespace: %s",
						"spec:",
						"  selector: pod-name == \"server\"",
						"  tier: t1",
						"  ingress:",
						"   - action: Allow",
					}, "\n"), f.Namespace.Name)
					calicoctl.Create(p2)
					defer calicoctl.Delete(p2)

					By("Testing server pod should not be accessible.")
					testCannotConnect(f, f.Namespace, clientName, service, 80)
				})
			})

			Context("defaults to deny if rules not match traffic.", func() {
				Specify("when it has a deny policy that does not apply to traffic", func() {
					By("Creating a policy in tier0 with rules not match traffic.")
					p := fmt.Sprintf(strings.Join([]string{"",
						"apiVersion: projectcalico.org/v3",
						"kind: NetworkPolicy",
						"metadata:",
						"  name: t0.test-deny",
						"  namespace: %s",
						"spec:",
						"  selector: pod-name == \"server\"",
						"  tier: t0",
						"  ingress:",
						"   - action: Deny",
						"     source:",
						"       selector: pod-name == \"not-client\"",
					}, "\n"), f.Namespace.Name)
					calicoctl.Create(p)
					defer calicoctl.Delete(p)

					By("Creating a policy in tier1 with rules to allow traffic.")
					p2 := fmt.Sprintf(strings.Join([]string{"",
						"apiVersion: projectcalico.org/v3",
						"kind: NetworkPolicy",
						"metadata:",
						"  name: t1.test-allow",
						"  namespace: %s",
						"spec:",
						"  selector: pod-name == \"server\"",
						"  tier: t1",
						"  ingress:",
						"   - action: Allow",
					}, "\n"), f.Namespace.Name)
					calicoctl.Create(p2)
					defer calicoctl.Delete(p2)

					// If a pod has been selected by tier0 and there is no explicit rules to pass traffic.
					// Traffic will be dropped regardless of rules on another tier allowing it.
					By("Testing server pod should not be accessible.")
					testCannotConnect(f, f.Namespace, clientName, service, 80)
				})

				Specify("when it has no policies", func() {
					By("Creating a policy in tier1 with rules to allow traffic and tier0 has no policy.")
					p := fmt.Sprintf(strings.Join([]string{"",
						"apiVersion: projectcalico.org/v3",
						"kind: NetworkPolicy",
						"metadata:",
						"  name: t1.test-allow",
						"  namespace: %s",
						"spec:",
						"  selector: pod-name == \"server\"",
						"  tier: t1",
						"  ingress:",
						"   - action: Allow",
					}, "\n"), f.Namespace.Name)
					calicoctl.Create(p)
					defer calicoctl.Delete(p)

					By("Testing server pod should be accessible.")
					testCanConnect(f, f.Namespace, clientName, service, 80)
				})
			})
		})
	})

})

func newTier(name string, order int) string {
	return fmt.Sprintf(strings.Join([]string{"" +
		"apiVersion: projectcalico.org/v3",
		"kind: Tier",
		"metadata:",
		"  name: %s",
		"spec:",
		"  order: %d",
	}, "\n"), name, order)
}
