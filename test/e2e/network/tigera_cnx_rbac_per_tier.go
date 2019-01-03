package network

import (
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"
)

type Verbs []string

var (
	VERBS_ALL = Verbs{"*"}
	// kubectl requires get access for write actions, so include it here.
	VERBS_WRITE      = Verbs{"create", "delete", "patch", "update", "get"}
	VERBS_READ       = Verbs{"get", "list", "watch"}
	VERBS_LIST_WATCH = Verbs{"list", "watch"}
	VERBS_GET        = Verbs{"get"}
)

type Action int

const (
	ACTION_CREATE Action = 1 << iota
	ACTION_DELETE
	ACTION_REPLACE
	ACTION_GET
	ACTION_LIST

	ACTIONS_ALL  = ACTION_CREATE | ACTION_DELETE | ACTION_REPLACE | ACTION_GET | ACTION_LIST
	ACTIONS_READ = ACTION_GET | ACTION_LIST
)

// perTierRbac is tiered policy Rbac configuration that is applied to the Rbac manifest.
type perTierRbac struct {
	// Do not include the pass-thru manifest used to provide all users with access to the raw
	// GNP and NP resource types.
	ExcludePassThruManifest bool

	// Verbs for the various tier, gnp and np sections of the cluster roles. This configures
	// the pseudo resource types introduced in v2.3.
	TierAll           Verbs
	TierDefault       Verbs
	Tier1             Verbs
	GnpAll            Verbs
	GnpTierDefaultAll Verbs
	GnpTier1All       Verbs
	Gnp               Verbs
	NpAll             Verbs
	NpTierDefaultAll  Verbs
	NpTier1All        Verbs
	Np                Verbs

	// Use cluster-scoped configuration for the NetworkPolicy resource.
	UseClusterScopedNetworkPolicy bool
}

type perTierRbacYAML struct {
	Namespace string
	Tier1     string
	Gnp       string
	Np        string
	User      string
	Rbac      perTierRbac
}

// perTierRbacTest is an individual tiered policy Rbac test case.
type perTierRbacTest struct {
	// tiered policy rbac configuration.``
	Rbac perTierRbac

	// Bit-wise set of actions that we expect to work for each resource type.
	TierDefault    Action
	Tier1          Action
	GnpTierDefault Action
	GnpTier1       Action
	NpTierDefault  Action
	NpTier1        Action
}

// These tests check that CNX per-tier policy RBAC works.
var _ = SIGDescribe("[Feature:CNX-v3-RBAC]", func() {
	var f = framework.NewDefaultFramework("cnx-per-tier-rbac")

	var (
		kubectl *calico.Kubectl
       )
	Context("[Feature:CNX-v3-RBAC-PerTier] Test CNX Per-Tier Policy RBAC", func() {
		var (
			testNamespace      string
			testUser           string
			testTier1          string
			testNpTierDefault  string
			testNpTier1        string
			testGnpTierDefault string
			testGnpTier1       string
		)

		BeforeEach(func() {
			testNamespace = f.Namespace.Name
			testUser = "user-" + utilrand.String(5)
			testTier1 = "tier1-" + utilrand.String(5)
			testNpTierDefault = "default.e2e-test-np-" + utilrand.String(5)
			testNpTier1 = testTier1 + ".e2e-test-np-" + utilrand.String(5)
			testGnpTierDefault = "default.e2e-test-gnp-" + utilrand.String(5)
			testGnpTier1 = testTier1 + ".e2e-test-gnp-" + utilrand.String(5)
		})

		AfterEach(func() {
			kubectl.Delete("globalnetworkpolicy.projectcalico.org", "", testGnpTierDefault, "")
			kubectl.Delete("globalnetworkpolicy.projectcalico.org", "", testGnpTier1, "")
			kubectl.Delete("networkpolicy.projectcalico.org", testNamespace, testNpTierDefault, "")
			kubectl.Delete("networkpolicy.projectcalico.org", testNamespace, testNpTier1, "")
			kubectl.Delete("clusterrolebinding", "", "ee-calico-tiered-policy-passthru", "")
			kubectl.Delete("clusterrole", "", "ee-calico-tiered-policy-passthru", "")
			kubectl.Delete("clusterrolebinding", "", "ee-calico-user-tiered-policy-tiers-and-gnp", "")
			kubectl.Delete("clusterrolebinding", "", "ee-calico-user-tiered-policy-np", "")
			kubectl.Delete("rolebinding", testNamespace, "ee-calico-user-tiered-policy-np", "")
			kubectl.Delete("clusterrole", "", "ee-calico-user-tiered-policy-tiers-and-gnp", "")
			kubectl.Delete("clusterrole", "", "ee-calico-user-tiered-policy-np", "")
			kubectl.Delete("tier.projectcalico.org", "", testTier1, "")
		})

		errorMessage := func(verb, kind, tier, ns string, canGetTier bool) string {
			var msg string
			switch tier {
			case "":
				// No tier, so these are the standard error messages.
				if ns != "" {
					msg = fmt.Sprintf("User %q cannot %s %s in namespace %q",
						testUser, verb, kind+".projectcalico.org", ns,
					)
				} else {
					msg = fmt.Sprintf("User %q cannot %s %s at the cluster scope",
						testUser, verb, kind+".projectcalico.org",
					)
				}
			default:
				// There is a tier specified, so return the special policy messages.
				if ns != "" {
					msg = fmt.Sprintf("User %q cannot %s %s in tier %q and namespace %q",
						testUser, verb, kind+".projectcalico.org", tier, ns,
					)
				} else {
					msg = fmt.Sprintf("User %q cannot %s %s in tier %q",
						testUser, verb, kind+".projectcalico.org", tier,
					)
				}
				if !canGetTier {
					msg += " (user cannot get tier)"
				}
			}
			return msg
		}

		checkCRUDError := func(err error, verb, kind, tier, ns string, succeed, canGetTier bool, t *perTierRbacTest) {
			if succeed {
				ExpectWithOffset(3, err).NotTo(HaveOccurred())
				return
			}
			ExpectWithOffset(3, err).To(HaveOccurred())

			// If the pass-thru policy has not been added then the error messages could be based on either
			// the tiered policy checks, or the RBAC for the real resource types. Easiest just not to validate
			// the text in this case.
			if !t.Rbac.ExcludePassThruManifest {
				ExpectWithOffset(3, err.Error()).To(ContainSubstring(errorMessage(verb, kind, tier, ns, canGetTier)))
			}
		}

		// testCreate checks whether the user is able to create the resource.
		// This function always creates the resource, and defers to an admin user if the resource cannot
		// be created by the user.
		testCreate := func(file string, kind, ns, name, tier string, actions, tierActions Action, t *perTierRbacTest) {
			By("creating " + kind + "(" + name + ")")
			yaml := calico.ReadTestFileOrDie(file, yamlConfig{Name: name, TierName: tier})
			err := kubectl.Create(yaml, ns, testUser)
			checkCRUDError(err, "create", kind, tier, ns, actions&ACTION_CREATE != 0, tierActions&ACTION_GET != 0, t)

			if err != nil {
				// Failed to create the resource using testUser access, so create using admin access.
				err = kubectl.Create(yaml, ns, "")
				ExpectWithOffset(2, err).NotTo(HaveOccurred())
			}
		}

		// testReplace checks whether the user is able to replace the resource.
		// This function always replaces the resource, and defers to an admin user if the resource cannot
		// be replaced by the user.
		testReplace := func(file string, kind, ns, name, tier string, actions, tierActions Action, t *perTierRbacTest) {
			By("replacing " + kind + "(" + name + ")")
			yaml := calico.ReadTestFileOrDie(file, yamlConfig{Name: name, TierName: tier})
			err := kubectl.Replace(yaml, ns, testUser)
			checkCRUDError(err, "update", kind, tier, ns, actions&ACTION_REPLACE != 0, tierActions&ACTION_GET != 0, t)

			if err != nil {
				// Failed to replace the resource using testUser access, so replace using admin access.
				err = kubectl.Replace(yaml, ns, "")
				ExpectWithOffset(2, err).NotTo(HaveOccurred())
			}
		}

		// testDelete checks whether the user is able to delete the resource.
		// This function always deletes the resource, and defers to an admin user if the resource cannot
		// be deleted by the user.
		testDelete := func(kind, ns, name string, actions, tierActions Action, t *perTierRbacTest) {
			By("deleting " + kind + "(" + name + ")")
			tier := ""
			if kind == "globalnetworkpolicies" || kind == "networkpolicies" {
				tier = strings.SplitN(name, ".", 2)[0]
			}

			err := kubectl.Delete(kind+".projectcalico.org", ns, name, testUser)
			checkCRUDError(err, "delete", kind, tier, ns, actions&ACTION_DELETE != 0, tierActions&ACTION_GET != 0, t)

			if err != nil {
				// Failed to delete the resource using testUser access, so delete using admin access.
				err = kubectl.Delete(kind+".projectcalico.org", ns, name, "")
				ExpectWithOffset(2, err).NotTo(HaveOccurred())
			}
		}

		// testGet checks whether the user is able to get the resource.
		// This function always gets the resource, and defers to an admin user if the resource cannot
		// be got by the user.
		testGet := func(kind, ns, name string, actions, tierActions Action, t *perTierRbacTest) {
			By("getting " + kind + "(" + name + ")")
			tier := ""
			if kind == "globalnetworkpolicies" || kind == "networkpolicies" {
				tier = strings.SplitN(name, ".", 2)[0]
			}

			_, err := kubectl.Get(kind+".projectcalico.org", ns, name, "", "yaml", testUser, false)
			checkCRUDError(err, "get", kind, tier, ns, actions&ACTION_GET != 0, tierActions&ACTION_GET != 0, t)

			if err != nil {
				// Failed to get the resource using testUser access, so get using admin access.
				_, err = kubectl.Get(kind+".projectcalico.org", ns, name, "", "yaml", "", false)
				ExpectWithOffset(2, err).NotTo(HaveOccurred())
			}
		}

		// testList checks whether the user is able to list the resource in the specified tier.
		// This function always lists the resource, and defers to an admin user if the resource cannot
		// be listed by the user.
		testList := func(kind, ns, tier string, actions, tierActions Action, t *perTierRbacTest) {
			By("listing " + kind + "(tier: " + tier + ")")
			var label string
			if tier != "" {
				// If a tier has been specified then construct the required tier label.
				label = fmt.Sprintf("projectcalico.org/tier==%s", tier)
			}

			_, err := kubectl.Get(kind+".projectcalico.org", ns, "", label, "yaml", testUser, false)
			checkCRUDError(err, "list", kind, tier, ns, actions&ACTION_LIST != 0, tierActions&ACTION_GET != 0, t)

			if err != nil {
				// Failed to list the resource using testUser access, so list using admin access.
				_, err = kubectl.Get(kind+".projectcalico.org", ns, "", label, "yaml", "", false)
				ExpectWithOffset(2, err).NotTo(HaveOccurred())
			}
		}

		testCNXRBAC := func(t *perTierRbacTest) {
			// Apply the RBAC manifest
			yaml := calico.ReadTestFileOrDie("cnx-rbac-user-tieredpolicy.yaml", &perTierRbacYAML{
				Namespace: testNamespace,
				Tier1:     testTier1,
				User:      testUser,
				Rbac:      t.Rbac,
				Gnp:       testGnpTier1, // Our exact match GNP is in tier1.
				Np:        testNpTier1,  // Our exact match NP is in tier1.
			})
			err := kubectl.Apply(yaml, "", "")
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			// Create, replace, get and list the tiers.
			// Note:
			// - tier parameter can be ignored since they are only relevant for Calico policy resources)
			// - it is not possible to create, modify or delete the default tier so don't bother attempting
			// - tier1 will be deleted later after the policy tests.
			testCreate("cnx-tier-1.yaml", "tiers", "", testTier1, "", t.Tier1, 0, t)
			testReplace("cnx-tier-2.yaml", "tiers", "", testTier1, "", t.Tier1, 0, t)
			testGet("tiers", "", testTier1, t.Tier1, 0, t)
			testGet("tiers", "", "default", t.TierDefault, 0, t)

			// Perform full CRUD on a NetworkPolicy in the default tier.
			testCreate("cnx-np-1.yaml", "networkpolicies", testNamespace, testNpTierDefault, "default", t.NpTierDefault, t.TierDefault, t)
			testReplace("cnx-np-2.yaml", "networkpolicies", testNamespace, testNpTierDefault, "default", t.NpTierDefault, t.TierDefault, t)
			testGet("networkpolicies", testNamespace, testNpTierDefault, t.NpTierDefault, t.TierDefault, t)
			testList("networkpolicies", testNamespace, "default", t.NpTierDefault, t.TierDefault, t)
			testDelete("networkpolicies", testNamespace, testNpTierDefault, t.NpTierDefault, t.TierDefault, t)

			// Perform full CRUD on a NetworkPolicy in tier1.
			testCreate("cnx-np-1.yaml", "networkpolicies", testNamespace, testNpTier1, testTier1, t.NpTier1, t.Tier1, t)
			testReplace("cnx-np-2.yaml", "networkpolicies", testNamespace, testNpTier1, testTier1, t.NpTier1, t.Tier1, t)
			testGet("networkpolicies", testNamespace, testNpTier1, t.NpTier1, t.Tier1, t)
			testList("networkpolicies", testNamespace, testTier1, t.NpTier1, t.Tier1, t)
			testDelete("networkpolicies", testNamespace, testNpTier1, t.NpTier1, t.Tier1, t)

			// Perform full CRUD on a GlobalNetworkPolicy in the default tier.
			testCreate("cnx-gnp-1.yaml", "globalnetworkpolicies", "", testGnpTierDefault, "default", t.GnpTierDefault, t.TierDefault, t)
			testReplace("cnx-gnp-2.yaml", "globalnetworkpolicies", "", testGnpTierDefault, "default", t.GnpTierDefault, t.TierDefault, t)
			testGet("globalnetworkpolicies", "", testGnpTierDefault, t.GnpTierDefault, t.TierDefault, t)
			testList("globalnetworkpolicies", "", "default", t.GnpTierDefault, t.TierDefault, t)
			testDelete("globalnetworkpolicies", "", testGnpTierDefault, t.GnpTierDefault, t.TierDefault, t)

			// Perform full CRUD on a GlobalNetworkPolicy in tier1.
			testCreate("cnx-gnp-1.yaml", "globalnetworkpolicies", "", testGnpTier1, testTier1, t.GnpTier1, t.Tier1, t)
			testReplace("cnx-gnp-2.yaml", "globalnetworkpolicies", "", testGnpTier1, testTier1, t.GnpTier1, t.Tier1, t)
			testGet("globalnetworkpolicies", "", testGnpTier1, t.GnpTier1, t.Tier1, t)
			testList("globalnetworkpolicies", "", testTier1, t.GnpTier1, t.Tier1, t)
			testDelete("globalnetworkpolicies", "", testGnpTier1, t.GnpTier1, t.Tier1, t)

			// Delete tier 1 now that all policies have been deleted from it. Do not attempt to delete the default
			// tier, which will fail.
			testDelete("tiers", "", testTier1, t.Tier1, 0, t)
		}

		It("no permissions; only manageable by a sysadmin", func() {
			testCNXRBAC(&perTierRbacTest{
				Rbac: perTierRbac{
					ExcludePassThruManifest: true,
				},
			})
		})
		It("full permissions using full wildcard, no passthru; policy only manageable by a sysadmin", func() {
			testCNXRBAC(&perTierRbacTest{
				Rbac: perTierRbac{
					ExcludePassThruManifest: true,
					TierAll:                 VERBS_ALL,
					GnpAll:                  VERBS_ALL,
					NpAll:                   VERBS_ALL,
				},
				TierDefault: ACTION_GET,
				Tier1:       ACTIONS_ALL,
			})
		})
		It("full permissions using full wildcard; policy fully manageable by user", func() {
			testCNXRBAC(&perTierRbacTest{
				Rbac: perTierRbac{
					TierAll: VERBS_ALL,
					GnpAll:  VERBS_ALL,
					NpAll:   VERBS_ALL,
				},
				TierDefault:    ACTION_GET,
				Tier1:          ACTIONS_ALL,
				GnpTierDefault: ACTIONS_ALL,
				GnpTier1:       ACTIONS_ALL,
				NpTierDefault:  ACTIONS_ALL,
				NpTier1:        ACTIONS_ALL,
			})
		})
		It("get tier wildcard, full policy permissions using full wildcard; policy fully manageable by user", func() {
			testCNXRBAC(&perTierRbacTest{
				Rbac: perTierRbac{
					TierAll: VERBS_READ,
					GnpAll:  VERBS_ALL,
					NpAll:   VERBS_ALL,
				},
				TierDefault:    ACTION_GET,
				Tier1:          ACTION_GET,
				GnpTierDefault: ACTIONS_ALL,
				GnpTier1:       ACTIONS_ALL,
				NpTierDefault:  ACTIONS_ALL,
				NpTier1:        ACTIONS_ALL,
			})
		})
		It("get default tier, full policy permissions using full wildcard; policy fully manageable by user in default tier only", func() {
			testCNXRBAC(&perTierRbacTest{
				Rbac: perTierRbac{
					TierDefault: VERBS_GET,
					GnpAll:      VERBS_ALL,
					NpAll:       VERBS_ALL,
				},
				TierDefault:    ACTION_GET,
				GnpTierDefault: ACTIONS_ALL,
				NpTierDefault:  ACTIONS_ALL,
			})
		})
		It("get tier1, full policy permissions using full wildcard; policy fully manageable by user in tier1 only", func() {
			testCNXRBAC(&perTierRbacTest{
				Rbac: perTierRbac{
					Tier1:  VERBS_GET,
					GnpAll: VERBS_ALL,
					NpAll:  VERBS_ALL,
				},
				Tier1:    ACTION_GET,
				GnpTier1: ACTIONS_ALL,
				NpTier1:  ACTIONS_ALL,
			})
		})
		It("get tier1, full NP permissions using full wildcard; NP fully manageable by user in tier1", func() {
			testCNXRBAC(&perTierRbacTest{
				Rbac: perTierRbac{
					Tier1: VERBS_GET,
					NpAll: VERBS_ALL,
				},
				Tier1:   ACTION_GET,
				NpTier1: ACTIONS_ALL,
			})
		})
		It("get default, full NP permissions using full wildcard, cluster scoped NP binding; NP fully manageable by user in default tier", func() {
			testCNXRBAC(&perTierRbacTest{
				Rbac: perTierRbac{
					TierDefault: VERBS_GET,
					NpAll:       VERBS_ALL,
					UseClusterScopedNetworkPolicy: true,
				},
				TierDefault:   ACTION_GET,
				NpTierDefault: ACTIONS_ALL,
			})
		})
		It("get default, full GNP permissions using full wildcard; GNP fully manageable by user in default tier", func() {
			testCNXRBAC(&perTierRbacTest{
				Rbac: perTierRbac{
					TierDefault: VERBS_GET,
					GnpAll:      VERBS_ALL,
				},
				TierDefault:    ACTION_GET,
				GnpTierDefault: ACTIONS_ALL,
			})
		})
		It("get tier1, full GNP permissions using full wildcard; GNP fully manageable by user in tier1", func() {
			testCNXRBAC(&perTierRbacTest{
				Rbac: perTierRbac{
					Tier1:  VERBS_GET,
					GnpAll: VERBS_ALL,
				},
				Tier1:    ACTION_GET,
				GnpTier1: ACTIONS_ALL,
			})
		})
		It("read tiers, read all policies, write NP in tier1; User can view all policies and write NPs in tier1", func() {
			testCNXRBAC(&perTierRbacTest{
				Rbac: perTierRbac{
					TierAll:    VERBS_READ,
					GnpAll:     VERBS_READ,
					NpAll:      VERBS_READ,
					NpTier1All: VERBS_WRITE,
				},
				TierDefault:    ACTION_GET,
				Tier1:          ACTION_GET,
				GnpTierDefault: ACTIONS_READ,
				GnpTier1:       ACTIONS_READ,
				NpTierDefault:  ACTIONS_READ,
				NpTier1:        ACTIONS_ALL,
			})
		})
		It("read tiers, read all policies, write NP in tier1, cluster scoped NP binding; User can view all policies and write NPs in tier1", func() {
			testCNXRBAC(&perTierRbacTest{
				Rbac: perTierRbac{
					TierAll:                       VERBS_READ,
					GnpAll:                        VERBS_READ,
					NpAll:                         VERBS_READ,
					NpTier1All:                    VERBS_WRITE,
					UseClusterScopedNetworkPolicy: true,
				},
				TierDefault:    ACTION_GET,
				Tier1:          ACTION_GET,
				GnpTierDefault: ACTIONS_READ,
				GnpTier1:       ACTIONS_READ,
				NpTierDefault:  ACTIONS_READ,
				NpTier1:        ACTIONS_ALL,
			})
		})
		It("list all tiers, get tier1, read all GNP; user can view GNPs in tier1", func() {
			testCNXRBAC(&perTierRbacTest{
				Rbac: perTierRbac{
					TierAll: VERBS_LIST_WATCH,
					Tier1:   VERBS_GET,
					GnpAll:  VERBS_READ,
				},
				Tier1:    ACTION_GET,
				GnpTier1: ACTIONS_READ,
			})
		})
		It("read all tiers, read GNP default tier, full access NP tier1; user can view GNP in default tier, and read/write NP in tier1", func() {
			testCNXRBAC(&perTierRbacTest{
				Rbac: perTierRbac{
					TierAll:           VERBS_READ,
					GnpTierDefaultAll: VERBS_READ,
					NpTier1All:        VERBS_ALL,
				},
				TierDefault:    ACTION_GET,
				Tier1:          ACTION_GET,
				GnpTierDefault: ACTIONS_READ,
				NpTier1:        ACTIONS_ALL,
			})
		})
		It("read all tiers, full access to specific GNP in tier1; user can get/replace/delete GNP in tier1", func() {
			testCNXRBAC(&perTierRbacTest{
				Rbac: perTierRbac{
					TierAll: VERBS_READ,
					Gnp:     VERBS_ALL,
				},
				TierDefault: ACTION_GET,
				Tier1:       ACTION_GET,
				GnpTier1:    ACTION_DELETE | ACTION_GET | ACTION_REPLACE,
			})
		})
		It("read all tiers, full access to specific NP in default tier; user can get/replace/delete NP in default tier", func() {
			testCNXRBAC(&perTierRbacTest{
				Rbac: perTierRbac{
					TierAll: VERBS_READ,
					Np:      VERBS_ALL,
				},
				TierDefault: ACTION_GET,
				Tier1:       ACTION_GET,
				NpTier1:     ACTION_DELETE | ACTION_GET | ACTION_REPLACE,
			})
		})
	})
})

