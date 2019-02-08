package ids

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/olivere/elastic"
	"strings"
	"time"
)

func GetDatafeeds(ctx context.Context, client *elastic.Client, feed_ids ...string) ([]DatafeedSpec, error) {
	params := strings.Join(feed_ids, ",")

	resp, err := client.PerformRequest(ctx, elastic.PerformRequestOptions{
		Method: "GET",
		Path: fmt.Sprintf("/_xpack/ml/datafeeds/%s", params),
	})
	if err != nil {
		return nil, err
	}

	var getDatafeedsResponse	GetDatafeedResponseSpec
	err = json.Unmarshal(resp.Body, &getDatafeedsResponse)
	if err != nil {
		return nil, err
	}

	return getDatafeedsResponse.Datafeeds, nil
}

func GetDatafeedStats(ctx context.Context, client *elastic.Client, feed_ids ...string) ([]DatafeedCountsSpec, error) {
	params := strings.Join(feed_ids, ",")

	resp, err := client.PerformRequest(ctx, elastic.PerformRequestOptions{
		Method: "GET",
		Path: fmt.Sprintf("/_xpack/ml/datafeeds/%s/_stats", params),
	})
	if err != nil {
		return nil, err
	}

	var getDatafeedStatsResponse	GetDatafeedStatsResponseSpec
	err = json.Unmarshal(resp.Body, &getDatafeedStatsResponse)
	if err != nil {
		return nil, err
	}

	return getDatafeedStatsResponse.Datafeeds, nil
}

type OpenDatafeedOptions struct {
	Start *Time
	End *Time
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

func StartDatafeed(ctx context.Context, client *elastic.Client, feed_id string, options *OpenDatafeedOptions) (bool, error) {
	requestOptions := elastic.PerformRequestOptions{
		Method: "POST",
		Path: fmt.Sprintf("/_xpack/ml/datafeeds/%s/_start", feed_id),
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
	Force bool
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

func StopDatafeed(ctx context.Context, client *elastic.Client, datafeed_id string, options *CloseDatafeedOptions) (bool, error) {
	requestOptions := elastic.PerformRequestOptions{
		Method: "POST",
		Path: fmt.Sprintf("/_xpack/ml/anomaly_detectors/%s/_stop", datafeed_id),
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

func GetJobs(ctx context.Context, client *elastic.Client, job_ids ...string) ([]JobSpec, error) {
	params := strings.Join(job_ids, ",")

	resp, err := client.PerformRequest(ctx, elastic.PerformRequestOptions{
		Method: "GET",
		Path: fmt.Sprintf("/_xpack/ml/anomaly_detectors/%s", params),
	})
	if err != nil {
		return nil, err
	}

	var getJobsResponse	GetJobResponseSpec
	err = json.Unmarshal(resp.Body, &getJobsResponse)
	if err != nil {
		return nil, err
	}

	return getJobsResponse.Jobs, nil
}

func GetJobStats(ctx context.Context, client *elastic.Client, job_ids ...string) ([]JobStatsSpec, error) {
	params := strings.Join(job_ids, ",")

	resp, err := client.PerformRequest(ctx, elastic.PerformRequestOptions{
		Method: "GET",
		Path: fmt.Sprintf("/_xpack/ml/anomaly_detectors/%s/_stats", params),
	})
	if err != nil {
		return nil, err
	}

	var getJobsStatsResponse	GetJobStatsResponseSpec
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

func OpenJob(ctx context.Context, client *elastic.Client, job_id string, options *OpenJobOptions) (bool, error) {
	requestOptions := elastic.PerformRequestOptions{
		Method: "POST",
		Path: fmt.Sprintf("/_xpack/ml/anomaly_detectors/%s/_open", job_id),
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
	Force bool
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

func CloseJob(ctx context.Context, client *elastic.Client, job_id string, options *CloseJobOptions) (bool, error) {
	requestOptions := elastic.PerformRequestOptions{
		Method: "POST",
		Path: fmt.Sprintf("/_xpack/ml/anomaly_detectors/%s/_close", job_id),
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
