package network

import (
	"fmt"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/kubernetes/test/utils/calico"
)

var _ = SIGDescribe("[Feature:CNX-APIServer]", func() {
	var (
		kubectl  *testKubectlCNXRBAC
		tierName string
	)
	Context("Test CNX API server", func() {
		var tierConfig *yamlConfig

		BeforeEach(func() {
			kubectl = &testKubectlCNXRBAC{}
			tierName = "cnx-test-tier"
		})

		AfterEach(func() {
			kubectl.delete("tier.p", "", tierName, "")
		})

		It("creates a tier using kubectl command", func() {
			By(fmt.Sprintf("Checking CNX API Server: kubectl command can create a tier %s", tierName))
			tierConfig = &yamlConfig{
				Name: createName(tierName),
			}
			tier := calico.ReadTestFileOrDie("cnx-tier-1.yaml", tierConfig)
			err := kubectl.apply(tier, "", "")
			Expect(err).NotTo(HaveOccurred())
		})
	})

})

