package ids

import (
	. "github.com/onsi/ginkgo"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"
)


var _ = SIGDescribe("[Feature:CNX-v3-GTF]", func() {
	// var f = framework.NewDefaultFramework("cnx-ids")
	// identifierKey := "identifier"

	var (
		kubectl *calico.Kubectl
	)
	Context("Elastic IDS Jobs and Datafeeds", func() {

		BeforeEach(func() {
			// client = InitClient(GetURI())
		})
		AfterEach(func() {
			// DeleteIndices(client)
		})

		It("Create GlobalThreatFeed", func() {
			// podServerA, serviceA := calico.CreateServerPodAndServiceWithLabels(f, f.Namespace, "server-a", []int{80}, map[string]string{identifierKey: "identA"})
			// framework.Logf("This is the podServer that was created: %v", podServerA)
			// framework.Logf("This is the service that was created: %v", serviceA)

			configmapDeploymentServiceStr := `
---
apiVersion: v1
kind: ConfigMap
data:
  nginx-ip-blacklist.conf: |
    server {
        location / {
            return 200 '218.92.1.158\n5.188.10.179\n185.22.209.14\n95.70.0.46\n191.96.249.183\n115.238.245.8\n122.226.181.164\n122.226.181.167\n';
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
`
			kubectl.Create(configmapDeploymentServiceStr,"default", "")

			globalThreatFeedStr := `
apiVersion: projectcalico.org/v3
kind: GlobalThreatFeed
metadata:
  name: global-threat-feed
spec:
  pull:
    http:
      url: http://ip-blacklist-deployment.default
  globalNetworkSet:
    labels:
      security-action: block
`
			kubectl.Create(globalThreatFeedStr,"", "")
			framework.Logf("This is the GTF that is passed in: %v", globalThreatFeedStr)

		})

	})
})
