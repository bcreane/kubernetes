/*
Copyright (c) 2019 Tigera, Inc. All rights reserved.
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
	"strings"
	"time"

	"k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/test/e2e/framework"
	imageutils "k8s.io/kubernetes/test/utils/image"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = SIGDescribe("HybridEgress", func() {

	f := framework.NewDefaultFramework("hybrid-egress")
	var (
		jig            *framework.ServiceTestJig
		winNodeNames   []string
		winNodeIPs     []string
		linuxNodeNames []string
		linuxNodeIPs   []string
	)

	BeforeEach(func() {
		framework.Logf("BeforeEach for Hybrid Egress")
		jig = framework.NewServiceTestJig(f.ClientSet, "hybrid-egress")
		winNodes, linuxNodes := getHybridNodes(f, 2)
		if len(winNodes.Items) == 0 || len(linuxNodes.Items) == 0 {
			framework.Skipf("No nodes exist, can't continue test.")
		}
		if len(winNodes.Items) < 2 || len(winNodes.Items) < 2 {
			framework.Skipf("Less than two schedulable nodes exist, can't continue test.")
		}
		//validate windows nodes
		winNodeNames, winNodeIPs, _ = getNodesInfo(f, winNodes, true)
		Expect(len(winNodeNames)).To(Equal(2))
		Expect(len(winNodeIPs)).To(Equal(2))
		//validate linux nodes
		linuxNodeNames, linuxNodeIPs, _ = getNodesInfo(f, linuxNodes, true)
		Expect(len(linuxNodeNames)).To(Equal(2))
		Expect(len(linuxNodeIPs)).To(Equal(2))

		jig.Client = f.ClientSet
	})

	type egressTest struct {
		// Any tweak needed to the target Service definition.
		svcTweak func(svc *v1.Service)
		// dstPod is the number of the destination pod, as per the diagram above.
		dstPod int
		// True if the destination pod should be host-networked.
		dstHostNetworked bool
		// True if the source pod should be host-networked.
		srcHostNetworked bool
		// True if the Linux client needs to created.
		isLinuxClientNeeded bool
		// True if the Linux server needs to created.
		isLinuxServerNeeded bool
		// How to access the Service.
		accessType string
	}

	describeEgressTest := func(c egressTest) func() {
		return func() {
			var (
				client       interface{}
				target       string
				applyLabels  map[string]string
				expectSNAT   bool
				reachability string
			)

			expectAccessAllowed := func() {
				if expectSNAT {
					reachability = reachableWithSNAT
				} else {
					reachability = reachableWithoutSNAT
				}
				testHybridConnection(f, client, target, reachability, c.isLinuxClientNeeded)
			}

			expectAccessDenied := func() {
				testHybridConnection(f, client, target, notReachable, c.isLinuxClientNeeded)
			}

			BeforeEach(func() {
				// Whenever we connect back to the same pod, we need SNAT or the pod will drop the
				// packet as a martian.
				expectSNAT = c.dstPod == 0

				// We only start up a single target pod, but we place it either on node[0] or node[1] depending
				// on the test.
				var node string
				if c.dstPod <= 1 {
					// Pods 0 and 1 belong on the first node.
					if c.isLinuxServerNeeded == true {
						node = linuxNodeNames[0]
					} else {
						node = winNodeNames[0]
					}
				} else {
					// Pod 2 is on the second node.
					if c.isLinuxServerNeeded == true {
						node = linuxNodeNames[1]
					} else {
						node = winNodeNames[1]
					}
				}

				svcPort := 8080
				svcClusterIP, svcNodePort, dstPod := setupPodService(f, jig, node, svcPort, c.svcTweak, c.dstHostNetworked, c.isLinuxServerNeeded)
				// Figure out the correct target to pass to wget, depending on the destination and type of test.
				// We may also flip the expectSNAT flag here if the scenario requires it.
				switch c.accessType {
				case "cluster IP":
					target = fmt.Sprintf("%v:%v", svcClusterIP, svcPort)
					if c.dstHostNetworked {
						// TODO If the destination pod is host networked, then, by the time Calico
						// decides whether to do "NAT-outgoing", we'll see the destination as
						// outside the cluster.
						expectSNAT = true
					}
				case "node0 NodePort":
					expectSNAT = true
					target = fmt.Sprintf("%v:%v", winNodeIPs[0], svcNodePort)
				case "node1 NodePort":
					expectSNAT = true
					target = fmt.Sprintf("%v:%v", winNodeIPs[1], svcNodePort)
				case "node2 NodePort":
					expectSNAT = true
					target = fmt.Sprintf("%v:%v", linuxNodeIPs[1], svcNodePort)
				case "external IP":
					// External IP does not work properly for 1.10 and 1.9. It has been fixed in 1.11 by PR https://github.com/kubernetes/kubernetes/pull/63066.
					framework.SkipUnlessServerVersionGTE(serverVersion, f.ClientSet.Discovery())

					expectSNAT = true
					target = fmt.Sprintf("%v:%v", DEFAULT_EXTERNAL_IP, svcPort)
				case "pod IP":
					expectSNAT = false
					target = fmt.Sprintf("%v:%s", dstPod.Status.PodIP, "8080")
				case "svc DNS":
					expectSNAT = false
					target = fmt.Sprintf("http://%s.%s.svc.cluster.local:%s", jig.Name, f.Namespace.Name, "8080")
				default:
					panic("Unhandled accessType: " + c.accessType)
				}
				if c.dstPod == 0 {
					// We're doing a loopback test where the pod accesses itself via a service of some kind.
					// Use the target pod we just created as the source pod.
					applyLabels = jig.Labels
					client = dstPod
				} else {
					// We're not doing a loopback test so we need to make a pod0 too i.e. client-pod.
					applyLabels = map[string]string{"pod-name": "hybrid-egress-source"}
					//we need to create different lables for client and server pod, so that service can have only
					//one backend pod i.e server-pod.
					//take backup of server Pod labels to use it later to apply target-specific policy with this lable.
					serverPodLables := jig.Labels

					jig.Labels = applyLabels
					// Pod 2 is on the second node.
					if c.isLinuxClientNeeded == true {
						node = linuxNodeNames[0]
					} else {
						node = winNodeNames[0]
					}
					client = launchEchoserverPodForHybrid(jig, f, node, "hybrid-egress-source", c.srcHostNetworked, c.isLinuxClientNeeded)
					//reassign lable to allow traffic on server for target-specific policy
					jig.Labels = serverPodLables
				}
			})

			It("should correctly implement NetworkPolicy [Feature:WindowsHybridPolicy]", func() {
				By("allowing connection with no NetworkPolicy")
				expectAccessAllowed()
				//skipping default deny and target-specific policy for windows, we can enable this code later
				if os.Getenv("WINDOWS_OS") == "" {

					By("denying traffic after installing a default-deny policy")
					defaultDenyPolicy := &networkingv1.NetworkPolicy{
						ObjectMeta: metav1.ObjectMeta{
							Name: "egress-default-deny",
						},
						Spec: networkingv1.NetworkPolicySpec{
							// Apply this policy to the source (pod0).
							PodSelector: metav1.LabelSelector{
								MatchLabels: applyLabels,
							},
							// Say that it's an egress policy, but don't allow any
							// destinations; that has the effect of being a default deny.
							PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
						},
					}
					defaultDenyPolicy, err := f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).Create(defaultDenyPolicy)
					Expect(err).NotTo(HaveOccurred())
					expectAccessDenied()

					if c.dstHostNetworked {
						By("Skipping policy tests for host networked target - not implemented")
						return
					}
					if c.accessType == "node1 NodePort" {
						By("Skipping policy tests for remote node port - NAT happens on other node")
						return
					}
					if c.accessType == "external IP" {
						By("Skipping policy tests for external IP - we fail to detect the forwarding")
						return
					}
					By("allowing traffic after installing a target-specific policy")
					// Configure policy for pod0 to allow egress to specific target.
					targetAccessPolicy := &networkingv1.NetworkPolicy{
						ObjectMeta: metav1.ObjectMeta{
							Name: "egress-target-access",
						},
						Spec: networkingv1.NetworkPolicySpec{
							// Apply this policy to the source (pod0) i.e. client-pod.
							PodSelector: metav1.LabelSelector{
								MatchLabels: applyLabels,
							},
							// Say that it's an egress policy.
							PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
							// Allow traffic only to the target service.
							Egress: []networkingv1.NetworkPolicyEgressRule{{
								To: []networkingv1.NetworkPolicyPeer{{
									PodSelector: &metav1.LabelSelector{
										MatchLabels: jig.Labels,
									},
								}},
							}},
						},
					}

					targetAccessPolicy, err = f.ClientSet.NetworkingV1().NetworkPolicies(f.Namespace.Name).Create(targetAccessPolicy)
					Expect(err).NotTo(HaveOccurred())
					expectAccessAllowed()
					By("denying traffic after removing the target-specific policy")
					cleanupNetworkPolicy(f, targetAccessPolicy)
					targetAccessPolicy = nil
					expectAccessDenied()
					By("allowing traffic after removing the default-deny policy")
					cleanupNetworkPolicy(f, defaultDenyPolicy)
					defaultDenyPolicy = nil
					expectAccessAllowed()
				}
			})
		}
	}
	//check connectivity from linux pod to windows pod
	Context("scenario-0C0: linux-pod0 -> pod IP -> win-pod1",
		describeEgressTest(egressTest{dstPod: 1, isLinuxClientNeeded: true, accessType: "pod IP"}))
	Context("scenario-0C1: linux-pod0 -> cluster IP -> win-pod1",
		describeEgressTest(egressTest{dstPod: 1, isLinuxClientNeeded: true, accessType: "cluster IP"}))
	Context("scenario-0C2: linux-pod0 -> win-node0 NodePort -> win-pod1",
		describeEgressTest(egressTest{dstPod: 1, isLinuxClientNeeded: true, accessType: "node0 NodePort"}))
	//Context("scenario-0C3: linux-pod0 -> win-node1 NodePort -> win-pod1",
	//	describeEgressTest(egressTest{dstPod: 1, isLinuxClientNeeded: true, accessType: "node1 NodePort"}))

	//check connectivity from windows pod to linux pod
	Context("scenario-0C4: win-pod0 -> pod IP -> linux-pod1",
		describeEgressTest(egressTest{dstPod: 1, isLinuxServerNeeded: true, accessType: "cluster IP"}))
	Context("scenario-0C5: win-pod0 -> cluster IP -> linux-pod1",
		describeEgressTest(egressTest{dstPod: 1, isLinuxServerNeeded: true, accessType: "cluster IP"}))
	Context("scenario-0C6: win-pod0 -> svc-DNS -> linux-pod1",
		describeEgressTest(egressTest{dstPod: 1, isLinuxServerNeeded: true, accessType: "svc DNS"}))
	Context("scenario-0C7: win-pod0 -> win-node0 NodePort -> linux-pod1",
		describeEgressTest(egressTest{dstPod: 1, isLinuxServerNeeded: true, accessType: "node0 NodePort"}))
	//NOT WORKING for now , hence commented
	//Context("scenario-0C8: win-pod0 -> win-node1 NodePort -> linux-pod1",
	//	describeEgressTest(egressTest{dstPod: 1, isLinuxServerNeeded: true, accessType: "node1 NodePort"}))
	Context("scenario-0C9: win-pod0 -> linux-node2 NodePort -> linux-pod1",
		describeEgressTest(egressTest{dstPod: 1, isLinuxServerNeeded: true, accessType: "node2 NodePort"}))

	//check connectivity from linux host to windows pod
	//connection is going through even if deny-policy has in-place, hence commenting for now
	Context("scenario-0H1: (host-networked) linux-pod0 -> pod IP -> win-pod1",
		describeEgressTest(egressTest{dstPod: 1, srcHostNetworked: true, isLinuxClientNeeded: true, accessType: "pod IP"}))
	Context("scenario-0H2: (host-networked) linux-pod0 -> cluster IP -> win-pod1",
		describeEgressTest(egressTest{dstPod: 1, srcHostNetworked: true, isLinuxClientNeeded: true, accessType: "cluster IP"}))
	Context("scenario-0H3: (host-networked) linux-pod0 ->  win-node0 NodePort -> win-pod1",
		describeEgressTest(egressTest{dstPod: 1, srcHostNetworked: true, isLinuxClientNeeded: true, accessType: "node0 NodePort"}))
	//NOT WORKING for now , hence commented
	//Context("scenario-0H4: (host-networked) linux-pod0 ->  win-node1 NodePort -> win-pod1",
	//	describeEgressTest(egressTest{dstPod: 1, srcHostNetworked: true, isLinuxClientNeeded: true, accessType: "node1 NodePort"}))
})

func setupPodService(f *framework.Framework, jig *framework.ServiceTestJig,
	nodeName string,
	svcPort int,
	tweak func(svc *v1.Service),
	dstHostNetworked bool,
	isLinuxPod bool,
) (string, int, *v1.Pod) {
	serviceName := jig.Name
	By("creating a TCP service " + serviceName + " in namespace " + f.Namespace.Name + ".")
	svc := jig.CreateTCPServiceWithPort(f.Namespace.Name, func(svc *v1.Service) {
		svc.Spec.Type = v1.ServiceTypeNodePort
		if tweak != nil {
			tweak(svc)
		}

	}, int32(svcPort))
	jig.SanityCheckService(svc, v1.ServiceTypeNodePort)
	svcIP := svc.Spec.ClusterIP
	svcNodePort := int(svc.Spec.Ports[0].NodePort)

	podName := jig.Name
	By("Creating a backend server pod " + podName + " which echoes back source ip.")
	pod := launchEchoserverPodForHybrid(jig, f, nodeName, podName, dstHostNetworked, isLinuxPod)
	// Waiting for service to expose endpoint.
	framework.ValidateEndpointsOrFail(f.ClientSet, f.Namespace.Name, serviceName, framework.PortsByPodName{podName: {svcPort}})

	return svcIP, svcNodePort, pod
}
func testHybridConnection(f *framework.Framework, client interface{}, target string, reachability string, isLinuxPod bool) {
	var execPod *v1.Pod
	var shell, opt, cmd string
	switch src := client.(type) {
	case *source:
		// Create a scratch pod to test the connection.
		execPod = createExecPodOrFail(f, src)
		defer cleanupExecPodOrFail(f, execPod)

	case *v1.Pod:
		// Use the pod that the caller has given us.
		execPod = src
	default:
		panic("Unhandled client type")
	}

	completedAttempts := 0
	startTime := time.Now()
	reason := "<unknown>"
	for completedAttempts <= 1 || time.Since(startTime) < 30*time.Second {
		if completedAttempts > 0 {
			framework.Logf("Retrying connectivity check...")
			time.Sleep(time.Second)
		}
		// First, do the connectivity check.
		if os.Getenv("WINDOWS_OS") != "" && isLinuxPod != true {
			framework.Logf("Checking connectivity with 'Invoke-Webrequest %v'", target)
			cmd = fmt.Sprintf("try {Invoke-Webrequest %v -TimeoutSec 2 -UseBasicParsing -DisableKeepAlive} catch { echo failed; exit 1 }; exit 0 ;", target)
			shell = "powershell.exe"
			opt = "-Command"
		} else {
			framework.Logf("Checking connectivity with 'wget %v'", target)
			cmd = fmt.Sprintf("wget -T 2 %v -O - | grep \"Connection Succeeded\" && exit 0 || exit 1", target)
			shell = "/bin/sh"
			opt = "-c"
		}
		stdout, err := framework.RunKubectl(
			"exec",
			fmt.Sprintf("--namespace=%v", execPod.Namespace),
			execPod.Name, "-c", "exec",
			"--",
			shell, opt, cmd)
		framework.Logf("Connected to: PodIP %s, output %s.", execPod.Status.PodIP, stdout)
		completedAttempts++

		// Then, figure out what the result means...
		if reachability != notReachable {
			if err != nil {
				// Expected a connection but it failed.
				reason = "Failure: Connection unexpectedly failed."
				framework.Logf(reason)
				continue
			}
			if os.Getenv("WINDOWS_OS") == "" {
				// Desired stdout in this format: "client_address=x.x.x.x\n"
				outputs := strings.Split(strings.TrimSpace(stdout), "=")
				if len(outputs) != 2 {
					reason = fmt.Sprintf("exec pod returned unexpected stdout format: [%s]\n", stdout)
					framework.Logf(reason)
					continue
				}
				if !execPod.Spec.HostNetwork && reachability == reachableWithoutSNAT {
					// Verify observed source IP if exec pod is not running in host network namespace
					// and we don't expect any SNAT in the data path.  With exec pod running in host
					// network namespace and the destination IP is a virtual IP (service IP), the source
					// IP that the destination sees may be different from the exec pod IP.  For
					// instance, If the host happens to have a local IP 10.x.x.x which is closer to
					// service IP 10.100.x.x than pod IP 192.168.x.x, this 10.x.x.x may be used by
					// kernel as source IP.
					sourceIP := outputs[1]
					if sourceIP != execPod.Status.PodIP {
						reason = "Failure: the server saw incorrect source IP, pod IP was unexpectedly SNATed."
						framework.Logf(reason)
						// We allow retries for this because there seems to be a race in kube-proxy's programming
						// that sometimes results in connectivity before NAT is in place.
						continue
					}
				}
			}
			return // Success!
		} else {
			if err == nil {
				reason = "Failure: Connection unexpectedly suceeded."
				framework.Logf(reason)
				continue
			}

			return // Success!
		}
	}

	Fail("Failed to establish expected connectivity after retries: " + reason)
}

//This function is duplicated from GetNodes() to get hybrid cluster nodes
// getHybridNodes returns the first maxNodesForTest nodes. Useful in large clusters
// where we don't eg: want to create an endpoint per node.
func getHybridNodes(f *framework.Framework, maxNodesForTest int) (*v1.NodeList, *v1.NodeList) {
	nodes := framework.GetReadySchedulableNodesOrDie(f.ClientSet)
	winNodeList := &v1.NodeList{}
	linuxNodeList := &v1.NodeList{}
	for _, node := range nodes.Items {
		//check for OS type of node to get OS specific list
		if strings.Contains(node.Status.NodeInfo.OSImage, "Windows") {
			winNodeList.Items = append(winNodeList.Items, node)
		} else {
			linuxNodeList.Items = append(linuxNodeList.Items, node)
		}
	}
	return winNodeList, linuxNodeList
}

//This function is duplicated from LaunchEchoserverPodOnNode() for win-->linux connection testing
// LaunchEchoserverPod launches a pod serving http on port 8091 to act
// as the target for source IP preservation test. The client's source ip would
// be echoed back by the web server.
func launchEchoserverPodForHybrid(j *framework.ServiceTestJig, f *framework.Framework, nodeName string, podName string, hostNetwork bool, isLinuxPod bool) *v1.Pod {
	fmt.Printf("Creating echo server pod %q in namespace %q", podName, f.Namespace.Name)
	pod := newEchoServerPodSpecForHybrid(podName, hostNetwork, isLinuxPod)
	pod.Spec.NodeName = nodeName
	for k, v := range j.Labels {
		pod.ObjectMeta.Labels[k] = v
	}
	podClient := f.ClientSet.CoreV1().Pods(f.Namespace.Name)
	_, err := podClient.Create(pod)
	framework.ExpectNoError(err)
	framework.ExpectNoError(f.WaitForPodRunning(podName))
	fmt.Printf("Echo server pod %q in namespace %q running", pod.Name, f.Namespace.Name)
	pod, err = f.ClientSet.Core().Pods(f.Namespace.Name).Get(podName, metav1.GetOptions{})
	framework.ExpectNoError(err)
	return pod
}

// This function is copied from newEchoServerPodSpec() for linux-->windows testing
// newEchoServerPodSpecForHybrid returns the pod spec of echo server pod
func newEchoServerPodSpecForHybrid(podName string, hostNetwork bool, isLinuxPod bool) *v1.Pod {
	var port int
	var clientImage, serverImage string
	var commandStr []string
	var nodeselector = map[string]string{}
	hostNet := false
	var env []v1.EnvVar
	one := int64(1)
	winVer := os.Getenv("WINDOWS_OS")
	if winVer != "" && isLinuxPod != true {
		port = 8080
		clientImage = "caltigera/hostexec:" + winVer
		serverImage = "caltigera/porter:" + winVer
		nodeselector["beta.kubernetes.io/os"] = "windows"
		env = []v1.EnvVar{
			{
				Name:  fmt.Sprintf("SERVE_PORT_%d", port),
				Value: "Connection Succeeded",
			},
		}
	} else {
		port = 8091
		hostNet = hostNetwork
		clientImage = "busybox"
		serverImage = imageutils.GetE2EImage(imageutils.EchoServer)
		nodeselector["beta.kubernetes.io/os"] = "linux"
		commandStr = []string{"/bin/sh", "-c", "trap \"echo Stopped; exit 0\" INT TERM EXIT; sleep 1000000"}
		env = []v1.EnvVar{}
	}
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
					Image:   clientImage,
					Command: commandStr,
				},
				{
					Name:  "echoserver",
					Image: serverImage,
					Env:   env,
					Ports: []v1.ContainerPort{{ContainerPort: int32(port)}},
				},
			},
			RestartPolicy:                 v1.RestartPolicyNever,
			NodeSelector:                  nodeselector,
			HostNetwork:                   hostNet,
			TerminationGracePeriodSeconds: &one, // Speed up pod termination.
		},
	}
	return pod
}
