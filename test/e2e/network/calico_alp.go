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
	"net/http"
	"strings"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labelutils "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/alp"
	"k8s.io/kubernetes/test/utils/calico"

	"k8s.io/api/core/v1"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = SIGDescribe("[Feature:CalicoPolicy-ALP] calico application layer policy", func() {
	var calicoctl *calico.Calicoctl

	f := framework.NewDefaultFramework("calico-alp")

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
		// The following code tries to get config information for calicoctl from k8s ConfigMap.
		// A framework clientset is needed to access k8s configmap but it will only be created in the context of BeforeEach or IT.
		// Current solution is to use BeforeEach because this function is not a test case.
		// This will avoid complexity of creating a client by ourself.
		calicoctl = calico.ConfigureCalicoctl(f)
		calicoctl.SetEnv("ALPHA_FEATURES", "serviceaccounts,httprules")
	})

	JustAfterEach(func() {
		if CurrentGinkgoTestDescription().Failed && framework.TestContext.DumpLogsOnFailure {
			framework.Logf(alp.GetIstioDiags(f))
		}
	})

	Context("with service running", func() {
		var podServer *v1.Pod
		var service *v1.Service

		BeforeEach(func() {
			// Create Server with Service
			By("Creating a simple server.")
			podServer, service = createIstioServerPodAndService(f, f.Namespace, "server", []int{80}, nil)
			framework.Logf("Waiting for Server to come up.")
			err := framework.WaitForPodRunningInNamespace(f.ClientSet, podServer)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			cleanupServerPodAndService(f, podServer, service)
			calicoctl.Cleanup()
		})

		Context("with no policy", func() {

			It("should allow pod with default service account to connect", func() {
				By("Creating client which will be able to contact the server since no policies are present.")
				testIstioCanConnect(f, f.Namespace, "default-can-connect", service, 80, podServer, nil)
			})
		})

		Context("with GlobalNetworkPolicy selecting \"can-connect\" service account", func() {

			BeforeEach(func() {
				gnp := `
- apiVersion: projectcalico.org/v3
  kind: GlobalNetworkPolicy
  metadata:
    name: svc-acct-can-connect
  spec:
    selector: pod-name == "server"
    ingress:
      - action: Allow
        source:
          serviceAccounts:
            names: ["can-connect"]
    egress:
      - action: Allow
`
				calicoctl.Apply(gnp)
			})

			AfterEach(func() {
				calicoctl.DeleteGNP("svc-acct-can-connect")
			})

			It("should allow \"can-connect\" pod to connect", func() {
				By("creating \"can-connect\" service account")
				sa := alp.CreateServiceAccount(f, "can-connect", f.Namespace.Name, map[string]string{"can-connect": "true"})
				defer alp.DeleteServiceAccount(f, sa)

				By("testing connectivity with pod using \"can-connect\" service account")
				testIstioCanConnect(f, f.Namespace, "pod-can-connect", service, 80, podServer, sa)
			})

			It("should not allow \"cannot-connect\" pod to connect", func() {
				By("creating \"cannot-connect\" service account")
				sa := alp.CreateServiceAccount(f, "cannot-connect", f.Namespace.Name, map[string]string{"can-connect": "false"})
				defer alp.DeleteServiceAccount(f, sa)

				By("testing connectivity with pod using \"cannot-connect\" service account")
				testIstioCannotConnect(f, f.Namespace, "pod-cannot-connect", service, 80, podServer, sa)
			})
		})
		Context("With GlobalNetworkPolicy matching service account selectors", func() {

			BeforeEach(func() {
				gnp := `
- apiVersion: projectcalico.org/v3
  kind: GlobalNetworkPolicy
  metadata:
    name: svc-acct-can-connect
  spec:
    selector: pod-name == "server"
    ingress:
    - action: Allow
      source:
        serviceAccounts:
          selector: can-connect == "true"
    egress:
    - action: Allow
`

				calicoctl.Apply(gnp)
			})

			AfterEach(func() {
				calicoctl.DeleteGNP("svc-acct-can-connect")
			})

			It("should allow \"can-connect\" pod to connect", func() {
				By("creating \"can-connect\" service account")
				sa := alp.CreateServiceAccount(f, "can-connect", f.Namespace.Name, map[string]string{"can-connect": "true"})
				defer alp.DeleteServiceAccount(f, sa)

				By("testing connectivity with pod using \"can-connect\" service account")
				testIstioCanConnect(f, f.Namespace, "client-can-connect", service, 80, podServer, sa)
			})

			It("should not allow \"cannot-connect\" pod to connect", func() {
				By("creating \"cannot-connect\" service account")
				sa := alp.CreateServiceAccount(f, "cannot-connect", f.Namespace.Name, map[string]string{"can-connect": "false"})
				defer alp.DeleteServiceAccount(f, sa)

				By("testing connectivity with pod using \"cannot-connect\" service account")
				testIstioCannotConnect(f, f.Namespace, "client-cannot-connect", service, 80, podServer, sa)
			})
		})
	})

	Describe("ALP http-method test", func() {
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
			cleanupServerPodAndService(f, podServer, service)
			calicoctl.Cleanup()
		})

		It("should enforce policy with http method rule", func() {
			By("creating policy which allow GET")
			gnp := `
- apiVersion: projectcalico.org/v3
  kind: GlobalNetworkPolicy
  metadata:
    name: http-method
  spec:
    selector: pod-name == "server"
    ingress:
      - action: Allow
        http:
          methods: ["GET"]
    egress:
      - action: Allow
`
			calicoctl.Apply(gnp)
			defer calicoctl.DeleteGNP("http-method")

			By("testing http method with pod using default service account, allow Get")
			testIstioCanGetPut(f, f.Namespace, http.MethodGet, service, podServer, nil)

			By("testing http method with pod using default service account, deny Put")
			testIstioCannotGetPut(f, f.Namespace, http.MethodPut, service, podServer, nil)

			By("modifying policy which allow PUT")
			gnp = `
- apiVersion: projectcalico.org/v3
  kind: GlobalNetworkPolicy
  metadata:
    name: http-method
  spec:
    selector: pod-name == "server"
    ingress:
      - action: Allow
        http:
          methods: ["PUT"]
    egress:
      - action: Allow
`
			calicoctl.Apply(gnp)

			By("testing http method with pod using default service account, deny Get")
			testIstioCannotGetPut(f, f.Namespace, http.MethodGet, service, podServer, nil)

			By("testing http method with pod using default service account, allow Put")
			testIstioCanGetPut(f, f.Namespace, http.MethodPut, service, podServer, nil)
		})

		It("should enforce policy with both http method and service account rule", func() {
			By("creating \"sa-first\" and \"sa-second\" service account")
			saFirst := alp.CreateServiceAccount(f, "sa-first", f.Namespace.Name, map[string]string{"sa-first": "true"})
			defer alp.DeleteServiceAccount(f, saFirst)
			saSecond := alp.CreateServiceAccount(f, "sa-second", f.Namespace.Name, map[string]string{"sa-second": "true"})
			defer alp.DeleteServiceAccount(f, saSecond)

			By("creating policy which allow GET with service account \"sa-first\"")
			gnp := `
- apiVersion: projectcalico.org/v3
  kind: GlobalNetworkPolicy
  metadata:
    name: http-sa
  spec:
    selector: pod-name == "server"
    ingress:
      - action: Allow
        http:
          methods: ["GET"]
        source:
          serviceAccounts:
            names: ["sa-first"]
    egress:
      - action: Allow
`
			calicoctl.Apply(gnp)
			defer calicoctl.DeleteGNP("http-sa")

			By("allow client with service account \"sa-first\" to GET")
			testIstioCanGetPut(f, f.Namespace, http.MethodGet, service, podServer, saFirst)
			By("deny client with service account \"sa-first\" to PUT")
			testIstioCannotGetPut(f, f.Namespace, http.MethodPut, service, podServer, saFirst)
			By("deny client with service account \"sa-second\" to GET")
			testIstioCannotGetPut(f, f.Namespace, http.MethodGet, service, podServer, saSecond)
			By("deny client with service account \"sa-second\" to PUT")
			testIstioCannotGetPut(f, f.Namespace, http.MethodPut, service, podServer, saSecond)

			By("modifying policy which allow PUT with service account \"sa-second\" ")
			gnp = `
- apiVersion: projectcalico.org/v3
  kind: GlobalNetworkPolicy
  metadata:
    name: http-sa
  spec:
    selector: pod-name == "server"
    ingress:
      - action: Allow
        http:
          methods: ["PUT"]
        source:
          serviceAccounts:
            names: ["sa-second"]
    egress:
      - action: Allow
`
			calicoctl.Apply(gnp)

			By("deny client with service account \"sa-first\" to GET")
			testIstioCannotGetPut(f, f.Namespace, http.MethodGet, service, podServer, saFirst)
			By("deny client with service account \"sa-first\" to PUT")
			testIstioCannotGetPut(f, f.Namespace, http.MethodPut, service, podServer, saFirst)
			By("deny client with service account \"sa-second\" to GET")
			testIstioCannotGetPut(f, f.Namespace, http.MethodGet, service, podServer, saSecond)
			By("allow client with service account \"sa-second\" to PUT")
			testIstioCanGetPut(f, f.Namespace, http.MethodPut, service, podServer, saSecond)
		})

	})

	Describe("calico network policy test", func() {
		// The tests in this context is for testing standard calico network policy without ALP special selectors.
		// The test cases are copied over from standard network policy e2e with some ALP tweaks.
		// -- Use createIstioServerPodAndService to create server pod and service.
		// -- Use alp.EnableIstioInjectionForNamespace to enable istio injection for any new namespace.
		// -- Use testIstioCanConnect/testIstioCannotConnect to test connection.
		// -- Add egress allow rule to istio pilot http discovery port 15003 for any egress test.

		Describe("ALP lable selector test", func() {
			var service *v1.Service
			var podServer *v1.Pod

			BeforeEach(func() {
				// Create Server with Service
				By("Creating a simple server.")
				podServer, service = createIstioServerPodAndService(f, f.Namespace, "server", []int{80}, nil)
				framework.Logf("Waiting for Server to come up.")
				err := framework.WaitForPodRunningInNamespace(f.ClientSet, podServer)
				Expect(err).NotTo(HaveOccurred())

				By("Creating client which will be able to contact the server since no policies are present.")
				testIstioCanConnect(f, f.Namespace, "client-can-connect", service, 80, podServer, nil)

			})

			AfterEach(func() {
				cleanupServerPodAndService(f, podServer, service)
				calicoctl.Cleanup()
			})

			It("should correctly be able to select endpoints for policies using label selectors", func() {
				nsA := f.Namespace
				podServerA := podServer
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
				alp.EnableIstioInjectionForNamespace(f, nsB)

				By("Creating simple servers B and C with labels.")
				identifierKey := "identifier"
				podServerB, serviceB := createIstioServerPodAndService(f, nsB, "server-b", []int{80}, map[string]string{identifierKey: "ident1"})
				defer cleanupServerPodAndService(f, podServerB, serviceB)
				framework.Logf("Waiting for Server to come up.")
				err = framework.WaitForPodRunningInNamespace(f.ClientSet, podServerB)
				Expect(err).NotTo(HaveOccurred())

				// Create a labeled server within namespace A: the namespace without a labeled server pod
				podServerC, serviceC := createIstioServerPodAndService(f, nsA, "server-c", []int{80}, map[string]string{identifierKey: "ident2"})
				defer cleanupServerPodAndService(f, podServerC, serviceC)
				framework.Logf("Waiting for Server to come up.")
				err = framework.WaitForPodRunningInNamespace(f.ClientSet, podServerC)
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
    ingress:
    - action: Deny
      source: {}
      destination: {}
`
				calicoctl.Apply(denyPolicyStr)
				defer calicoctl.DeleteGNP("default-deny-all")

				// Test that none of the pods are able to reach each other since they all have a pod-name selector
				By("deny A -> A")
				testIstioCannotConnect(f, nsA, "client-a", serviceA, 80, podServerA, nil)
				By("deny B -> B")
				testIstioCannotConnect(f, nsB, "client-b", serviceB, 80, podServerB, nil)
				By("deny B -> A")
				testIstioCannotConnect(f, nsB, "client-b", serviceA, 80, podServerA, nil)
				By("deny A -> B")
				testIstioCannotConnect(f, nsA, "client-a", serviceB, 80, podServerB, nil)
				By("deny A -> C")
				testIstioCannotConnect(f, nsA, "client-a", serviceC, 80, podServerC, nil)
				By("deny B -> C")
				testIstioCannotConnect(f, nsB, "client-b", serviceC, 80, podServerC, nil)

				By("Creating an ingress policy to allow traffic from namespace B to any pods with with a specific label.")
				policyNameAllowB := fmt.Sprintf("%s", "ingress-allow-b")
				policyStrAllowB := fmt.Sprintf(`
- apiVersion: projectcalico.org/v3
  kind: GlobalNetworkPolicy
  metadata:
    name: %s
  spec:
    order: 900
    selector: has(%s)
    ingress:
    - action: Allow
      source:
        namespaceSelector: %s == "%s"
`,
					policyNameAllowB, "pod-name", nsBLabelName, nsBLabelValue)
				calicoctl.Apply(policyStrAllowB)
				defer calicoctl.DeleteGNP(policyNameAllowB)

				// Test that any pod can receive traffic from namespace B only
				By("deny A -> A")
				testIstioCannotConnect(f, nsA, "client-a", serviceA, 80, podServerA, nil)
				By("allow B -> B")
				testIstioCanConnect(f, nsB, "client-b", serviceB, 80, podServerB, nil)
				By("allow B -> A")
				testIstioCanConnect(f, nsB, "client-b", serviceA, 80, podServerA, nil)
				By("deny A -> B")
				testIstioCannotConnect(f, nsA, "client-a", serviceB, 80, podServerB, nil)
				By("deny A -> C")
				testIstioCannotConnect(f, nsA, "client-a", serviceC, 80, podServerC, nil)
				By("allow B -> C")
				testIstioCanConnect(f, nsB, "client-b", serviceC, 80, podServerC, nil)

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
    - action: Allow
      source:
        namespaceSelector: %s == "%s"
    - action: Deny
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
				testIstioCannotConnect(f, nsA, "client-a", serviceA, 80, podServerA, nil)
				By("allow B -> B")
				testIstioCanConnect(f, nsB, "client-b", serviceB, 80, podServerB, nil)
				By("allow B -> A")
				testIstioCanConnect(f, nsB, "client-b", serviceA, 80, podServerA, nil)
				By("deny A -> B")
				testIstioCannotConnect(f, nsA, "client-a", serviceB, 80, podServerB, nil)
				By("allow A -> C")
				testIstioCanConnect(f, nsA, "client-a", serviceC, 80, podServerC, nil)
				By("deny B -> C")
				testIstioCannotConnect(f, nsB, "client-b", serviceC, 80, podServerC, nil)
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
				alp.EnableIstioInjectionForNamespace(f, nsB)

				By("Creating a simple server server-b in namespace A.")
				podServerAB, serviceAB := createIstioServerPodAndService(f, nsA, "server-b", []int{80}, nil)
				framework.Logf("Waiting for Server to come up.")
				err = framework.WaitForPodRunningInNamespace(f.ClientSet, podServerAB)
				Expect(err).NotTo(HaveOccurred())
				defer cleanupServerPodAndService(f, podServerAB, serviceAB)

				By("Creating a simple server server-b in namespace B.")
				podServerBB, serviceBB := createIstioServerPodAndService(f, nsB, "server-b", []int{80}, nil)
				framework.Logf("Waiting for Server to come up.")
				err = framework.WaitForPodRunningInNamespace(f.ClientSet, podServerBB)
				Expect(err).NotTo(HaveOccurred())
				defer cleanupServerPodAndService(f, podServerBB, serviceBB)

				By("Creating client from namespace A which will be able to contact the server in namespace B since no policies are present.")
				By("allow A.client-a -> A.server-b")
				testIstioCanConnect(f, nsA, "client-a", serviceAB, 80, podServerAB, nil) //allow A.client-a -> A.server-b
				By("allow A.client-a -> B.server-b")
				testIstioCanConnect(f, nsA, "client-a", serviceBB, 80, podServerBB, nil) //allow A.client-a -> B.server-b
				By("allow B.client-a -> A.server-b")
				testIstioCanConnect(f, nsB, "client-a", serviceAB, 80, podServerAB, nil) //allow B.client-a -> A.server-b

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
					"      ports: [53]\n"+
					"  - action: Allow\n"+ // Istio special: allow egress to Pilot http discovery.
					"    protocol: TCP\n"+
					"    destination:\n"+
					"      selector: projectcalico.org/namespace == \"%s\" && istio == \"pilot\"\n"+
					"      ports: [%d]",
					policyName, nsA.Name, alp.IstioNamespace, alp.PilotDiscoveryPort)
				calicoctl.Apply(policyStr)
				defer calicoctl.DeleteGNP(policyName)

				By("Creating client-a from namespace A which will not be able to contact the server in namespace A, B since egress deny policies are present.")
				By("deny A.client-a -> A.server-b")
				testIstioCannotConnect(f, nsA, "client-a", serviceAB, 80, podServerAB, nil) //deny A.client-a -> A.server-b
				By("deny A.client-a -> B.server-b")
				testIstioCannotConnect(f, nsA, "client-a", serviceBB, 80, podServerBB, nil) //deny A.client-a -> B.server-b
				By("allow A.client-b -> A.server-b")
				testIstioCanConnect(f, nsA, "client-b", serviceAB, 80, podServerAB, nil) //allow A.client-b -> A.server-b
				By("allow B.client-a -> A.server-b")
				testIstioCanConnect(f, nsB, "client-a", serviceAB, 80, podServerAB, nil) //allow B.client-a -> A.server-b

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
				testIstioCannotConnect(f, nsA, "client-a", serviceAB, 80, podServerAB, nil) //deny A.client-a -> A.server-b
				By("allow A.client-a -> B.server-b")
				testIstioCanConnect(f, nsA, "client-a", serviceBB, 80, podServerBB, nil) //allow A.client-a -> B.server-b
				By("allow A.client-b -> A.server-b")
				testIstioCanConnect(f, nsA, "client-b", serviceAB, 80, podServerAB, nil) //allow A.client-b -> A.server-b
				By("allow B.client-a -> A.server-b")
				testIstioCanConnect(f, nsB, "client-a", serviceAB, 80, podServerAB, nil) //allow B.client-a -> A.server-b
			})

		})

		Describe("ALP named port test", func() {
			var service *v1.Service
			var podServer *v1.Pod

			BeforeEach(func() {
				By("Creating a simple server that serves on port 80 and 81.")
				podServer, service = createIstioServerPodAndService(f, f.Namespace, "server", []int{80, 81}, nil)

				By("Waiting for pod ready", func() {
					err := f.WaitForPodReady(podServer.Name)
					Expect(err).NotTo(HaveOccurred())
				})

				// Create pods, which should be able to communicate with the server on port 80 and 81.
				By("Testing pods can connect to both ports when no policy is present.")
				testIstioCanConnect(f, f.Namespace, "client-can-connect-80", service, 80, podServer, nil)
				testIstioCanConnect(f, f.Namespace, "client-can-connect-81", service, 81, podServer, nil)
			})

			AfterEach(func() {
				cleanupServerPodAndService(f, podServer, service)
			})
			It("should allow ingress access on one named port", func() {
				policy := &networkingv1.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "allow-client-a-via-named-port-ingress-rule",
					},
					Spec: networkingv1.NetworkPolicySpec{
						// Apply this policy to the Server
						PodSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{
								"pod-name": podServer.Name,
							},
						},
						// Allow traffic to only one named port: "serve-80".
						Ingress: []networkingv1.NetworkPolicyIngressRule{{
							Ports: []networkingv1.NetworkPolicyPort{{
								Port: &intstr.IntOrString{Type: intstr.String, StrVal: "serve-80"},
							}},
						}},
					},
				}

				policy, err := f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).Create(policy)
				Expect(err).NotTo(HaveOccurred())
				defer cleanupNetworkPolicy(f, policy)

				By("Creating client-a which should be able to contact the server.", func() {
					testIstioCanConnect(f, f.Namespace, "client-a", service, 80, podServer, nil)
				})
				By("Creating client-b which should not be able to contact the server on port 81.", func() {
					testIstioCannotConnect(f, f.Namespace, "client-b", service, 81, podServer, nil)
				})
			})

			It("should allow egress access on one named port [Feature:NetworkPolicy]", func() {
				framework.SkipUnlessServerVersionGTE(egressVersion, f.ClientSet.Discovery())
				clientPodName := "client-a"
				protocolUDP := v1.ProtocolUDP
				protocolTCP := v1.ProtocolTCP
				policy := &networkingv1.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "allow-client-a-via-named-port-egress-rule",
					},
					Spec: networkingv1.NetworkPolicySpec{
						// Apply this policy to client-a
						PodSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{
								"pod-name": clientPodName,
							},
						},
						// Allow traffic to only one named port: "serve-80".
						Egress: []networkingv1.NetworkPolicyEgressRule{{
							Ports: []networkingv1.NetworkPolicyPort{
								{
									Port: &intstr.IntOrString{Type: intstr.String, StrVal: "serve-80"},
								},
								// Allow DNS look-ups
								{
									Protocol: &protocolUDP,
									Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: 53},
								},
								// Allow istio pilot http-discovery
								{
									Protocol: &protocolTCP,
									Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: int32(alp.PilotDiscoveryPort)},
								},
							},
						}},
					},
				}

				policy, err := f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).Create(policy)
				Expect(err).NotTo(HaveOccurred())
				defer cleanupNetworkPolicy(f, policy)

				By("Creating client-a which should be able to contact the server.", func() {
					testIstioCanConnect(f, f.Namespace, clientPodName, service, 80, podServer, nil)
				})
				By("Creating client-a which should not be able to contact the server on port 81.", func() {
					testIstioCannotConnect(f, f.Namespace, clientPodName, service, 81, podServer, nil)
				})
			})
		})
	})
})

var _ = SIGDescribe("[Feature:CalicoPolicy-ALP] prevent sidecar injection with per-pod override annotation ", func() {

	f := framework.NewDefaultFramework("sidecar-injection-override")

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

	JustAfterEach(func() {
		if CurrentGinkgoTestDescription().Failed && framework.TestContext.DumpLogsOnFailure {
			framework.Logf(alp.GetIstioDiags(f))
		}
	})

	Describe("Tests to verify pod annotation overrides namespace label and prevents sidecar injection for that pod", func() {

		It("should prevent sidecar injection for pod with annotation: \"sidecar.istio.io/inject:false\" in istio enabled namespace", func() {
			By("Creating pod in istio enabled namespace with override annotation")
			setAnnotation := func(pod *v1.Pod) {
				if pod.Annotations == nil {
					pod.Annotations = make(map[string]string)
				}
				pod.Annotations["sidecar.istio.io/inject"] = "false"
			}
			podName := framework.CreateExecPodOrFail(f.ClientSet, f.Namespace.Name, "alpexec-", setAnnotation)
			pod, err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).Get(podName, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			defer func() {
				framework.DeletePodOrFail(f.ClientSet, f.Namespace.Name, podName)
				alp.WaitForPodNotFoundInNamespace(f, f.Namespace, podName)
			} ()

			By("Verify sidecar containers for pod")
			sidecars := alp.VerifySideCarsForPod(pod)
			Expect(sidecars).To(BeFalse())
		})
	})
})

var _ = SIGDescribe("[Feature:CalicoPolicy-ALP] calico application layer policy for pod that is not running dikastes", func() {
        var calicoctl *calico.Calicoctl

        f := framework.NewDefaultFramework("calico-alp-without-dikastes")

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
	})

	BeforeEach(func() {
		// The following code tries to get config information for calicoctl from k8s ConfigMap.
		// A framework clientset is needed to access k8s configmap but it will only be created in the context of BeforeEach or IT.
		// Current solution is to use BeforeEach because this function is not a test case.
		// This will avoid complexity of creating a client by ourself.
		calicoctl = calico.ConfigureCalicoctl(f)
		calicoctl.SetEnv("ALPHA_FEATURES", "serviceaccounts,httprules")
	})

	JustAfterEach(func() {
		if CurrentGinkgoTestDescription().Failed && framework.TestContext.DumpLogsOnFailure {
			framework.Logf(alp.GetIstioDiags(f))
		}
	})

	Describe("HTTP based policy tests for pods not running dikastes", func() {
		var service *v1.Service
		var podServer *v1.Pod

		BeforeEach(func() {

			// Create Server with Service
			By("Creating a server which support GET/PUT and does not run dikastes.")
			podServer, service = createGetPutPodAndService(f, f.Namespace, "server", 2379, nil)
			framework.Logf("Waiting for Server to come up.")
			err := framework.WaitForPodRunningInNamespace(f.ClientSet, podServer)
			Expect(err).NotTo(HaveOccurred())

			By("Creating client which will be able to GET/PUT the server since no policies are present.")
			testIstioCanGetPut(f, f.Namespace, http.MethodGet, service, podServer, nil)
			testIstioCanGetPut(f, f.Namespace, http.MethodPut, service, podServer, nil)
		})
		AfterEach(func() {
			cleanupServerPodAndService(f, podServer, service)
			calicoctl.Cleanup()
		})
		It("should ignore policy with http method rule", func() {

			By("creating policy which allow only GET")
			gnp := `
- apiVersion: projectcalico.org/v3
  kind: GlobalNetworkPolicy
  metadata:
    name: http-method
  spec:
    selector: pod-name == "server"
    ingress:
      - action: Allow
        http:
          methods: ["GET"]
    egress:
      - action: Allow
`
			calicoctl.Apply(gnp)
			defer calicoctl.DeleteGNP("http-method")

			By("testing http method with pod using default service account, allow Get")
			testIstioCanGetPut(f, f.Namespace, http.MethodGet, service, podServer, nil)

			By("testing http method with pod using default service account, allow Put")
			testIstioCanGetPut(f, f.Namespace, http.MethodPut, service, podServer, nil)

		})
		It("should ignore policy with both http method and service account rule", func() {
			By("creating \"connect\" service account")
			connect := alp.CreateServiceAccount(f, "connect", f.Namespace.Name, map[string]string{"connect": "true"})
			defer alp.DeleteServiceAccount(f, connect)

			By("creating policy which allow only GET with service account \"connect\"")
			gnp := `
- apiVersion: projectcalico.org/v3
  kind: GlobalNetworkPolicy
  metadata:
    name: http-sa
  spec:
    selector: pod-name == "server"
    ingress:
      - action: Allow
        http:
          methods: ["GET"]
        source:
          serviceAccounts:
            names: ["connect"]
    egress:
      - action: Allow
`
			calicoctl.Apply(gnp)
			defer calicoctl.DeleteGNP("http-sa")

			By("allow client with service account \"connect\" to GET")
			testIstioCanGetPut(f, f.Namespace, http.MethodGet, service, podServer, connect)
			By("allow client with service account \"connect\" to PUT as the policy should be ignored")
			testIstioCanGetPut(f, f.Namespace, http.MethodPut, service, podServer, connect)

		})
	})
})

// createIstioServerPodAndService works just like createServerPodAndService(), but with some Istio specific tweaks.
func createIstioServerPodAndService(f *framework.Framework, namespace *v1.Namespace, podName string, ports []int, labels map[string]string) (*v1.Pod, *v1.Service) {
	pod, service := createServerPodAndServiceX(f, namespace, podName, ports,
		func(pod *v1.Pod) {
			// Apply labels.
			for k, v := range labels {
				pod.Labels[k] = v
			}

			oldContainers := pod.Spec.Containers
			pod.Spec.Containers = []v1.Container{}
			for _, container := range oldContainers {
				// Strip out readiness probe because Istio doesn't support HTTP health probes when in mTLS mode.
				container.ReadinessProbe = nil
				pod.Spec.Containers = append(pod.Spec.Containers, container)
			}
		},
		func(svc *v1.Service) {
			oldPorts := svc.Spec.Ports
			svc.Spec.Ports = []v1.ServicePort{}
			for _, port := range oldPorts {
				// Istio requires service ports to be named <protocol>[-<suffix>]
				port.Name = fmt.Sprintf("http-%d", port.Port)
				svc.Spec.Ports = append(svc.Spec.Ports, port)
			}
		},
	)

	alp.VerifyContainersForPod(pod)

	return pod, service
}

// createIstioGetPutPodAndService works just like createServerPodAndService(), but with some Istio specific tweaks.
func createIstioGetPutPodAndService(f *framework.Framework, namespace *v1.Namespace, podName string, port int, labels map[string]string) (*v1.Pod, *v1.Service) {
	pod, service := createGetPutPodAndService(f, namespace, podName, port, labels)

        // Verify sidecar injection for pods.
        alp.VerifyContainersForPod(pod)

	return pod, service
}

// createGetPutPodAndService works just like createServerPodAndService(), with some istio specific tweaks.
func createGetPutPodAndService(f *framework.Framework, namespace *v1.Namespace, podName string, port int, labels map[string]string) (*v1.Pod, *v1.Service) {
	pod, service := createServerPodAndServiceX(f, namespace, podName, []int{port},
		func(pod *v1.Pod) {
			// Apply labels.
			for k, v := range labels {
				pod.Labels[k] = v
			}

			oldContainers := pod.Spec.Containers
			pod.Spec.Containers = []v1.Container{}
			for _, container := range oldContainers {
				// Strip out readiness probe because Istio doesn't support HTTP health probes when in mTLS mode.
				container.ReadinessProbe = nil
				container.Image = "quay.io/coreos/etcd:v2.2.0"
				container.Args = []string{
					"-advertise-client-urls",
					fmt.Sprintf("http://svc-get-put:%d", port),
					"-listen-client-urls",
					fmt.Sprintf("http://0.0.0.0:%d", port),
				}

				pod.Spec.Containers = append(pod.Spec.Containers, container)
			}
		},
		func(svc *v1.Service) {
			oldPorts := svc.Spec.Ports
			svc.Spec.Ports = []v1.ServicePort{}
			for _, port := range oldPorts {
				// Istio requires service ports to be named <protocol>[-<suffix>]
				port.Name = fmt.Sprintf("http-%d", port.Port)
				svc.Spec.Ports = append(svc.Spec.Ports, port)
			}
			svc.Name = "svc-get-put"
		},
	)

	return pod, service
}

// testIstioCanConnect works like testCanConnect(), but takes the target Pod for diagnostics, and an optional Service
// Account for the probe pod.
func testIstioCanConnect(f *framework.Framework, ns *v1.Namespace, podName string, service *v1.Service, targetPort int, targetPod *v1.Pod, account *v1.ServiceAccount) {
	testIstioCanConnectX(f, ns, podName, service, targetPort, targetPod, func(pod *v1.Pod) {
		if account != nil {
			pod.Spec.ServiceAccountName = account.Name
		}
	})
}

// testIstioCanConnectX works like testCanConnectX(), but has Istio specific tweaks and diagnostics.
func testIstioCanConnectX(f *framework.Framework, ns *v1.Namespace, podName string, service *v1.Service, targetPort int, targetPod *v1.Pod, podCustomizer func(pod *v1.Pod)) {
	By(fmt.Sprintf("Creating client pod %s that should successfully connect to %s.", podName, service.Name))

	// Make sure we do not have pod with same name which is still terminating from previous call to this function.
	// This is required because there are still chances that a client pod with same name is still exist
	// in same namespace. (see below defer function).
	err := alp.WaitForPodNotFoundInNamespace(f, ns, podName)
	if err != nil {
		framework.Failf("pod %q was not deleted: %v", podName, err)
	}

	pc := alp.WrapPodCustomizerIncreaseRetries(podCustomizer)
	target := fmt.Sprintf("%s.%s:%d", service.Name, service.Namespace, targetPort)
	podClient := createNetworkClientPodX(f, ns, podName, target, pc)
	containerName := podClient.Spec.Containers[0].Name
	defer func() {
		// Deferring deleting client pod after test is done.
		// Note it only makes API call to delete the pod and there are good chances pod is still terminating
		// after this function returns. This approach (not waiting for cleanup) is faster because most of the time,
		// we would not create client pod with same name right after previous test. However, if we really need to do
		// that, alp.WaitForPodNodeFoundInNamespace is called (see above) to make sure the previous client pod get
		// properly terminated before a new one is going to be created.

		By(fmt.Sprintf("Cleaning up the pod %s", podName))
		if err := f.ClientSet.CoreV1().Pods(ns.Name).Delete(podClient.Name, nil); err != nil {
			framework.Failf("unable to cleanup pod %v: %v", podClient.Name, err)
		}
	}()

	alp.VerifyContainersForPod(podClient)

	// Istio injects proxy sidecars into the pod, and these sidecars do not exit when the main probe container finishes.
	// So, we can't use WaitForPodSuccessInNamespace to wait for the probe to finish. Instead, we use
	// WaitForContainerSuccess which just waits for a specific container in the pod to finish.
	framework.Logf("Waiting for %s to complete.", podClient.Name)
	err = alp.WaitForContainerSuccess(f.ClientSet, podClient, containerName)
	if err != nil {
		framework.Logf("Client container was not successful %v", err)

		diags := alp.GetProbeAndTargetDiags(f, podClient, targetPod, containerName)

		framework.Failf("Pod %s should be able to connect to service %s, but was not able to connect.%s",
			podName, service.Name, diags)

		// Dump debug information for the test namespace.
		framework.DumpDebugInfo(f.ClientSet, f.Namespace.Name)
	}
}

// testIstioCannotConnect works like testCannotConnect(), but the target pod for diagnostics and an optional service
// account.
func testIstioCannotConnect(f *framework.Framework, ns *v1.Namespace, podName string, service *v1.Service, targetPort int, targetPod *v1.Pod, account *v1.ServiceAccount) {
	testIstioCannotConnectX(f, ns, podName, service, targetPort, targetPod, func(pod *v1.Pod) {
		if account != nil {
			pod.Spec.ServiceAccountName = account.Name
		}
	})
}

// testIstioCannotConnectX works like testCannotConnectX(), but has Istio specific tweaks.
func testIstioCannotConnectX(f *framework.Framework, ns *v1.Namespace, podName string, service *v1.Service, targetPort int, targetPod *v1.Pod, podCustomizer func(pod *v1.Pod)) {
	By(fmt.Sprintf("Creating client pod %s that should not be able to connect to %s.", podName, service.Name))

	// Make sure we do not have pod with same name which is still terminating previous call to this function.
	// This is required because there are still chances that a client pod with same name is still exist
	// in same namespace. (see below defer function).
	err := alp.WaitForPodNotFoundInNamespace(f, ns, podName)
	if err != nil {
		framework.Failf("pod %q was not deleted: %v", podName, err)
	}

	pc := alp.WrapPodCustomizerIncreaseRetries(podCustomizer)
	target := fmt.Sprintf("%s.%s:%d", service.Name, service.Namespace, targetPort)
	podClient := createNetworkClientPodX(f, ns, podName, target, pc)
	containerName := podClient.Spec.Containers[0].Name
	defer func() {
		// Deferring deleting client pod after test is done.
		// Note it only makes API call to delete the pod and there are good chances pod is still terminating
		// after this function returns. This approach (not waiting for cleanup) is faster because most of the time,
		// we would not create client pod with same name right after previous test. However, if we really need to do
		// that, alp.WaitForPodNodeFoundInNamespace is called (see above) to make sure the previous client pod get
		// properly terminated before a new one is going to be created.

		By(fmt.Sprintf("Cleaning up the pod %s", podName))
		if err := f.ClientSet.CoreV1().Pods(ns.Name).Delete(podClient.Name, nil); err != nil {
			framework.Failf("unable to cleanup pod %v: %v", podClient.Name, err)
		}
	}()

	alp.VerifyContainersForPod(podClient)

	// Istio injects proxy sidecars into the pod, and these sidecars do not exit when the main probe container finishes.
	// So, we can't use WaitForPodSuccessInNamespace to wait for the probe to finish. Instead, we use
	// WaitForContainerSuccess which just waits for a specific container in the pod to finish.
	framework.Logf("Waiting for pod <%s> to complete by checking container <%s> .", podClient.Name, containerName)
	err = alp.WaitForContainerSuccess(f.ClientSet, podClient, containerName)

	// We expect an error here since it's a cannot connect test.
	// Dump debug information if the error was nil.
	if err == nil {
		// Get logs from the target, both Dikastes and the proxy (Envoy)
		diags := alp.GetProbeAndTargetDiags(f, podClient, targetPod, containerName)

		framework.Failf("Pod %s should not be able to connect to service %s, but was able to connect.%s",
			podName, service.Name, diags)

		// Dump debug information for the test namespace.
		framework.DumpDebugInfo(f.ClientSet, f.Namespace.Name)
	}
}

func testIstioGetPutCmd(service *v1.Service, method string) (string, string) {
	var cmd string
	var expect string
	port := service.Spec.Ports[0].Port

	// Setup retry. Each retry max timeout 5 seconds. Total timeout 50 seconds.
	retryArgs := fmt.Sprintf("--connect-timeout 3 --max-time 5 --retry %d --retry-delay 0 --retry-max-time 50 --retry-connrefused",
		alp.NumberOfRetries)

	switch method {
	case http.MethodGet:
		cmd = fmt.Sprintf("curl %s -v http://%s:%d/v2/keys?recursive=true", retryArgs, service.Name, port)
		expect = `"action":"get"`
	case http.MethodPut:
		cmd = fmt.Sprintf("curl %s -k -v http://%s:%d/v2/keys/accounts/519940/balance -d value='20000.00' -XPUT",
			retryArgs, service.Name, port)
		expect = `"action":"set"`

	default:
		framework.Failf("Unknown http method <%s>", method)
		return "", ""
	}

	return cmd, expect
}

func testIstioCanGetPut(f *framework.Framework, ns *v1.Namespace, method string, service *v1.Service, targetPod *v1.Pod, account *v1.ServiceAccount) {
	cmd, expect := testIstioGetPutCmd(service, method)

	clientPod, output, err := calico.ExecuteCmdInPodX(f, cmd, func(pod *v1.Pod) {
		// Do not use same pod name for hostexec pod.
		// This is to work around cni-plugin issue https://github.com/projectcalico/cni-plugin/issues/515
		pod.Name = fmt.Sprintf("%s%s", "getput-", utilrand.String(5))
		pod.Spec.HostNetwork = false
		pod.Spec.RestartPolicy = v1.RestartPolicyNever

		if account != nil {
			pod.Spec.ServiceAccountName = account.Name
		}
	})
	Expect(clientPod).NotTo(BeNil())

	defer func() {
		// Clean up the pod
		f.PodClient().Delete(clientPod.Name, metav1.NewDeleteOptions(0))
		err := framework.WaitForPodToDisappear(f.ClientSet, f.Namespace.Name, clientPod.Name, labelutils.Everything(), time.Second, wait.ForeverTestTimeout)
		if err != nil {
			framework.Failf("Failed to delete %s pod: %v", clientPod.Name, err)
		}
	}()

	if err != nil || !strings.Contains(output, expect) {

		framework.Logf("Execution of cmd <%s> was not successful. response: %s, error: %v", cmd, output, err)

		containerName := clientPod.Spec.Containers[0].Name
		diags := alp.GetProbeAndTargetDiags(f, clientPod, targetPod, containerName)

		framework.Failf("Pod %s should be able to http <%s> service %s, but was not able to connect.%s",
			clientPod.Name, method, service.Name, diags)

		// Dump debug information for the test namespace.
		framework.DumpDebugInfo(f.ClientSet, f.Namespace.Name)
		return
	}
	framework.Logf("Curl cmd <%s> returns successfully with response %#v", cmd, output)
}

func testIstioCannotGetPut(f *framework.Framework, ns *v1.Namespace, method string, service *v1.Service, targetPod *v1.Pod, account *v1.ServiceAccount) {
	cmd, expect := testIstioGetPutCmd(service, method)

	clientPod, output, err := calico.ExecuteCmdInPodX(f, cmd, func(pod *v1.Pod) {
		pod.Name = fmt.Sprintf("%s%s", "getput-", utilrand.String(5))
		pod.Spec.HostNetwork = false
		pod.Spec.RestartPolicy = v1.RestartPolicyNever

		if account != nil {
			pod.Spec.ServiceAccountName = account.Name
		}
	})
	Expect(clientPod).NotTo(BeNil())

	defer func() {
		// Clean up the pod
		f.PodClient().Delete(clientPod.Name, metav1.NewDeleteOptions(0))
		err := framework.WaitForPodToDisappear(f.ClientSet, f.Namespace.Name, clientPod.Name, labelutils.Everything(), time.Second, wait.ForeverTestTimeout)
		if err != nil {
			framework.Failf("Failed to delete %s pod: %v", clientPod.Name, err)
		}
	}()

	// We are testing CannotGetPut. Execution of command should get no error but without a valid response.
	// Log error if not.
	if err != nil || strings.Contains(output, expect) {
		framework.Logf("Execution of cmd <%s> was successful. response: %s, error: %v", cmd, output, err)

		containerName := clientPod.Spec.Containers[0].Name
		diags := alp.GetProbeAndTargetDiags(f, clientPod, targetPod, containerName)

		framework.Failf("Pod %s should not be able to http <%s> service %s, but was able to connect.%s",
			clientPod.Name, method, service.Name, diags)

		// Dump debug information for the test namespace.
		framework.DumpDebugInfo(f.ClientSet, f.Namespace.Name)
		return
	}
}
