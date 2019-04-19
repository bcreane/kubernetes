package ids

import (
	"fmt"
	"k8s.io/api/core/v1"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/fields"
	"time"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"
)


var _ = SIGDescribe("[Feature:CNX-v3-GTF]", func() {
	var f = framework.NewDefaultFramework("cnx-ids")
	identifierKey := "identifier"
	var err error

	var (
		kubectl *calico.Kubectl
	)
	Context("Elastic IDS Jobs and Datafeeds", func() {

		BeforeEach(func() {
			// client = InitClient(GetURI())
		})
		AfterEach(func() {
			// DeleteIndices(client)
			err = kubectl.Delete("globalthreatfeed.projectcalico.org", "","global-threat-feed", "")
			Expect(err).To(BeNil())
		})

		It("Create GlobalThreatFeed", func() {
			var pods *v1.PodList
			podServerA, serviceA := calico.CreateServerPodAndServiceWithLabels(f, f.Namespace, "server-a", []int{80}, map[string]string{identifierKey: "server-blacklist"})
			podServerB, serviceB := calico.CreateServerPodAndServiceWithLabels(f, f.Namespace, "server-b", []int{80}, map[string]string{identifierKey: "server-blacklist"})
			podServerC, serviceC := calico.CreateServerPodAndServiceWithLabels(f, f.Namespace, "server-c", []int{80}, map[string]string{identifierKey: "server-blacklist"})

			framework.Logf("podServerA:serviceA: %v:%v", podServerA.Name, serviceA.Name)
			framework.Logf("podServerB:serviceB: %v:%v", podServerB.Name, serviceB.Name)
			framework.Logf("podServerC:serviceC: %v:%v", podServerC.Name, serviceC.Name)

			// collect all pods the have the label server-blacklist
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

			// collect all podIPs the have the label server-blacklist
			var blacklistIPs []string
			for _, pod := range pods.Items {
				framework.Logf("Creating client pod %s has a pod IP of: %s", f.Namespace.Name, pod.Status.PodIP)
				blacklistIPs = append(blacklistIPs, pod.Status.PodIP)
			}

			// convert blacklistIPs into a string seperated by newlines
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

			// create a configmap, deployment and service that serve the IPs of the pods labeled with server-blacklist
			err = kubectl.Create(configmapDeploymentServiceStr,f.Namespace.Name, "")
			Expect(err).To(BeNil())
			framework.Logf("This is the configmap that is passed in: %v", configmapDeploymentServiceStr)

			// create url for the GlobalThreatFeed to query
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

			// create GlobalThreatFeed and GlobalNetworkSet that queries the service that serves blacklist IPs
			err = kubectl.Create(globalThreatFeedStr,"", "")
			Expect(err).To(BeNil())
			framework.Logf("GlobalThreatFeed passed in: %v", globalThreatFeedStr)

			// to allow time for globalNetworkSet to start and populate .spec.nets
			time.Sleep(90 * time.Second)

			By("Pinging the IMCP allowed server pods")
			for _, pod := range pods.Items {
				icmpPod, err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).Get(pod.Name, meta_v1.GetOptions{})
				Expect(err).To(BeNil())

				pingClient := "client-can-ping-" + icmpPod.Name
				framework.Logf("This is the pingClient: %v", pingClient)
				calico.TestCanPing(f, f.Namespace, pingClient, icmpPod)
			}

		})

	})
})
