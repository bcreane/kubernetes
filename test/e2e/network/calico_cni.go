/*
Copyright (c) 2017-2018 Tigera, Inc. All rights reserved.
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
	"os"
	"strconv"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = SIGDescribe("CALICO-CNI", func() {

	// Test calico CNI while working with various cloud platforms.
	f := framework.NewDefaultFramework("calico-cni")
	var (
		jig             *framework.ServiceTestJig
		nodeNames       []string
		testNode        string
		initialPods     *v1.PodList
		initialPodCount int
		initialIPCount  int

		ipSet map[string]struct{}
	)

	checkAndAddIP := func(pod *v1.Pod) {
		ip := pod.Status.PodIP
		Expect(ipSet).ShouldNot(HaveKey(ip))
		ipSet[ip] = struct{}{}
	}

	BeforeEach(func() {
		framework.Logf("BeforeEach for calico cni test")

		// borrow some of the util functions from service e2e
		jig = framework.NewServiceTestJig(f.ClientSet, "calico-cni")
		nodes := jig.GetNodes(2)
		if len(nodes.Items) == 0 {
			framework.Skipf("No schedulable nodes exist, can't continue test.")
		}

		nodeNames, _, _ = getNodesInfo(f, nodes, false)
		Expect(len(nodeNames)).Should(BeNumerically(">", 0))

		testNode = nodeNames[0]
		initialPods = podsOnNodes(f, testNode)
		initialPodCount = len(initialPods.Items)

		By("Check initial pod ips and setup ipset")
		ipSet = map[string]struct{}{}
		for _, pod := range initialPods.Items {
			// Host networked pods all have same ip
			ip := pod.Status.PodIP
			ipSet[ip] = struct{}{}
		}
		initialIPCount = len(ipSet)

		framework.Logf("Node for testing <%s>, with %d pods %d IPs", testNode, initialPodCount, initialIPCount)
	})

	Context("CNI with Max IP per Node", func() {
		var maxPods int
		var maxTestPods int
		var testPods []*v1.Pod

		BeforeEach(func() {
			testPods = []*v1.Pod{}

			if maxIPStr := os.Getenv("MAX_IP_PER_NODE"); maxIPStr != "" {
				maxIP, err := strconv.Atoi(maxIPStr)
				Expect(err).NotTo(HaveOccurred())
				Expect(maxIP).Should(BeNumerically(">", initialIPCount))

				maxPods = maxIP
				maxTestPods = maxIP - initialIPCount

				framework.Logf("Use maxPods %d, maxTestPods %d", maxPods, maxTestPods)
			} else {
				framework.Skipf("No Env for MAX_IP_PER_NODE. Skip test.")
			}
		})

		AfterEach(func() {
			By("Cleaning up test pods.")
			for _, pod := range testPods {
				if pod != nil {
					if err := f.ClientSet.CoreV1().Pods(pod.Namespace).Delete(pod.Name, nil); err != nil {
						framework.Failf("unable to cleanup pod %v: %v", pod.Name, err)
					}
				}
			}
			Eventually(func() bool {
				pods, err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).List(metav1.ListOptions{})
				Expect(err).NotTo(HaveOccurred())
				return len(pods.Items) == 0
			}, 3*time.Minute, 1*time.Second).Should(BeTrue())
		})

		It("should correctly create/delete pods [Feature:Azure-IPAM]", func() {
			By("start to create max pods on node")
			for i := 0; i < maxTestPods; i++ {
				pod := createEchoserverPodOnNode(f, testNode, calico.GenerateRandomName(fmt.Sprintf("azure-ipam-%d", i)), false, true)
				framework.Logf("created pod %s and it is running, ip %s", pod.Name, pod.Status.PodIP)
				checkAndAddIP(pod)
				testPods = append(testPods, pod)
			}

			By("Should not be able to create a new pod on node")
			noIPPod := createEchoserverPodOnNode(f, testNode, calico.GenerateRandomName("azure-ipam-no-ip"), false, false)
			testPods = append(testPods, noIPPod)
			framework.Logf("created pod %s, check for status", noIPPod.Name)
			Eventually(func() bool {
				pod, err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).Get(noIPPod.Name, metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
				return pod.Status.Phase == v1.PodFailed
			}, 2*time.Minute, 1*time.Second).Should(BeTrue())

			By("Remove first test pod completely and release one IP")
			ipAvailable := testPods[0].Status.PodIP
			err := f.ClientSet.CoreV1().Pods(testPods[0].Namespace).Delete(testPods[0].Name, nil)
			Expect(err).NotTo(HaveOccurred())
			err = f.WaitForPodNotFound(testPods[0].Name, 2*time.Minute)
			Expect(err).NotTo(HaveOccurred())
			testPods[0] = nil

			By("Create another pod and it should be able to grab IP and run")
			ipPod := createEchoserverPodOnNode(f, testNode, calico.GenerateRandomName("azure-ipam-got-ip"), false, false)
			testPods = append(testPods, ipPod)
			err = f.WaitForPodRunning(ipPod.Name)
			Expect(err).NotTo(HaveOccurred())
			pod, err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).Get(ipPod.Name, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(pod.Status.PodIP).To(Equal(ipAvailable))
		})
	})
})

// checkPodEvent checks if we got an event for pod with a substring.
func checkPodEvent(f *framework.Framework, pod *v1.Pod, eventString string) bool {
	selector := fields.Set{
		"involvedObject.kind":      "Pod",
		"involvedObject.name":      pod.Name,
		"involvedObject.namespace": f.Namespace.Name,
	}.AsSelector().String()
	options := metav1.ListOptions{FieldSelector: selector}

	// Get current events
	events, err := f.ClientSet.CoreV1().Events(f.Namespace.Name).List(options)
	Expect(err).NotTo(HaveOccurred())

	for _, event := range events.Items {
		framework.Logf("Pod %s got event { %s }", pod.Name, event.Message)
		if strings.Contains(event.Message, eventString) {
			return true
		}
	}

	return false
}

// podsOnNodes returns pod list on a node.
func podsOnNodes(f *framework.Framework, nodeName string) *v1.PodList {
	fieldSelector := fields.SelectorFromSet(fields.Set{"spec.nodeName": nodeName})
	options := metav1.ListOptions{FieldSelector: fieldSelector.String()}

	pods, err := f.ClientSet.CoreV1().Pods(v1.NamespaceAll).List(options)
	Expect(err).NotTo(HaveOccurred())

	return pods
}

// newEchoServerPodSpec returns the pod spec of echo server pod. This is copied over from framework/service_util.go.
func newEchoServerPodSpec(podName string, hostNetwork bool) *v1.Pod {
	port := 8091
	one := int64(1)
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   podName,
			Labels: map[string]string{"pod-name": podName},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				// Include a busybox container in the echo server.  This is useful for doing loopback tests, where
				// a service pod accesses itself via the service cluster IP, for example.  We make it the first
				// container so `kubectl exec` will choose it by default.
				{
					Name:    "exec",
					Image:   "busybox",
					Command: []string{"sh", "-c", "trap \"echo Stopped; exit 0\" INT TERM EXIT; sleep 1000000"},
				},
				{
					Name:  "echoserver",
					Image: "gcr.io/google_containers/echoserver:1.6",
					Ports: []v1.ContainerPort{{ContainerPort: int32(port)}},
				},
			},
			RestartPolicy:                 v1.RestartPolicyNever,
			HostNetwork:                   hostNetwork,
			TerminationGracePeriodSeconds: &one, // Speed up pod termination.
		},
	}
	return pod
}

// createchoserverPodOnNode launches a pod serving http on port 8091 to act
// as the target for source IP preservation test. The client's source ip would
// be echoed back by the web server. This function is similar to framework/service_util.go/LauchEchoserverPodOnNode.
func createEchoserverPodOnNode(f *framework.Framework, nodeName, podName string, hostNetwork bool, waitRunning bool) *v1.Pod {
	framework.Logf("Creating echo server pod %q in namespace %q", podName, f.Namespace.Name)
	pod := newEchoServerPodSpec(podName, hostNetwork)
	pod.Spec.NodeName = nodeName

	podClient := f.ClientSet.Core().Pods(f.Namespace.Name)
	_, err := podClient.Create(pod)
	Expect(err).NotTo(HaveOccurred())

	if waitRunning {
		err = f.WaitForPodRunning(podName)
		Expect(err).NotTo(HaveOccurred())
		framework.Logf("Echo server pod %q in namespace %q running", pod.Name, f.Namespace.Name)
	}

	pod, err = f.ClientSet.Core().Pods(f.Namespace.Name).Get(podName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())
	return pod
}
