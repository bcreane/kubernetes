package network

import (
	"fmt"

	"k8s.io/api/core/v1"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// This is NOT a real e2e test - it is a test which always fails, to facilitate testing of e2e diags collection, etc.
var _ = SIGDescribe("[Feature:AlwaysFail]", func() {
	var calicoctl *calico.Calicoctl
	var service *v1.Service
	var podServer *v1.Pod
	f := framework.NewDefaultFramework("always-fail")

	BeforeEach(func() {
		// The following code tries to get config information for calicoctl from k8s ConfigMap.
		// A framework clientset is needed to access k8s configmap but it will only be created in the context of BeforeEach or IT.
		// Current solution is to use BeforeEach because this function is not a test case.
		// This will avoid complexity of creating a client by ourself.
		calicoctl = calico.ConfigureCalicoctl(f)
	})

	PContext("always-fail-context", func() {
		BeforeEach(func() {
			// Create Server with Service
			By("always-fail By 1")
			podServer, service = createServerPodAndService(f, f.Namespace, "server", []int{80})
			framework.Logf("Waiting for Server to come up.")
			err := framework.WaitForPodRunningInNamespace(f.ClientSet, podServer)
			Expect(err).NotTo(HaveOccurred())

			By("always-fail By 2")
			testCanConnect(f, f.Namespace, "client-can-connect", service, 80)

		})

		AfterEach(func() {
			calicoctl.Cleanup()
			cleanupServerPodAndService(f, podServer, service)
		})

		It("always-fail It 1", func() {
			err := fmt.Errorf("always-fail error 1")
			Expect(err).NotTo(HaveOccurred())
		})
		It("always-fail It 2", func() {
			err := fmt.Errorf("always-fail error 2")
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
