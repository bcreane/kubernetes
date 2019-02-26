// Copyright (c) 2018-2019 Tigera, Inc. All rights reserved.

package app

import (
	"math/rand"
	"time"

	"github.com/tigera/flowsynth/pkg/flows"
	"github.com/tigera/flowsynth/pkg/scheduler"
)

type podScheduler struct {
	namespace string
	name      string
	s         scheduler.Scheduler
	pods      []Pod
}

type Basic struct {
	podScheduler
	NumPods     int
	FlowsPerSec float64
	DestPort    int
	Scaler      TrafficScaler
}

type BasicConfig struct {
	Namespace   string        `yaml:"Namespace"`
	Name        string        `yaml:"Name"`
	NumPods     int           `yaml:"NumPods"`
	FlowsPerSec float64       `yaml:"FlowsPerSec"`
	DestPort    int           `yaml:"DestPort"`
	Scaler      TrafficScaler `yaml:"Scaler"`
}

func NewBasic(cfg BasicConfig, s scheduler.Scheduler) *Basic {
	return &Basic{
		podScheduler: podScheduler{
			namespace: cfg.Namespace,
			name:      cfg.Name,
			s:         s,
		},
		NumPods:     cfg.NumPods,
		FlowsPerSec: cfg.FlowsPerSec,
		DestPort:    cfg.DestPort,
		Scaler:      cfg.Scaler,
	}
}

func (a *Basic) GetFlowData(start time.Time, end time.Time, updates map[string]chan<- *flows.FlowUpdate) {
	// Handle creating pods
	for len(a.pods) < a.NumPods {
		a.newPod()
	}

	numSec := end.Sub(start).Seconds()
	numFlows := int(numSec * a.FlowsPerSec * a.Scaler.Scale(start))
	for i := 0; i < numFlows; i++ {
		dst, _ := a.randPod()
		update := MakeFlowUpdates(FlowUpdatesConfig{
			Src:      ExternalHost(randIP()),
			Dest:     dst,
			DestPort: a.DestPort,
		})
		for _, u := range update {
			updates[dst.Node] <- u
		}
	}
	return
}

func (a *podScheduler) newPod() {
	suffix := randString(6)
	ip, node := a.s.GetPodIP()
	ref := Pod{
		Name:           a.name + "-" + suffix,
		Namespace:      a.namespace,
		AggregatedName: a.name + "-*",
		Node:           node,
		IP:             ip,
	}
	a.pods = append(a.pods, ref)
}

func (a *podScheduler) randPod() (Pod, int) {
	r := rand.Intn(len(a.pods))
	return a.pods[r], r
}

// deletePodAtIndex removes the indexed pod from the pods slice without copying
func (a *podScheduler) deletePodAtIndex(i int) {
	// copy the end pod over the one we are deleting
	l := len(a.pods) - 1
	a.pods[i] = a.pods[l]
	// slice out the end pod
	a.pods = a.pods[:l]
}

func (a *Basic) GetPodByServiceName(name string) Pod {
	// Pick any pod and ignore the service name
	p, _ := a.randPod()
	return p
}
