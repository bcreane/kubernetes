package ids

import (
	"context"
	"k8s.io/kubernetes/test/e2e/framework"
	"strings"
	"time"

	"github.com/olivere/elastic"
	. "github.com/onsi/gomega"
	"github.com/tigera/flowsynth/pkg/out"
)

const JobTimeout = 180
const JobPollInterval = 1

func InitClient(uri string) *elastic.Client {
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
		if strings.HasPrefix(indexName, out.FlowLogIndexPrefix) {
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

	framework.Logf("Clearing data in Elastic for %v", ts.Job)
	DeleteIndices(client)
	framework.Logf("Running Flowsynth for %v.", ts.Job)
	RunFlowSynth(ctx, ts.Config)
	framework.Logf("Complete. Sleeping.")
	time.Sleep(30 * time.Second)
	framework.Logf("Proceeding.")

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

	Eventually(func() bool {
		dfStats, err := GetDatafeedStats(ctx, client, ts.Datafeed)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(dfStats)).To(Equal(1))

		return dfStats[0].State == "closed"
	}, JobTimeout, JobPollInterval)

	records, err := GetRecords(ctx, client, ts.Job, &GetRecordsOptions{
		Start:          &ts.Config.StartTime,
		End:            &ts.Config.EndTime,
		RecordScore:    ts.Config.RecordScore,
	})
	Expect(err).NotTo(HaveOccurred())

	Expect(len(records) >= ts.Config.NumRecords).To(BeTrue(),
	"At least %d anomalies were detected with score >= 75", ts.Config.NumRecords)
}
