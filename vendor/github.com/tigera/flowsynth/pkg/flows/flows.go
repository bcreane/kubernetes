// Copyright (c) 2018-2019 Tigera, Inc. All rights reserved.

package flows

import (
	"fmt"
	"net"
	"strings"
	"time"
)

type FlowLogEndpointType string
type FlowLogAction string
type FlowLogReporter string
type FlowLogSubnetType string

const (
	FlowLogNamespaceGlobal  = "-"
	FlowLogFieldNotIncluded = "-"
	UnsetIntField           = -1

	FlowLogActionAllow FlowLogAction = "allow"
	FlowLogActionDeny  FlowLogAction = "deny"

	FlowLogReporterSrc FlowLogReporter = "src"
	FlowLogReporterDst FlowLogReporter = "dst"

	FlowLogEndpointTypeWep FlowLogEndpointType = "wep"
	FlowLogEndpointTypeHep FlowLogEndpointType = "hep"
	FlowLogEndpointTypeNs  FlowLogEndpointType = "ns"
	FlowLogEndpointTypeNet FlowLogEndpointType = "net"

	PrivateNet       FlowLogSubnetType = "pvt"
	AWSMetaServerNet FlowLogSubnetType = "aws"
	PublicNet        FlowLogSubnetType = "pub"
)

type EndpointMetadata struct {
	Type           FlowLogEndpointType `json:"type"`
	Namespace      string              `json:"namespace"`
	Name           string              `json:"name"`
	AggregatedName string              `json:"aggregated_name"`
}

// Tuple represents a 5-Tuple value that identifies a connection/flow of packets
// with an implicit notion of Direction that comes with the use of a source and
// destination. This is a hashable object and can be used as a map's key.
type Tuple struct {
	Src   [16]byte
	Dst   [16]byte
	Proto int
	L4Src int
	L4Dst int
}

type FlowMeta struct {
	Tuple    Tuple            `json:"tuple"`
	SrcMeta  EndpointMetadata `json:"sourceMeta"`
	DstMeta  EndpointMetadata `json:"destinationMeta"`
	Action   FlowLogAction    `json:"action"`
	Reporter FlowLogReporter  `json:"flowReporter"`
}

type FlowSpec struct {
	FlowLabels
	FlowPolicies
	FlowStats
}

type FlowLabels struct {
	SrcLabels map[string]string
	DstLabels map[string]string
}

type FlowPolicies map[string]empty

// FlowData is metadata and stats about a flow (or aggregated group of flows).
// This is an internal structure for book keeping; FlowLog is what actually gets
// passed to dispatchers or serialized.
type FlowData struct {
	FlowMeta
	FlowSpec
}

type FlowUpdate struct {
	FlowData
	Type UpdateType
}

type UpdateType int

const (
	UpdateTypeReport UpdateType = iota
	UpdateTypeExpire
)

const (
	UpdateTypeReportStr = "report"
	UpdateTypeExpireStr = "expire"
)

func (ut UpdateType) String() string {
	if ut == UpdateTypeReport {
		return UpdateTypeReportStr
	}
	return UpdateTypeExpireStr
}

// FlowLog is a record of flow data (metadata & reported stats) including
// timestamps. A FlowLog is ready to be serialized to an output format.
type FlowLog struct {
	StartTime, EndTime time.Time
	Host               string
	FlowMeta
	FlowLabels
	FlowPolicies
	FlowReportedStats
}

// FlowStats captures stats associated with a given FlowMeta
type FlowStats struct {
	FlowReportedStats
	flowReferences
}

// FlowReportedStats are the statistics we actually report out in flow logs.
type FlowReportedStats struct {
	PacketsIn             int `json:"packetsIn"`
	PacketsOut            int `json:"packetsOut"`
	BytesIn               int `json:"bytesIn"`
	BytesOut              int `json:"bytesOut"`
	HTTPRequestsAllowedIn int `json:"httpRequestsAllowedIn"`
	HTTPRequestsDeniedIn  int `json:"httpRequestsDeniedIn"`
	NumFlows              int `json:"numFlows"`
	NumFlowsStarted       int `json:"numFlowsStarted"`
	NumFlowsCompleted     int `json:"numFlowsCompleted"`
}

// flowReferences are internal only stats used for computing numbers of flows
type flowReferences struct {
	// The set of unique flows that were started within the reporting interval. This is added to when a new flow
	// (i.e. one that is not currently active) is reported during the reporting interval. It is reset when the
	// flow data is reported.
	flowsStartedRefs tupleSet
	// The set of unique flows that were completed within the reporting interval. This is added to when a flow
	// termination is reported during the reporting interval. It is reset when the flow data is reported.
	flowsCompletedRefs tupleSet
	// The current set of active flows. The set may increase and decrease during the reporting interval.
	flowsRefsActive tupleSet
	// The set of unique flows that have been active at any point during the reporting interval. This is added
	// to during the reporting interval, and is reset to the set of active flows when the flow data is reported.
	flowsRefs tupleSet
}

func (f FlowStats) GetActiveFlowsCount() int {
	return len(f.flowsRefsActive)
}

// FlowSpec has FlowStats that are stats assocated with a given FlowMeta
// These stats are to be refreshed everytime the FlowData
// {FlowMeta->FlowStats} is published so as to account
// for correct no. of started flows in a given aggregation
// interval.
func (f *FlowSpec) Reset() {
	f.flowsStartedRefs = NewTupleSet()
	f.flowsCompletedRefs = NewTupleSet()
	f.flowsRefs = f.flowsRefsActive.Copy()
	f.FlowReportedStats = FlowReportedStats{
		NumFlows: f.flowsRefs.Len(),
	}

	return
}

// ToFlowLog converts a FlowData to a FlowLog
func (f FlowData) ToFlowLog(startTime, endTime time.Time, includeLabels bool, includePolicies bool) FlowLog {
	var fl FlowLog
	fl.FlowMeta = f.FlowMeta
	fl.FlowReportedStats = f.FlowReportedStats
	fl.StartTime = startTime
	fl.EndTime = endTime

	if includeLabels {
		fl.FlowLabels = f.FlowLabels
	}

	if !includePolicies {
		fl.FlowPolicies = nil
	} else {
		fl.FlowPolicies = f.FlowPolicies
	}

	return fl
}

type empty struct{}

var emptyValue = empty{}

type tupleSet map[Tuple]empty

func NewTupleSet() tupleSet {
	return make(tupleSet)
}

func (set tupleSet) Len() int {
	return len(set)
}

func (set tupleSet) Add(t Tuple) {
	set[t] = emptyValue
}

func (set tupleSet) Discard(t Tuple) {
	delete(set, t)
}

func (set tupleSet) Contains(t Tuple) bool {
	_, present := set[t]
	return present
}

func (set tupleSet) Copy() tupleSet {
	ts := NewTupleSet()
	for tuple := range set {
		ts.Add(tuple)
	}
	return ts
}

func (f *FlowSpec) Aggregate(u FlowUpdate) {
	f.aggregateFlowLabels(u.FlowLabels)
	f.FlowPolicies.aggregate(u.FlowPolicies)
	f.aggregateFlowStats(u)
}

func NewFlowSpec(u FlowUpdate) *FlowSpec {
	flowsRefs := NewTupleSet()
	flowsRefs.Add(u.Tuple)
	flowsStartedRefs := NewTupleSet()
	flowsCompletedRefs := NewTupleSet()
	flowsRefsActive := NewTupleSet()

	switch u.Type {
	case UpdateTypeReport:
		flowsStartedRefs.Add(u.Tuple)
		flowsRefsActive.Add(u.Tuple)
	case UpdateTypeExpire:
		flowsCompletedRefs.Add(u.Tuple)
	}

	s := FlowStats{
		FlowReportedStats: FlowReportedStats{
			NumFlows:              flowsRefs.Len(),
			NumFlowsStarted:       flowsStartedRefs.Len(),
			NumFlowsCompleted:     flowsCompletedRefs.Len(),
			PacketsIn:             u.PacketsIn,
			BytesIn:               u.BytesIn,
			PacketsOut:            u.PacketsOut,
			BytesOut:              u.BytesOut,
			HTTPRequestsAllowedIn: u.HTTPRequestsAllowedIn,
			HTTPRequestsDeniedIn:  u.HTTPRequestsDeniedIn,
		},
		flowReferences: flowReferences{
			// flowsRefs track the flows that were tracked
			// in the give interval
			flowsRefs:          flowsRefs,
			flowsStartedRefs:   flowsStartedRefs,
			flowsCompletedRefs: flowsCompletedRefs,
			// flowsRefsActive tracks the active (non-completed)
			// flows associated with the flowMeta
			flowsRefsActive: flowsRefsActive,
		},
	}
	l := FlowLabels{make(map[string]string), make(map[string]string)}
	for k, v := range u.SrcLabels {
		l.SrcLabels[k] = v
	}
	for k, v := range u.DstLabels {
		l.DstLabels[k] = v
	}
	p := make(FlowPolicies)
	for k := range u.FlowPolicies {
		p[k] = emptyValue
	}
	return &FlowSpec{l, p, s}
}

func (f *FlowLabels) aggregateFlowLabels(u FlowLabels) {
	f.SrcLabels = intersectLabels(u.SrcLabels, f.SrcLabels)
	f.DstLabels = intersectLabels(u.DstLabels, f.DstLabels)
}

func intersectLabels(in, out map[string]string) map[string]string {
	common := map[string]string{}
	for k := range out {
		// Skip Calico labels from the logs
		if strings.HasPrefix(k, "projectcalico.org/") {
			continue
		}
		if v, ok := in[k]; ok && v == out[k] {
			common[k] = v
		}
	}
	return common
}

func (fp FlowPolicies) aggregate(u FlowPolicies) {
	for p := range u {
		fp[p] = emptyValue
	}
}

func (f *FlowStats) aggregateFlowStats(u FlowUpdate) {
	// TODO(doublek): Handle metadata updates.
	switch {
	case u.Type == UpdateTypeReport && !f.flowsRefsActive.Contains(u.Tuple):
		f.flowsStartedRefs.Add(u.Tuple)
		f.flowsRefsActive.Add(u.Tuple)
	case u.Type == UpdateTypeExpire:
		f.flowsCompletedRefs.Add(u.Tuple)
		f.flowsRefsActive.Discard(u.Tuple)
	}
	f.flowsRefs.Add(u.Tuple)
	f.NumFlows = f.flowsRefs.Len()
	f.NumFlowsStarted = f.flowsStartedRefs.Len()
	f.NumFlowsCompleted = f.flowsCompletedRefs.Len()
	f.PacketsIn += u.PacketsIn
	f.BytesIn += u.BytesIn
	f.PacketsOut += u.PacketsOut
	f.BytesOut += u.BytesOut
	f.HTTPRequestsAllowedIn += u.HTTPRequestsAllowedIn
	f.HTTPRequestsDeniedIn += u.HTTPRequestsDeniedIn
}

func ipStrTo16Byte(ipStr string) [16]byte {
	addr := net.ParseIP(ipStr)
	var addrB [16]byte
	copy(addrB[:], addr.To16()[:16])
	return addrB
}

func flattenLabels(labels map[string]string) []string {
	respSlice := []string{}
	for k, v := range labels {
		l := fmt.Sprintf("%v=%v", k, v)
		respSlice = append(respSlice, l)
	}

	return respSlice
}

func unflattenLabels(labelSlice []string) map[string]string {
	resp := map[string]string{}
	for _, label := range labelSlice {
		labelKV := strings.Split(label, "=")
		if len(labelKV) != 2 {
			continue
		}
		resp[labelKV[0]] = labelKV[1]
	}

	return resp
}
