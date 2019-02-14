package ids

import (
	"fmt"
	"github.com/olivere/elastic"
	. "github.com/onsi/ginkgo"
	"github.com/tigera/flowsynth/pkg/app"
	"github.com/tigera/flowsynth/pkg/out"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"
)

const (
	DefaultElasticScheme = "http"
	DefaultElasticHost = "elasticsearch-tigera-elasticsearch.calico-monitoring.svc.cluster.local"
	DefaultElasticPort = 9200
	NumDays = 3
)

var _ = SIGDescribe("[Feature:CNX-v3-IDS]", func() {
	Context("Elastic IDS Jobs and Datafeeds", func() {
		var client *elastic.Client
		BeforeEach(func() {
			client = InitClient(GetURI())
		})

		It("Machine Learning is enabled", func() { MachineLearningEnabled(client) })

		It("No extra jobs are defined", func() { CheckExtraJobs(client, Tests) })
		It("No extra datafeeds are defined", func() { CheckExtraDatafeeds(client, Tests) })

		for idx := range Tests {
			tSpec := Tests[idx]
			It(fmt.Sprintf("Datafeed %s is defined", tSpec.Datafeed), func() { DatafeedExists(client, tSpec.Datafeed) })
			It(fmt.Sprintf("Job %s is defined", tSpec.Job), func() { JobExists(client, tSpec.Job) })
			It(fmt.Sprintf("Job %s runs successfully", tSpec.Job), func() { RunJob(client, tSpec) })
		}
	})
})

var Tests = []TestSpec{
	{"inbound_connection_spike", "datafeed-inbound_connection_spike", GenConfig(
		75, 1,
		app.Event{
			InboundConnectionSpike: &app.SpecInboundConnectionSpike{
				Service:  "basic",
				NumFlows: 100000,
				DestPort: 443,
			},
		},
	)},
	{"ip_sweep_external", "datafeed-ip_sweep_external", GenConfig(0, 0)},
	{"ip_sweep_pods", "datafeed-ip_sweep_pods", GenConfig(
		75, 1,
		app.Event{
			IPSweep: &app.SpecIPSweep{
				Service: "basic",
			},
		},
	)},
	{"pod_outlier_ip_activity", "datafeed-pod_outlier_ip_activity", GenConfig(
		75, 1,
		app.Event{
			OutboundIP: &app.SpecOutboundIP{
				Service:  "basic",
				NumFlows: 1000000,
				Nets:     []*net.IPNet{MakeNet("172.16.7.0/24")},
			},
		},
	)},
	{"port_scan_external", "datafeed-port_scan_external", GenConfig(0, 0)},
	{"port_scan_pods", "datafeed-port_scan_pods", GenConfig(
		75, 1,
		app.Event{
			PortScan: &app.SpecPortScan{
				Service: "basic",
			},
		},
	)},
	{"service_bytes_anomaly", "datafeed-service_bytes_anomaly", GenConfig(
		75, 2,
		app.Event{
			ServiceBytesAnomaly: &app.SpecServiceBytesAnomaly{
				Service:     "basic",
				NumFlows:    100000,
				NumBytesIn:  10000000,
				NumBytesOut: 1000,
				DestPort:    443,
			},
		},
		app.Event{
			ServiceBytesAnomaly: &app.SpecServiceBytesAnomaly{
				Service:     "basic",
				NumFlows:    100000,
				NumBytesIn:  1000,
				NumBytesOut: 10000000,
				DestPort:    443,
			},
		},
	)},
}

func GetURI() string {
	uri := os.Getenv("ELASTIC_URI")
	if uri != "" {
		return uri
	}

	scheme := os.Getenv("ELASTIC_SCHEME")
	if scheme == "" {
		scheme = DefaultElasticScheme
	}

	host := os.Getenv("ELASTIC_HOST")
	if host == "" {
		host = DefaultElasticHost
	}

	portStr := os.Getenv("ELASTIC_PORT")
	var port int64
	if portStr == "" {
		port = DefaultElasticPort
	} else {
		var err error
		port, err = strconv.ParseInt(portStr, 10, 16)
		if err != nil {
			panic(err)
		}
	}

	return (&url.URL{
		Scheme: scheme,
		Host:   fmt.Sprintf("%s:%d", host, port),
	}).String()
}

type TestSpec struct {
	Job      string
	Datafeed string
	Config   TestConfig
}

type TestConfig struct {
	RecordScore int
	NumRecords int
	NumNodes   int
	PodNetwork *net.IPNet
	StartTime  time.Time
	EndTime    time.Time
	Apps       []app.AppConfig
	Outs       []out.OutConfig
}

func (c *TestConfig) MarshalYAML() (interface{}, error) {
	v := struct {
		NumNodes   int             `yaml:"NumNodes"`
		PodNetwork string          `yaml:"PodNetwork"`
		StartTime  string          `yaml:"StartTime"`
		EndTime    string          `yaml:"EndTime"`
		Apps       []app.AppConfig `yaml:"Apps"`
		Outs       []out.OutConfig `yaml:"Outs"`
	}{
		c.NumNodes,
		c.PodNetwork.String(),
		c.StartTime.Format("2006-01-02"),
		c.EndTime.Format("2006-01-02"),
		c.Apps,
		c.Outs,
	}
	return &v, nil
}

func GenConfig(threshold, numRecords int, events ...app.Event) TestConfig {
	endTime := RoundTimeToDay(time.Now().UTC())
	startTime := endTime.AddDate(0, 0, -NumDays)

	for idx := range events {
		events[idx].At = endTime.Add(time.Second * time.Duration(-(idx+1) * 3600 * 6))
		if events[idx].At.Before(startTime) || events[idx].At.After(endTime) {
			panic (fmt.Sprintf("Time %v is out of range [%v, %v]", events[idx].At, startTime, endTime))
		}
	}

	return TestConfig{
		RecordScore: threshold,
		NumRecords: numRecords,
		NumNodes:   3,
		PodNetwork: MakeNet("10.10.1.0/25"),
		StartTime:  startTime,
		EndTime:    endTime,
		Apps: []app.AppConfig{
			{
				Type: "WrappedApp",
				Spec: app.WrappedAppConfig{
					App: app.AppConfig{
						Type: "MultiService",
						Spec: app.MultiServiceConfig{
							Namespace: "default",
							Services: []app.ServiceConfig{
								{
									Name:               "basic",
									Port:               443,
									FlowsPerSecPod:     1.0,
									Threshold:          0.35,
									IngressFlowsPerSec: 1.0,
									Scaler: app.TrafficScaler{
										Weekly:   []app.Phaser{{Amp: 0.4, Phase: 2.5}},
										Daily:    []app.Phaser{{Amp: 0.5, Phase: 0}},
										Constant: 0.3,
										Noise:    0.05,
									},
									IngressNets: []*net.IPNet{
										MakeNet("172.16.2.0/24"),
										MakeNet("192.168.3.0/24"),
									},
									Upstreams: []app.UpstreamConfig{
										{
											ConstantFlowsPerSec: 0.1,
											ExternalNets:        []*net.IPNet{MakeNet("192.0.3.0/24")},
											ExternalPort:        80,
										},
									},
								},
							},
						},
					},
					Events: events,
				},
			},
		},
		Outs: []out.OutConfig{
			{
				Type: "Elastic",
				Spec: out.ElasticOutConfig{
					URL: MakeURI(GetURI()),
				},
			},
		},
	}
}
