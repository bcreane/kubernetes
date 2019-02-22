// Copyright (c) 2019 Tigera, Inc. All rights reserved.

package app

import (
	"fmt"
	"net"
	"time"

	"github.com/tigera/flowsynth/pkg/flows"
)

type SpecOutboundIP struct {
	Service  string
	NumFlows int
	Nets     []*net.IPNet
}

func (s *SpecOutboundIP) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var es struct {
		Service  string   `yaml:"Service"`
		NumFlows int      `yaml:"NumFlows"`
		Nets     []string `yaml:"Nets"`
	}

	err := unmarshal(&es)
	if err != nil {
		return err
	}

	s.Service = es.Service
	s.NumFlows = es.NumFlows

	for _, cidr := range es.Nets {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("Could not parse CIDR %v: %v", cidr, err)
		}

		s.Nets = append(s.Nets, n)
	}

	return nil
}

func (s SpecOutboundIP) addEventLogs(updates map[string]chan<- *flows.FlowUpdate, at time.Time, app WrappableApp) {
	pod := app.GetPodByServiceName(s.Service)

	for i := 0; i < s.NumFlows; i++ {
		fu := MakeFlowUpdates(FlowUpdatesConfig{
			Src:      pod,
			Dest:     ExternalHost(randIP(s.Nets...)),
			Reporter: flows.FlowLogReporterSrc,
		})
		for _, f := range fu {
			updates[pod.Node] <- f
		}
	}
}
