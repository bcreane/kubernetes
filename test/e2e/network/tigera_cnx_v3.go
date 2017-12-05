// Copyright (c) 2017 Tigera, Inc. All rights reserved.

package network

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"
)

var _ = SIGDescribe("[Feature:CNX-v3] Drop Action Override Tests", func() {
	f := framework.NewDefaultFramework("network-policy")
	felixConfigNeeded := true

	BeforeEach(func() {
		if felixConfigNeeded {
			// Test setting the Felix config using the environment variables
			calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_PROMETHEUSREPORTERENABLED", "true")
			calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_PROMETHEUSREPORTERPORT", "9081")
			calico.RestartCalicoNodePods(f.ClientSet, "")
			felixConfigNeeded = false
		}
	})

	Context("DropActionOverride", func() {

		testFunc := func(dropActionOverride, daoMethod, dropType string) func() {
			return func() {
				ns := f.Namespace
				serverPod, service := createServerPodAndService(f, ns, "server", []int{80, 443})

				defer func() {
					By("Cleaning up the server and the service.")
					cleanupServerPodAndService(f, serverPod, service)
				}()

				framework.Logf("Waiting for Server to come up.")
				err := framework.WaitForPodRunningInNamespace(f.ClientSet, serverPod)
				Expect(err).NotTo(HaveOccurred())

				calicoctl := calico.ConfigureCalicoctl(f)

				switch dropType {
				case "policyDeny":
					// DROP for port 80 will come from a DENY rule in a Calico
					// policy.  Since we're directly programming Calico policy,
					// we can also use that to accept port 443 traffic.
					calicoctl.Apply(fmt.Sprintf(
						`
- apiVersion: projectcalico.org/v3
  kind: GlobalNetworkPolicy
  metadata:
    name: policydeny
  spec:
    order: 10
    types:
      - Ingress
    ingress:
      - action: Allow
        protocol: TCP
        source:
          selector: pod-name == "client-a"
        destination:
          ports: [443]
      - action: Deny
        protocol: TCP
        source:
          selector: pod-name == "client-a"
        destination:
          ports: [80]
    selector: pod-name == "%s"
`,
						serverPod.Name))
					defer func() {
						calicoctl.DeleteGNP("policydeny")
					}()

				case "profileDeny":
					// DROP for port 80 will come from a DENY rule in the Calico
					// profile that is generated from the server pod's
					// Namespace.  Here we create a NetworkPolicy to allow port
					// 443 traffic through.
					policy := networkingv1.NetworkPolicy{
						ObjectMeta: metav1.ObjectMeta{
							Name: "allow-client-port-443",
						},
						Spec: networkingv1.NetworkPolicySpec{
							// Apply this policy to the Server
							PodSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{
									"pod-name": serverPod.Name,
								},
							},
							// Allow traffic only from client-a on port 443.
							Ingress: []networkingv1.NetworkPolicyIngressRule{{
								From: []networkingv1.NetworkPolicyPeer{{
									PodSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"pod-name": "client-a",
										},
									},
								}},
								Ports: []networkingv1.NetworkPolicyPort{{
									Port: &intstr.IntOrString{IntVal: 443},
								}},
							}},
						},
					}

					result := networkingv1.NetworkPolicy{}
					err = f.ClientSet.Extensions().RESTClient().Post().Namespace(ns.Name).
						Resource("networkpolicies").Body(&policy).Do().Into(&result)
					Expect(err).NotTo(HaveOccurred())
					defer func() {
						By("Cleaning up the policy.")
						if err = f.ClientSet.Extensions().RESTClient().
							Delete().
							Namespace(ns.Name).
							Resource("networkpolicies").
							Name(policy.Name).
							Do().Error(); err != nil {
							framework.Failf("unable to cleanup policy %v: %v", policy.Name, err)
						}
					}()
				default:
					panic("Unhandled dropType")
				}

				calicoctl.Get("globalnetworkpolicy", "-o", "yaml")
				calicoctl.Get("profile", "-o", "yaml")

				By("Creating client-a, which can connect on port 443")
				testCanConnect(f, ns, "client-a", service, 443)

				By("Setting DropActionOverride")
				serverPodNow, err := f.ClientSet.Core().Pods(ns.Name).Get(serverPod.Name, metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
				serverNodeName := serverPodNow.Spec.NodeName
				serverNode, err := f.ClientSet.Core().Nodes().Get(serverNodeName, metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
				framework.Logf("Server is running on %v", serverNodeName)
				if dropActionOverride != "" {
					var felixConfig string
					var cleanConfig string
					switch daoMethod {
					case "calicoctl global":
						felixConfig = fmt.Sprintf(`
apiVersion: projectcalico.org/v3
kind: FelixConfiguration
metadata:
  name: default
spec:
  ipipEnabled: true
  logSeverityScreen: Info
  reportingIntervalSecs: 0
  dropActionOverride: %s
`,
							dropActionOverride)

						cleanConfig = `
apiVersion: projectcalico.org/v3
kind: FelixConfiguration
metadata:
  name: default
spec:
  ipipEnabled: true
  logSeverityScreen: Info
  reportingIntervalSecs: 0`

					case "calicoctl node":
						felixConfig = fmt.Sprintf(`
apiVersion: projectcalico.org/v3
kind: FelixConfiguration
metadata:
  name: %s
spec:
  defaultEndpointToHostAction: Return
  dropActionOverride: %s
`,
							"node."+serverNodeName, dropActionOverride)

						cleanConfig = fmt.Sprintf(`
apiVersion: projectcalico.org/v3
kind: FelixConfiguration
metadata:
  name: %s
spec:
  defaultEndpointToHostAction: Return
`,
							"node."+serverNodeName)
					case "pod env":
						// Modify the calico-node pod environment.
						By("setting the drop action override in the pod environment")
						calico.SetCalicoNodeEnvironment(f.ClientSet, "FELIX_DROPACTIONOVERRIDE", dropActionOverride)
						defer func() {
							By("clean up calico-node pod env")
							calico.SetCalicoNodeEnvironment(f.ClientSet, "FELIX_DROPACTIONOVERRIDE", "")
							calico.RestartCalicoNodePods(f.ClientSet, serverNodeName)
						}()

						// Kill the calico-node pod on the server's node, so
						// that it restarts with the new environment.
						calico.RestartCalicoNodePods(f.ClientSet, serverNodeName)
					default:
						panic("Unhandled daoMethod")
					}

					if felixConfig != "" && cleanConfig != "" {
						By("applying the felix config with a drop action override")
						calicoctl.Apply(felixConfig)
						defer func() {
							By("cleaning up felix config drop action override")
							calicoctl.Apply(cleanConfig)
						}()

					}
				}

				time.Sleep(3 * time.Second)
				initPackets := sumCalicoDeniedPackets(serverPodNow.Status.HostIP)
				serverSyslogCount := calico.CountSyslogLines(serverNode)

				By("Creating client-a that tries to connect on port 80")
				switch dropActionOverride {
				case "Drop", "", "LogAndDrop":
					testCannotConnect(f, ns, "client-a", service, 80)
				case "Accept", "LogAndAccept":
					testCanConnect(f, ns, "client-a", service, 80)
				default:
					panic("Unhandled override setting")
				}

				time.Sleep(20 * time.Second)

				// Regardless of DropActionOverride, there should always be an
				// increase in the calico_denied_packets metric.
				nowPackets := sumCalicoDeniedPackets(serverPodNow.Status.HostIP)
				Expect(nowPackets).To(BeNumerically(">", initPackets))

				// When DropActionOverride begins with "Log", there should be new
				// syslogs for the packets to port 80.
				newDropLogs := calico.GetNewCalicoDropLogs(serverNode, serverSyslogCount, "calico-drop")
				framework.Logf("New drop logs: %#v", newDropLogs)
				if strings.HasPrefix(dropActionOverride, "Log") {
					if len(newDropLogs) >= 0 {
						newSyslogCount := calico.CountSyslogLines(serverNode)
						Expect(newSyslogCount).NotTo(BeZero())
					}
					Expect(len(newDropLogs)).NotTo(BeZero())
				} else {
					Expect(len(newDropLogs)).To(BeZero())
				}

				// Run calicoq commands.
				calico.Calicoq("eval", "pod-name=='client-a'")
				calico.Calicoq("policy", "policydeny")
				calico.Calicoq("host", serverNodeName)
				calico.Calicoq("endpoint", "client")
			}
		}
		It("not set, profileDeny", testFunc(
			"",
			"",
			"profileDeny",
		))
		It("Drop, calicoctl global, profileDeny", testFunc(
			"Drop",
			"calicoctl global",
			"profileDeny",
		))
		It("Accept, calicoctl node, profileDeny", testFunc(
			"Accept",
			"calicoctl node",
			"profileDeny",
		))
		It("LogAndDrop, pod env, profileDeny", testFunc(
			"LogAndDrop",
			"pod env",
			"profileDeny",
		))
		It("LogAndAccept, calicoctl global, profileDeny", testFunc(
			"LogAndAccept",
			"calicoctl global",
			"profileDeny",
		))
		It("LogAndAccept, pod env, profileDeny", testFunc(
			"LogAndAccept",
			"pod env",
			"profileDeny",
		))
		It("Accept, calicoctl node, policyDeny", testFunc(
			"Accept",
			"calicoctl node",
			"policyDeny",
		))
	})
})
