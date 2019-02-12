// Copyright (c) 2018-2019 Tigera, Inc. All rights reserved.

package synthesizer

import (
	"time"

	"github.com/tigera/flowsynth/pkg/flows"

	log "github.com/sirupsen/logrus"
)

// AggregationKind determines the flow log key
type AggregationKind int

const (
	// Default is based on purely duration.
	Default AggregationKind = iota
	// SourcePort accumulates tuples with everything same but the source port
	SourcePort
	// PrefixName accumulates tuples with everything same but the prefix name
	PrefixName
)

const noRuleActionDefined = ""

// aggregator is responsible for creating, aggregating, and storing
// aggregated flow logs until the flow logs are exported.
type aggregator struct {
	kind            AggregationKind
	flowStore       map[flows.FlowMeta]*flows.FlowSpec
	includeLabels   bool
	includePolicies bool
	handledAction   flows.FlowLogAction
}

// NewFlowLogAggregator constructs a FlowLogAggregator
func NewFlowLogAggregator() *aggregator {
	return &aggregator{
		kind:      Default,
		flowStore: make(map[flows.FlowMeta]*flows.FlowSpec),
	}
}

func (c *aggregator) AggregateOver(kind AggregationKind) *aggregator {
	c.kind = kind
	return c
}

func (c *aggregator) IncludeLabels(b bool) *aggregator {
	c.includeLabels = b
	return c
}

func (c *aggregator) IncludePolicies(b bool) *aggregator {
	c.includePolicies = b
	return c
}

func (c *aggregator) ForAction(a flows.FlowLogAction) *aggregator {
	c.handledAction = a
	return c
}

// FeedUpdate constructs and aggregates flow logs from MetricUpdates.
func (c *aggregator) FeedUpdate(u flows.FlowUpdate) {
	// Filter out any action that we aren't configured to handle.
	if c.handledAction != noRuleActionDefined && c.handledAction != u.Action {
		log.Debugf("Update %v not handled", u)
		return
	}

	log.WithField("update", u).Debug("Flow Log Aggregator got Update")
	flowMeta := AggFlowMeta(u.FlowMeta, c.kind)
	fl, ok := c.flowStore[flowMeta]
	if !ok {
		fl = flows.NewFlowSpec(u)
	} else {
		fl.Aggregate(u)
	}
	c.flowStore[flowMeta] = fl

	return
}

// Get returns all aggregated flow logs, as a list of string pointers, since the last time a Get
// was called. Calling Get will also clear the stored flow logs once the flow logs are returned.
func (c *aggregator) Get(start, end time.Time) []*flows.FlowLog {
	log.Debug("Get from flow log aggregator")
	resp := make([]*flows.FlowLog, 0, len(c.flowStore))
	for flowMeta, flowSpecs := range c.flowStore {
		flowLog := flows.FlowData{flowMeta, *flowSpecs}.ToFlowLog(start, end, c.includeLabels, c.includePolicies)
		resp = append(resp, &flowLog)
		c.calibrateFlowStore(flowMeta)
	}
	return resp
}

func (c *aggregator) calibrateFlowStore(flowMeta flows.FlowMeta) {
	// discontinue tracking the stats associated with the
	// flow meta if no more associated 5-tuples exist.
	if c.flowStore[flowMeta].GetActiveFlowsCount() == 0 {
		delete(c.flowStore, flowMeta)
		return
	}

	// reset flow stats for the next interval
	c.flowStore[flowMeta].Reset()
}

func newFlowMetaWithSourcePortAggregation(f flows.FlowMeta) flows.FlowMeta {
	f.Tuple.L4Src = flows.UnsetIntField
	return f
}

func newFlowMetaWithPrefixNameAggregation(f flows.FlowMeta) flows.FlowMeta {
	f.Tuple.Src = [16]byte{}
	f.Tuple.L4Src = flows.UnsetIntField
	f.Tuple.Dst = [16]byte{}
	f.SrcMeta.Name = flows.FlowLogFieldNotIncluded
	f.DstMeta.Name = flows.FlowLogFieldNotIncluded
	return f
}

func AggFlowMeta(fm flows.FlowMeta, kind AggregationKind) flows.FlowMeta {
	switch kind {
	case Default:
		return fm
	case SourcePort:
		return newFlowMetaWithSourcePortAggregation(fm)
	case PrefixName:
		return newFlowMetaWithPrefixNameAggregation(fm)
	}
	panic("AggregationKind not recognized")
}
