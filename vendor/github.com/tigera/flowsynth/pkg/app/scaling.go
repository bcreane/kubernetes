// Copyright (c) 2018-2019 Tigera, Inc. All rights reserved.

package app

import (
	"time"

	"github.com/tigera/flowsynth/pkg/flows"
	"github.com/tigera/flowsynth/pkg/scheduler"
)

type thresholdPodScaler struct {
	podScheduler
	flowsPerSecPod float64
	threshold      float64 // proportion above or below ideal FPS to trigger autoscale
}

type Scaling struct {
	thresholdPodScaler
	flowsPerSec float64
	destPort    int
	scaler      TrafficScaler
}

type ScalingConfig struct {
	Namespace   string        `yaml:"Namespace"`
	Name        string        `yaml:"Name"`
	NumPods     int           `yaml:"NumPods"`
	FlowsPerSec float64       `yaml:"FlowsPerSec"`
	DestPort    int           `yaml:"DestPort"`
	Scaler      TrafficScaler `yaml:"Scaler"`
	Threshold   float64       `yaml:"Threshold"`
}

func NewScaling(cfg ScalingConfig, s scheduler.Scheduler) *Scaling {
	return &Scaling{
		thresholdPodScaler: thresholdPodScaler{
			podScheduler: podScheduler{
				namespace: cfg.Namespace,
				name:      cfg.Name,
				s:         s,
			},
			threshold:      cfg.Threshold,
			flowsPerSecPod: cfg.FlowsPerSec / float64(cfg.NumPods),
		},
		flowsPerSec: cfg.FlowsPerSec,
		destPort:    cfg.DestPort,
		scaler:      cfg.Scaler,
	}
}

func (a *Scaling) GetFlowData(start time.Time, end time.Time, updates map[string]chan<- *flows.FlowUpdate) {
	// scale the number of pods
	scale := a.scaler.Scale(start)
	fps := a.flowsPerSec * scale
	a.scalePods(fps)

	numSec := end.Sub(start).Seconds()
	numFlows := int(numSec * a.flowsPerSec * scale)
	for i := 0; i < numFlows; i++ {
		dst, _ := a.randPod()
		update := MakeFlowUpdates(FlowUpdatesConfig{
			Src:      ExternalHost(randIP()),
			Dest:     dst,
			DestPort: a.destPort,
		})
		for _, u := range update {
			updates[dst.Node] <- u
		}
	}
	return
}

func (a *thresholdPodScaler) scalePods(flowsPerSec float64) {
	targetPods := flowsPerSec / a.flowsPerSecPod
	ratio := targetPods / float64(len(a.pods))
	if ratio > (1 + a.threshold) {
		toAdd := int(targetPods) - len(a.pods)
		for i := 0; i < toAdd; i++ {
			a.newPod()
		}
	} else if ratio < (1 - a.threshold) {
		toDel := len(a.pods) - int(targetPods)
		// don't scale below one pod
		if toDel >= len(a.pods) {
			toDel = len(a.pods) - 1
		}
		for i := 0; i < toDel; i++ {
			pref, pidx := a.randPod()
			a.s.ReleaseIP(pref.IP.String())
			a.deletePodAtIndex(pidx)
		}
	}
	// always have at least one pod
	if len(a.pods) == 0 {
		a.newPod()
	}
}

func (a *Scaling) GetPodByServiceName(name string) Pod {
	// Pick any pod and ignore the service name
	p, _ := a.randPod()
	return p
}
