// Copyright (c) 2019 Tigera, Inc. All rights reserved.

package app

import (
	"time"

	"github.com/tigera/flowsynth/pkg/flows"
)

type SpecServiceBytesAnomaly struct {
	Service     string `yaml:"Service"`
	NumFlows    int    `yaml:"NumFlows"`
	NumBytesIn  int    `yaml:"NumBytesIn"`
	NumBytesOut int    `yaml:"NumBytesOut"`
	DestPort    int    `yaml:"DestPort"`
}

func (s SpecServiceBytesAnomaly) addEventLogs(updates map[string]chan<- *flows.FlowUpdate, at time.Time, app WrappableApp) {
	pod := app.GetPodByServiceName(s.Service)

	for i := 0; i < s.NumFlows; i++ {
		fu := MakeFlowUpdates(FlowUpdatesConfig{
			Src:      ExternalHost(randIP()),
			Dest:     pod,
			DestPort: s.DestPort,
			BytesIn:  s.NumBytesIn,
			BytesOut: s.NumBytesOut,
		})
		for _, f := range fu {
			updates[pod.Node] <- f
		}
	}
}
