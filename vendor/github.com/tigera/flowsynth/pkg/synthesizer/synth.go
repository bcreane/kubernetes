// Copyright (c) 2018-2019 Tigera, Inc. All rights reserved.

package synthesizer

import (
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/tigera/flowsynth/pkg/app"
	"github.com/tigera/flowsynth/pkg/flows"
	"github.com/tigera/flowsynth/pkg/out"
)

type Synthesizer interface {
	RegisterApp(a app.Application)
	RegisterOutput(o out.Output)
	Synthesize(start, end time.Time)
	StopOutputs()
}

type synth struct {
	apps []app.Application
	outs []out.Output
	aggs map[string]map[flows.FlowLogAction]*aggregator
	wg   sync.WaitGroup
}

func NewSynthesizer(nodes []string) Synthesizer {
	aggs := make(map[string]map[flows.FlowLogAction]*aggregator)
	for _, n := range nodes {
		aggs[n] = make(map[flows.FlowLogAction]*aggregator)
		aggs[n][flows.FlowLogActionAllow] = NewFlowLogAggregator().
			ForAction(flows.FlowLogActionAllow).
			AggregateOver(SourcePort)
		aggs[n][flows.FlowLogActionDeny] = NewFlowLogAggregator().
			ForAction(flows.FlowLogActionDeny).
			AggregateOver(SourcePort)
	}
	return &synth{nil, nil, aggs, sync.WaitGroup{}}
}

func (s *synth) RegisterApp(a app.Application) {
	s.apps = append(s.apps, a)
}

func (s *synth) RegisterOutput(o out.Output) {
	s.outs = append(s.outs, o)
}

func (s *synth) Synthesize(start, end time.Time) {
	t := start
	for t.Before(end) {
		log.WithField("time", t).Debug("Getting logs")

		fChans := s.startAggregation()

		// Fetch all data for 5 minutes
		e := t.Add(5 * time.Minute)
		w := sync.WaitGroup{}
		w.Add(len(s.apps))
		for i := 0; i < len(s.apps); i++ {
			a := s.apps[i]
			go func() {
				a.GetFlowData(t, e, fChans)
				w.Done()
			}()
		}
		w.Wait()
		log.WithField("time", t).Debug("Got logs")

		s.endAggregation(fChans)

		// Output data
		for node, aggs := range s.aggs {
			for _, agg := range aggs {
				flowLogs := agg.Get(t, e)
				for _, l := range flowLogs {
					l.Host = node
					for _, o := range s.outs {
						o.Write(l)
					}
				}
			}
		}
		t = e
		log.WithField("end", t).Info("Done synthesizing timestep")
	}
}

func (s *synth) StopOutputs() {
	for _, o := range s.outs {
		o.Stop()
	}
}

func (s *synth) startAggregation() map[string]chan<- *flows.FlowUpdate {
	fChans := make(map[string]chan<- *flows.FlowUpdate)
	for node, a := range s.aggs {
		ch := make(chan *flows.FlowUpdate)
		fChans[node] = ch

		// Kick off a goroutine that fans out the updates
		go func(ch <-chan *flows.FlowUpdate, allow, deny *aggregator) {
			s.wg.Add(1)
			for u := range ch {
				allow.FeedUpdate(*u)
				deny.FeedUpdate(*u)
			}
			s.wg.Done()
		}(ch, a[flows.FlowLogActionAllow], a[flows.FlowLogActionDeny])
	}
	return fChans
}

func (s *synth) endAggregation(fChans map[string]chan<- *flows.FlowUpdate) {
	// Closing all the channels will cause the goroutines in startAggregation
	// to complete
	for _, ch := range fChans {
		close(ch)
	}
	s.wg.Wait()
}
