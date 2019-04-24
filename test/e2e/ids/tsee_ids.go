package ids

import (
	"context"
	"fmt"
	"github.com/olivere/elastic"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/json"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"
	"time"
)


var _ = SIGDescribe("[Feature:CNX-v3-SuspiciousIPs]", func() {
	var f = framework.NewDefaultFramework("cnx-suspicious-ips")
	identifierKey := "identifier"
	var err error

	var (
		kubectl *calico.Kubectl
	)
	Context("Suspicious IP security events.", func() {
		var client *elastic.Client
		BeforeEach(func() {
			client = InitClient(GetURI())
		})
		AfterEach(func() {
			DeleteIndices(client)
			err = kubectl.Delete("globalthreatfeed.projectcalico.org", "","global-threat-feed", "")
			Expect(err).To(BeNil())
		})

		It("Create GlobalThreatFeed, generate traffic to suspicious ips and verify security events have been created.", func() {
			var pods *v1.PodList
			podServerA, serviceA := calico.CreateServerPodAndServiceWithLabels(f, f.Namespace, "server-a", []int{80}, map[string]string{identifierKey: "server-blacklist"})
			podServerB, serviceB := calico.CreateServerPodAndServiceWithLabels(f, f.Namespace, "server-b", []int{80}, map[string]string{identifierKey: "server-blacklist"})
			podServerC, serviceC := calico.CreateServerPodAndServiceWithLabels(f, f.Namespace, "server-c", []int{80}, map[string]string{identifierKey: "server-blacklist"})

			framework.Logf("podServerA:serviceA: %v:%v", podServerA.Name, serviceA.Name)
			framework.Logf("podServerB:serviceB: %v:%v", podServerB.Name, serviceB.Name)
			framework.Logf("podServerC:serviceC: %v:%v", podServerC.Name, serviceC.Name)

			By("Collect all pods the have the label server-blacklist.")
			labelSelector := fields.SelectorFromSet(fields.Set(map[string]string{identifierKey: "server-blacklist"})).String()
			options := meta_v1.ListOptions{LabelSelector: labelSelector}

			pods, err = f.ClientSet.CoreV1().Pods(f.Namespace.Name).List(options)
			Expect(err).To(BeNil())

			for _, pod := range pods.Items {
				err = framework.WaitForPodRunningInNamespace(f.ClientSet, &pod)
				Expect(err).To(BeNil())
			}

			pods, err = f.ClientSet.CoreV1().Pods(f.Namespace.Name).List(options)
			Expect(err).To(BeNil())

			By("Collect all podIPs the have the label server-blacklist.")
			var blacklistIPs []string
			for _, pod := range pods.Items {
				framework.Logf("Creating client pod %s has a pod IP of: %s", f.Namespace.Name, pod.Status.PodIP)
				blacklistIPs = append(blacklistIPs, pod.Status.PodIP)
			}

			// Convert blacklistIPs into a string separated by newlines
			blacklistIPStr := strings.Join(blacklistIPs, "\n")
			blacklistIPStrConv:= strconv.QuoteToASCII(blacklistIPStr)
			framework.Logf("blacklistIPStrConv is: %s", blacklistIPStrConv)

			configmapDeploymentServiceStr := fmt.Sprintf( `
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
			err = kubectl.Create(configmapDeploymentServiceStr,f.Namespace.Name, "")
			Expect(err).To(BeNil())
			framework.Logf("This is the configmap that is passed in: %v", configmapDeploymentServiceStr)

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
			err = kubectl.Create(globalThreatFeedStr,"", "")
			Expect(err).To(BeNil())
			framework.Logf("GlobalThreatFeed passed in: %v", globalThreatFeedStr)

			By("Allow time for globalNetworkSet to start and populate .spec.nets.")
			time.Sleep(60 * time.Second)

			By("Creating clients to ICMP ping the blacklist server pods.")
			for _, pod := range pods.Items {
				icmpPod, err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).Get(pod.Name, meta_v1.GetOptions{})
				Expect(err).To(BeNil())

				pingClient := "client-can-ping-" + icmpPod.Name
				framework.Logf("This is the pingClient: %v", pingClient)
				calico.TestCanPing(f, f.Namespace, pingClient, icmpPod)
			}

			By("Allow time for Felix to export the flow logs.")
			time.Sleep(180 * time.Second)

			By("Verifying security events are created in elasticsearch under index: tigera_secure_ee_events*.")
			var searchKey = "dest_ip"
			for _, pod := range pods.Items {
				searchEvents(client, searchKey, pod.Status.PodIP)
			}

		})

	})
})

func searchEvents(client *elastic.Client, searchKey string, searchValue string) {
	framework.Logf("Searching elasticsearch for %s: %s.", searchKey,searchValue)
	ctx := context.Background()
	termQuery := elastic.NewTermQuery(searchKey, searchValue)
	searchResult, err := client.Search().
		Index("tigera_secure_ee_events*").
		Query(termQuery).
		Pretty(true).
		Do(ctx)

	if err != nil {
		panic(err)
	}

	fmt.Printf("Query %s: %s took %d milliseconds\n", searchKey, searchValue, searchResult.TookInMillis)
	fmt.Printf("Found %s: %s in a total of %d entries\n", searchKey, searchValue, searchResult.TotalHits())
	Expect(searchResult.Hits.TotalHits).ToNot(BeZero())

	var jsonObject map[string]interface{}
	for _, hit := range searchResult.Hits.Hits {
		err := json.Unmarshal(*hit.Source, &jsonObject)
		if err != nil {
			err = fmt.Errorf("Failed to deserialize jsonPayload as json object %s", jsonObject)
		}
		fmt.Printf("%s: %s Entry: %s\n", searchKey, searchValue, hit.Source)
	}
}
