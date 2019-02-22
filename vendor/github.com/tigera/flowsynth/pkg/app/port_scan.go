// Copyright (c) 2018-2019 Tigera, Inc. All rights reserved.

package app

import (
	"math/rand"
	"time"

	"github.com/tigera/flowsynth/pkg/flows"
)

type SpecPortScan struct {
	Service string `yaml:"Service"`
}

func (s SpecPortScan) addEventLogs(updates map[string]chan<- *flows.FlowUpdate, at time.Time, app WrappableApp) {
	pod := app.GetPodByServiceName(s.Service)
	// TODO: another IP in the cluster?
	destIP := randIP()
	// scan 1024 ports, sending a single packet to each, no reply.
	for i := 1; i <= 1024; i++ {
		eLogs := podSrcFlowUpdate(pod, rand.Intn(2<<15), "src", flows.FlowLogActionAllow)
		flowUpdateSetDest(eLogs, destIP, i, flows.FlowLogEndpointTypeNet, "-", "-", string(flows.PublicNet))
		stats := eLogs[0].FlowStats
		stats.PacketsOut = 1
		stats.BytesOut = 49
		for _, u := range eLogs {
			updates[pod.Node] <- u
		}
	}

}
