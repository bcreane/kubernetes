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

	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/aws"

	"k8s.io/api/core/v1"
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

	f := framework.NewDefaultFramework("e2e-anx-sg")

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
		// Cleanup AWS resources created for the test.
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

	Context("with rds instance", func() {
		BeforeEach(func() {
			var err error

			By("Creating a security group (allow SG) for rds instance")
			sgRds, err = awsctl.Client.CreateVpcSG("sgRds", "SG for RDS instance 1")
			Expect(err).NotTo(HaveOccurred())

			By("Creating a db subnet group")
			subnetGroup, err = awsctl.Client.CreateDBSubnetGroup("sgE2E")
			Expect(err).NotTo(HaveOccurred())

			By("Creating a rds instance with allow SG and get endpoint address with allow SG")
			rds, endPoint, portInt64, err = awsctl.Client.CreateRDSInstance("sgE2e", subnetGroup, sgRds, RDSPassword, RDSDBName)
			Expect(err).NotTo(HaveOccurred())
			port = fmt.Sprint(portInt64)

			By("Add rules to allow SG to allow traffic to rds instances")
			err = awsctl.Client.AuthorizeSGIngressSrcSG(sgRds, "tcp", portInt64, portInt64, []string{sgRds, awsctl.Config.TrustSG})
			Expect(err).NotTo(HaveOccurred())
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
			testCannotConnectRds(f, f.Namespace, "rds-client", endPoint, port, RDSPassword, RDSDBName, sgDeny)

			By("client pod in allow sg should be able to access rds service")
			testCanConnectRds(f, f.Namespace, "rds-client", endPoint, port, RDSPassword, RDSDBName, sgRds)
		})

	})
})

func testCanConnectRds(f *framework.Framework, ns *v1.Namespace, podName string, endPoint string, port string, password string, dbName string, sg string) {
	By(fmt.Sprintf("Creating client pod %s that should successfully connect to %s:%s.", podName, endPoint, port))
	podClient := createRdsClientPod(f, ns, podName, endPoint, port, password, dbName, sg)
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

func testCannotConnectRds(f *framework.Framework, ns *v1.Namespace, podName string, endPoint string, port string, password string, dbName string, sg string) {
	By(fmt.Sprintf("Creating client pod %s that should not be able to connect to %s:%s.", podName, endPoint, port))
	podClient := createRdsClientPod(f, ns, podName, endPoint, port, password, dbName, sg)
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

func createRdsClientPod(f *framework.Framework, namespace *v1.Namespace, podName string, endPoint string, port string, password string, dbName string, sg string) *v1.Pod {
	dbCmd := fmt.Sprintf("PGCONNECT_TIMEOUT=3 PGPASSWORD=%s psql --host=%s --port=%s --username=master --dbname=%s -c 'select 1'",
		password, endPoint, port, dbName)
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
				"pod-name": podName,
			},
			Annotations: map[string]string{
				"aws.tigera.io/security-groups": fmt.Sprintf(`["%s"]`, sg),
			},
		},
		Spec: v1.PodSpec{
			RestartPolicy: v1.RestartPolicyNever,
			Containers: []v1.Container{
				{
					Name:  fmt.Sprintf("%s-container", podName),
					Image: "launcher.gcr.io/google/postgresql9",
					Args: []string{
						"/bin/sh",
						"-c",
						fmt.Sprintf("for i in $(seq 1 5); do %s && exit 0 || sleep 3; done; exit 1", dbCmd),
					},
				},
			},
		},
	}

	var err error
	pod, err = f.ClientSet.CoreV1().Pods(namespace.Name).Create(pod)

	Expect(err).NotTo(HaveOccurred())

	return pod
}
