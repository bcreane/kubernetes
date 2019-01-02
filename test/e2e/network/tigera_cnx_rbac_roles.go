package network

import (
	"fmt"
	"time"

	"github.com/ghodss/yaml"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/api/rbac/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"
)

type clusterRoleBindConfigStruct struct {
	Name            string
	UserName        string
	ClusterRoleName string
}

// These tests check that CNX RBAC works.
var _ = SIGDescribe("[Feature:CNX-v3-RBAC]", func() {
	var f = framework.NewDefaultFramework("cnx-rbac")
	var (
		kubectl *calico.Kubectl
	)
	Context("Test CNX RBAC", func() {
		type oracleKey struct {
			action, object, tier string
		}
		var (
			clusterRoleBindConfig                                      *clusterRoleBindConfigStruct
			tierConfig, gnpConfig, npConfig, npDefConfig, gnpDefConfig *yamlConfig
			testNameSpace                                              string
		)

		BeforeEach(func() {
			testNameSpace = f.Namespace.Name
			kubectl = &calico.Kubectl{}
			// Bind test user to "test" cluster role (which will be created in the tests themselves)
			clusterRoleBindConfig = &clusterRoleBindConfigStruct{
				Name:            "test",
				UserName:        "testuser",
				ClusterRoleName: "test",
			}
			clusterRoleBind := calico.ReadTestFileOrDie("cnx-clusterrolebinding.yaml", clusterRoleBindConfig)
			kubectl.Apply(clusterRoleBind, "", "")

			// Create Policies within default Tier
			npDefConfig = &yamlConfig{
				Name:     "default.e2e-test-np",
				TierName: "default",
			}
			np := calico.ReadTestFileOrDie("cnx-np-1.yaml", npDefConfig)
			kubectl.Apply(np, testNameSpace, "")

			// Create GlobalNetworkPolicy within Tier
			gnpDefConfig = &yamlConfig{
				Name:     "default.e2e-test-gnp",
				TierName: "default",
			}
			gnp := calico.ReadTestFileOrDie("cnx-gnp-1.yaml", gnpDefConfig)
			kubectl.Apply(gnp, "", "")

			// Create Tier
			tierConfig = &yamlConfig{
				Name: createName("e2e-test-tier"),
			}
			tier := calico.ReadTestFileOrDie("cnx-tier-1.yaml", tierConfig)
			kubectl.Apply(tier, "", "")

			// Create Policies within Tier
			npConfig = &yamlConfig{
				Name:     fmt.Sprintf("%s.e2e-test-np", tierConfig.Name),
				TierName: tierConfig.Name,
			}
			np = calico.ReadTestFileOrDie("cnx-np-1.yaml", npConfig)
			kubectl.Apply(np, testNameSpace, "")

			// Create GlobalNetworkPolicy within Tier
			gnpConfig = &yamlConfig{
				Name:     fmt.Sprintf("%s.e2e-test-gnp", tierConfig.Name),
				TierName: tierConfig.Name,
			}
			gnp = calico.ReadTestFileOrDie("cnx-gnp-1.yaml", gnpConfig)
			kubectl.Apply(gnp, "", "")

			// Allow fully permissive access to the pseudo Calico tiered policy resource types. This ensures
			// CNX v2.2+ behaves like older versions.
			permissiveRBAC := calico.ReadTestFileOrDie("cnx-rbac-permissive-tieredpolicy.yaml", clusterRoleBindConfig)
			kubectl.Apply(permissiveRBAC, "", "")
		})

		AfterEach(func() {
			kubectl.Delete("globalnetworkpolicy.p", "", gnpConfig.Name, "")
			kubectl.Delete("globalnetworkpolicy.p", "", "default.e2e-test-gnp", "")
			kubectl.Delete("networkpolicy.p", testNameSpace, npConfig.Name, "")
			kubectl.Delete("networkpolicy.p", testNameSpace, "default.e2e-test-np", "")
			time.Sleep(3 * time.Second)
			kubectl.Delete("tier.p", "", "test-tier2", "")
			kubectl.Delete("tier.p", "", tierConfig.Name, "")
			kubectl.Delete("clusterrolebinding", "", clusterRoleBindConfig.Name, "")
		})

		applyObject := func(object interface{}, user string) {
			// Convert to yaml text to apply it via kubectl
			bs, err := yaml.Marshal(object)
			Expect(err).NotTo(HaveOccurred())
			yamlString := string(bs)
			kubectl.Apply(yamlString, "", "")
		}

		consultOracle := func(oracle map[oracleKey]bool, action string, object string, tier string, user string, err error) {
			if oracle[oracleKey{action, object, tier}] {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(HaveOccurred())
			}
		}
		testCNXRBAC := func(clusterRoleStruct interface{}, user string, tiers []string, nps map[string]string, gnps map[string]string, oracle map[oracleKey]bool) {
			applyObject(clusterRoleStruct, "")
			for _, tier := range tiers {
				// fish out the NP and GNP associated with the tier under test
				np := nps[tier]
				gnp := gnps[tier]

				// NP tests
				By(fmt.Sprintf("Checking user: %s can get networkpolicy %s in tier %s", user, np, tier))
				_, err := kubectl.Get("networkpolicy.p", testNameSpace, np, "", "yaml", user, false)
				consultOracle(oracle, "get", "np", tier, user, err)

				By(fmt.Sprintf("Checking user: %s can watch networkpolicy in tier %s", user, tier))
				_, err = kubectl.Get("networkpolicy.p", testNameSpace, np, "", "yaml", user, true)
				consultOracle(oracle, "watch", "np", tier, user, err)

				By(fmt.Sprintf("Checking user: %s can list networkpolicy in tier %s", user, tier))
				_, err = kubectl.Get("networkpolicy.p", testNameSpace, "", fmt.Sprintf("projectcalico.org/tier==%s", tier), "yaml", user, false)
				consultOracle(oracle, "list", "np", tier, user, err)

				By(fmt.Sprintf("Checking user: %s can update networkpolicy in tier %s", user, tier))
				npConfig2 := &yamlConfig{
					Name:     np,
					TierName: tier,
				}
				np2 := calico.ReadTestFileOrDie("cnx-np-2.yaml", npConfig2)
				err = kubectl.Apply(np2, testNameSpace, user)
				consultOracle(oracle, "update", "np", tier, user, err)

				By(fmt.Sprintf("Checking user: %s can create networkpolicy in tier %s", user, tier))
				npConfig3 := &yamlConfig{
					Name:     fmt.Sprintf("%s.e2e-test-np2", tier),
					TierName: tier,
				}
				np3 := calico.ReadTestFileOrDie("cnx-np-1.yaml", npConfig3)
				err = kubectl.Create(np3, testNameSpace, user)
				consultOracle(oracle, "create", "np", tier, user, err)
				// And now clean it up...
				_ = kubectl.Delete("networkpolicy.p", testNameSpace, npConfig3.Name, "")

				By(fmt.Sprintf("Checking user: %s can patch networkpolicy in tier %s", user, tier))
				patch := "{\"spec\":{\"order\":100.0}}"
				err = kubectl.Patch("networkpolicy.p", testNameSpace, np, user, patch)
				consultOracle(oracle, "patch", "np", tier, user, err)

				By(fmt.Sprintf("Checking user: %s can delete networkpolicy in tier %s", user, tier))
				err = kubectl.Delete("networkpolicy.p", testNameSpace, np, user)
				consultOracle(oracle, "delete", "np", tier, user, err)

				// GNP tests
				By(fmt.Sprintf("Checking user: %s can get GNP %s in tier %s", user, gnp, tier))
				_, err = kubectl.Get("globalnetworkpolicy.p", "", gnp, "", "yaml", user, false)
				consultOracle(oracle, "get", "gnp", tier, user, err)

				By(fmt.Sprintf("Checking user: %s can watch GNP in tier %s", user, tier))
				_, err = kubectl.Get("globalnetworkpolicy.p", "", gnp, "", "yaml", user, true)
				consultOracle(oracle, "watch", "gnp", tier, user, err)

				By(fmt.Sprintf("Checking user: %s can list GNP in tier %s", user, tier))
				_, err = kubectl.Get("globalnetworkpolicy.p", "", "", fmt.Sprintf("projectcalico.org/tier==%s", tier), "yaml", user, false)
				consultOracle(oracle, "list", "gnp", tier, user, err)

				By(fmt.Sprintf("Checking user: %s can update GNP in tier %s", user, tier))
				gnpConfig2 := &yamlConfig{
					Name:     gnp,
					TierName: tier,
				}
				gnp2 := calico.ReadTestFileOrDie("cnx-gnp-2.yaml", gnpConfig2)
				err = kubectl.Apply(gnp2, "", user)
				consultOracle(oracle, "update", "gnp", tier, user, err)

				By(fmt.Sprintf("Checking user: %s can create GNP in tier %s", user, tier))
				gnpConfig3 := &yamlConfig{
					Name:     fmt.Sprintf("%s.e2e-test-gnp2", tier),
					TierName: tier,
				}
				gnp3 := calico.ReadTestFileOrDie("cnx-gnp-1.yaml", gnpConfig3)
				err = kubectl.Create(gnp3, "", user)
				consultOracle(oracle, "create", "gnp", tier, user, err)
				// And now clean it up...
				_ = kubectl.Delete("globalnetworkpolicy.p", "", gnpConfig3.Name, "")

				By(fmt.Sprintf("Checking user: %s can patch GNP in tier %s", user, tier))
				patch = "{\"spec\":{\"order\":100.0}}"
				err = kubectl.Patch("globalnetworkpolicy.p", "", gnp, user, patch)
				consultOracle(oracle, "patch", "gnp", tier, user, err)

				By(fmt.Sprintf("Checking user: %s can delete GNP in tier %s", user, tier))
				err = kubectl.Delete("globalnetworkpolicy.p", "", gnp, user)
				consultOracle(oracle, "delete", "gnp", tier, user, err)

				// Tier tests
				By(fmt.Sprintf("Checking user: %s can get Tier %s", user, tier))
				_, err = kubectl.Get("tier.p", "", tier, "", "yaml", user, false)
				consultOracle(oracle, "get", "tier", tier, user, err)

				By(fmt.Sprintf("Checking user: %s can watch Tier %s", user, tier))
				_, err = kubectl.Get("tier.p", "", tier, "", "yaml", user, true)
				consultOracle(oracle, "watch", "tier", tier, user, err)

				By(fmt.Sprintf("Checking user: %s can list Tier %s", user, tier))
				_, err = kubectl.Get("tier.p", "", "", "", "yaml", user, false)
				consultOracle(oracle, "list", "tier", tier, user, err)

				By(fmt.Sprintf("Checking user: %s can update Tier %s", user, tier))
				tierConfig2 := &yamlConfig{
					Name: tier,
				}
				tier2 := calico.ReadTestFileOrDie("cnx-tier-2.yaml", tierConfig2)
				err = kubectl.Apply(tier2, "", user)
				consultOracle(oracle, "update", "tier", tier, user, err)

				By(fmt.Sprintf("Checking user: %s can create Tier %s", user, tier))
				tierConfig3 := &yamlConfig{
					Name: "test-tier2",
				}
				tier3 := calico.ReadTestFileOrDie("cnx-tier-1.yaml", tierConfig3)
				err = kubectl.Create(tier3, "", user)
				consultOracle(oracle, "create", "tier", tier, user, err)
				// And now clean it up...
				_ = kubectl.Delete("tier.p", "", tierConfig3.Name, "")

				By(fmt.Sprintf("Checking user: %s can patch Tier %s", user, tier))
				patch = "{\"spec\":{\"order\":150.0}}"
				err = kubectl.Patch("tier.p", "", tier, user, patch)
				consultOracle(oracle, "patch", "tier", tier, user, err)

				By(fmt.Sprintf("Checking user: %s can delete Tier %s", user, tier))
				// Empty the tier first using the admin user (you can only delete an empty tier)
				_ = kubectl.Delete("networkpolicy.p", testNameSpace, np, "")
				_ = kubectl.Delete("globalnetworkpolicy.p", "", gnp, "")
				time.Sleep(3 * time.Second)
				// Now do the 'real' test
				err = kubectl.Delete("tier.p", "", tier, user)
				consultOracle(oracle, "delete", "tier", tier, user, err)
			}
		}

		It("allows 'admin' to do everything", func() {
			clusterRoleStruct := &v1beta1.ClusterRole{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ClusterRole",
					APIVersion: "rbac.authorization.k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Rules: []v1beta1.PolicyRule{},
			}

			tiers := []string{"default", tierConfig.Name}
			np := map[string]string{
				npConfig.TierName:    npConfig.Name,
				npDefConfig.TierName: npDefConfig.Name,
			}
			gnp := map[string]string{
				gnpConfig.TierName:    gnpConfig.Name,
				gnpDefConfig.TierName: gnpDefConfig.Name,
			}
			actions := []string{
				"get",
				"watch",
				"list",
				"update",
				"create",
				"patch",
				"delete",
			}
			objects := []string{
				"np",
				"gnp",
				"tier",
			}
			//  Set expected results for the admin user
			oracle := make(map[oracleKey]bool)
			for _, action := range actions {
				for _, object := range objects {
					for _, tier := range tiers {
						// admin user can do everything, except...
						value := true
						if object == "tier" && tier == "default" {
							// There are some things you cannot do to the default tier
							switch action {
							case "update":
								value = false
							case "patch":
								value = false
							case "delete":
								value = false
							}
						}
						oracle[oracleKey{action, object, tier}] = value
					}
				}
			}

			testCNXRBAC(clusterRoleStruct, "", tiers, np, gnp, oracle)
		})
		It("allows 'nouser' to do nothing", func() {
			clusterRoleStruct := &v1beta1.ClusterRole{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ClusterRole",
					APIVersion: "rbac.authorization.k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Rules: []v1beta1.PolicyRule{},
			}

			tiers := []string{"default", tierConfig.Name}
			np := map[string]string{
				npConfig.TierName:    npConfig.Name,
				npDefConfig.TierName: npDefConfig.Name,
			}
			gnp := map[string]string{
				gnpConfig.TierName:    gnpConfig.Name,
				gnpDefConfig.TierName: gnpDefConfig.Name,
			}
			oracle := make(map[oracleKey]bool)
			testCNXRBAC(clusterRoleStruct, "nouser", tiers, np, gnp, oracle)
		})

		It("allows testuser to get/list NPs when permitted", func() {
			clusterRoleStruct := &v1beta1.ClusterRole{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ClusterRole",
					APIVersion: "rbac.authorization.k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Rules: []v1beta1.PolicyRule{
					{
						APIGroups:     []string{"projectcalico.org"},
						Resources:     []string{"tiers"},
						ResourceNames: []string{"default"},
						Verbs:         []string{"get"},
					},
					{
						APIGroups: []string{"projectcalico.org"},
						Resources: []string{"networkpolicies"},
						Verbs:     []string{"get", "list"},
					},
				},
			}
			tiers := []string{"default", tierConfig.Name}
			np := map[string]string{
				npConfig.TierName:    npConfig.Name,
				npDefConfig.TierName: npDefConfig.Name,
			}
			gnp := map[string]string{
				gnpConfig.TierName:    gnpConfig.Name,
				gnpDefConfig.TierName: gnpDefConfig.Name,
			}
			oracle := map[oracleKey]bool{
				oracleKey{"list", "np", "default"}:  true,
				oracleKey{"get", "np", "default"}:   true,
				oracleKey{"get", "tier", "default"}: true,
			}
			testCNXRBAC(clusterRoleStruct, "testuser", tiers, np, gnp, oracle)
		})
		It("allows testuser to update/create/patch/delete NPs in non-default tier when permitted", func() {
			clusterRoleStruct := &v1beta1.ClusterRole{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ClusterRole",
					APIVersion: "rbac.authorization.k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Rules: []v1beta1.PolicyRule{
					{
						APIGroups: []string{"projectcalico.org"},
						Resources: []string{"networkpolicies"},
						Verbs:     []string{"get", "update", "create", "patch", "delete"},
					},
					{
						APIGroups:     []string{"projectcalico.org"},
						Resources:     []string{"tiers"},
						ResourceNames: []string{tierConfig.Name},
						Verbs:         []string{"get"},
					},
				},
			}
			tiers := []string{"default", tierConfig.Name}
			np := map[string]string{
				npConfig.TierName:    npConfig.Name,
				npDefConfig.TierName: npDefConfig.Name,
			}
			gnp := map[string]string{
				gnpConfig.TierName:    gnpConfig.Name,
				gnpDefConfig.TierName: gnpDefConfig.Name,
			}
			oracle := map[oracleKey]bool{
				oracleKey{"get", "tier", tierConfig.Name}:  true,
				oracleKey{"get", "np", tierConfig.Name}:    true,
				oracleKey{"update", "np", tierConfig.Name}: true,
				oracleKey{"create", "np", tierConfig.Name}: true,
				oracleKey{"patch", "np", tierConfig.Name}:  true,
				oracleKey{"delete", "np", tierConfig.Name}: true,
			}
			testCNXRBAC(clusterRoleStruct, "testuser", tiers, np, gnp, oracle)
		})
		It("allows testuser to update/create/patch/delete NPs in default tier when permitted", func() {
			clusterRoleStruct := &v1beta1.ClusterRole{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ClusterRole",
					APIVersion: "rbac.authorization.k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Rules: []v1beta1.PolicyRule{
					{
						APIGroups: []string{"projectcalico.org"},
						Resources: []string{"networkpolicies"},
						Verbs:     []string{"get", "update", "create", "patch", "delete"},
					},
					{
						APIGroups:     []string{"projectcalico.org"},
						Resources:     []string{"tiers"},
						ResourceNames: []string{"default"},
						Verbs:         []string{"get"},
					},
				},
			}
			tiers := []string{"default", tierConfig.Name}
			np := map[string]string{
				npConfig.TierName:    npConfig.Name,
				npDefConfig.TierName: npDefConfig.Name,
			}
			gnp := map[string]string{
				gnpConfig.TierName:    gnpConfig.Name,
				gnpDefConfig.TierName: gnpDefConfig.Name,
			}
			oracle := map[oracleKey]bool{
				oracleKey{"get", "tier", "default"}:  true,
				oracleKey{"get", "np", "default"}:    true,
				oracleKey{"update", "np", "default"}: true,
				oracleKey{"create", "np", "default"}: true,
				oracleKey{"patch", "np", "default"}:  true,
				oracleKey{"delete", "np", "default"}: true,
			}
			testCNXRBAC(clusterRoleStruct, "testuser", tiers, np, gnp, oracle)
		})

		It("allows testuser to get/list GNPs when permitted", func() {
			clusterRoleStruct := &v1beta1.ClusterRole{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ClusterRole",
					APIVersion: "rbac.authorization.k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Rules: []v1beta1.PolicyRule{
					{
						APIGroups:     []string{"projectcalico.org"},
						Resources:     []string{"tiers"},
						ResourceNames: []string{"default"},
						Verbs:         []string{"get"},
					},
					{
						APIGroups: []string{"projectcalico.org"},
						Resources: []string{"globalnetworkpolicies"},
						Verbs:     []string{"get", "list"},
					},
				},
			}
			tiers := []string{"default", tierConfig.Name}
			np := map[string]string{
				npConfig.TierName:    npConfig.Name,
				npDefConfig.TierName: npDefConfig.Name,
			}
			gnp := map[string]string{
				gnpConfig.TierName:    gnpConfig.Name,
				gnpDefConfig.TierName: gnpDefConfig.Name,
			}
			oracle := map[oracleKey]bool{
				oracleKey{"list", "gnp", "default"}: true,
				oracleKey{"get", "gnp", "default"}:  true,
				oracleKey{"get", "tier", "default"}: true,
			}
			testCNXRBAC(clusterRoleStruct, "testuser", tiers, np, gnp, oracle)
		})

		It("allows testuser to update/create/patch/delete GNPs in non-default tier when permitted", func() {
			clusterRoleStruct := &v1beta1.ClusterRole{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ClusterRole",
					APIVersion: "rbac.authorization.k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Rules: []v1beta1.PolicyRule{
					{
						APIGroups: []string{"projectcalico.org"},
						Resources: []string{"globalnetworkpolicies"},
						Verbs:     []string{"get", "update", "create", "patch", "delete"},
					},
					{
						APIGroups:     []string{"projectcalico.org"},
						Resources:     []string{"tiers"},
						ResourceNames: []string{tierConfig.Name},
						Verbs:         []string{"get"},
					},
				},
			}
			tiers := []string{"default", tierConfig.Name}
			np := map[string]string{
				npConfig.TierName:    npConfig.Name,
				npDefConfig.TierName: npDefConfig.Name,
			}
			gnp := map[string]string{
				gnpConfig.TierName:    gnpConfig.Name,
				gnpDefConfig.TierName: gnpDefConfig.Name,
			}
			oracle := map[oracleKey]bool{
				oracleKey{"get", "tier", tierConfig.Name}:   true,
				oracleKey{"get", "gnp", tierConfig.Name}:    true,
				oracleKey{"update", "gnp", tierConfig.Name}: true,
				oracleKey{"create", "gnp", tierConfig.Name}: true,
				oracleKey{"patch", "gnp", tierConfig.Name}:  true,
				oracleKey{"delete", "gnp", tierConfig.Name}: true,
			}
			testCNXRBAC(clusterRoleStruct, "testuser", tiers, np, gnp, oracle)
		})

		It("allows testuser to update/create/patch/delete GNPs in default tier when permitted", func() {
			clusterRoleStruct := &v1beta1.ClusterRole{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ClusterRole",
					APIVersion: "rbac.authorization.k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Rules: []v1beta1.PolicyRule{
					{
						APIGroups: []string{"projectcalico.org"},
						Resources: []string{"globalnetworkpolicies"},
						Verbs:     []string{"get", "update", "create", "patch", "delete"},
					},
					{
						APIGroups:     []string{"projectcalico.org"},
						Resources:     []string{"tiers"},
						ResourceNames: []string{"default"},
						Verbs:         []string{"get"},
					},
				},
			}
			tiers := []string{"default", tierConfig.Name}
			np := map[string]string{
				npConfig.TierName:    npConfig.Name,
				npDefConfig.TierName: npDefConfig.Name,
			}
			gnp := map[string]string{
				gnpConfig.TierName:    gnpConfig.Name,
				gnpDefConfig.TierName: gnpDefConfig.Name,
			}
			oracle := map[oracleKey]bool{
				oracleKey{"get", "tier", "default"}:   true,
				oracleKey{"get", "gnp", "default"}:    true,
				oracleKey{"update", "gnp", "default"}: true,
				oracleKey{"create", "gnp", "default"}: true,
				oracleKey{"patch", "gnp", "default"}:  true,
				oracleKey{"delete", "gnp", "default"}: true,
			}
			testCNXRBAC(clusterRoleStruct, "testuser", tiers, np, gnp, oracle)
		})

		It("allows testuser to get/list tiers when permitted", func() {
			clusterRoleStruct := &v1beta1.ClusterRole{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ClusterRole",
					APIVersion: "rbac.authorization.k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Rules: []v1beta1.PolicyRule{
					{
						APIGroups: []string{"projectcalico.org"},
						Resources: []string{"tiers"},
						Verbs:     []string{"get", "list"},
					},
				},
			}
			tiers := []string{"default", tierConfig.Name}
			np := map[string]string{
				npConfig.TierName:    npConfig.Name,
				npDefConfig.TierName: npDefConfig.Name,
			}
			gnp := map[string]string{
				gnpConfig.TierName:    gnpConfig.Name,
				gnpDefConfig.TierName: gnpDefConfig.Name,
			}
			oracle := map[oracleKey]bool{
				oracleKey{"get", "tier", "default"}:        true,
				oracleKey{"list", "tier", "default"}:       true,
				oracleKey{"get", "tier", tierConfig.Name}:  true,
				oracleKey{"list", "tier", tierConfig.Name}: true,
			}
			testCNXRBAC(clusterRoleStruct, "testuser", tiers, np, gnp, oracle)
		})

		It("allows testuser to update/create/patch/delete tiers when permitted", func() {
			clusterRoleStruct := &v1beta1.ClusterRole{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ClusterRole",
					APIVersion: "rbac.authorization.k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Rules: []v1beta1.PolicyRule{
					{
						APIGroups: []string{"projectcalico.org"},
						Resources: []string{"tiers"},
						Verbs:     []string{"get", "update", "create", "patch", "delete"},
					},
				},
			}
			tiers := []string{"default", tierConfig.Name}
			np := map[string]string{
				npConfig.TierName:    npConfig.Name,
				npDefConfig.TierName: npDefConfig.Name,
			}
			gnp := map[string]string{
				gnpConfig.TierName:    gnpConfig.Name,
				gnpDefConfig.TierName: gnpDefConfig.Name,
			}
			oracle := map[oracleKey]bool{
				oracleKey{"get", "tier", "default"}:    true,
				oracleKey{"create", "tier", "default"}: true,
				// You can't edit default tier, so no expectation of that working.
				oracleKey{"get", "tier", tierConfig.Name}:    true,
				oracleKey{"update", "tier", tierConfig.Name}: true,
				oracleKey{"create", "tier", tierConfig.Name}: true,
				oracleKey{"patch", "tier", tierConfig.Name}:  true,
				oracleKey{"delete", "tier", tierConfig.Name}: true,
			}
			testCNXRBAC(clusterRoleStruct, "testuser", tiers, np, gnp, oracle)
		})
	})
})

