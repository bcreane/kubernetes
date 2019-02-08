package ids

import (
	"fmt"
	"github.com/olivere/elastic"
	. "github.com/onsi/ginkgo"
)

type TestSpec struct {
	Job string
	Datafeed string
}

var Tests23 = []TestSpec{
	{"inbound_connection_spike", "datafeed-inbound_connection_spike"},
	{"ip_sweep_external", "datafeed-ip_sweep_external"},
	{"ip_sweep_pods", "datafeed-ip_sweep_pods"},
	{"pod_outlier_ip_activity", "datafeed-pod_outlier_ip_activity"},
	{"port_scan_external", "datafeed-port_scan_external"},
	{"port_scan_pods", "datafeed-port_scan_pods"},
	{"service_bytes_anomaly", "datafeed-service_bytes_anomaly"},
}

var _ = SIGDescribe("[Feature:CNX-v3-IDS]", func() {
	Context("Elastic IDS Jobs and Datafeeds", func() {
		var client *elastic.Client
		BeforeEach(func() {
			client = InitClient()
		})

		AfterEach(func() { DeleteIndices(client) })

		It("Machine Learning is enabled", func() { MachineLearningEnabled(client) })

		It("No extra jobs are defined", func() { CheckExtraJobs(client, Tests23) })
		It("No extra datafeeds are defined", func() { CheckExtraDatafeeds(client, Tests23) })

		for idx := range Tests23 {
			tSpec := Tests23[idx]
			It(fmt.Sprintf("Datafeed %s is defined", tSpec.Datafeed), func() { DatafeedExists(client, tSpec.Datafeed) })
			It(fmt.Sprintf("Job %s is defined", tSpec.Job), func() { JobExists(client, tSpec.Job) })
			It(fmt.Sprintf("Job %s runs successfully", tSpec.Job), func() { RunJob(client, tSpec) })
		}
	})
})
