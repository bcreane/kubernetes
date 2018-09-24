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
	"math/rand"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/aws"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

const (
	numSGs = 50
)

// The test runs Anx security group scale testing.
var _ = SIGDescribe("[Feature:Anx-SG-Scale] anx security group scale testing", func() {
	var awsctl *aws.Awsctl

	var podSgs []string

	// Go's RNG is not seeded by default.
	rand.Seed(time.Now().UTC().UnixNano())

	f := framework.NewDefaultFramework("e2e-anx-sgscale")

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

		// Cleanup and reinitialise podSg.
		for _, sg := range podSgs {
			err := awsctl.Client.DeleteVpcSG(sg)
			Expect(err).NotTo(HaveOccurred())
		}
		podSgs = nil
	})

	Context(fmt.Sprintf("%d SGs with rds instance", numSGs), func() {
		var sgRds, subnetGroup string
		var rds, endPoint, port string
		var portInt64 int64
		BeforeEach(func() {
			var err error

			By("Creating a security group for rds instance")
			sgRds, err = awsctl.Client.CreateVpcSG("sgRds", "SG for RDS instance 1 (scale test)")
			Expect(err).NotTo(HaveOccurred())

			By("Creating a db subnet group")
			subnetGroup, err = awsctl.Client.CreateDBSubnetGroup("sgE2E")
			Expect(err).NotTo(HaveOccurred())

			By("Creating a rds instance with SG and get endpoint address with SG")
			rds, endPoint, portInt64, err = awsctl.Client.CreateRDSInstance("sgE2E", subnetGroup, sgRds, RDSPassword, RDSDBName)
			Expect(err).NotTo(HaveOccurred())
			port = fmt.Sprint(portInt64)

			By("create SGs with default rules")
			for i := 0; i < numSGs; i++ {
				sg, err := awsctl.Client.CreateVpcSG(fmt.Sprintf("podSg%d", i), "SG for Pod (scale test - 1 pod 50 sg)")
				Expect(err).NotTo(HaveOccurred())
				podSgs = append(podSgs, sg)
			}

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
			}, 2*time.Minute).ShouldNot(HaveOccurred())
		})

		AfterEach(func() {
			// Revoke all ingress rules for sgRDS first.
			if sgRds != "" {
				err := awsctl.Client.RevokeSecurityGroupsIngress(sgRds)
				Expect(err).NotTo(HaveOccurred())

				for _, pSg := range podSgs {
					err := awsctl.Client.RevokeSecurityGroupsIngress(pSg)
					Expect(err).NotTo(HaveOccurred())
				}

			}

			// Cleanup rds instance and associated sg and subnet group.
			if rds != "" {
				err := awsctl.Client.DeleteRDSInstance(rds)
				Expect(err).NotTo(HaveOccurred())
			}
			if sgRds != "" {
				err := awsctl.Client.DeleteVpcSG(sgRds)
				Expect(err).NotTo(HaveOccurred())
			}
			if subnetGroup != "" {
				err := awsctl.Client.DeleteDBSubnetGroup(subnetGroup)
				Expect(err).NotTo(HaveOccurred())
			}
		})

		allowOneSGToRdsSG := func(podSG string) {
			By("Add ingress allow rule to RDS sg to allow traffic from one pod SG")
			err := awsctl.Client.AuthorizeSGIngressSrcSG(sgRds, "tcp", portInt64, portInt64, []string{sgRds, podSG})
			Expect(err).NotTo(HaveOccurred())

			By("Add ingress allow rule to one pod SG to allow traffic from RDS sg")
			err = awsctl.Client.AuthorizeSGIngressSrcSG(podSG, "tcp", 0, 65535, []string{sgRds})
			Expect(err).NotTo(HaveOccurred())
		}

		// runTest start numSGs goroutines to test connection from pods to rds instance.
		// Check that only the pod that has been assigned to the known allow sg can make connection to rds instance.
		runTest := func(allowSGIndex int) {
			var wg sync.WaitGroup
			for i := 0; i < numSGs; i++ {
				wg.Add(1)

				go func(index int) {
					defer GinkgoRecover()
					defer wg.Done()

					if index != allowSGIndex {
						By("client pod should not be able to access rds service if it is not assigned to an allow sg")
						testCannotConnectRds(f, f.Namespace, fmt.Sprintf("rds-client%d-deny", index), endPoint, port, RDSPassword, RDSDBName, []string{podSgs[index]})
					} else {
						By("client pod should be able to access rds service if it is assigned to an allow sg")
						testCanConnectRds(f, f.Namespace, fmt.Sprintf("rds-client%d-allow", index), endPoint, port, RDSPassword, RDSDBName, []string{podSgs[index]})
					}

				}(i)
				// This helps with running in EKS with heptio-authenticator-aws
				// (it was producing Unauthenticated errors)
				time.Sleep(time.Second)
			}

			// Wait for all test goroutines to complete.
			wg.Wait()

			// Wait for all pods been deleted.
			By("waiting for all test pods been deleted")
			framework.ExpectNoError(wait.Poll(time.Millisecond*100, time.Minute*5, func() (bool, error) {
				podClient := f.ClientSet.CoreV1().Pods(f.Namespace.Name)
				pods, err := podClient.List(metav1.ListOptions{})
				Expect(err).NotTo(HaveOccurred())
				for _, pod := range pods.Items {
					if strings.Contains(pod.Name, "rds-client") {
						return false, nil
					}
				}
				return true, nil
			}))
		}

		waitForSG := func() {
			// according to spec, it could take up to 2 minutes for a sg been implemented fully.
			// For our test, wait 90 seconds and pod will retry for 30 seconds.
			By("waiting for sg poll")
			time.Sleep(90 * time.Second)
		}

		It("should allow one pod with multiple sgs to connect rds instance when one SG has an allow rule", func() {
			randNum := rand.Intn(numSGs)
			oneSG := podSgs[randNum] // Select one random SG.

			By("client pod should not be able to access rds service")
			testCannotConnectRds(f, f.Namespace, "rds-client", endPoint, port, RDSPassword, RDSDBName, podSgs)

			allowOneSGToRdsSG(oneSG)
			waitForSG()

			By("client pod should be able to access rds service")
			testCanConnectRds(f, f.Namespace, "rds-client", endPoint, port, RDSPassword, RDSDBName, podSgs)

			By("testing aws controller restart")
			restartAwsController(f)
			waitForSG()

			By("client pod should be able to access rds service")
			testCanConnectRds(f, f.Namespace, "rds-client", endPoint, port, RDSPassword, RDSDBName, podSgs)
		})

		It("should allow one pod to connect rds instance when one SG has an ingress allow rule", func() {
			randNum := rand.Intn(numSGs)
			oneSG := podSgs[randNum] // Select one random SG.

			allowOneSGToRdsSG(oneSG)
			waitForSG()

			runTest(randNum)

			By("testing aws controller restart")
			restartAwsController(f)
			waitForSG()

			runTest(randNum)
		})

		It("should allow one pod to connect rds instance with one egress allow rule", func() {
			By("Remove all egress rules for pod SGs")
			for _, sg := range podSgs {
				err := awsctl.Client.RevokeSecurityGroupsEgress(sg)
				Expect(err).NotTo(HaveOccurred())
			}

			randNum := rand.Intn(numSGs)
			oneSG := podSgs[randNum] // Select one random SG.

			allowOneSGToRdsSG(oneSG)

			By("Add egress allow rule to one pod SG to allow traffic to RDS sg")
			err := awsctl.Client.AuthorizeSGEgressDstSG(oneSG, "tcp", portInt64, portInt64, []string{sgRds})
			Expect(err).NotTo(HaveOccurred())

			waitForSG()

			runTest(randNum)

			By("testing aws controller restart")
			restartAwsController(f)
			waitForSG()

			runTest(randNum)
		})
	})
})

func restartAwsController(f *framework.Framework) {
	// Get calico-node pod on the node.
	labelSelector := labels.SelectorFromSet(labels.Set(map[string]string{"k8s-app": "tigera-cloud-controllers"}))
	options := metav1.ListOptions{LabelSelector: labelSelector.String()}
	podClient := f.ClientSet.CoreV1().Pods("kube-system")

	// Get aws controller pod.
	pods, err := podClient.List(options)
	Expect(err).NotTo(HaveOccurred())
	Expect(pods.Items).To(HaveLen(1), "Failed to find tigera aws controller pod")
	pod := &pods.Items[0]

	// Delete it.
	By("deleting aws controller")
	err = podClient.Delete(pod.Name, metav1.NewDeleteOptions(0))
	Expect(err).NotTo(HaveOccurred())

	// Wait for it up again.
	By("waiting for aws controller to recover")
	framework.ExpectNoError(wait.Poll(time.Millisecond*100, time.Second*60, func() (bool, error) {
		pods, err := podClient.List(options)
		Expect(err).NotTo(HaveOccurred())
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp == nil && podutil.IsPodReady(&pod) {
				return true, nil
			}
		}
		return false, nil
	}))

	By("aws controller restarted")
}
