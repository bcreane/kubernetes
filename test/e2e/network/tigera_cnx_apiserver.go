package network

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ghodss/yaml"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"
)

var _ = SIGDescribe("[Feature:CNX-APIServer]", func() {
	var f = framework.NewDefaultFramework("cnx-apiserver")
	var kubectl *testKubectlCNXAPI

	Context("Test CNX API server with 'Tier'", func() {
		var (
			tierConfig *yamlConfig
			tierName   string
		)

		BeforeEach(func() {
			kubectl = &testKubectlCNXAPI{}
			tierName = "cnx-test-tier"
		})

		AfterEach(func() {
			kubectl.delete("tier.p", "", tierName)
		})

		It("creates a Tier using kubectl command", func() {
			By(fmt.Sprintf("Checking CNX API Server: kubectl command can create a Tier %s", tierName))
			tierConfig = &yamlConfig{
				Name: createName(tierName),
			}
			tier := calico.ReadTestFileOrDie("cnx-tier-1.yaml", tierConfig)
			err := kubectl.apply(tier, "")
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("Test CNX API server with 'NetworkPolicy'", func() {
		var (
			tierConfig, npConfig *yamlConfig
			testNameSpace        string
			jsonData             interface{}
		)

		BeforeEach(func() {
			testNameSpace = f.Namespace.Name
			kubectl = &testKubectlCNXAPI{}

			// Create Tier
			tierConfig = &yamlConfig{
				Name: createName("cnx-api-test-tier"),
			}
			tier := calico.ReadTestFileOrDie("cnx-tier-1.yaml", tierConfig)
			kubectl.apply(tier, "")

			// Create a NetworkPolicy within the Tier
			npConfig = &yamlConfig{
				Name:     fmt.Sprintf("%s.cnx-api-test-np", tierConfig.Name),
				TierName: tierConfig.Name,
			}
			np := calico.ReadTestFileOrDie("cnx-np-1.yaml", npConfig)
			kubectl.apply(np, testNameSpace)
		})

		AfterEach(func() {
			kubectl.delete("networkpolicy.p", testNameSpace, npConfig.Name)
			time.Sleep(3 * time.Second)
			kubectl.delete("tier.p", "", tierConfig.Name)
		})

		It("Get a NetworkPolicy using kubectl command", func() {
			By(fmt.Sprintf("Checking CNX API Server: kubectl command can get a NetworkPolicy %s in json output format", tierConfig.Name))
			output, err := kubectl.get("networkpolicy.p", testNameSpace, npConfig.Name, "", "json")
			Expect(err).NotTo(HaveOccurred())
			err = json.Unmarshal([]byte(output), &jsonData)
			npName := jsonData.(map[string]interface{})["metadata"].(map[string]interface{})["name"].(string)
			Expect(npName).To(Equal(npConfig.Name))

			By(fmt.Sprintf("Checking CNX API Server: kubectl command can get a NetworkPolicy %s in yaml output format", tierConfig.Name))
			output, err = kubectl.get("networkpolicy.p", testNameSpace, npConfig.Name, "", "yaml")
			Expect(err).NotTo(HaveOccurred())
			err = yaml.Unmarshal([]byte(output), &jsonData)
			npName = jsonData.(map[string]interface{})["metadata"].(map[string]interface{})["name"].(string)
			Expect(npName).To(Equal(npConfig.Name))
		})
	})
})

type testKubectlCNXAPI struct {
}

func (k *testKubectlCNXAPI) create(yaml string, ns string) error {
	options := []string{"create", "-f", "-"}
	if ns != "" {
		options = append(options, fmt.Sprintf("--namespace=%v", ns))
	}
	_, err := framework.NewKubectlCommand(options...).WithStdinData(yaml).Exec()
	return err
}

func (k *testKubectlCNXAPI) apply(yaml string, ns string) error {
	options := []string{"apply", "-f", "-"}
	if ns != "" {
		options = append(options, fmt.Sprintf("--namespace=%v", ns))
	}
	_, err := framework.NewKubectlCommand(options...).WithStdinData(yaml).Exec()
	return err
}

func (k *testKubectlCNXAPI) get(kind, ns, name string, label string, output_option string) (string, error) {
	options := []string{"get", kind, "-o", output_option}
	if name != "" {
		options = append(options, name)
	}
	if ns != "" {
		options = append(options, fmt.Sprintf("--namespace=%v", ns))
	}
	if label != "" {
		options = append(options, fmt.Sprintf("-l %s", label))
	}

	output, err := framework.NewKubectlCommand(options...).Exec()
	return output, err
}

func (k *testKubectlCNXAPI) delete(kind, ns, name string) error {
	options := []string{"delete", kind, name}
	if ns != "" {
		options = append(options, fmt.Sprintf("--namespace=%v", ns))
	}
	_, err := framework.NewKubectlCommand(options...).Exec()
	return err
}

