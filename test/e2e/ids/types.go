package ids

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

type GetJobResponseSpec struct {
	Count int `json:"count"`
	Jobs []JobSpec `json:"jobs"`
}

type JobSpec struct {
	AnalysisConfig AnalysisConfigSpec `json:"analysis_config"`
	AnalysisLimits AnalysisLimitsSpec `json:"analysis_limits"`
	CreateTime int `json:"create_time"`
	CustomSettings interface{} `json:"custom_settings"`
	DataDescription DataDescriptionSpec `json:"data_description"`
	Description string `json:"description"`
	EstablishedModelMemory uint64 `json:"established_model_memory"`
	FinishedTime Time `json:"finished_time"`
	Groups []string `json:"groups"`
	Id string `json:"job_id"`
	Type string `json:"job_type"`
	Version string `json:"job_version"`
	ModelPlotConfig ModelPlotConfigSpec `json:"model_plot_config"`
	ModelSnapshotId string `json:"model_snapshot_id"`
	ModelSnapshotRetentionDays int `json:"model_snapshot_retention_days"`
	RenormalizationWindowDays uint64 `json:"renormalization_window_days"`
	ResultsIndexName string `json:"results_index_name"`
	ResultsRetentionDays uint64 `json:"results_retention_days"`
}

type AnalysisConfigSpec struct {
	BucketSpan string `json:"bucket_span"`
	CategorizationFieldName string `json:"categorization_field_name"`
	CategorizationFilters []string `json:"categorization_filters"`
	CategorizationAnalyzer CategorizationAnalyzerSpec `json:"categorization_analyzer"` // incomplete
	Detectors []DetectorsSpec `json:"detectors"`
	Influencers []string `json:"influencers"`
	Latency Duration `json:"latency"`
	MultivariateByFields bool `json:"multivariate_by_fields"`
	SummaryCountFieldName string `json:"summary_count_field_name"`
}

type CategorizationAnalyzerSpec struct {
	CharFilter []interface{} `json:"char_filter"`
	Tokenizer interface{} `json:"tokenizer"`
	Filter []interface{} `json:"filter"`
}

type DetectorsSpec struct {
	ByFieldName string `json:"by_field_name"`
	Description string `json:"detector_description"`
	Index int `json:"detector_index"`
	ExcludeFrequent string `json:"exclude_frequent"`
	FieldName string `json:"field_name"`
	Function string `json:"function"`
	OverFieldName string `json:"over_field_name"`
	PartitionFieldName string `json:"partition_field_name"`
	UseNull bool `json:"use_null"`
}

type AnalysisLimitsSpec struct {
	CategorizationExamplesLimit uint64 `json:"categorization_examples_limit"`
	ModelMemoryLimit interface{} `json:"model_memory_limit"`
}

type DataDescriptionSpec struct {
	Format string `json:"format"`
	TimeField string `json:"time_field"`
	TimeFormat string `json:"time_format"`
}

type ModelPlotConfigSpec struct {
	Enabled bool `json:"enabled"`
	Terms string `json:"terms"`
}

type GetJobStatsResponseSpec struct {
	Count int `json:"count"`
	Jobs []JobStatsSpec `json:"jobs"`
}

type JobStatsSpec struct {
	AssignmentExplanation string `json:"assignment_explanation"`
	DataCounts DataCountsSpec `json:"data_counts"`
	Id string `json:"job_id"`
	ModelSizeStats ModelSizeStatsSpec `json:"model_size_stats"`
	Node NodeSpec `json:"node"`
	OpenTime Duration `json:"open_time"`
	State string `json:"state"`
}

type DataCountsSpec struct {
	BucketCount uint64 `json:"bucket_count"`
	EarliestRecordTimestamp Time `json:"earliest_record_timestamp"`
	EmptyBucketCount uint64 `json:"empty_bucket_count"`
	InputBytes uint64 `json:"input_bytes"`
	InputFieldCount uint64 `json:"input_field_count"`
	InputRecordCount uint64 `json:"input_record_count"`
	InvalidDateCount uint64 `json:"invalid_date_count"`
	Id string `json:"job_id"`
	LastDataTime Time `json:"last_data_time"`
	LatestEmptyBucketTimestamp Time `json:"latest_empty_bucket_timestamp"`
	LatestRecordTimestamp Time `json:"latest_record_timestamp"`
	LatestSparseBucketTimestamp Time `json:"latest_sparse_bucket_timestamp"`
	MissingFieldCount uint64 `json:"missing_field_count"`
	OutOfOrderTimestampCount uint64 `json:"out_of_order_timestamp_count"`
	ProcessedFieldCount uint64 `json:"processed_field_count"`
	ProcessedRecordCount uint64 `json:"processed_record_count"`
	SparseBucketCount uint64 `json:"sparse_bucket_count"`
}

type ModelSizeStatsSpec struct {
	BucketAllocationFailuresCount uint64 `json:"bucket_allocation_failures_count"`
	Id string `json:"job_id"`
	LogTime Time `json:"log_time"`
	MemoryStatus string `json:"memory_status"`
	ModelBytes uint64 `json:"model_bytes"`
	ResultType string `json:"result_type"`
	TotalByFieldCount uint64 `json:"total_by_field_count"`
	TotalOverFieldCount uint64 `json:"total_over_field_count"`
	TotalPartitionFieldCount uint64 `json:"total_partition_field_count"`
	Timestamp Time `json:"timestamp"`
}

type NodeSpec struct {
	Id string `json:"id"`
	Name string `json:"name"`
	EphemeralId string `json:"ephemeral_id"`
	TransportAddress string `json:"transport_address"`
	Attributes interface{} `json:"attributes"`
}

type GetDatafeedResponseSpec struct {
	Count int `json:"count"`
	Datafeeds []DatafeedSpec `json:"datafeeds"`
}

type DatafeedSpec struct {
	Aggregations interface{} `json:"aggregations"`
	ChunkingConfig ChunkingConfigSpec `json:"chunking_config"`
	Id string `json:"datafeed_id"`
	Frequency Duration `json:"frequency"`
	Indices []string `json:"indices"`
	JobId string `json:"job_id"`
	Query interface{} `json:"query"`
	QueryDelay   Duration        `json:"query_delay"`
	ScriptFields ScriptFieldSpec `json:"script_fields"`
	ScrollSize   uint64          `json:"scroll_size"`
	Types        []string        `json:"types"`
}

type ChunkingConfigSpec struct {
	Mode string `json:"mode"`
	TimeSpan Duration `json:"time_span"`
}

type ScriptFieldSpec struct {
	Lang string `json:"lang"`
	Source interface{} `json:"source"`
	Params map[string]interface{} `json:"params"`
}

type GetDatafeedStatsResponseSpec struct {
	Count int                      `json:"count"`
	Datafeeds []DatafeedCountsSpec `json:"datafeeds"`
}

type DatafeedCountsSpec struct {
	AssignmentExplanation string `json:"assignment_explanation"`
	Id string `json:"datafeed_id"`
	Node NodeSpec `json:"node"`
	State string `json:"state"`
}

type GetBucketsResponseSpec struct {
	Count int `json:"count"`
	Buckets []BucketSpec `json:"buckets"`
}

type BucketSpec struct {
	AnomalyScore float64 `json:"anomaly_score"`
	BucketInfluencers []string `json:"bucket_influencers"`
	BucketSpan int `json:"bucket_span"`
	EventCount int `json:"event_count"`
	InitialAnomalyScore float64 `json:"initial_anomaly_score"`
	IsInterim bool `json:"is_interim"`
	Id string `json:"job_id"`
	ResultType string `json:"result_type"`
	Timestamp Time `json:"timestamp"`
}

type GetRecordsResponseSpec struct {
	Count int `json:"count"`
	Records []RecordSpec `json:"records"`
}

type RecordSpec struct {
	Actual []interface{} `json:"actual"`
	BucketSpan int `json:"bucket_span"`
	ByFieldName string `json:"by_field_name"`
	ByFieldValue string `json:"by_field_value"`
	Causes []interface{} `json:"causes"`
	DetectorIndex int `json:"detector_index"`
	FieldName string `json:"field_name"`
	Function string `json:"function"`
	FunctionDescription string `json:"function_description"`
	Influencers []InfluencerSpec `json:"influencers"`
	InitialRecordScore float64 `json:"initial_record_score"`
	IsInterim bool `json:"is_interim"`
	Id string `json:"job_id"`
	OverFieldName string `json:"over_field_name"`
	OverFieldValue string `json:"over_field_value"`
	PartitionFieldName string `json:"partition_field_name"`
	PartitionFieldValue string `json:"partition_field_value"`
	Probability float64 `json:"probability"`
	MultiBucketImpact float64 `json:"multi_bucket_impact"`
	RecordScore float64 `json:"record_score"`
	ResultType string `json:"result_type"`
	Timestamp Time `json:"timestamp"`
	Typical []interface{} `json:"typical"`
	Fields map[string]interface{}
}

type _recordSpec *RecordSpec

func (rs *RecordSpec) UnmarshalJSON(data []byte) error {
	r := _recordSpec(rs)
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	rs.Fields = make(map[string]interface{})

	objValue := reflect.ValueOf(rs).Elem()
	fields := make(map[string]reflect.Value)
	for i := 0; i != objValue.NumField(); i++ {
		fieldName := strings.Split(objValue.Type().Field(i).Tag.Get("json"), ",")[0]
		fields[fieldName] = objValue.Field(i)
	}

	f := make(map[string]json.RawMessage)
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}

	for key, chunk := range f {
		if _, found := fields[key]; !found {
			var i interface{}
			if err := json.Unmarshal(chunk, &i); err != nil {
				return err
			}
			rs.Fields[key] = i
		}
	}

	return nil
}

type InfluencerSpec struct {
	FieldName string `json:"influencer_field_name"`
	FieldValues []interface{} `json:"influencer_field_values"`
}

type Duration time.Duration

func (f *Duration) UnmarshalJSON(data []byte) error {
	var i int64
	if err := json.Unmarshal(data, &i); err == nil {
		*f = Duration(time.Second * time.Duration(i))
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*f = Duration(d)
	return nil
}

func (f *Duration) MarshalJSON() ([]byte, error) {
	s := time.Duration(*f).String()
	return json.Marshal(&s)
}

type Time time.Time

func (t *Time) UnmarshalJSON(data []byte) error {
	var i int64
	if err := json.Unmarshal(data, &i); err == nil {
		*t = Time(time.Unix(i/1000, (time.Duration(i%1000)*time.Millisecond).Nanoseconds()))
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return err
	}
	*t = Time(tm)
	return nil
}

func (t *Time) MarshalJSON() ([]byte, error) {
	s := time.Time(*t).Format(time.RFC3339)
	return json.Marshal(&s)
}
