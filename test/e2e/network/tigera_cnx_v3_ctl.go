// Copyright (c) 2017 Tigera, Inc. All rights reserved.

package network

import (
	"fmt"
	"math"
	"strings"

	"github.com/ghodss/yaml"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"k8s.io/kubernetes/staging/src/k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"
)

// TODO (rlb):
// -  Check CNX only fields don't prevent OS from managing what would otherwise be a valid resource (in the event
//    the user goes back to open source).
// -  Check error message text in both calicoctl and kubectl
// -  Explicit test to make sure deleting a namespace deletes all associated policies automatically (only valid in
//    kubectl)

// yamlConfig contains fields that these tests substitute into the manifest files.
type yamlConfig struct {
	Name     string
	TierName string
	Label    string
}

// These tests check the compatibility between calicoctl and kubectl for the resources that may be managed
// through the CNX Aggregated API Server.
//
// These tests assume the kubernetes cluster is installed with CNX.
//
// There are known syntactical and output formatting differences between calicoctl and kubectl, however,
// for Calico specific data, the actual contents for each resource should be the same between CLIs. Also,
// the OS calicoctl omits the tier name from policy resource names. Whilst CNX can handle the OS names
// in the resource definitions for a create action but any queries made with CNX or Kubectl will return
// the name including the tier.
var _ = SIGDescribe("[Feature:CNX-v3-calicoctl] calicoctl and kubectl are compatible", func() {
	f := framework.NewDefaultFramework("calicoctl-kubectl")
	var (
		osCalicoctl, cnxCalicoctl, cnxKubectl testCLI
		testNamespace                         string
	)

	Context("Compatibility of Open source manifests with CNX", func() {

		BeforeEach(func() {
			By("Creating CLI resources")
			osCalicoctlOpts := calico.CalicoctlOptions{Image: framework.TestContext.CalicoCtlOpenSourceImage}
			cnxCalicoctl = &testCalicoctlCNX{calico.ConfigureCalicoctl(f)}
			osCalicoctl = &testCalicoctlOS{&testCalicoctlCNX{calico.ConfigureCalicoctl(f, osCalicoctlOpts)}}
			cnxKubectl = &testKubectlCNX{}
			testNamespace = f.Namespace.Name
		})

		AfterEach(func() {
			By("Cleaning up CLI resources")
			cnxCalicoctl.cleanup()
			osCalicoctl.cleanup()
			cnxKubectl.cleanup()
		})

		// testOSPolicyManifests tests that OpenSource manifests containing NP or GNP can be created or applied using
		// CNX calicoctl or kubectl.
		//
		// Known limitations:
		// -  kubectl can only handle a missing tier in the policy name during creation of a resource
		// -  kubectl manifest policy name must include the tier when updating an existing resource
		// -  kubectl requires the tier in the policy name when specified on the CLI
		testOSPolicyManifests := func(kind string, manifestName string) func() {
			return func() {
				// Create the config to substitute into the supplied manifest and then load the manifest.
				// Since these will be OpenSource manifests, the name will not include the tier prefix.
				policyConfig := &yamlConfig{
					Name: createName("e2e-" + strings.ToLower(kind)),
				}
				manifest := calico.ReadTestFileOrDie(manifestName, policyConfig)
				cnxPolicyName := "default." + policyConfig.Name

				// Construct a friendly name for the test resource and determine the namespace to pass to the CLI
				// (we only pass the test namespace for the resources that are namespaced).
				var resource, resourceNamespace string
				switch kind {
				case "NetworkPolicy":
					resource = kind + "(" + testNamespace + "/" + policyConfig.Name + ")"
					resourceNamespace = testNamespace
				case "GlobalNetworkPolicy":
					resource = kind + "(" + policyConfig.Name + ")"
				default:
					panic("These tests are for NP and GNP only")
				}

				// Make sure we tidy up the resources we create. This will fail if the test runs to completion, so
				// ignore any failures here.
				defer osCalicoctl.delete(kind, resourceNamespace, policyConfig.Name)

				// Create the resource using OpenSource calicoctl, get the settings. Note that osCalicoctl
				// verifies that the tier name is not in the policy settings and then modifies the policy
				// settings to include the tier name - this makes comparison with the CNX CLIs easier.
				By("Using " + osCalicoctl.name() + " to create the resource in manifest " + manifestName)
				osCalicoctl.create(manifest, resourceNamespace)

				By("Using " + osCalicoctl.name() + " to get the settings of " + resource)
				osSettings := osCalicoctl.get(kind, resourceNamespace, policyConfig.Name)

				// Check the resource looks the same with CNX calicoctl and kubectl. Note that cnxKubectl
				// removes the selfLink metadata field so that it is easier to compare with calicoctl.
				By("Using " + cnxCalicoctl.name() + " to get the settings of " + resource + " to compare with OpenSource")
				Expect(cnxCalicoctl.get(kind, resourceNamespace, policyConfig.Name)).To(Equal(osSettings))

				By("Using " + cnxKubectl.name() + " to get the settings of " + resource + " to compare with OpenSource")
				Expect(cnxKubectl.get(kind, resourceNamespace, cnxPolicyName)).To(Equal(osSettings))

				// Delete the resource using the open source calicoctl.
				By("Using " + osCalicoctl.name() + " to delete the settings of " + resource)
				err := osCalicoctl.delete(kind, resourceNamespace, policyConfig.Name)
				Expect(err).NotTo(HaveOccurred())

				// Compare with the two CNX CLIs.
				for _, cli := range []testCLI{cnxCalicoctl, cnxKubectl} {
					// Create the resource using a different CLI .
					By("Using " + cli.name() + " to create the resource in manifest " + manifestName)
					cli.create(manifest, resourceNamespace)

					// Check that the settings match across CLIs and that the key settings match those from
					// the open source creation.
					By("Using " + osCalicoctl.name() + " to get the settings of " + resource)
					newOsSettings := osCalicoctl.get(kind, resourceNamespace, policyConfig.Name)

					By("Using " + cnxCalicoctl.name() + " to get the settings of " + resource + " to compare with OpenSource")
					Expect(cnxCalicoctl.get(kind, resourceNamespace, policyConfig.Name)).To(Equal(newOsSettings))

					By("Using " + cnxKubectl.name() + " to get the settings of " + resource + " to compare with OpenSource")
					Expect(cnxKubectl.get(kind, resourceNamespace, cnxPolicyName)).To(Equal(newOsSettings))

					// Compare cleaned versions of the OS settings - this actually checks that the resource
					// settings are actually the same as created by OpenSource and CNX.
					osSettings.RemoveInstanceData()
					newOsSettings.RemoveInstanceData()
					Expect(osSettings).To(Equal(newOsSettings))

					// Check it can be deleted by the new CLI. Note that we prepend the "default" tier for this.
					// If the AAPIS starts supporting other resource types supported by the OS calicoctl, we'll
					// need to be more selective about this name tweaking, and only do it for policy resource types.
					err = cli.delete(kind, resourceNamespace, cnxPolicyName)
					Expect(err).NotTo(HaveOccurred())

					// Open source and CNX should no longer see the resource.
					By("Using " + osCalicoctl.name() + " to check the resource is deleted")
					Expect(osCalicoctl.exists(kind, resourceNamespace, policyConfig.Name)).To(BeFalse())

					By("Using " + cli.name() + " to check the resource is deleted")
					Expect(cli.exists(kind, resourceNamespace, cnxPolicyName)).To(BeFalse())
				}
			}
		}
		It("Checking NetworkPolicy can be managed with os-np-1.yaml", testOSPolicyManifests(
			"NetworkPolicy", "os-np-1.yaml",
		))
		It("Checking NetworkPolicy can be managed with os-np-2.yaml", testOSPolicyManifests(
			"NetworkPolicy", "os-np-2.yaml",
		))
		It("Checking GlobalNetworkPolicy can be managed with os-gnp-1.yaml", testOSPolicyManifests(
			"GlobalNetworkPolicy", "os-gnp-1.yaml",
		))
		It("Checking GlobalNetworkPolicy can be managed with os-gnp-2.yaml", testOSPolicyManifests(
			"GlobalNetworkPolicy", "os-gnp-2.yaml",
		))
	})

	Context("Compatibility of manageable configuration using calicoctl and kubectl", func() {

		var tierConfig *yamlConfig

		BeforeEach(func() {
			By("Creating CLI resources")
			cnxCalicoctl = &testCalicoctlCNX{calico.ConfigureCalicoctl(f)}
			cnxKubectl = &testKubectlCNX{}
			testNamespace = f.Namespace.Name

			// Create a tier which will be substituted into the policy specific yaml tests.
			By("Creating testing-tier")
			tierConfig = &yamlConfig{
				Name: createName("e2e-test-tier"),
			}
			tier := calico.ReadTestFileOrDie("cnx-tier-1.yaml", tierConfig)
			cnxCalicoctl.apply(tier, "")
		})

		AfterEach(func() {
			// Delete the tier created for the tests.
			By("Deleting testing-tier")
			cnxCalicoctl.delete("Tier", "", tierConfig.Name)

			By("Cleaning up CLI resources")
			cnxCalicoctl.cleanup()
			cnxKubectl.cleanup()
		})

		// testCNXCLIs runs through some CNX specific manifests and checks both calicoctl and kubectl configure
		// from the manifests in the same way.
		testCNXCLIs := func(kind, manifestName string) func() {
			return func() {
				// Construct a name and a friendly name for the test resource and determine the namespace to pass
				// to the CLI (we only pass the test namespace for the resources that are namespaced).
				var name, resource, resourceNamespace string
				switch kind {
				case "NetworkPolicy":
					name = tierConfig.Name + "." + createName(strings.ToLower(kind))
					resource = kind + "(" + testNamespace + "/" + name + ")"
					resourceNamespace = testNamespace
				case "GlobalNetworkPolicy":
					name = tierConfig.Name + "." + createName(strings.ToLower(kind))
					resource = kind + "(" + name + ")"
				case "Tier":
					name = createName("e2e-tier")
					resource = kind + "(" + name + ")"
				case "GlobalNetworkSet":
					name = createName("e2e-globalnetworkset")
					resource = kind + "(" + name + ")"
				default:
					panic("These tests are for CNX configuration only")
				}

				// Read in the supplied manifest substiuting the name and tier name as appropriate.
				resourceConfig := &yamlConfig{
					Name:     name,
					TierName: tierConfig.Name,
				}
				manifest := calico.ReadTestFileOrDie(manifestName, resourceConfig)

				// Make sure we tidy up the resources we create. This will fail if the test runs to completion, so
				// ignore any failures here.
				defer cnxCalicoctl.delete(kind, resourceNamespace, name)

				// Create the resource using CNX calicoctl (regarded as the "source of truth")
				By("Using " + cnxCalicoctl.name() + " to create the resource in manifest " + manifestName)
				cnxCalicoctl.create(manifest, resourceNamespace)
				By("Using " + cnxCalicoctl.name() + " to get the settings of " + resource)
				cnxSettings := cnxCalicoctl.get(kind, resourceNamespace, name)

				// Check the resource looks the same with CNX calicoctl and kubectl. Note that cnxKubectl
				// removes the selfLink metadata field so that it is easier to compare with calicoctl.
				By("Using " + cnxKubectl.name() + " to get the settings of " + resource + " to compare with " + cnxCalicoctl.name())
				Expect(cnxKubectl.get(kind, resourceNamespace, name)).To(Equal(cnxSettings))

				// Delete the resource using calicoctl.
				By("Using " + cnxCalicoctl.name() + " to delete the settings of " + resource)
				err := cnxCalicoctl.delete(kind, resourceNamespace, name)
				Expect(err).NotTo(HaveOccurred())

				// Compare with the result of creating through kubectl.
				By("Using " + cnxKubectl.name() + " to create the resource in manifest " + manifestName)
				cnxKubectl.create(manifest, resourceNamespace)

				// Check that the settings match across CLIs and that the key settings match those from
				// the open source creation.
				By("Using " + cnxKubectl.name() + " to get the settings of " + resource)
				newCnxSettings := cnxKubectl.get(kind, resourceNamespace, name)

				By("Using " + cnxCalicoctl.name() + " to get the settings of " + resource + " to compare with " + cnxKubectl.name())
				Expect(cnxCalicoctl.get(kind, resourceNamespace, name)).To(Equal(newCnxSettings))

				// Compare cleaned versions of the CNX settings - this actually checks that the resource
				// settings were configured the same in both CLIs
				cnxSettings.RemoveInstanceData()
				newCnxSettings.RemoveInstanceData()
				Expect(cnxSettings).To(Equal(newCnxSettings))

				// Check it can be deleted by the new CLI. Note that we pre-pend the "default" tier for this.
				// If the AAPIS starts supporting other resource types we'll need to be more selective about
				// this name tweaking, and only do it for policy resource types.
				err = cnxKubectl.delete(kind, resourceNamespace, name)
				Expect(err).NotTo(HaveOccurred())

				// Neither calicoctl nor kubectl should see the resource anymore.
				Expect(cnxCalicoctl.exists(kind, resourceNamespace, name)).To(BeFalse())
				Expect(cnxKubectl.exists(kind, resourceNamespace, name)).To(BeFalse())
			}
		}
		It("Checking NetworkPolicy can be managed with cnx-tier-1.yaml", testCNXCLIs(
			"Tier", "cnx-tier-1.yaml",
		))
		It("Checking Tier can be managed with cnx-tier-2.yaml", testCNXCLIs(
			"Tier", "cnx-tier-2.yaml",
		))
		It("Checking Tier can be managed with cnx-tier-3.yaml", testCNXCLIs(
			"Tier", "cnx-tier-3.yaml",
		))
		It("Checking GlobalNetworkPolicy can be managed with cnx-gnp-1.yaml", testCNXCLIs(
			"GlobalNetworkPolicy", "cnx-gnp-1.yaml",
		))
		It("Checking GlobalNetworkPolicy can be managed with cnx-gnp-2.yaml", testCNXCLIs(
			"GlobalNetworkPolicy", "cnx-gnp-2.yaml",
		))
		It("Checking NetworkPolicy can be managed with cnx-np-1.yaml", testCNXCLIs(
			"NetworkPolicy", "cnx-np-1.yaml",
		))
		It("Checking NetworkPolicy can be managed with cnx-np-2.yaml", testCNXCLIs(
			"NetworkPolicy", "cnx-np-2.yaml",
		))
		It("Checking GlobalNetworkSet can be managed with cnx-gns-1.yaml", testCNXCLIs(
			"GlobalNetworkSet", "cnx-gns-1.yaml",
		))
	})

	Context("Kubernetes policy is read-only through calicoctl and kubectl", func() {

		var k8sPolicyName string
		var calicoPolicyName string

		BeforeEach(func() {
			By("Creating CLI resources")
			cnxCalicoctl = &testCalicoctlCNX{calico.ConfigureCalicoctl(f)}
			cnxKubectl = &testKubectlCNX{}
			testNamespace = f.Namespace.Name

			// Create an arbitrary name for the policy, and substitute into the kubernetes policy
			// manifest.
			k8sPolicyName = createName("test.policy")
			calicoPolicyName = "knp.default." + k8sPolicyName
			manifest := calico.ReadTestFileOrDie("k8s-np-1.yaml", yamlConfig{Name: k8sPolicyName})

			By("Creating Kubernetes NetworkPolicy(" + testNamespace + "/" + k8sPolicyName + ")")
			cnxKubectl.create(manifest, testNamespace)

			// Wait for the policy to appear in calicoctl
			By("Waiting for Calico NetworkPolicy(" + testNamespace + "/" + calicoPolicyName + ") to be created")
			Eventually(func() bool {
				return cnxCalicoctl.exists("NetworkPolicy", testNamespace, calicoPolicyName)
			}, "30s", "100ms").Should(BeTrue())
		})

		AfterEach(func() {
			By("Cleaning up calicoctl resources")
			cnxCalicoctl.cleanup()
			cnxKubectl.cleanup()

			// Delete the kubernetes policy.
			By("Deleting kubernetes policy")
			framework.NewKubectlCommand(
				"delete", "NetworkPolicy", k8sPolicyName, fmt.Sprintf("--namespace=%v", testNamespace),
			).Exec()
		})

		It("Verify calicoctl and kubectl can view the Calico kubernetes-backed policy", func() {
			c := cnxCalicoctl.get("NetworkPolicy", testNamespace, calicoPolicyName)
			k := cnxKubectl.get("NetworkPolicy", testNamespace, calicoPolicyName)
			Expect(c).To(Equal(k))
		})

		It("Verify calicoctl cannot edit or delete the Calico kubernetes-backed policy", func() {
			res := cnxCalicoctl.get("NetworkPolicy", testNamespace, calicoPolicyName)
			res.AddLabel("testkey", "testvalue")
			cnxCalicoctl.replaceExpectError(res.String(), testNamespace)
			err := cnxCalicoctl.delete("NetworkPolicy", testNamespace, calicoPolicyName)
			Expect(err).To(HaveOccurred())
		})

		It("Verify kubectl cannot edit or delete the Calico kubernetes-backed policy", func() {
			res := cnxKubectl.get("NetworkPolicy", testNamespace, calicoPolicyName)
			res.AddLabel("testkey", "testvalue")
			cnxKubectl.replaceExpectError(res.String(), testNamespace)
			err := cnxKubectl.delete("NetworkPolicy", testNamespace, calicoPolicyName)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("Cannot create a knp. prefixed NetworkPolicy using kubectl", func() {

		BeforeEach(func() {
			By("Creating CLI resources")
			cnxKubectl = &testKubectlCNX{}
			testNamespace = f.Namespace.Name
		})

		It("Verify kubectl can create and replace a NetworkPolicy with non-'knp.' prefix", func() {
			config := yamlConfig{
				Name:     createName("default.foobarbaz"),
				TierName: "default",
			}
			manifest := calico.ReadTestFileOrDie("cnx-np-1.yaml", config)

			cnxKubectl.create(manifest, testNamespace)
			res := cnxKubectl.get("NetworkPolicy", testNamespace, config.Name)
			res.AddLabel("testkey", "testvalue")
			cnxKubectl.replace(res.String(), testNamespace)
		})

		It("Verify kubectl cannot create a NetworkPolicy with 'knp.' prefix", func() {
			config := yamlConfig{
				Name:     createName("knp.default.foobarbaz"),
				TierName: "default",
			}
			manifest := calico.ReadTestFileOrDie("cnx-np-1.yaml", config)

			cnxKubectl.createExpectError(manifest, testNamespace)
		})
	})

	Context("Invalid CNX configuration cannot be applied by calicoctl or kubectl", func() {

		BeforeEach(func() {
			By("Creating CLI resources")
			cnxCalicoctl = &testCalicoctlCNX{calico.ConfigureCalicoctl(f)}
			cnxKubectl = &testKubectlCNX{}
			testNamespace = f.Namespace.Name
		})

		// testInvalid is used to check that a particular manifest cannot be created using either calicoctl
		// or kubectl.
		testInvalid := func(kind string, manifestName string) func() {
			return func() {
				// Construct a name for the test resource and determine the namespace to pass
				// to the CLI (we only pass the test namespace for the resources that are namespaced).
				var name, resourceNamespace string
				switch kind {
				case "NetworkPolicy":
					name = "default." + createName(strings.ToLower(kind))
					resourceNamespace = testNamespace
				case "GlobalNetworkPolicy":
					name = "default." + createName(strings.ToLower(kind))
				case "Tier":
					name = createName("e2e-tier")
				case "GlobalNetworkSet":
					name = createName("e2e-globalnetworkset")
				default:
					panic("These tests are for CNX configuration only")
				}

				// Read in the supplied manifest substituting the name and tier name as appropriate.
				resourceConfig := &yamlConfig{
					Name:     name,
					TierName: "default",
				}
				manifest := calico.ReadTestFileOrDie(manifestName, resourceConfig)

				// Make sure we tidy up the resources we create. This is a fail safe in case these tests fail (in
				// which case they'll inadvertently create a resource we weren't expecting).
				defer cnxCalicoctl.delete(kind, resourceNamespace, name)

				// Create the resource using OpenSource calicoctl - this should fail.
				By("Using " + cnxCalicoctl.name() + " to (fail to) create the resource in manifest " + manifestName)
				cnxCalicoctl.createExpectError(manifest, resourceNamespace)

				By("Using " + cnxKubectl.name() + " to (fail to) create the resource in manifest " + manifestName)
				cnxKubectl.createExpectError(manifest, resourceNamespace)
			}
		}
		It("Invalid Tier: name has a dot in it", testInvalid(
			"Tier", "cnx-tier-1-bad-namewithdot.yaml",
		))
		It("Invalid Tier: order is not a number", testInvalid(
			"Tier", "cnx-tier-1-bad-invalidorder.yaml",
		))
		It("Invalid GlobalNetworkPolicy: name has a dot in it", testInvalid(
			"GlobalNetworkPolicy", "cnx-gnp-1-bad-namewithdot.yaml",
		))
		It("Invalid NetworkPolicy: name has a dot in it", testInvalid(
			"NetworkPolicy", "cnx-np-1-bad-namewithdot.yaml",
		))
		It("Invalid GlobalNetworkPolicy: invalid combination of PreDNAT, DoNotTrack, ApplyOnForward and Types", testInvalid(
			"GlobalNetworkPolicy", "cnx-gnp-1-bad-dntpdaof.yaml",
		))
		//TODO: See Jira CNX-2394
		PIt("Invalid NetworkPolicy: has GlobalNetworkPolicy only fields (PreDNAT, DoNotTrack, ApplyOnForward)", testInvalid(
			"NetworkPolicy", "cnx-np-1-bad-gnpfields.yaml",
		))
		It("Invalid GlobalNetworkPolicy: invalid action", testInvalid(
			"GlobalNetworkPolicy", "cnx-gnp-1-bad-invalidaction.yaml",
		))
		It("Invalid NetworkPolicy: invalid action", testInvalid(
			"NetworkPolicy", "cnx-np-1-bad-invalidaction.yaml",
		))
		It("Invalid GlobalNetworkPolicy: invalid types field", testInvalid(
			"GlobalNetworkPolicy", "cnx-gnp-1-bad-invalidtypes.yaml",
		))
		It("Invalid GlobalNetworkSet: invalid subnet", testInvalid(
			"GlobalNetworkSet", "cnx-gns-1-bad-subnet.yaml",
		))
	})
})

// ResourceData is a generic map representation of a resource.
type ResourceData map[string]interface{}

// Kind returns the resource kind.
func (r ResourceData) Kind() string {
	k, ok := r["kind"].(string)
	Expect(ok).To(BeTrue())
	return k
}

// Metadata returns the resource metadata as a generic map representation.
func (r ResourceData) Metadata() map[string]interface{} {
	m, ok := r["metadata"].(map[string]interface{})
	Expect(ok).To(BeTrue())
	return m
}

// Spec returns the resource spec as a generic map representation.
func (r ResourceData) Spec() map[string]interface{} {
	s, ok := r["spec"].(map[string]interface{})
	Expect(ok).To(BeTrue())
	return s
}

// String creates a YAML string representation of the resource.
func (r ResourceData) String() string {
	b, err := yaml.Marshal(r)
	Expect(err).NotTo(HaveOccurred())
	return string(b)
}

// FromString populates the resource data from the supplied YAML string.
func (r ResourceData) FromString(s string) ResourceData {
	err := yaml.Unmarshal([]byte(s), &r)
	Expect(err).NotTo(HaveOccurred())
	return r
}

// RemoveInstanceData removes the instance-specific data from the resource. This makes comparison of the same
// resource (but created at different times) to be compared.
func (r ResourceData) RemoveInstanceData() {
	m, ok := r["metadata"].(map[string]interface{})
	Expect(ok).To(BeTrue())
	delete(m, "uid")
	delete(m, "resourceVersion")
	delete(m, "creationTimestamp")
}

// AddLabel addes a key/value to the metadata labels.
func (r ResourceData) AddLabel(key, value string) {
	meta, ok := r["metadata"].(map[string]interface{})
	Expect(ok).To(BeTrue())
	labels, ok := meta["labels"].(map[string]interface{})
	if !ok {
		labels = make(map[string]interface{}, 0)
		meta["labels"] = labels
	}
	labels[key] = value
}

// testCLI is an interface used for testing kubectl and calicoctl CLIs. It is a simple
// interface to handle minor syntactical differences and to modify the data to ensure
// the data can be compared between OS and CNX. This isn't a more generic interface simply
// because this wrapper is only really useful when comaring one CLI against another; for
// tests using a specific CLI, the behaviour of that CLI should be known, and not worked
// around in the tests.
type testCLI interface {
	create(yaml string, ns string)
	createExpectError(yaml string, ns string)
	apply(yaml string, ns string)
	replace(yaml string, ns string)
	replaceExpectError(yaml string, ns string)
	get(kind, ns, name string) ResourceData
	exists(kind, ns, name string) bool
	delete(kind, ns, name string) error
	name() string
	cleanup()
}

// testCalicoctlCNX implements the testCLI interface for CNX calicoctl.
type testCalicoctlCNX struct {
	*calico.Calicoctl
}

func (c *testCalicoctlCNX) create(yaml string, ns string) {
	if ns == "" {
		c.Create(yaml)
	} else {
		c.Create(yaml, fmt.Sprintf("--namespace=%v", ns))
	}
}

func (c *testCalicoctlCNX) createExpectError(yaml string, ns string) {
	if ns == "" {
		err := c.CreateWithError(yaml)
		Expect(err).To(HaveOccurred())
	} else {
		err := c.CreateWithError(yaml, fmt.Sprintf("--namespace=%v", ns))
		Expect(err).To(HaveOccurred())
	}
}

func (c *testCalicoctlCNX) apply(yaml string, ns string) {
	if ns == "" {
		c.Apply(yaml)
	} else {
		c.Apply(yaml, fmt.Sprintf("--namespace=%v", ns))
	}
}

func (c *testCalicoctlCNX) replace(yaml string, ns string) {
	if ns == "" {
		c.Replace(yaml)
	} else {
		c.Replace(yaml, fmt.Sprintf("--namespace=%v", ns))
	}
}

func (c *testCalicoctlCNX) replaceExpectError(yaml string, ns string) {
	if ns == "" {
		err := c.ReplaceWithError(yaml)
		Expect(err).To(HaveOccurred())
	} else {
		err := c.ReplaceWithError(yaml, fmt.Sprintf("--namespace=%v", ns))
		Expect(err).To(HaveOccurred())
	}
}

func (c *testCalicoctlCNX) get(kind, ns, name string) ResourceData {
	var resp string
	if ns == "" {
		resp = c.Get(kind, name, "-o", "yaml")
	} else {
		resp = c.Get(kind, name, fmt.Sprintf("--namespace=%v", ns), "-o", "yaml")
	}
	return ResourceData{}.FromString(resp)
}

func (c *testCalicoctlCNX) exists(kind, ns, name string) bool {
	if ns == "" {
		_, err := c.ExecReturnError("get", kind, name)
		return err == nil
	} else {
		_, err := c.ExecReturnError("get", kind, name, fmt.Sprintf("--namespace=%v", ns))
		return err == nil
	}
}

func (c *testCalicoctlCNX) delete(kind, ns, name string) error {
	if ns == "" {
		_, err := c.ExecReturnError("delete", kind, name)
		return err
	} else {
		_, err := c.ExecReturnError("delete", kind, name, fmt.Sprintf("--namespace=%v", ns))
		return err
	}
}

func (c *testCalicoctlCNX) name() string {
	return "CNX calicoctl"
}

func (c *testCalicoctlCNX) cleanup() {
	c.Cleanup()
}

// testCalicoctlOS implements the testCLI interface for OpenSource calicoctl. It extends testCalicoctlCNX to
// inherit the basic functionality.
type testCalicoctlOS struct {
	*testCalicoctlCNX
}

// get is overridden to always include the default tier in a get response. This makes comparison with
// CNX and kubectl simpler.
func (c *testCalicoctlOS) get(kind, namespace, name string) ResourceData {
	res := c.testCalicoctlCNX.get(kind, namespace, name)
	if kind == "GlobalNetworkPolicy" || kind == "NetworkPolicy" {
		meta := res.Metadata()
		spec := res.Spec()

		// Sanity check this is an OS calicoctl by verifying the tier field is missing from the spec
		// and from the name. Note that we can't check that the tier label is not present because once
		// we modify the resource using CNX then that label will be inserted into the resource (even
		// for OpenSource).
		_, ok := spec["tier"].(string)
		Expect(ok).To(BeFalse(), "tier field in spec - this must be CNX calicoctl")
		name, ok = meta["name"].(string)
		Expect(ok).To(BeTrue())
		Expect(name).ToNot(MatchRegexp("default[.].*"), "tier included in name - this must be CNX calicoctl")

		// Modify the required fields to add the tier information.
		meta["name"] = "default." + name
		spec["tier"] = "default"
		res.AddLabel("projectcalico.org/tier", "default")
	}
	return res
}

func (c *testCalicoctlOS) name() string {
	return "OpenSource calicoctl"
}

// testKubectlCNX implements the testCLI interface for CNX kubectl (actually it is the same kubectl, but
// it assumes the deployment has a CNX AAPIS running which can handle Calico specific resources).
type testKubectlCNX struct{}

func (k *testKubectlCNX) create(yaml string, ns string) {
	if ns == "" {
		framework.RunKubectlOrDieInput(yaml, "create", "-f", "-")
	} else {
		framework.RunKubectlOrDieInput(yaml, "create", "-f", "-", fmt.Sprintf("--namespace=%v", ns))
	}
}

func (k *testKubectlCNX) createExpectError(yaml string, ns string) {
	if ns == "" {
		_, err := framework.NewKubectlCommand("create", "-f", "-").WithStdinData(yaml).Exec()
		Expect(err).To(HaveOccurred())
	} else {
		_, err := framework.NewKubectlCommand("create", "-f", "-", fmt.Sprintf("--namespace=%v", ns)).WithStdinData(yaml).Exec()
		Expect(err).To(HaveOccurred())
	}
}

func (k *testKubectlCNX) apply(yaml string, ns string) {
	if ns == "" {
		framework.RunKubectlOrDieInput(yaml, "apply", "-f", "-")
	} else {
		framework.RunKubectlOrDieInput(yaml, "apply", "-f", "-", fmt.Sprintf("--namespace=%v", ns))
	}
}

func (k *testKubectlCNX) replace(yaml string, ns string) {
	if ns == "" {
		framework.RunKubectlOrDieInput(yaml, "replace", "-f", "-")
	} else {
		framework.RunKubectlOrDieInput(yaml, "replace", "-f", "-", fmt.Sprintf("--namespace=%v", ns))
	}
}

func (k *testKubectlCNX) replaceExpectError(yaml string, ns string) {
	if ns == "" {
		_, err := framework.NewKubectlCommand("replace", "-f", "-").WithStdinData(yaml).Exec()
		Expect(err).To(HaveOccurred())
	} else {
		_, err := framework.NewKubectlCommand("replace", "-f", "-", fmt.Sprintf("--namespace=%v", ns)).WithStdinData(yaml).Exec()
		Expect(err).To(HaveOccurred())
	}
}

func (k *testKubectlCNX) get(kind, ns, name string) ResourceData {
	// Include the project calico api group in the kind string for kubectl.
	kind = kind + ".projectcalico.org"
	var resp string
	if ns == "" {
		resp = framework.RunKubectlOrDie("get", kind, name, "-o", "yaml")
	} else {
		resp = framework.RunKubectlOrDie("get", kind, name, fmt.Sprintf("--namespace=%v", ns), "-o", "yaml")
	}
	v := ResourceData{}.FromString(resp)

	// AAPIS fills in the selfLink which calicoctl will not, so remove it for ease of comparison.
	meta, ok := v["metadata"].(map[string]interface{})
	Expect(ok).To(BeTrue())
	delete(meta, "selfLink")

	return v
}

func (k *testKubectlCNX) exists(kind, ns, name string) bool {
	// Include the project calico api group in the kind string for kubectl.
	kind = kind + ".projectcalico.org"
	if ns == "" {
		_, err := framework.NewKubectlCommand("get", kind, name).Exec()
		return err == nil
	} else {
		_, err := framework.NewKubectlCommand("get", kind, name, fmt.Sprintf("--namespace=%v", ns)).Exec()
		return err == nil
	}
}

func (k *testKubectlCNX) delete(kind, ns, name string) error {
	// Include the project calico api group in the kind string for kubectl.
	kind = kind + ".projectcalico.org"
	if ns == "" {
		_, err := framework.NewKubectlCommand("delete", kind, name).Exec()
		return err
	} else {
		_, err := framework.NewKubectlCommand("delete", kind, name, fmt.Sprintf("--namespace=%v", ns)).Exec()
		return err
	}
}

func (k *testKubectlCNX) name() string {
	return "Kubectl"
}

func (k *testKubectlCNX) cleanup() {
	// no-op
}

// createName creates an arbitrary name with the supplied prefix. It simply appends a random number to the
// prefix. We don't need this to be super random - it's more a tool to handle poorly behaving tests that
// fail and don't clean up after themselves.
func createName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, rand.Int63nRange(0, math.MaxInt64))
}
