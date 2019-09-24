package ids

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/olivere/elastic"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/api/core/v1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"

	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"
)

var suspiciousDomains = []string{"test00.malware.tigera.net", "test01.malware.tigera.net", "tëst02.malware.tigera.net"}

var _ = SIGDescribe("[Feature:CNX-v3-SuspiciousIPs][Feature:EE-v2.4]", func() {
	var f = framework.NewDefaultFramework("cnx-suspicious-ips")
	var err error
	var pods *v1.PodList
	var kubectl *calico.Kubectl

	Context("Suspicious IP security events.", func() {
		var client *elastic.Client
		BeforeEach(func() {
			client = InitClient(GetURI())
			WaitForElastic(context.Background(), client)
			calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_FLOWLOGSFLUSHINTERVAL", "10")
			calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_FLOWLOGSFILEAGGREGATIONKINDFORALLOWED", "1")
			calico.RestartCalicoNodePods(f.ClientSet, "")

			pods = createSuspiciousIPsCfg(f, kubectl)
		})
		AfterEach(func() {
			DeleteIndices(client)
			calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_FLOWLOGSFLUSHINTERVAL", "300")
			calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_FLOWLOGSFILEAGGREGATIONKINDFORALLOWED", "2")
			calico.RestartCalicoNodePods(f.ClientSet, "")

			err = kubectl.Delete("globalthreatfeed.projectcalico.org", "", "global-threat-feed", "")
			Expect(err).To(BeNil())
		})

		It("Generate traffic to suspicious ips and verify security events have been created.", func() {
			By("Polling the GlobalNetworkSet to check that it's been created.")
			globalNetworkSetName := "threatfeed" + "." + "global-threat-feed"
			checkGlobalNetworkSet(kubectl, globalNetworkSetName)

			By("Creating clients to ICMP ping the blacklist server pods.")
			for _, pod := range pods.Items {
				icmpPod, err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).Get(pod.Name, meta_v1.GetOptions{})
				Expect(err).To(BeNil())

				pingClient := "client-can-ping-" + icmpPod.Name
				framework.Logf("This is the pingClient: %v", pingClient)
				calico.TestCanPing(f, f.Namespace, pingClient, icmpPod)
			}

			By("Waiting for blacklist server IPs to be indexed in tigera_secure_ee_events*")
			var searchKey = "dest_ip"
			for _, pod := range pods.Items {
				framework.Logf("Searching for %s: %s in at least one tigera_secure_ee_events* record", searchKey, pod.Status.PodIP)
				CheckSearchEvents(client, "tigera_secure_ee_events*", searchKey, pod.Status.PodIP)
			}
		})
	})
})

var _ = SIGDescribe("[Feature:CNX-v3-SuspiciousDomains][Feature:EE-v2.6]", func() {
	var f = framework.NewDefaultFramework("cnx-suspicious-domains")
	var err error
	var kubectl *calico.Kubectl

	Context("Suspicious DNS security events.", func() {
		var client *elastic.Client
		BeforeEach(func() {
			client = InitClient(GetURI())
			WaitForElastic(context.Background(), client)
			calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_DNSLOGSFILEENABLED", "true")
			calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_DNSLOGSFLUSHINTERVAL", "10")
			calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_DNSLOGSFILEAGGREGATIONKIND", "0")
			calico.RestartCalicoNodePods(f.ClientSet, "")

			createSuspiciousDomainsThreatFeed(f, kubectl)
		})
		AfterEach(func() {
			DeleteIndices(client)
			calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_DNSLOGSFLUSHINTERVAL", "300")
			calico.SetCalicoNodeEnvironmentWithRetry(f.ClientSet, "FELIX_DNSLOGSFILEAGGREGATIONKIND", "1")
			calico.RestartCalicoNodePods(f.ClientSet, "")

			err = kubectl.Delete("globalthreatfeed.projectcalico.org", "", "global-threat-feed", "")
			Expect(err).To(BeNil())
		})

		It("Generate queries to suspicious domains and verify security events have been created.", func() {

			By("Creating clients to query the suspicious domains")
			calico.TestDNSQuery(f, f.Namespace, "dns-querier", suspiciousDomains)

			By("Waiting for detection of DNS queries in tigera_secure_ee_events*")
			var searchKey = "suspicious_domains"
			for _, domain := range suspiciousDomains {
				framework.Logf("Searching for %s: %s in at least one tigera_secure_ee_events* record", searchKey, domain)
				CheckSearchEvents(client, "tigera_secure_ee_events*", searchKey, domain)
			}
		})
	})
})

func createSuspiciousIPsCfg(f *framework.Framework, kubectl *calico.Kubectl) *v1.PodList {
	identifierKey := "identifier"
	podServerA, serviceA := calico.CreateServerPodAndServiceWithLabels(f, f.Namespace, "server-a", []int{80}, map[string]string{identifierKey: "server-blacklist"})
	podServerB, serviceB := calico.CreateServerPodAndServiceWithLabels(f, f.Namespace, "server-b", []int{80}, map[string]string{identifierKey: "server-blacklist"})
	podServerC, serviceC := calico.CreateServerPodAndServiceWithLabels(f, f.Namespace, "server-c", []int{80}, map[string]string{identifierKey: "server-blacklist"})

	framework.Logf("podServerA:serviceA: %v:%v", podServerA.Name, serviceA.Name)
	framework.Logf("podServerB:serviceB: %v:%v", podServerB.Name, serviceB.Name)
	framework.Logf("podServerC:serviceC: %v:%v", podServerC.Name, serviceC.Name)

	By("Collect all pods that have the label server-blacklist.")
	labelSelector := fields.SelectorFromSet(fields.Set(map[string]string{identifierKey: "server-blacklist"})).String()
	options := meta_v1.ListOptions{LabelSelector: labelSelector}

	pods, err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).List(options)
	Expect(err).To(BeNil())

	for _, pod := range pods.Items {
		err = framework.WaitForPodRunningInNamespace(f.ClientSet, &pod)
		Expect(err).To(BeNil())
	}

	pods, err = f.ClientSet.CoreV1().Pods(f.Namespace.Name).List(options)
	Expect(err).To(BeNil())

	By("Collect all podIPs that have the label server-blacklist.")
	var blacklistIPs []string
	for _, pod := range pods.Items {
		framework.Logf("Creating client pod %s that has a pod IP of: %s", f.Namespace.Name, pod.Status.PodIP)
		blacklistIPs = append(blacklistIPs, pod.Status.PodIP)
	}

	// Convert blacklistIPs into a string separated by newlines
	blacklistIPStr := strings.Join(blacklistIPs, "\n")
	blacklistIPStrConv := strconv.QuoteToASCII(blacklistIPStr)
	framework.Logf("blacklistIPStrConv is: %s", blacklistIPStrConv)

	configmapDeploymentServiceStr := fmt.Sprintf(`
---
apiVersion: v1
kind: ConfigMap
data:
  nginx-ip-blacklist.conf: |
    server {
        location / {
            return 200 %s;
            add_header Content-Type text/plain;
        }
    }
metadata:
  name: ip-blacklist-configmap
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ip-blacklist-deployment
  labels:
    app: nginx
spec:
  replicas: 2
  selector:
    matchLabels:
      app: blacklist
  template:
    metadata:
      labels:
        app: blacklist
    spec:
      containers:
        - name: nginx
          image: nginx
          volumeMounts:
          - name: ip-blacklist-configmap
            mountPath: /etc/nginx/conf.d
      volumes:
        - name: ip-blacklist-configmap
          configMap:
            name: ip-blacklist-configmap
---
kind: Service
apiVersion: v1
metadata:
  name: ip-blacklist-deployment
spec:
  ports:
    - name: http
      port: 80
      targetPort: 80
  selector:
    app: blacklist
`,
		blacklistIPStrConv)

	By("Creating a configmap, deployment and service that serve the IPs of the pods labeled with server-blacklist.")
	framework.Logf("This is the configmap that is passed in: %v", configmapDeploymentServiceStr)
	err = kubectl.Create(configmapDeploymentServiceStr, f.Namespace.Name, "")
	Expect(err).To(BeNil())

	// Create url for the GlobalThreatFeed to query
	globalThreatFeedURL := "ip-blacklist-deployment" + "." + f.Namespace.Name
	globalThreatFeedStr := fmt.Sprintf(`
apiVersion: projectcalico.org/v3
kind: GlobalThreatFeed
metadata:
  name: global-threat-feed
spec:
  pull:
    http:
      url: http://%s
  globalNetworkSet:
    labels:
      security-action: block
`,
		globalThreatFeedURL)

	By("Creating a GlobalThreatFeed and GlobalNetworkSet that queries the service that serves blacklist IPs.")
	framework.Logf("GlobalThreatFeed passed in: %v", globalThreatFeedStr)
	err = kubectl.Create(globalThreatFeedStr, "", "")
	Expect(err).To(BeNil())
	return pods
}

func createSuspiciousDomainsThreatFeed(f *framework.Framework, kubectl *calico.Kubectl) {
	// we need to escape the newlines, otherwise it breaks the nginx config language.
	// however, we don't want to escape all non-ASCII characters, e.g. strconv.QuoteToASCII
	// because the domain list includes some international characters, which should be left
	// in UTF-8 encoding.
	blacklist := strings.Join(suspiciousDomains, "\\n")
	configmapDeploymentServiceStr := fmt.Sprintf(`
---
apiVersion: v1
kind: ConfigMap
data:
  nginx-dns.conf: |
    server {
        location / {
            return 200 %s;
            add_header Content-Type text/plain;
        }
    }
metadata:
  name: dns-threats
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dns-threatfeed
  labels:
    app: nginx
spec:
  replicas: 2
  selector:
    matchLabels:
      app: dns-threatfeed
  template:
    metadata:
      labels:
        app: dns-threatfeed
    spec:
      containers:
        - name: nginx
          image: nginx
          volumeMounts:
          - name: config
            mountPath: /etc/nginx/conf.d
      volumes:
        - name: config
          configMap:
            name: dns-threats
---
kind: Service
apiVersion: v1
metadata:
  name: dns-threatfeed
spec:
  ports:
    - name: http
      port: 80
      targetPort: 80
  selector:
    app: dns-threatfeed
`,
		blacklist)

	By("Creating a configmap, deployment and service that serve the IPs of the pods labeled with the threatfeed.")
	framework.Logf("This is the configmap that is passed in: %v", configmapDeploymentServiceStr)
	err := kubectl.Create(configmapDeploymentServiceStr, f.Namespace.Name, "")
	Expect(err).To(BeNil())

	// Create url for the GlobalThreatFeed to query
	globalThreatFeedURL := "dns-threatfeed" + "." + f.Namespace.Name
	globalThreatFeedStr := fmt.Sprintf(`
apiVersion: projectcalico.org/v3
kind: GlobalThreatFeed
metadata:
  name: global-threat-feed
spec:
  content: DomainNameSet
  pull:
    http:
      url: http://%s
`,
		globalThreatFeedURL)

	By("Creating a GlobalThreatFeed and GlobalNetworkSet that queries the service that serves blacklist IPs.")
	framework.Logf("GlobalThreatFeed passed in: %v", globalThreatFeedStr)
	err = kubectl.Create(globalThreatFeedStr, "", "")
	Expect(err).To(BeNil())
	return
}

func checkGlobalNetworkSet(kubectl *calico.Kubectl, globalNetworkSetName string) {
	Eventually(func() string {
		return checkGlobalNetworkSetExist(kubectl, globalNetworkSetName)
	}, 1*time.Minute, 2*time.Second).Should(Equal(globalNetworkSetName))
}

func checkGlobalNetworkSetExist(kubectl *calico.Kubectl, globalNetworkSetName string) string {
	output, _ := kubectl.Get("globalnetworksets.projectcalico.org", "", globalNetworkSetName, "", "jsonpath={.metadata.name}", "", false)
	if output == globalNetworkSetName {
		outputYaml, _ := kubectl.Get("globalnetworksets.projectcalico.org", "", globalNetworkSetName, "", "yaml", "", false)
		framework.Logf("kubectl get globalnetworksets.p %s -o=yaml\n%s", globalNetworkSetName, outputYaml)
	}
	return output
}
