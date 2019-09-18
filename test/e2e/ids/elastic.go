package ids

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"k8s.io/kubernetes/test/e2e/framework"

	"github.com/olivere/elastic"
	. "github.com/onsi/gomega"
	"github.com/tigera/flowsynth/pkg/out"
)

const JobTimeout = time.Second * 180
const JobPollInterval = time.Second
const PostFlowsynthSleepTime = 60 * time.Second
const ElasticHealthTimeout = time.Minute * 5
const ElasticHealthPollInterval = time.Second * 5

func InitClient(uri string) *elastic.Client {

	client, err := elastic.NewClient(
		elastic.SetErrorLog(log.New(os.Stderr, "ELASTIC ", log.LstdFlags)),
		elastic.SetInfoLog(log.New(os.Stdout, "", log.LstdFlags)),
		elastic.SetURL(uri),
		elastic.SetSniff(false))

	Expect(err).NotTo(HaveOccurred())
	return client
}

func WaitForElastic(ctx context.Context, client *elastic.Client) {
	ctx, _ = context.WithTimeout(ctx, ElasticHealthTimeout)
	lastError := "context expired before getting health for the first time"
	for {
		select {
		case <-ctx.Done():
			framework.Failf("deadline exceeded for elasticsearch to become healthy, last error was: %s", lastError)
		default:
			r, err := client.ClusterHealth().Do(ctx)
			if err != nil {
				lastError = fmt.Sprintf("failed to get elasticsearch health: %s", err.Error())
				time.Sleep(ElasticHealthPollInterval)
				continue
			}
			if r.Status != "green" {
				lastError = fmt.Sprintf("elasticsearch ClusterHealth.Status %s", r.Status)
				time.Sleep(ElasticHealthPollInterval)
				continue
			}
			return
		}
	}
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

	refreshResult, err := client.Refresh().Do(ctx)
	Expect(err).NotTo(HaveOccurred())
	Expect(refreshResult.Shards.Failed).To(Equal(0), "No shards failed to refresh.")

	jobStats, err := GetJobStats(ctx, client, ts.Job)
	Expect(err).NotTo(HaveOccurred())
	Expect(len(jobStats)).To(Equal(1))
	Expect(jobStats[0].State).To(Equal("closed"))

	framework.Logf("Opening job %s", ts.Job)
	opened, err := OpenJob(ctx, client, ts.Job, nil)
	Expect(err).NotTo(HaveOccurred())
	Expect(opened).To(BeTrue())

	defer func() {
		framework.Logf("Closing job %s", ts.Job)
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

	framework.Logf("Starting datafeed %s", ts.Datafeed)
	start := Time(time.Unix(0, 0))
	end := Time(time.Now())
	started, err := StartDatafeed(ctx, client, ts.Datafeed, &OpenDatafeedOptions{
		Start: &start,
		End:   &end,
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(started).To(BeTrue())

	defer func() {
		framework.Logf("Stopping datafeed %s", ts.Datafeed)
		stopped, err := StopDatafeed(ctx, client, ts.Datafeed, &CloseDatafeedOptions{
			Force: true,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(stopped).To(BeTrue())
	}()

	Eventually(func() string {
		dfStats, err := GetDatafeedStats(ctx, client, ts.Datafeed)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(dfStats)).To(Equal(1))

		return dfStats[0].State

	}, JobTimeout, JobPollInterval).
		Should(Equal("stopped"), fmt.Sprintf("Datafeed runs to completion within %v", JobTimeout))
	framework.Logf("Job %s completed", ts.Job)

	// This works around a bug in Flowsynth where it is using local time instead of UTC
	_, offset := time.Now().Zone()
	tzOffset := time.Duration(int64(offset) * int64(time.Nanosecond))

	jobStats, err = GetJobStats(ctx, client, ts.Job)
	Expect(err).NotTo(HaveOccurred())
	Expect(len(jobStats)).To(Equal(1))
	Expect(jobStats[0].DataCounts.ProcessedRecordCount).To(BeNumerically(">", 0), "Processed record count must be non-zero")
	Expect(time.Time(jobStats[0].DataCounts.LatestRecordTimestamp)).To(BeTemporally(">=", ts.Config.EndTime.Add(tzOffset).Add(-time.Second*3600)), "All records must have been processed")

	records, err := GetRecords(ctx, client, ts.Job, &GetRecordsOptions{
		Start:       &ts.Config.StartTime,
		End:         &ts.Config.EndTime,
		RecordScore: ts.Config.RecordScore,
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(len(records) >= ts.Config.NumRecords).To(BeTrue(),
		"At least %d anomalies were detected with score >= 75", ts.Config.NumRecords)
}

//CheckSearchEvents searches for a key and value in the given index in Elasticsearch
func CheckSearchEvents(client *elastic.Client, index, searchKey, searchValue string) {
	framework.Logf("CheckSearchEvents: client: %+v index: %v key:%v value:%v", client, index, searchKey, searchValue)
	Eventually(func() int {
		return checkSearchEventsExist(client, index, searchKey, searchValue)
	}, 3*time.Minute, 1*time.Second).Should(BeNumerically(">", 0))
}

func checkSearchEventsExist(client *elastic.Client, index, searchKey, searchValue string) int {
	ctx, cancel := context.WithTimeout(context.Background(), framework.SingleCallTimeout)
	defer cancel()
	termQuery := elastic.NewTermQuery(searchKey, searchValue)
	searchResult, err := client.Search().
		Index(index).
		Query(termQuery).
		Pretty(true).
		Do(ctx)

	Expect(err).ToNot(HaveOccurred())

	if int(searchResult.Hits.TotalHits) > 0 {
		framework.Logf("Found %s: %s in a total of %d record(s)\n", searchKey, searchValue, searchResult.Hits.TotalHits)
	}
	return int(searchResult.Hits.TotalHits)
}
