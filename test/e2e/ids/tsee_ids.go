package ids

import (
	. "github.com/onsi/ginkgo"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"
)


var _ = SIGDescribe("[Feature:CNX-v3-GTF]", func() {
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
			globalThreatFeedStr := `
apiVersion: projectcalico.org/v3
kind: GlobalThreatFeed
metadata:
  name: sample-global-threat-feed
spec:
  pull:
    http:
      url: https://an.example.threat.feed/blacklist
  globalNetworkSet:
    labels:
      security-action: block
`
			kubectl.Create(globalThreatFeedStr,"", "")
			framework.Logf("This is the GTF that is passed in: %v", globalThreatFeedStr)

		})

	})
})
