package ids

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/olivere/elastic"
	. "github.com/onsi/gomega"
	flowsynth "github.com/tigera/flowsynth/pkg/out"
)

const DefaultElasticURI = "http://elasticsearch-tigera-elasticsearch.calico-monitoring.svc.cluster.local:9200"

func InitClient() *elastic.Client {
	uri := os.Getenv("ELASTIC_URI")
	if uri == "" {
		uri = DefaultElasticURI
	}

	client, err := elastic.NewClient(
		elastic.SetURL(uri),
	)
	Expect(err).NotTo(HaveOccurred())
	return client
}

func DeleteIndices(client *elastic.Client) {
	ctx := context.Background()

	indexNames, err := client.IndexNames()
	Expect(err).NotTo(HaveOccurred())

	toDelete := []string{}
	for _, indexName := range indexNames {
		if strings.HasPrefix(indexName, flowsynth.FlowLogIndexPrefix) {
			toDelete = append(toDelete, indexName)
		}
	}

	if len(toDelete) > 0 {
		resp, err := client.DeleteIndex(toDelete...).Do(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Acknowledged).To(BeTrue())
	}
}

func MachineLearningEnabled(client *elastic.Client) {
	ctx := context.Background()
	info, err := client.XPackInfo().Do(ctx)
	Expect(err).NotTo(HaveOccurred())

	Expect(info.Features.MachineLearning.Enabled).To(BeTrue())
	Expect(info.Features.MachineLearning.Available).To(BeTrue())
}

func CheckExtraJobs(client *elastic.Client, tests []TestSpec) {
	ctx := context.Background()

	jobIDs := make([]string, 0)
	for _, tSpec := range tests {
		jobIDs = append(jobIDs, tSpec.Job)
	}

	jobs, err := GetJobs(ctx, client)
	Expect(err).NotTo(HaveOccurred())
	for _, job := range jobs {
		Expect(jobIDs).To(ContainElement(job.Id))
	}
}

func CheckExtraDatafeeds(client *elastic.Client, tests []TestSpec) {
	ctx := context.Background()

	feedIDs := make([]string, 0)
	for _, tSpec := range tests {
		feedIDs = append(feedIDs, tSpec.Datafeed)
	}

	datafeeds, err := GetDatafeeds(ctx, client)
	Expect(err).NotTo(HaveOccurred())
	for _, datafeed := range datafeeds {
		Expect(feedIDs).To(ContainElement(datafeed.Id))
	}
}

func DatafeedExists(client *elastic.Client, feedID string) {
	ctx := context.Background()
	datafeeds, err := GetDatafeeds(ctx, client, feedID)
	Expect(err).NotTo(HaveOccurred())
	Expect(len(datafeeds)).To(Equal(1))
}

func JobExists(client *elastic.Client, jobID string) {
	ctx := context.Background()
	jobs, err := GetJobs(ctx, client, jobID)
	Expect(err).NotTo(HaveOccurred())
	Expect(len(jobs)).To(Equal(1))
}

func RunJob(client *elastic.Client, ts TestSpec) {
	ctx := context.Background()

	jobStats, err := GetJobStats(ctx, client, ts.Job)
	Expect(err).NotTo(HaveOccurred())
	Expect(len(jobStats)).To(Equal(1))
	Expect(jobStats[0].State).To(Equal("closed"))

	opened, err := OpenJob(ctx, client, ts.Job, nil)
	Expect(err).NotTo(HaveOccurred())
	Expect(opened).To(BeTrue())

	defer func() {
		closed, err := CloseJob(ctx, client, ts.Job, &CloseJobOptions{
			Force: true,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(closed).To(BeTrue())
	}()

	dfStats, err := GetDatafeedStats(ctx, client, ts.Datafeed)
	Expect(err).NotTo(HaveOccurred())
	Expect(len(dfStats)).To(Equal(1))
	Expect(dfStats[0].State).To(Equal("stopped"))

	now := Time(time.Now())
	started, err := StartDatafeed(ctx, client, ts.Datafeed, &OpenDatafeedOptions{
		End: &now,
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(started).To(BeTrue())

	defer func() {
		stopped, err := StopDatafeed(ctx, client, ts.Datafeed, &CloseDatafeedOptions{
			Force: true,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(stopped).To(BeTrue())
	}()

	for {
		dfStats, err := GetDatafeedStats(ctx, client, ts.Datafeed)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(dfStats)).To(Equal(1))

		if dfStats[0].State == "closed" {
			break
		}

		fmt.Printf("Waiting on %v...\n", ts.Datafeed)
		time.Sleep(1)
	}

}
