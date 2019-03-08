/*
Copyright (c) 2018 Tigera, Inc. All rights reserved.

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
	"strings"
	"time"

	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/aws"

	"k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/kubernetes/test/utils/calico"
)

const (
	RDSPassword = "F00bar123"
	RDSDBName   = "dbname"
)

var _ = SIGDescribe("[Feature:Anx-SG-Int] anx security group policy", func() {
	var awsctl *aws.Awsctl
	var sgRds, subnetGroup string
	var rds, endPoint, port string
	var portInt64 int64
	var calicoctl *calico.Calicoctl

	f := framework.NewDefaultFramework("e2e-anx-sg")

	var addSg = func(sgName string, desc string) string {
		sgId, err := awsctl.CreateTestVpcSG(sgName, desc)
		Expect(err).NotTo(HaveOccurred())
		return sgId
	}

	BeforeEach(func() {
		// See if anx is installed.
		if !aws.CheckAnxInstalled(f) {
			framework.Skipf("Checking anx install failed. Skip anx security group tests.")
		}
	})

	BeforeEach(func() {
		// The following code tries to get config information for awsctl from k8s ConfigMap.
		// A framework clientset is needed to access k8s configmap but it will only be created in the context of BeforeEach or It.
		// Current solution is to use BeforeEach because this function is not a test case.
		// This will avoid complexity of creating a client ourselves.
		awsctl = aws.ConfigureAwsctl(f)
		// The following code tries to get config information for calicoctl from k8s ConfigMap.
		// A framework clientset is needed to access k8s configmap but it will only be created in the context of BeforeEach or IT.
		// Current solution is to use BeforeEach because this function is not a test case.
		// This will avoid complexity of creating a client by ourself.
		calicoctl = calico.ConfigureCalicoctl(f)

		err := awsctl.CleanupTestSGs()
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		// Dump GNPs and SGs on failure
		if CurrentGinkgoTestDescription().Failed {
			gnps, err := framework.RunKubectl("get", "globalnetworkpolicies", "-o", "yaml")
			Expect(err).NotTo(HaveOccurred())
			framework.Logf("Dumping GlobalNetworkPolicies:\n%s", gnps)
			err = awsctl.DumpSGsInVpc()
			Expect(err).NotTo(HaveOccurred())
		}

		// Cleanup AWS resources created for the test.
		if rds != "" {
			err := awsctl.Client.DeleteRDSInstance(rds)
			Expect(err).NotTo(HaveOccurred())
			rds = ""
		}
		if subnetGroup != "" {
			err := awsctl.Client.DeleteDBSubnetGroup(subnetGroup)
			Expect(err).NotTo(HaveOccurred())
			subnetGroup = ""
		}

		err := awsctl.CleanupTestSGs()
		Expect(err).NotTo(HaveOccurred())
	})

	Context("with rds instance", func() {
		BeforeEach(func() {
			var err error

			By("Creating a security group (allow SG) for rds instance")
			sgRds, err = awsctl.CreateTestVpcSG("sgRds", "SG for RDS instance 1")
			Expect(err).NotTo(HaveOccurred())

			By("Creating a db subnet group")
			subnetGroup, err = awsctl.Client.CreateDBSubnetGroup("sgE2E")
			Expect(err).NotTo(HaveOccurred())

			By("Creating a rds instance with allow SG and get endpoint address with allow SG")
			rds, endPoint, portInt64, err = awsctl.Client.CreateRDSInstance("sgE2e", subnetGroup, sgRds, RDSPassword, RDSDBName)
			Expect(err).NotTo(HaveOccurred())
			port = fmt.Sprint(portInt64)

			By("Add rules to allow SG to allow traffic to rds instances")
			err = awsctl.Client.AuthorizeSGIngressSrcSG(sgRds, "tcp", portInt64, portInt64, []string{sgRds})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() error {
				sgs, err := awsctl.Client.GetRDSInstanceSecurityGroups(rds)
				if err != nil {
					return err
				}

				for _, v := range sgs {
					if v == awsctl.Config.TrustSG {
						return nil
					}
				}
				return fmt.Errorf("RDS Instance %s did not have trust SG: %v", rds, sgs)
			}, 2*time.Minute, 10*time.Second).ShouldNot(HaveOccurred())
		})

		It("should allow pod to connect rds instance with allow SG", func() {
			By("create a deny SG with no rules")
			sgDeny, err := awsctl.Client.CreateVpcSG("sgDeny", "SG for RDS instance 1")
			Expect(err).NotTo(HaveOccurred())

			defer func() {
				err := awsctl.Client.DeleteVpcSG(sgDeny)
				Expect(err).NotTo(HaveOccurred())
			}()

			By("client pod in deny sg should not be able to access rds service")
			testCannotConnectRds(f, f.Namespace, "rds-client", endPoint, port, RDSPassword, RDSDBName, []string{sgDeny})

			By("client pod in allow sg should be able to access rds service")
			testCanConnectRds(f, f.Namespace, "rds-client", endPoint, port, RDSPassword, RDSDBName, []string{sgRds})
		})

	})

	Context("with SecurityGroups to restrict and allow traffic", func() {
		var podInSg1 *v1.Pod
		var serviceInSg1 *v1.Service
		var podInSg2 *v1.Pod
		var serviceInSg2 *v1.Service
		var sg1, sg2, sg3 string
		var nsA, nsB *v1.Namespace
		BeforeEach(func() {
			var err error

			By("Creating security group 1")
			sg1 = addSg("sg1", "Security Group 1")
			// Allow ssh inbound for connecting with SSH to instances
			err = awsctl.Client.AuthorizeSGIngressIPRange(sg1, "tcp", 22, 22, []string{"0.0.0.0/0"})
			Expect(err).NotTo(HaveOccurred())

			By("Creating security group 2")
			sg2 = addSg("sg2", "Security Group 2")
			// Allow ssh inbound for connecting with SSH to instances
			err = awsctl.Client.AuthorizeSGIngressIPRange(sg2, "tcp", 22, 22, []string{"0.0.0.0/0"})
			Expect(err).NotTo(HaveOccurred())

			By("Creating security group 3")
			sg3 = addSg("sg3", "Security Group 3")

			// Pods in SG 1 can access pods in SG 2
			By("Add rules to allow traffic from SG 1 to SG 2")
			err = awsctl.Client.AuthorizeSGIngressSrcSG(sg2, "tcp", 5555, 5555, []string{sg1})
			Expect(err).NotTo(HaveOccurred())

			By("Add rules to allow traffic from SG 2 to SG 2")
			err = awsctl.Client.AuthorizeSGIngressSrcSG(sg2, "tcp", 5555, 5555, []string{sg2})
			Expect(err).NotTo(HaveOccurred())

			By("Add rules to allow traffic from SG 3 to SG 2")
			err = awsctl.Client.AuthorizeSGIngressSrcSG(sg2, "tcp", 5555, 5555, []string{sg3})
			Expect(err).NotTo(HaveOccurred())

			nsA = f.Namespace
			nsBName := f.BaseName + "-b"
			// The CreateNamespace helper uses the input name as a Name Generator, so the namespace itself
			// will have a different name than what we are setting as the value of ns-name.
			// This is fine as long as we don't try to match the label as nsB.Name in our policy.
			nsB, err = f.CreateNamespace(nsBName, map[string]string{
				"ns-name": nsBName,
			})
			Expect(err).NotTo(HaveOccurred())

			podInSg1, serviceInSg1 = createServerPodAndServiceX(f, nsA, "pod-in-sg1-nsa", []int{5555},
				func(pod *v1.Pod) {
					if pod.Annotations == nil {
						pod.Annotations = map[string]string{}
					}
					pod.Annotations["aws.tigera.io/security-groups"] = fmt.Sprintf("[\"%s\"]", sg1)
				},
				func(svc *v1.Service) {},
			)
			podInSg2, serviceInSg2 = createServerPodAndServiceX(f, nsB, "pod-in-sg2-nsb", []int{5555},
				func(pod *v1.Pod) {
					if pod.Annotations == nil {
						pod.Annotations = map[string]string{}
					}
					pod.Annotations["aws.tigera.io/security-groups"] = fmt.Sprintf("[\"%s\"]", sg2)
				},
				func(svc *v1.Service) {},
			)
			// Ensure that the pod Status includes an IP
			Eventually(func() error {
				podName := podInSg1.Name
				pod, err := f.ClientSet.CoreV1().Pods(nsA.Name).Get(podName, metav1.GetOptions{})
				if err != nil {
					return fmt.Errorf("Unable to get pod %s: %v", podName, err)
				}
				if pod.Status.PodIP == "" {
					return fmt.Errorf("Pod %s did not have a PodIP: %v", podName, pod)
				}
				podInSg1 = pod
				podName = podInSg2.Name
				pod, err = f.ClientSet.CoreV1().Pods(nsB.Name).Get(podName, metav1.GetOptions{})
				if err != nil {
					return fmt.Errorf("Unable to get pod %s: %v", podName, err)
				}
				if pod.Status.PodIP == "" {
					return fmt.Errorf("Pod %s did not have a PodIP: %v", podName, pod)
				}
				podInSg2 = pod
				return nil
			}, time.Minute, 5*time.Second).ShouldNot(HaveOccurred())

			// Wait for the SGs created to show up as globalnetworkpolicies
			Eventually(func() error {
				gnps, err := framework.RunKubectl("get", "globalnetworkpolicies")
				if err != nil {
					return fmt.Errorf("GNP sg-local.%s for SG was not found: %v", sg2, err)
				}
				if !strings.Contains(gnps, sg1) {
					return fmt.Errorf("GNPs do not contain policy for SG %s", sg1)
				}
				if !strings.Contains(gnps, sg2) {
					return fmt.Errorf("GNPs do not contain policy for SG %s", sg2)
				}
				if !strings.Contains(gnps, sg3) {
					return fmt.Errorf("GNPs do not contain policy for SG %s", sg3)
				}
				return nil
			}, 3*time.Minute, 10*time.Second).ShouldNot(HaveOccurred())
		})

		AfterEach(func() {
			cleanupServerPodAndService(f, podInSg1, serviceInSg1)
			cleanupServerPodAndService(f, podInSg2, serviceInSg2)
		})
		// The default pod group is changed to allow manipulation of SG to result
		// in traffic allowed or denied.
		Context("pod-to-pod traffic can be managed with SecurityGroups", func() {
			BeforeEach(func() {
				err := awsctl.RemoveIngressRulesInTigeraPodDefaultSG()
				Expect(err).NotTo(HaveOccurred())
				// Wait for the TigeraPodDefault SG to have the DNS port that is added by the function above.
				Eventually(func() error {
					//crd, err := f.ClientSet.Core().Pods(nsA.Name).Get(serverInSg2.Name, metav1.GetOptions{})
					sg, err := awsctl.GetTigeraPodDefaultSG()
					if err != nil {
						return fmt.Errorf("Failed to get TigeraPodDefault SG: %v", err)
					}
					sgId := *sg.GroupId
					gnp, err := framework.RunKubectl("get", "globalnetworkpolicies",
						fmt.Sprintf("sg-local.%s", sgId), "-o", "yaml")
					if err != nil {
						return fmt.Errorf("GNP sg-local.%s for SG was not found: %v", sgId, err)
					}
					if strings.Contains(gnp, "- 53") {
						return nil
					}
					return fmt.Errorf("PodDefaultSG does not contain DNS port\n%s", gnp)
				}, 3*time.Minute, 10*time.Second).ShouldNot(HaveOccurred())
			})
			AfterEach(func() {
				err := awsctl.RestoreIngressRulesInTigeraPodDefaultSG()
				Expect(err).NotTo(HaveOccurred())
				// Wait for the DNS port to be removed from the TigeraPodDefault
				Eventually(func() error {
					sg, err := awsctl.GetTigeraPodDefaultSG()
					if err != nil {
						return fmt.Errorf("Failed to get TigeraPodDefault SG: %v", err)
					}
					sgId := *sg.GroupId
					gnp, err := framework.RunKubectl("get", "globalnetworkpolicies",
						fmt.Sprintf("sg-local.%s", sgId), "-o", "yaml")
					if err != nil {
						return fmt.Errorf("GNP sg-local.%s for SG was not found: %v", sgId, err)
					}
					if !strings.Contains(gnp, "- 53") {
						return nil
					}
					return fmt.Errorf("PodDefaultSG still contains DNS port:\n%s", gnp)
				}, 3*time.Minute, 10*time.Second).ShouldNot(HaveOccurred())
			})

			It("should allow pod with SG 1 label to connect to pod with SG 2 label", func() {
				By("Pods with SG 1 annotation can access pods with SG 2 annotation")
				target := fmt.Sprintf("%s.%s:%d", serviceInSg2.Name, serviceInSg2.Namespace, 5555)
				testCanConnectWithSg(f, nsA, "client1-can-connect-to2", sg1, serviceInSg2, target)
				By("Pods with SG 2 annotation can access pods with SG 2 annotation")
				testCanConnectWithSg(f, nsA, "client2-can-connect-to2", sg2, serviceInSg2, target)
				target = fmt.Sprintf("%s.%s:%d", serviceInSg1.Name, serviceInSg1.Namespace, 5555)
				By("Pods with SG 2 annotation cannot access service with SG 1 annotation")
				testCannotConnectWithSg(f, nsA, "client2-cannot-connect-to-sg1", sg2, serviceInSg1, target)
				By("Pods without SG annotation cannot access service with SG 2 annotation")
				testCannotConnect(f, nsA, "client-cannot-connect-to-sg2", serviceInSg2, 5555)
			})

			It("is possible to select a security group with network policy", func() {
				target := fmt.Sprintf("%s.%s:%d", serviceInSg2.Name, serviceInSg2.Namespace, 5555)
				By("creating a client in security group 1 can connect to the server in SG 2.", func() {
					testCanConnectWithSg(f, nsA, "client-in-sg1-can-connect-to-sg2", sg1, serviceInSg2, target)
				})
				By("creating a NP for the server which blocks traffic from pods not in security group 3.")
				policy := &networkingv1.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "allow-sg3-client-to-sg2-pod",
						Annotations: map[string]string{
							"rules.networkpolicy.tigera.io/match-security-groups": "true",
						},
					},
					Spec: networkingv1.NetworkPolicySpec{
						// Apply this policy to the Server
						PodSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{
								"pod-name": podInSg2.Name,
							},
						},
						// Allow traffic only from client-a
						Ingress: []networkingv1.NetworkPolicyIngressRule{{
							From: []networkingv1.NetworkPolicyPeer{{
								PodSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										fmt.Sprintf("sg.aws.tigera.io/%s", sg3): "",
									},
								},
							}},
						}},
					},
				}

				policy, err := f.ClientSet.NetworkingV1().NetworkPolicies(nsB.Name).Create(policy)
				Expect(err).NotTo(HaveOccurred())
				defer cleanupNetworkPolicy(f, policy)

				By("creating a client in security group 3 can connect to the server in SG 2.", func() {
					testCanConnectWithSg(f, nsA, "client-in-sg3-can-connect-to-sg2", sg3, serviceInSg2, target)
				})
				By("creating a client in security group 1 cannot connect to the server in sg 2.", func() {
					testCannotConnectWithSg(f, nsA, "client-in-sg1-cannot-connect-to-sg2", sg1, serviceInSg2, target)
				})
			})
		})

		Context("checking traffic with instances", func() {
			var instanceSg1Ip string
			var instanceSg1Id string
			var instanceSg2Ip string
			var instanceSg2Id string
			var ncInstListenCmd string
			var ncCleanupCmd string
			var ncConnectCmd string
			var ensureInstanceAndHep = func(instanceId string) error {
				_, _, code, err := awsctl.RunSSHCommandOnInstance(instanceId, "/bin/true", 5*time.Second)
				if err != nil {
					return fmt.Errorf("SSH command failed: %v", err)
				}
				if code != 0 {
					return fmt.Errorf("SSH command was not successful: %v", code)
				}
				result, err := calicoctl.ExecReturnError(
					"get", "hostendpoints", "-o", "yaml")
				if err != nil {
					return fmt.Errorf("Failed to query for HEPs: %v", err)
				}
				ip, err := awsctl.Client.GetInstancePrivateIp(instanceId)
				if err != nil {
					return fmt.Errorf("Failed to get IP for Instance %s: %v", instanceId, err)
				}
				if !strings.Contains(result, ip) {
					return fmt.Errorf("Failed to find Instance %s's IP %s in HEPs",
						instanceSg1Id, ip)
				}
				return err
			}
			BeforeEach(func() {
				ncCleanupCmd = "rm -f e2e-listen; killall nc"
				ncConnectCmd = "for i in $(seq 1 5); do timeout 1 nc -z %s && exit 0 || sleep 5; done; cat /etc/resolv.conf; exit 1"
				ncInstListenCmd = "nohup /bin/sh -c \"touch e2e-listen; while [ -f e2e-listen ]; do echo Hello from $HOSTNAME  | nc -l %s; done\" &>/dev/null &"
				var err error
				// Create command for Instance to run that will connect to the above server pod
				instanceSg1Id, err = awsctl.Client.CreateInstance("instance-with-SG1", sg1, "")
				Expect(err).NotTo(HaveOccurred())
				instanceSg2Id, err = awsctl.Client.CreateInstance("instance-with-SG2", sg2, "")
				Expect(err).NotTo(HaveOccurred())
				Eventually(func() error {
					// Ensure the instance in SG2 is up things have been loaded
					if err = ensureInstanceAndHep(instanceSg1Id); err != nil {
						return err
					}
					if instanceSg1Ip, err = awsctl.Client.GetInstancePrivateIp(instanceSg1Id); err != nil {
						return err
					}

					// Ensure the instance in SG2 is up things have been loaded
					if err = ensureInstanceAndHep(instanceSg2Id); err != nil {
						return err
					}
					if instanceSg2Ip, err = awsctl.Client.GetInstancePrivateIp(instanceSg2Id); err != nil {
						return err
					}

					return nil
				}, 5*time.Minute, 20*time.Second).ShouldNot(HaveOccurred())
			})
			AfterEach(func() {
				err := awsctl.Client.DeleteInstance(instanceSg1Id)
				Expect(err).NotTo(HaveOccurred())
				err = awsctl.Client.DeleteInstance(instanceSg2Id)
				Expect(err).NotTo(HaveOccurred())
			})

			It("SecurityGroups should control instance access to pods", func() {
				connectCmd := "for i in $(seq 1 5); do timeout 5 wget -T 5 %s:%s -O - && exit 0 || sleep 1; done; exit 1"
				By("server pod in SG2 should be able to receive connection from instance in SG1")
				_, _, code, err := awsctl.RunSSHCommandOnInstance(instanceSg1Id,
					fmt.Sprintf(connectCmd, podInSg2.Status.PodIP, "5555"), 5*time.Second)
				Expect(err).NotTo(HaveOccurred())
				Expect(code).To(Equal(0))

				By("server pod in SG1 should not be able to receive a connection from instance in SG1")
				_, _, code, err = awsctl.RunSSHCommandOnInstance(instanceSg1Id,
					fmt.Sprintf(connectCmd, podInSg1.Status.PodIP, "5555"), 50*time.Second)
				Expect(err).NotTo(HaveOccurred())
				Expect(code).NotTo(Equal(0))

				By("server instance in SG2 should be able to receive a connection from pod in SG1", func() {
					_, _, code, err = awsctl.RunSSHCommandOnInstance(instanceSg2Id,
						fmt.Sprintf(ncInstListenCmd, "5555"), 50*time.Second)
					Expect(err).NotTo(HaveOccurred())
					Expect(code).To(Equal(0))
					defer func() {
						_, _, code, err = awsctl.RunSSHCommandOnInstance(instanceSg2Id,
							ncCleanupCmd, 50*time.Second)
					}()
					target := fmt.Sprintf("%s %s", instanceSg2Ip, "5555")
					dummyService := &v1.Service{
						ObjectMeta: metav1.ObjectMeta{
							Name: "instance-with-SG2",
						},
					}
					testCanConnectWithSgX(f, nsA, "client1-can-connect-to2", sg1, dummyService, target, func(pod *v1.Pod) {
						pod.Spec.Containers[0].Args = []string{
							"/bin/sh",
							"-c",
							fmt.Sprintf(ncConnectCmd, target)}
					})
				})
				By("server instance in SG1 should not be reachable by a pod in SG1", func() {
					_, _, code, err = awsctl.RunSSHCommandOnInstance(instanceSg1Id,
						fmt.Sprintf(ncInstListenCmd, "5555"), 50*time.Second)
					Expect(err).NotTo(HaveOccurred())
					Expect(code).To(Equal(0))
					defer func() {
						_, _, code, err = awsctl.RunSSHCommandOnInstance(instanceSg1Id,
							ncCleanupCmd, 50*time.Second)
					}()
					target := fmt.Sprintf("%s %s", instanceSg1Ip, "5555")
					dummyService := &v1.Service{
						ObjectMeta: metav1.ObjectMeta{
							Name: "instance-with-SG1",
						},
					}
					testCannotConnectWithSgX(f, nsA, "client1-can-connect-to1", sg1, dummyService, target, func(pod *v1.Pod) {
						pod.Spec.Containers[0].Args = []string{
							"/bin/sh",
							"-c",
							fmt.Sprintf(ncConnectCmd, target)}
					})
				})
			})

			It("SecurityGroups should control pod access to instances", func() {
				_, _, code, err := awsctl.RunSSHCommandOnInstance(instanceSg2Id,
					fmt.Sprintf(ncInstListenCmd, "5555"), 50*time.Second)
				Expect(err).NotTo(HaveOccurred())
				Expect(code).To(Equal(0))
				defer func() {
					_, _, code, err = awsctl.RunSSHCommandOnInstance(instanceSg2Id,
						ncCleanupCmd, 50*time.Second)
					Expect(err).NotTo(HaveOccurred())
				}()
				By("client pod in sg1 should be able to access instance service")
				target := fmt.Sprintf("%s %d", instanceSg2Ip, 5555)
				dummyService := &v1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name: "instance-with-SG2",
					},
				}
				By("Pods with SG 1 annotation can access pods with SG 2 annotation")
				testCanConnectWithSgX(f, nsA, "client1-can-connect-instance-2", sg1, dummyService, target, func(pod *v1.Pod) {
					pod.Spec.Containers[0].Image = "busybox"
					pod.Spec.Containers[0].Args = []string{
						"/bin/sh",
						"-c",
						fmt.Sprintf(ncConnectCmd, target),
					}
				})

				By("Pods without SG annotation cannot access service with SG 2 annotation")
				testCannotConnectX(f, nsA, "client-cannot-connect-to-sg2", dummyService, target, func(pod *v1.Pod) {
					pod.Spec.Containers[0].Image = "busybox"
					pod.Spec.Containers[0].Args = []string{
						"/bin/sh",
						"-c",
						fmt.Sprintf(ncConnectCmd, target),
					}
				})
			})
		})
	})
})

func testCanConnectRds(f *framework.Framework, ns *v1.Namespace, podName string, endPoint string, port string, password string, dbName string, sgs []string) {
	By(fmt.Sprintf("Creating client pod %s that should successfully connect to %s:%s.", podName, endPoint, port))
	podClient := createRdsClientPod(f, ns, podName, endPoint, port, password, dbName, sgs)
	defer func() {
		By(fmt.Sprintf("Cleaning up the pod %s", podName))
		if err := f.ClientSet.CoreV1().Pods(ns.Name).Delete(podClient.Name, nil); err != nil {
			framework.Failf("unable to cleanup pod %v: %v", podClient.Name, err)
		}
	}()

	framework.Logf("Waiting for %s to complete.", podClient.Name)
	err := framework.WaitForPodNoLongerRunningInNamespace(f.ClientSet, podClient.Name, ns.Name)
	Expect(err).NotTo(HaveOccurred(), "Pod did not finish as expected.")

	framework.Logf("Waiting for %s to complete.", podClient.Name)
	err = framework.WaitForPodSuccessInNamespace(f.ClientSet, podClient.Name, ns.Name)

	if err != nil {
		framework.Logf("Expected pod %s to connect and complete but did not: %s", podClient.Name, err)
		// Collect/log Calico diags.
		logErr := calico.LogCalicoDiagsForPodNode(f, podClient.Name)
		if logErr != nil {
			framework.Logf("Error getting Calico diags: %v", logErr)
		}

		// Collect pod logs when we see a failure.
		logs, logErr := framework.GetPodLogs(f.ClientSet, f.Namespace.Name, podName, fmt.Sprintf("%s-container", podName))
		if logErr != nil {
			framework.Failf("Error getting container logs: %s", logErr)
		}

		// Collect current NetworkPolicies applied in the test namespace.
		policies, err := f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).List(metav1.ListOptions{})
		if err != nil {
			framework.Logf("error getting current NetworkPolicies for %s namespace: %s", f.Namespace.Name, err)
		}

		// Collect the list of pods running in the test namespace.
		podsInNS, err := framework.GetPodsInNamespace(f.ClientSet, f.Namespace.Name, map[string]string{})
		if err != nil {
			framework.Logf("error getting pods for %s namespace: %s", f.Namespace.Name, err)
		}

		pods := []string{}
		for _, p := range podsInNS {
			pods = append(pods, fmt.Sprintf("Pod: %s, Status: %s\n", p.Name, p.Status.String()))
		}

		framework.Failf("Pod %s should be able to connect to endpoint %s, but was not able to connect.\nPod logs:\n%s\n\n Current NetworkPolicies:\n\t%v\n\n Pods:\n\t%v\n\n", podName, endPoint, logs, policies.Items, pods)

		// Dump debug information for the test namespace.
		framework.DumpDebugInfo(f.ClientSet, f.Namespace.Name)
	}
}

func testCannotConnectRds(f *framework.Framework, ns *v1.Namespace, podName string, endPoint string, port string, password string, dbName string, sgs []string) {
	By(fmt.Sprintf("Creating client pod %s that should not be able to connect to %s:%s.", podName, endPoint, port))
	podClient := createRdsClientPod(f, ns, podName, endPoint, port, password, dbName, sgs)
	defer func() {
		By(fmt.Sprintf("Cleaning up the pod %s", podName))
		if err := f.ClientSet.CoreV1().Pods(ns.Name).Delete(podClient.Name, nil); err != nil {
			framework.Failf("unable to cleanup pod %v: %v", podClient.Name, err)
		}
	}()

	framework.Logf("Waiting for %s to complete.", podClient.Name)
	err := framework.WaitForPodSuccessInNamespace(f.ClientSet, podClient.Name, ns.Name)

	// We expect an error here since it's a cannot connect test.
	// Dump debug information if the error was nil.
	if err == nil {
		// Collect pod logs when we see a failure.
		logs, logErr := framework.GetPodLogs(f.ClientSet, f.Namespace.Name, podName, fmt.Sprintf("%s-container", podName))
		if logErr != nil {
			framework.Failf("Error getting container logs: %s", logErr)
		}

		// Collect current NetworkPolicies applied in the test namespace.
		policies, err := f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).List(metav1.ListOptions{})
		if err != nil {
			framework.Logf("error getting current NetworkPolicies for %s namespace: %s", f.Namespace.Name, err)
		}

		// Collect the list of pods running in the test namespace.
		podsInNS, err := framework.GetPodsInNamespace(f.ClientSet, f.Namespace.Name, map[string]string{})
		if err != nil {
			framework.Logf("error getting pods for %s namespace: %s", f.Namespace.Name, err)
		}

		pods := []string{}
		for _, p := range podsInNS {
			pods = append(pods, fmt.Sprintf("Pod: %s, Status: %s\n", p.Name, p.Status.String()))
		}

		framework.Failf("Pod %s should not be able to connect to endpoint %s, but was able to connect.\nPod logs:\n%s\n\n Current NetworkPolicies:\n\t%v\n\n Pods:\n\t %v\n\n", podName, endPoint, logs, policies.Items, pods)

		// Dump debug information for the test namespace.
		framework.DumpDebugInfo(f.ClientSet, f.Namespace.Name)
	}
}

func createRdsClientPod(f *framework.Framework, namespace *v1.Namespace, podName string, endPoint string, port string, password string, dbName string, sgs []string) *v1.Pod {
	dbEnv := fmt.Sprintf("PGCONNECT_TIMEOUT=1 PGPASSWORD=%s", password)
	dbCmd := fmt.Sprintf("psql --host=%s --port=%s --username=master --dbname=%s -c 'select 1'",
		endPoint, port, dbName)

	sgsNew := []string{}
	for _, sg := range sgs {
		sgsNew = append(sgsNew, fmt.Sprintf(`"%s"`, sg))
	}

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
				"pod-name": podName,
			},
			Annotations: map[string]string{
				"aws.tigera.io/security-groups": fmt.Sprintf(`[%s]`, strings.Join(sgsNew, ", ")),
			},
		},
		// It could take time for anx controller to be notified with new sg or new rules and to render those into network policies.
		// Set the total test time for up to 30 seconds.
		Spec: v1.PodSpec{
			RestartPolicy: v1.RestartPolicyNever,
			Containers: []v1.Container{
				{
					Name:  fmt.Sprintf("%s-container", podName),
					Image: "launcher.gcr.io/google/postgresql9",
					Args: []string{
						"/bin/sh",
						"-c",
						// psql doesn't seem to be honoring the PGCONNECT_TIMEOUT so use timeout command to force it.
						fmt.Sprintf("for i in $(seq 1 10); do %s /usr/bin/timeout 1 %s && exit 0 || sleep 3; done; exit 1",
							dbEnv, dbCmd),
					},
				},
			},
		},
	}

	var err error
	var p *v1.Pod
	p, err = f.ClientSet.CoreV1().Pods(namespace.Name).Create(pod)

	// Hack for heptio-authenticator-aws where Unauthorized happens sometimes
	if status, ok := err.(*errors.StatusError); ok && status.ErrStatus.Message == "Unauthorized" {
		p, err = f.ClientSet.CoreV1().Pods(namespace.Name).Create(pod)
	}
	pod = p

	Expect(err).NotTo(HaveOccurred())

	return pod
}

func addSgAnnotation(pod *v1.Pod, sg string) {
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations["aws.tigera.io/security-groups"] = fmt.Sprintf("[\"%s\"]", sg)
}

func testCanConnectWithSgX(f *framework.Framework, ns *v1.Namespace, podName string, sg string, service *v1.Service, target string, podCustomizer func(pod *v1.Pod)) {
	testCanConnectX(f, ns, podName, service, target, func(pod *v1.Pod) {
		addSgAnnotation(pod, sg)
		podCustomizer(pod)
	}, func() {})
}

func testCanConnectWithSg(f *framework.Framework, ns *v1.Namespace, podName string, sg string, service *v1.Service, target string) {
	testCanConnectWithSgX(f, ns, podName, sg, service, target, func(pod *v1.Pod) {})
}

func testCannotConnectWithSgX(f *framework.Framework, ns *v1.Namespace, podName string, sg string, service *v1.Service, target string, podCustomizer func(pod *v1.Pod)) {
	testCannotConnectX(f, ns, podName, service, target,
		func(pod *v1.Pod) {
			addSgAnnotation(pod, sg)
		})
}

func testCannotConnectWithSg(f *framework.Framework, ns *v1.Namespace, podName string, sg string, service *v1.Service, target string) {
	testCannotConnectWithSgX(f, ns, podName, sg, service, target, func(pod *v1.Pod) {})
}
