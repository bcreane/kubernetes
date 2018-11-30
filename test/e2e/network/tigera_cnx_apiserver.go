package network

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ghodss/yaml"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"
)

var _ = SIGDescribe("[Feature:CNX-APIServer]", func() {
	var kubectl *calico.Kubectl
	var jsonData interface{}

	testCNXCRUDAPI := func(resName, resKind, resYamlFile, namespace, patch string, resConfig *yamlConfig) {
		// Test kubectl can 'Create' the CNX resource 		
		By(fmt.Sprintf("Checking kubectl command can create a %s %s", resName, resConfig.Name))
		resManifest := calico.ReadTestFileOrDie(resYamlFile, resConfig)
		error := kubectl.Create(resManifest, namespace, "")
		Expect(error).NotTo(HaveOccurred())

		// Test kubectl can 'Get' the CNX resource with different output options
		By(fmt.Sprintf("Checking kubectl command can get a %s %s", resName, resConfig.Name))
		output, err := kubectl.Get(resKind, namespace, resConfig.Name, "", "", "", false)
		Expect(err).NotTo(HaveOccurred())
		getName := strings.Fields(strings.Split(output, "\n")[1])[0]
		Expect(getName).To(Equal(resConfig.Name))

		By(fmt.Sprintf("Checking kubectl command can get a %s %s in json output format", resName, resConfig.Name))
		output, err = kubectl.Get(resKind, namespace, resConfig.Name, "", "json", "", false)
		Expect(err).NotTo(HaveOccurred())
		err = json.Unmarshal([]byte(output), &jsonData)
		getName = jsonData.(map[string]interface{})["metadata"].(map[string]interface{})["name"].(string)
		Expect(getName).To(Equal(resConfig.Name))

		By(fmt.Sprintf("Checking kubectl command can get a %s %s in yaml output format", resName, resConfig.Name))
		output, err = kubectl.Get(resKind, namespace, resConfig.Name, "", "yaml", "", false)
		Expect(err).NotTo(HaveOccurred())
		err = yaml.Unmarshal([]byte(output), &jsonData)
		getName = jsonData.(map[string]interface{})["metadata"].(map[string]interface{})["name"].(string)
		Expect(getName).To(Equal(resConfig.Name))

		// Test kubectl can 'Update' the CNX resource
		By(fmt.Sprintf("Checking kubectl command can update a %s %s", resName, resConfig.Name))
		err = kubectl.Patch(resKind, namespace, resConfig.Name, "", patch)
		Expect(err).NotTo(HaveOccurred())

		// Test kubectl can 'Delete' the CNX resource
		By(fmt.Sprintf("Checking kubectl command can delete a %s %s", resName, resConfig.Name))
		err = kubectl.Delete(resKind, namespace, resConfig.Name, "")
		Expect(err).NotTo(HaveOccurred())
	}

	Context("Test CNX API server with 'Tier' and 'GlobalNetworkSet'", func() {
		var (
			tierConfig, gnsConfig *yamlConfig
		)

		// Get a 'kubectl' client
		kubectl = &calico.Kubectl{}

		It("Test the CRUD operations on 'Tier' using kubectl command", func() {
			// Create a Tier
			tierConfig = &yamlConfig{
				Name: createName("cnx-api-test-tier"),
			}
			patch := "{\"spec\":{\"order\":90.0}}"

			testCNXCRUDAPI("Tier", "tier.p", "cnx-tier-1.yaml", "", patch, tierConfig)
		})

		It("Test the CRUD operations on 'GlobalNetworkSet' using kubectl command", func() {
			// Create a GlobalNetworkSet
			gnsConfig = &yamlConfig{
				Name: createName("cnx-api-test-gns"),
			}
			patch := "{\"metadata\":{\"labels\":{\"app\":\"frontend\"}}}"

			testCNXCRUDAPI("GlobalNetworkSet", "globalnetworkset.p", "cnx-gns-1.yaml", "", patch, gnsConfig)
		})
	})

	Context("Test CNX API server with 'NetworkPolicy' and 'GlobalNetworkPolicy'", func() {
		var (
			tierConfig, npConfig, gnpConfig *yamlConfig
			testNamespace                   string
		)

		// Get a framework object with a new namespace for testing
		var f = framework.NewDefaultFramework("cnx-apiserver")

		BeforeEach(func() {
			testNamespace = f.Namespace.Name
			kubectl = &calico.Kubectl{}

			// Create a Tier
			tierConfig = &yamlConfig{
				Name: createName("cnx-api-test-tier"),
			}
			tier := calico.ReadTestFileOrDie("cnx-tier-1.yaml", tierConfig)
			kubectl.Create(tier, "", "")
		})

		AfterEach(func() {
			kubectl.Delete("tier.p", "", tierConfig.Name, "")
		})

		It("Test the CRUD operations on 'NetworkPolicy' using kubectl command", func() {
			// Create a NetworkPolicy
			npConfig = &yamlConfig{
				Name:     fmt.Sprintf("%s.cnx-api-test-np", tierConfig.Name),
				TierName: tierConfig.Name,
			}
			patch := "{\"spec\":{\"order\":90.0}}"

			testCNXCRUDAPI("NetworkPolicy", "networkpolicy.p", "cnx-np-1.yaml", "", patch, npConfig)
		})

		It("Test the CRUD operations on 'GlobalNetworkPolicy' using kubectl command", func() {
			// Create a GlobalNetworkPolicy within the Tier
			gnpConfig = &yamlConfig{
				Name:     fmt.Sprintf("%s.cnx-api-test-gnp", tierConfig.Name),
				TierName: tierConfig.Name,
			}
			patch := "{\"spec\":{\"order\":90.0}}"

			testCNXCRUDAPI("GlobalNetworkPolicy", "globalnetworkpolicy.p", "cnx-gnp-1.yaml", "", patch, gnpConfig)
		})
	})
})

