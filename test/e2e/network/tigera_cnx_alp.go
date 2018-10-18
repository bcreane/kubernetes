/*
Copyright (c) 2018 Tigera, Inc. All rights reserved.

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
	"net/http"

	"k8s.io/api/core/v1"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/alp"
	"k8s.io/kubernetes/test/utils/calico"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = SIGDescribe("[Feature:CNX-ALP] Tigera CNX application layer policy", func() {
	var calicoctl *calico.Calicoctl

	f := framework.NewDefaultFramework("cnx-alp")

	Context("tiered application layer policy tests", func() {
		BeforeEach(func() {
			var err error

			// See if Istio is installed. If not, then skip these tests so we don't cause spurious failures on non-Istio
			// test environments.
			istioInstalled, err := alp.CheckIstioInstall(f)
			if err != nil {
				framework.Skipf("Checking istio install failed. Skip ALP tests.")
			}
			if !istioInstalled {
				framework.Skipf("Istio not installed. ALP tests not supported.")
			}

			// Namespace for the test, labeled so that Istio Sidecar Injector will add the Dikastes & Envoy sidecars.
			alp.EnableIstioInjectionForNamespace(f, f.Namespace)
		})

		BeforeEach(func() {
			// Common set up for these simple connectivity tests:
			// - Calicoctl initialised
			// - License applied
			// - Tier t0 created
			// - Simple server used to determine connectivity.
			calicoctl = calico.ConfigureCalicoctl(f)
			calicoctl.SetEnv("ALPHA_FEATURES", "serviceaccounts,httprules")

			By("Applying a test CNX license.")
			calicoctl.ApplyCNXLicense()

			By("Creating tier0.")
			result, err := calicoctl.ExecReturnError("delete", "tier", "t0", "--skip-not-exists")
			if err != nil {
				framework.Failf("Error deleting calico Tier 't0': %s", result)
			}
			calicoctl.Create(newTier("t0", 98))
		})

		JustAfterEach(func() {
			if CurrentGinkgoTestDescription().Failed && framework.TestContext.DumpLogsOnFailure {
				framework.Logf(alp.GetIstioDiags(f))
			}
		})

		AfterEach(func() {
			defer calicoctl.Cleanup()
			result, err := calicoctl.ExecReturnError("delete", "tier", "t0", "--skip-not-exists")
			if err != nil {
				framework.Failf("Error deleting calico Tier 't0': %s", result)
			}
		})

		Context("simple tier/policy ordering using service account matches", func() {
			var service *v1.Service
			var podServer *v1.Pod

			BeforeEach(func() {
				By("Creating a simple server.")
				podServer, service = createIstioServerPodAndService(f, f.Namespace, "server", []int{80}, nil)
				framework.Logf("Waiting for Server to come up.")
				err := framework.WaitForPodRunningInNamespace(f.ClientSet, podServer)
				Expect(err).NotTo(HaveOccurred())
			})

			AfterEach(func() {
				if podServer != nil && service != nil {
					cleanupServerPodAndService(f, podServer, service)
					podServer = nil
					service = nil
				}
			})

			It("should honor tier and policy ordering / policies matching on service account names", func() {
				// This is a 3-part test where we apply a default tier GNP, a t0 tier NP and a t0 tier GNP
				// in the order defined below. All policies main selectors match on the server pod. Table
				// shows expected connectivity after applying each policy. We are not removing policies, so
				// final entry has three policies applied.
				//
				// Tier    | Order | Action SA-a | Action SA-b |    Expected Connectivity
				// default |       | Allow  <-+  | -           |    SA-a
				// t0      | 100   | Deny     |  | Allow       |    SA-b
				// t0      | 10    | Pass  ---+  | Allow       |    SA-a and SA-b
				//
				// This test uses a mixture of name and label selection of service accounts in the policy rules.
				By("creating two service accounts for the test")
				sa := alp.CreateServiceAccount(f, "svc-acct-a", f.Namespace.Name, map[string]string{"svc-acct-id": "a"})
				sb := alp.CreateServiceAccount(f, "svc-acct-b", f.Namespace.Name, map[string]string{"svc-acct-id": "b"})
				defer alp.DeleteServiceAccount(f, sa)
				defer alp.DeleteServiceAccount(f, sb)

				By("creating a global network policy in the default tier; allow svc-acct-a")
				gnp := `
apiVersion: projectcalico.org/v3
kind: GlobalNetworkPolicy
metadata:
  name: default.svc-acct-a-allow
spec:
  selector: pod-name == "server"
  ingress:
  - action: Allow
    source:
      serviceAccounts:
        names: ["svc-acct-a"]
  egress:
  - action: Allow
`
				calicoctl.Apply(gnp)
				defer calicoctl.DeleteGNP("default.svc-acct-a-allow")

				By("verifying pod (svc-acct-a) can connect")
				testIstioCanConnect(f, f.Namespace, "pod-can-connect", service, 80, podServer, sa)
				By("verifying pod (svc-acct-b) cannot connect")
				testIstioCannotConnect(f, f.Namespace, "pod-cannot-connect", service, 80, podServer, sb)

				By("creating a network policy in tier t0; deny svc-acct-a, allow svc-acct-b")
				np := fmt.Sprintf(`
apiVersion: projectcalico.org/v3
kind: NetworkPolicy
metadata:
  name: t0.svc-acct-a-deny-b-allow
  namespace: %s
spec:
  order: 100
  tier: t0
  selector: pod-name == "server"
  ingress:
  - action: Deny
    source:
      serviceAccounts:
        selector: svc-acct-id == "a"
  - action: Allow
    source:
      serviceAccounts:
        selector: svc-acct-id == "b"
  egress:
  - action: Allow
`, f.Namespace.Name)
				calicoctl.Apply(np)
				defer calicoctl.DeleteNP(f.Namespace.Name, "t0.svc-acct-a-deny-b-allow")

				By("verifying pod (svc-acct-a) cannot connect")
				testIstioCannotConnect(f, f.Namespace, "pod-cannot-connect", service, 80, podServer, sa)
				By("verifying pod (svc-acct-b) can connect")
				testIstioCanConnect(f, f.Namespace, "pod-can-connect", service, 80, podServer, sb)

				By("creating a lower order global network policy in tier t0; pass svc-acct-a (allow in default tier), allow svc-acct-b")
				gnp = `
apiVersion: projectcalico.org/v3
kind: GlobalNetworkPolicy
metadata:
  name: t0.svc-acct-a-pass-b-allow
spec:
  order: 10
  tier: t0
  selector: pod-name == "server"
  ingress:
  - action: Pass
    source:
      serviceAccounts:
        names: ["svc-acct-a"]
  - action: Allow
    source:
      serviceAccounts:
        names: ["svc-acct-b"]
  egress:
  - action: Allow
`
				calicoctl.Apply(gnp)
				defer calicoctl.DeleteGNP("t0.svc-acct-a-pass-b-allow")

				By("verifying pod (svc-acct-a) can connect")
				testIstioCanConnect(f, f.Namespace, "pod-can-connect", service, 80, podServer, sa)
				By("verifying pod (svc-acct-b) can connect")
				testIstioCanConnect(f, f.Namespace, "pod-can-connect", service, 80, podServer, sb)
			})
		})

		Context("simple tier/policy ordering using http matches and service accounts", func() {
			var service *v1.Service
			var podServer *v1.Pod

			BeforeEach(func() {
				// Create Server with Service
				By("Creating a server support GET/PUT.")
				podServer, service = createIstioGetPutPodAndService(f, f.Namespace, "server", 2379, nil)
				framework.Logf("Waiting for Server to come up.")
				err := framework.WaitForPodRunningInNamespace(f.ClientSet, podServer)
				Expect(err).NotTo(HaveOccurred())

				By("Creating client which will be able to GET/PUT the server since no policies are present.")
				testIstioCanGetPut(f, f.Namespace, http.MethodGet, service, podServer, nil)
				testIstioCanGetPut(f, f.Namespace, http.MethodPut, service, podServer, nil)

			})

			AfterEach(func() {
				if podServer != nil && service != nil {
					cleanupServerPodAndService(f, podServer, service)
					podServer = nil
					service = nil
				}
			})

			It("should disallow unsupported policy configuration", func() {
				// We cannot create explicit Deny rules when the HTTP match is specified (since Felix ignores
				// it and would therefore be overzealous in dropping the packet). Verify we disallow this.
				By("verifying it is not possible to create a Deny rule when HTTP is specified")
				gnp := `
apiVersion: projectcalico.org/v3
kind: GlobalNetworkPolicy
metadata:
  name: default.this-should-fail
spec:
  tier: default
  selector: pod-name == "server"
  ingress:
  - action: Deny
    http:
      methods: ["GET"]
  egress:
  - action: Allow
`
				err := calicoctl.ApplyWithError(gnp)
				Expect(err).To(HaveOccurred())

				// For similar reasons it's not possible to create a Pass rule when HTTP is specified.
				By("verifying it is not possible to create a Pass rule when HTTP is specified")
				np := `
apiVersion: projectcalico.org/v3
kind: NetworkPolicy
metadata:
  name: default.this-should-fail-as-well
  namespace: default
spec:
  tier: default
  selector: pod-name == "server"
  ingress:
  - action: Pass
    http:
      methods: ["GET"]
  egress:
  - action: Allow
`
				err = calicoctl.ApplyWithError(np)
				Expect(err).To(HaveOccurred())
			})

			It("should honor tier and policy ordering / policies matching on http method and service acccount names", func() {
				// This is a 3-part test where we apply a default tier NP, a t0 tier NP and a t0 tier GNP
				// in the order defined below. All policies main selectors match on the server pod. Expected
				// connectivity after applying each policy is described. We are not removing policies, so
				// final entry has three policies applied.
				//
				// This tests HTTP matches in two tiers, applying three policies that test the following:
				// -  NP in default namespace and default tier to allow all - this should not impact our tests
				//    which use a different namespace.
				// -  A higher order NP in the default tier which allows GET for svc-acct-a and svc-acct-b and
				//    allows PUT for svc-acct-a.  PUT for svc-acct-b is therefore implicity denied.
				// -  A tier t0 NP that allows PUT for svc-acct-a and svc-acct-b, and then Denies svt-acct-a and
				//    Passes for svt-acct-b. Thus GET for svc-acct-b is Allowed by the default tier policy.
				// -  A lower order tier t0 GNP that Allows TCP GET for scv-acct-a, Allows UDP GET for svc-acct-b
				//    and otherwise passes svc-acct-b. Since http is TCP based we expect svc-acct-b just to match on
				//    the pass action and therefore will have the same connectivity from the default profile.
				//
				// This test uses a mixture of name and label selection of service accounts in the policy rules.
				//
				// Note that this is an interesting test case because it includes a HTTP Allow match followed by both a
				// Deny and a Pass match. Since Felix ignores the HTTP part of the match, the Allow match will trump
				// both the Deny and Pass in Felix. Thus, the handling of the Deny and Pass rules and subsequent policy
				// matching is performed in Dikastes.
				By("creating two service accounts for the test")
				sa := alp.CreateServiceAccount(f, "svc-acct-a", f.Namespace.Name, map[string]string{"svc-acct-id": "a"})
				sb := alp.CreateServiceAccount(f, "svc-acct-b", f.Namespace.Name, map[string]string{"svc-acct-id": "b"})
				defer alp.DeleteServiceAccount(f, sa)
				defer alp.DeleteServiceAccount(f, sb)

				// This is a quick add-on test, to ensure a policy in a different namespace will not impact the
				// test which uses a different namespace.
				By("creating an allow all network policy in the default namespace and default tier; should not impact test")
				np := `
apiVersion: projectcalico.org/v3
kind: NetworkPolicy
metadata:
  name: default.allow-all
  namespace: default
spec:
  order: 10
  tier: default
  selector: pod-name == "server"
  ingress:
  - action: Allow
  egress:
  - action: Allow
`
				calicoctl.Apply(np)
				defer calicoctl.DeleteNP("default", "default.allow-all")

				By("creating a network policy in the default tier; allow: put/get svc-acct-a; get svc-acct-b (implicit deny for put)")
				np = fmt.Sprintf(`
apiVersion: projectcalico.org/v3
kind: NetworkPolicy
metadata:
  name: default.get-a-b-put-a
  namespace: %s
spec:
  order: 100
  tier: default
  selector: pod-name == "server"
  ingress:
  - action: Allow
    http:
      methods: ["GET"]
    source:
      serviceAccounts:
        names: ["svc-acct-a", "svc-acct-b"]
  - action: Allow
    http:
      methods: ["PUT"]
    source:
      serviceAccounts:
        names: ["svc-acct-a"]
  egress:
  - action: Allow
`, f.Namespace.Name)
				calicoctl.Apply(np)
				defer calicoctl.DeleteNP(f.Namespace.Name, "default.get-a-b-put-a")

				By("verifying pod (svc-acct-a) can GET")
				testIstioCanGetPut(f, f.Namespace, http.MethodGet, service, podServer, sa)
				By("verifying pod (svc-acct-b) can GET")
				testIstioCanGetPut(f, f.Namespace, http.MethodGet, service, podServer, sb)
				By("verifying pod (svc-acct-a) can PUT")
				testIstioCanGetPut(f, f.Namespace, http.MethodPut, service, podServer, sa)
				By("verifying pod (svc-acct-b) cannot PUT")
				testIstioCannotGetPut(f, f.Namespace, http.MethodPut, service, podServer, sb)

				By("creating a network policy in the t0 tier; put svc-acct-a/b; deny svc-acct-a; pass svc-acct-b")
				np = fmt.Sprintf(`
apiVersion: projectcalico.org/v3
kind: NetworkPolicy
metadata:
  name: t0.allow-put-a-b-deny-a-pass-b
  namespace: %s
spec:
  order: 100
  tier: t0
  selector: pod-name == "server"
  ingress:
  - action: Allow
    http:
      methods: ["PUT"]
    source:
      serviceAccounts:
        selector: svc-acct-id == "a" || svc-acct-id == "b"
  - action: Deny
    source:
      serviceAccounts:
        names: ["svc-acct-a"]
  - action: Pass
    source:
      serviceAccounts:
        names: ["svc-acct-b"]
  egress:
  - action: Allow
`, f.Namespace.Name)
				calicoctl.Apply(np)
				defer calicoctl.DeleteNP(f.Namespace.Name, "t0.allow-put-a-b-deny-a-pass-b")

				By("verifying pod (svc-acct-a) cannot GET")
				testIstioCannotGetPut(f, f.Namespace, http.MethodGet, service, podServer, sa)
				By("verifying pod (svc-acct-b) can GET") // From the pass to next tier
				testIstioCanGetPut(f, f.Namespace, http.MethodGet, service, podServer, sb)
				By("verifying pod (svc-acct-a) can PUT")
				testIstioCanGetPut(f, f.Namespace, http.MethodPut, service, podServer, sa)
				By("verifying pod (svc-acct-b) can PUT")
				testIstioCanGetPut(f, f.Namespace, http.MethodPut, service, podServer, sb)

				By("creating a global network policy in the t0 tier; allow-TCP-a; allow-UDP-b; pass-b")
				np = `
apiVersion: projectcalico.org/v3
kind: GlobalNetworkPolicy
metadata:
  name: t0.allow-tcp-a-udp-b-pass-b
spec:
  order: 99
  tier: t0
  selector: pod-name == "server"
  ingress:
  - action: Allow
    protocol: TCP
    http:
      methods: ["GET", "PUT"]
    source:
      serviceAccounts:
        selector: svc-acct-id == "a"
  - action: Allow
    protocol: UDP
    http:
      methods: ["GET", "PUT"]
    source:
      serviceAccounts:
        names: ["svc-acct-b"]
  - action: Pass
    source:
      serviceAccounts:
        selector: svc-acct-id == "b"
  egress:
  - action: Allow
`
				calicoctl.Apply(np)
				defer calicoctl.DeleteGNP("t0.allow-tcp-a-udp-b-pass-b")

				By("verifying pod (svc-acct-a) can GET")
				testIstioCanGetPut(f, f.Namespace, http.MethodGet, service, podServer, sa)
				By("verifying pod (svc-acct-b) can GET")
				testIstioCanGetPut(f, f.Namespace, http.MethodGet, service, podServer, sb)
				By("verifying pod (svc-acct-a) can PUT") // From the pass to next tier
				testIstioCanGetPut(f, f.Namespace, http.MethodPut, service, podServer, sa)
				By("verifying pod (svc-acct-b) cannot PUT") // From the pass to next tier
				testIstioCannotGetPut(f, f.Namespace, http.MethodPut, service, podServer, sb)
			})
		})
	})
})
