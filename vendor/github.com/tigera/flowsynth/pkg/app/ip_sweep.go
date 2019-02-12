// Copyright (c) 2019 Tigera, Inc. All rights reserved.

package app

import (
	"math/rand"
	"net"
	"time"

	"github.com/tigera/flowsynth/pkg/flows"
)

type SpecIPSweep struct {
	Service string `yaml:"Service"`
}

func (s SpecIPSweep) addEventLogs(updates map[string]chan<- *flows.FlowUpdate, at time.Time, app WrappableApp) {
	pod := app.GetPodByServiceName(s.Service)
	// TODO: another IP in the cluster?
	// scan 255 IPs in the local subnet, sending a single packet to each, no reply.
	for i := 0; i <= 255; i++ {
		destIP := make(net.IP, 16)
		copy(destIP, pod.IP.To16())
		if pod.IP.To16()[15] == byte(i) {
			continue
		}
		destIP[15] = byte(i)
		eLogs := podSrcFlowUpdate(pod, rand.Intn(2<<15), "src", flows.FlowLogActionAllow)
		flowUpdateSetDest(eLogs, destIP, 80, flows.FlowLogEndpointTypeNet, "-", "-", string(flows.PublicNet))
		stats := eLogs[0].FlowStats
		stats.PacketsOut = 1
		stats.BytesOut = 49
		for _, u := range eLogs {
			updates[pod.Node] <- u
		}
	}

}
