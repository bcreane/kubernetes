// Copyright (c) 2017 Tigera, Inc. All rights reserved.

package network

import (
	"bufio"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var (
	felixConfigNeeded = true
	datastoreType     = ""
)

var _ = framework.KubeDescribe("Essentials", func() {
	f := framework.NewDefaultFramework("network-policy")

	BeforeEach(func() {
		if felixConfigNeeded {
			calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_PROMETHEUSREPORTERENABLED", "true")
			calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_PROMETHEUSREPORTERPORT", "9081")
			calico.RestartCalicoNodePods(f.ClientSet, "")
			felixConfigNeeded = false
		}
		if datastoreType == "" {
			// Infer datastore type by reading /etc/calico/calicoctl.cfg.
			b, err := ioutil.ReadFile("/etc/calico/calicoctl.cfg")
			Expect(err).NotTo(HaveOccurred())
			for _, line := range strings.Split(string(b), "\n") {
				if strings.Contains(line, "datastoreType") {
					if strings.Contains(line, "kubernetes") {
						datastoreType = "kdd"
					}
					if strings.Contains(line, "etcd") {
						datastoreType = "etcd"
					}
				}
			}
			framework.Logf("datastoreType = %v", datastoreType)
			Expect(datastoreType).NotTo(Equal(""))
		}
	})

	Context("DropActionOverride", func() {

		testFunc := func(dropActionOverride, daoMethod, dropType string) func() {
			return func() {
				ns := f.Namespace
				serverPod, service := createServerPodAndService(f, ns, "server", []int{80, 443})
				defer func() {
					By("Cleaning up the server.")
					if err := f.ClientSet.Core().Pods(ns.Name).Delete(serverPod.Name, nil); err != nil {
						framework.Failf("unable to cleanup pod %v: %v", serverPod.Name, err)
					}
				}()
				defer func() {
					By("Cleaning up the server's service.")
					if err := f.ClientSet.Core().Services(ns.Name).Delete(service.Name, nil); err != nil {
						framework.Failf("unable to cleanup svc %v: %v", service.Name, err)
					}
				}()
				framework.Logf("Waiting for Server to come up.")
				err := framework.WaitForPodRunningInNamespace(f.ClientSet, serverPod)
				Expect(err).NotTo(HaveOccurred())

				switch dropType {
				case "policyDeny":
					if datastoreType == "kdd" {
						Skip("can't write Calico policy with KDD")
					}

					// DROP for port 80 will come from a DENY rule in a Calico
					// policy.  Since we're directly programming Calico policy,
					// we can also use that to accept port 443 traffic.
					calico.CalicoctlApply(
						`
- apiVersion: v1
  kind: policy
  metadata:
    name: policyDeny
  spec:
    order: 10
    ingress:
      - action: allow
        protocol: tcp
        source:
          selector: pod-name=='client-a'
        destination:
          ports: [443]
      - action: deny
        protocol: tcp
        source:
          selector: pod-name=='client-a'
        destination:
          ports: [80]
    selector: pod-name=='%s'
`,
						serverPod.Name)
					defer func() {
						calico.Calicoctl("delete", "policy", "policyDeny")
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

				calico.Calicoctl("get", "policy", "-o", "yaml")
				calico.Calicoctl("get", "profile", "-o", "yaml")

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
					switch daoMethod {
					case "calicoctl global":
						calico.Calicoctl(
							"config",
							"set",
							"DropActionOverride",
							dropActionOverride,
							"--raw=felix",
						)
						defer func() {
							By("clean up calicoctl global setting")
							calico.Calicoctl(
								"config",
								"unset",
								"DropActionOverride",
								"--raw=felix",
							)
						}()
					case "calicoctl node":
						if datastoreType == "kdd" {
							Skip("can't do node-specific calicoctl config with KDD")
						}

						calico.Calicoctl(
							"config",
							"set",
							"DropActionOverride",
							dropActionOverride,
							"--node="+serverNodeName,
							"--raw=felix",
						)
						defer func() {
							By("clean up calicoctl node setting")
							calico.Calicoctl(
								"config",
								"unset",
								"DropActionOverride",
								"--node="+serverNodeName,
								"--raw=felix",
							)
						}()
					case "pod env":
						// Modify the calico-node pod environment.
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
				}

				time.Sleep(3 * time.Second)
				initPackets := sumCalicoDeniedPackets(serverPodNow.Status.HostIP)
				serverSyslogCount := calico.CountSyslogLines(serverNode)

				By("Creating client-a that tries to connect on port 80")
				switch dropActionOverride {
				case "DROP", "", "LOG-and-DROP":
					testCannotConnect(f, ns, "client-a", service, 80)
				case "ACCEPT", "LOG-and-ACCEPT":
					testCanConnect(f, ns, "client-a", service, 80)
				default:
					panic("Unhandled override setting")
				}

				time.Sleep(20 * time.Second)

				// Regardless of DropActionOverride, there should always be an
				// increase in the calico_denied_packets metric.
				nowPackets := sumCalicoDeniedPackets(serverPodNow.Status.HostIP)
				Expect(nowPackets).To(BeNumerically(">", initPackets))

				// When DropActionOverride begins with "LOG-", there should be new
				// syslogs for the packets to port 80.
				newDropLogs := calico.GetNewCalicoDropLogs(serverNode, serverSyslogCount, "calico-drop")
				framework.Logf("New drop logs: %#v", newDropLogs)
				if strings.HasPrefix(dropActionOverride, "LOG-") {
					if len(newDropLogs) >= 0 {
						_, err = framework.IssueSSHCommandWithResult(
							"wc -l /var/log/syslog",
							"gce",
							serverNode)
						framework.ExpectNoError(err)
						_, err = framework.IssueSSHCommandWithResult(
							"ls -lrt /var/log/",
							"gce",
							serverNode)
						framework.ExpectNoError(err)
						_, err = framework.IssueSSHCommandWithResult(
							"sudo iptables -L -n -v",
							"gce",
							serverNode)
						framework.ExpectNoError(err)
					}
					Expect(len(newDropLogs)).NotTo(BeZero())
				} else {
					Expect(len(newDropLogs)).To(BeZero())
				}

				// Run calicoq commands.
				calico.Calicoq("eval", "pod-name=='client-a'")
				calico.Calicoq("policy", "policyDeny")
				calico.Calicoq("host", serverNodeName)
				calico.Calicoq("endpoint", "client")
			}
		}
		It("not set, profileDeny", testFunc(
			"",
			"",
			"profileDeny",
		))
		It("DROP, calicoctl global, profileDeny", testFunc(
			"DROP",
			"calicoctl global",
			"profileDeny",
		))
		It("ACCEPT, calicoctl node, profileDeny", testFunc(
			"ACCEPT",
			"calicoctl node",
			"profileDeny",
		))
		It("LOG-and-DROP, pod env, profileDeny", testFunc(
			"LOG-and-DROP",
			"pod env",
			"profileDeny",
		))
		It("LOG-and-ACCEPT, calicoctl global, profileDeny", testFunc(
			"LOG-and-ACCEPT",
			"calicoctl global",
			"profileDeny",
		))
		It("LOG-and-ACCEPT, pod env, profileDeny", testFunc(
			"LOG-and-ACCEPT",
			"pod env",
			"profileDeny",
		))
		It("ACCEPT, calicoctl node, policyDeny", testFunc(
			"ACCEPT",
			"calicoctl node",
			"policyDeny",
		))
	})
})

var _ = framework.KubeDescribe("Essentials CalicoQ", func() {
	It("should output version info when asked", func() {
		By("Running command line client")
		stdout, stderr, err := calico.Calicoq("version")
		Expect(err).NotTo(HaveOccurred())
		Expect(stderr).To(Equal(""))
		lines := strings.Split(stdout, "\n")
		Expect(lines[0]).To(HavePrefix("Version:"))
		Expect(lines[1]).To(HavePrefix("Build date:"))
		Expect(lines[2]).To(HavePrefix("Git tag ref:"))
		Expect(lines[3]).To(HavePrefix("Git commit:"))
	})
})

func getFelixMetrics(felixIP, name string) (metrics []string, err error) {
	var resp *http.Response
	for retry := 0; retry < 5; retry++ {
		resp, err = http.Get("http://" + felixIP + ":9081/metrics")
		if err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return
	}
	framework.Logf("Metric response %#v", resp)
	defer resp.Body.Close()

	metrics = []string{}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		framework.Logf("Line: %v", line)
		if strings.HasPrefix(line, name) {
			metrics = append(metrics, strings.TrimSpace(strings.TrimPrefix(line, name)))
		}
	}
	err = scanner.Err()
	return
}

func sumCalicoDeniedPackets(felixIP string) (sum int64) {
	metrics, err := getFelixMetrics(felixIP, "calico_denied_packets")
	Expect(err).NotTo(HaveOccurred())
	sum = 0
	for _, metric := range metrics {
		words := strings.Split(metric, " ")
		count, err := strconv.ParseInt(words[1], 10, 64)
		Expect(err).NotTo(HaveOccurred())
		sum += count
	}
	framework.Logf("Denied packets = %v", sum)
	return
}