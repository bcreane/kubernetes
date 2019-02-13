package ids

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/olivere/elastic"
	"strings"
	"time"
)

func GetDatafeeds(ctx context.Context, client *elastic.Client, feedIDs ...string) ([]DatafeedSpec, error) {
	params := strings.Join(feedIDs, ",")

	resp, err := client.PerformRequest(ctx, elastic.PerformRequestOptions{
		Method: "GET",
		Path:   fmt.Sprintf("/_xpack/ml/datafeeds/%s", params),
	})
	if err != nil {
		return nil, err
	}

	var getDatafeedsResponse GetDatafeedResponseSpec
	err = json.Unmarshal(resp.Body, &getDatafeedsResponse)
	if err != nil {
		return nil, err
	}

	return getDatafeedsResponse.Datafeeds, nil
}

func GetDatafeedStats(ctx context.Context, client *elastic.Client, feedIDs ...string) ([]DatafeedCountsSpec, error) {
	params := strings.Join(feedIDs, ",")

	resp, err := client.PerformRequest(ctx, elastic.PerformRequestOptions{
		Method: "GET",
		Path:   fmt.Sprintf("/_xpack/ml/datafeeds/%s/_stats", params),
	})
	if err != nil {
		return nil, err
	}

	var getDatafeedStatsResponse GetDatafeedStatsResponseSpec
	err = json.Unmarshal(resp.Body, &getDatafeedStatsResponse)
	if err != nil {
		return nil, err
	}

	return getDatafeedStatsResponse.Datafeeds, nil
}

type OpenDatafeedOptions struct {
	Start   *Time
	End     *Time
	Timeout *Duration
}

func (o *OpenDatafeedOptions) MarshalJSON() ([]byte, error) {
	v := make(map[string]interface{})
	if o.Start != nil {
		v["start"] = o.Start
	}
	if o.End != nil {
		v["end"] = o.End
	}
	if o.Timeout != nil {
		v["timeout"] = o.Timeout
	}
	return json.Marshal(&v)
}

func StartDatafeed(ctx context.Context, client *elastic.Client, feedID string, options *OpenDatafeedOptions) (bool, error) {
	requestOptions := elastic.PerformRequestOptions{
		Method: "POST",
		Path:   fmt.Sprintf("/_xpack/ml/datafeeds/%s/_start", feedID),
	}
	if options != nil {
		requestOptions.Body = options
	}
	resp, err := client.PerformRequest(ctx, requestOptions)
	if err != nil {
		return false, err
	}

	var openJobResponse map[string]bool
	err = json.Unmarshal(resp.Body, &openJobResponse)
	if err != nil {
		return false, err
	}

	return openJobResponse["started"], nil
}

type CloseDatafeedOptions struct {
	Force   bool
	Timeout *time.Duration
}

func (o *CloseDatafeedOptions) MarshalJSON() ([]byte, error) {
	v := map[string]interface{}{
		"force": o.Force,
	}
	if o.Timeout != nil {
		v["timeout"] = o.Timeout
	}
	return json.Marshal(&v)
}

func StopDatafeed(ctx context.Context, client *elastic.Client, feedID string, options *CloseDatafeedOptions) (bool, error) {
	requestOptions := elastic.PerformRequestOptions{
		Method: "POST",
		Path:   fmt.Sprintf("/_xpack/ml/datafeeds/%s/_stop", feedID),
	}
	if options != nil {
		requestOptions.Body = options
	}
	resp, err := client.PerformRequest(ctx, requestOptions)
	if err != nil {
		return false, err
	}

	var openDatafeedResponse map[string]bool
	err = json.Unmarshal(resp.Body, &openDatafeedResponse)
	if err != nil {
		return false, err
	}

	return openDatafeedResponse["stopped"], nil
}

func GetJobs(ctx context.Context, client *elastic.Client, jobIDs ...string) ([]JobSpec, error) {
	params := strings.Join(jobIDs, ",")

	resp, err := client.PerformRequest(ctx, elastic.PerformRequestOptions{
		Method: "GET",
		Path:   fmt.Sprintf("/_xpack/ml/anomaly_detectors/%s", params),
	})
	if err != nil {
		return nil, err
	}

	var getJobsResponse GetJobResponseSpec
	err = json.Unmarshal(resp.Body, &getJobsResponse)
	if err != nil {
		return nil, err
	}

	return getJobsResponse.Jobs, nil
}

func GetJobStats(ctx context.Context, client *elastic.Client, jobIDs ...string) ([]JobStatsSpec, error) {
	params := strings.Join(jobIDs, ",")

	resp, err := client.PerformRequest(ctx, elastic.PerformRequestOptions{
		Method: "GET",
		Path:   fmt.Sprintf("/_xpack/ml/anomaly_detectors/%s/_stats", params),
	})
	if err != nil {
		return nil, err
	}

	var getJobsStatsResponse GetJobStatsResponseSpec
	err = json.Unmarshal(resp.Body, &getJobsStatsResponse)
	if err != nil {
		return nil, err
	}

	return getJobsStatsResponse.Jobs, nil
}

type OpenJobOptions struct {
	Timeout *Duration
}

func (o *OpenJobOptions) MarshalJSON() ([]byte, error) {
	v := make(map[string]interface{})
	if o.Timeout != nil {
		v["timeout"] = o.Timeout
	}
	return json.Marshal(&v)
}

func OpenJob(ctx context.Context, client *elastic.Client, jobID string, options *OpenJobOptions) (bool, error) {
	requestOptions := elastic.PerformRequestOptions{
		Method: "POST",
		Path:   fmt.Sprintf("/_xpack/ml/anomaly_detectors/%s/_open", jobID),
	}
	if options != nil {
		requestOptions.Body = options
	}
	resp, err := client.PerformRequest(ctx, requestOptions)
	if err != nil {
		return false, err
	}

	var openJobResponse map[string]bool
	err = json.Unmarshal(resp.Body, &openJobResponse)
	if err != nil {
		return false, err
	}

	return openJobResponse["opened"], nil
}

type CloseJobOptions struct {
	Force   bool
	Timeout *time.Duration
}

func (o *CloseJobOptions) MarshalJSON() ([]byte, error) {
	v := map[string]interface{}{
		"force": o.Force,
	}
	if o.Timeout != nil {
		v["timeout"] = o.Timeout
	}
	return json.Marshal(&v)
}

func CloseJob(ctx context.Context, client *elastic.Client, jobID string, options *CloseJobOptions) (bool, error) {
	requestOptions := elastic.PerformRequestOptions{
		Method: "POST",
		Path:   fmt.Sprintf("/_xpack/ml/anomaly_detectors/%s/_close", jobID),
	}
	if options != nil {
		requestOptions.Body = options
	}
	resp, err := client.PerformRequest(ctx, requestOptions)
	if err != nil {
		return false, err
	}

	var openJobResponse map[string]bool
	err = json.Unmarshal(resp.Body, &openJobResponse)
	if err != nil {
		return false, err
	}

	return openJobResponse["closed"], nil
}

type PageOptionsSpec struct {
	From int `json:"from"`
	Size int `json:"size"`
}

type GetBucketsOptions struct {
	Timestamp *time.Time
	AnomalyScore float64
	Desc bool
	End *time.Time
	ExcludeInterim bool
	Expand bool
	Page *PageOptionsSpec
	Sort *string
	Start *time.Time
}

func (o *GetBucketsOptions) MarshalJSON() ([]byte, error) {
	v := map[string]interface{} {
		"anomaly_score":    o.AnomalyScore,
		"desc":           o.Desc,
		"exclude_interim": o.ExcludeInterim,
		"expand": o.Expand,
	}
	if o.End != nil {
		v["end"] = o.End.Format(time.RFC3339)
	}
	if o.Page != nil {
		v["page"] = *o.Page
	}
	if o.Sort != nil {
		v["sort"] = *o.Sort
	}
	if o.Start != nil {
		v["start"] = o.Start.Format(time.RFC3339)
	}

	return json.Marshal(&v)
}

func GetBuckets(ctx context.Context, client *elastic.Client, jobID string, options *GetBucketsOptions) ([]BucketSpec, error) {
	optTimestamp := ""
	if options.Timestamp != nil {
		optTimestamp = fmt.Sprintf("/%s", options.Timestamp.Format(time.RFC3339))
	}

	requestOptions := elastic.PerformRequestOptions{
		Method: "POST",
		Path:   fmt.Sprintf("/_xpack/ml/anomaly_detectors/%s/results/buckets%s", jobID, optTimestamp),
	}
	if options != nil {
		requestOptions.Body = options
	}
	resp, err := client.PerformRequest(ctx, requestOptions)
	if err != nil {
		return nil, err
	}

	var getBucketsResponse GetBucketsResponseSpec
	err = json.Unmarshal(resp.Body, &getBucketsResponse)
	if err != nil {
		return nil, err
	}

	return getBucketsResponse.Buckets, nil
}

type GetRecordsOptions struct {
	Desc bool
	End *time.Time
	ExcludeInterim bool
	Page *PageOptionsSpec
	RecordScore float64
	Sort *string
	Start *time.Time
}

func (o *GetRecordsOptions) MarshalJSON() ([]byte, error) {
	v := map[string]interface{} {
		"desc":           o.Desc,
		"exclude_interim": o.ExcludeInterim,
		"record_score":    o.RecordScore,
	}
	if o.End != nil {
		v["end"] = o.End.Format(time.RFC3339)
	}
	if o.Page != nil {
		v["page"] = *o.Page
	}
	if o.Sort != nil {
		v["sort"] = *o.Sort
	}
	if o.Start != nil {
		v["start"] = o.Start.Format(time.RFC3339)
	}

	return json.Marshal(&v)
}


func GetRecords(ctx context.Context, client *elastic.Client, jobID string, options *GetRecordsOptions) ([]RecordSpec, error) {
	requestOptions := elastic.PerformRequestOptions{
		Method: "POST",
		Path:   fmt.Sprintf("/_xpack/ml/anomaly_detectors/%s/results/records", jobID),
	}
	if options != nil {
		requestOptions.Body = options
	}
	resp, err := client.PerformRequest(ctx, requestOptions)
	if err != nil {
		return nil, err
	}

	var getRecordsResponse GetRecordsResponseSpec
	err = json.Unmarshal(resp.Body, &getRecordsResponse)
	if err != nil {
		return nil, err
	}

	return getRecordsResponse.Records, nil
}