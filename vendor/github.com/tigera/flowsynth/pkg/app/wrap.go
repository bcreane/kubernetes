package app

import (
	"net"
	"time"

	"github.com/tigera/flowsynth/pkg/util"

	"github.com/tigera/flowsynth/pkg/scheduler"

	"github.com/tigera/flowsynth/pkg/flows"
)

type WrappedApp struct {
	App    WrappableApp
	Events []Event
}

type WrappedAppConfig struct {
	App    AppConfig `yaml:"App"`
	Events []Event   `yaml:"Events"`
}

type Event struct {
	At                     time.Time
	PortScan               *SpecPortScan
	IPSweep                *SpecIPSweep
	InboundConnectionSpike *SpecInboundConnectionSpike
	ServiceBytesAnomaly    *SpecServiceBytesAnomaly
	OutboundIP             *SpecOutboundIP
}

func (e *Event) UnmarshalYAML(unmarshal func(interface{}) error) error {
	es := struct {
		At                     string                      `yaml:"At"`
		PortScan               *SpecPortScan               `yaml:"PortScan"`
		IPSweep                *SpecIPSweep                `yaml:"IPSweep"`
		InboundConnectionSpike *SpecInboundConnectionSpike `yaml:"InboundConnectionSpike"`
		ServiceBytesAnomaly    *SpecServiceBytesAnomaly    `yaml:"ServiceBytesAnomaly"`
		OutboundIP             *SpecOutboundIP             `yaml:"OutboundIP"`
	}{}
	err := unmarshal(&es)
	if err != nil {
		return err
	}
	at, err := util.ParseANSITime(es.At)
	if err != nil {
		return err
	}
	e.At = at
	e.PortScan = es.PortScan
	e.IPSweep = es.IPSweep
	e.InboundConnectionSpike = es.InboundConnectionSpike
	e.ServiceBytesAnomaly = es.ServiceBytesAnomaly
	e.OutboundIP = es.OutboundIP
	return nil
}

func NewWrappedApp(cfg WrappedAppConfig, s scheduler.Scheduler) *WrappedApp {
	a := cfg.App.New(s)
	return &WrappedApp{App: a.(WrappableApp), Events: cfg.Events}
}

func (a *WrappedApp) GetFlowData(start time.Time, end time.Time, updates map[string]chan<- *flows.FlowUpdate) {
	a.App.GetFlowData(start, end, updates)
	for _, e := range a.Events {
		if (start.Before(e.At) || start.Equal(e.At)) && end.After(e.At) {
			a.addEventLogs(updates, e)
		}
	}
	return
}

func (a *WrappedApp) addEventLogs(updates map[string]chan<- *flows.FlowUpdate, e Event) {
	if e.PortScan != nil {
		e.PortScan.addEventLogs(updates, e.At, a.App)
	}
	if e.IPSweep != nil {
		e.IPSweep.addEventLogs(updates, e.At, a.App)
	}
	if e.InboundConnectionSpike != nil {
		e.InboundConnectionSpike.addEventLogs(updates, e.At, a.App)
	}
	if e.ServiceBytesAnomaly != nil {
		e.ServiceBytesAnomaly.addEventLogs(updates, e.At, a.App)
	}
	if e.OutboundIP != nil {
		e.OutboundIP.addEventLogs(updates, e.At, a.App)
	}
}

func podSrcFlowUpdate(src Pod, srcPort int, reporter string, action flows.FlowLogAction) []*flows.FlowUpdate {
	t := flows.Tuple{
		Proto: 6, // TCP
		L4Src: srcPort,
	}
	copy(t.Src[:], src.IP.To16())
	m := flows.FlowMeta{
		Tuple: t,
		SrcMeta: flows.EndpointMetadata{
			Type:           flows.FlowLogEndpointTypeWep,
			Name:           src.Name,
			Namespace:      src.Namespace,
			AggregatedName: src.AggregatedName,
		},
		Reporter: flows.FlowLogReporter(reporter),
		Action:   action,
	}
	l := flows.FlowLabels{make(map[string]string), make(map[string]string)}
	p := make(flows.FlowPolicies)
	stats := flows.FlowStats{}

	// Include a Report, followed by Expire to indicate that the flow started
	// and finished in this report interval.
	rs := flows.FlowSpec{l, p, stats}
	rd := flows.FlowData{m, rs}
	report := flows.FlowUpdate{rd, flows.UpdateTypeReport}
	es := flows.FlowSpec{l, p, flows.FlowStats{}}
	ed := flows.FlowData{m, es}
	expire := flows.FlowUpdate{ed, flows.UpdateTypeExpire}
	return []*flows.FlowUpdate{&report, &expire}
}

func flowUpdateSetDest(updates []*flows.FlowUpdate, ip net.IP, port int, t flows.FlowLogEndpointType, name, namespace, name_aggr string) {
	for i := 0; i < len(updates); i++ {
		u := updates[i]
		u.Tuple.L4Dst = port
		copy(u.Tuple.Dst[:], ip.To16())
		u.FlowMeta.DstMeta = flows.EndpointMetadata{
			Type:           t,
			Name:           name,
			Namespace:      namespace,
			AggregatedName: name_aggr,
		}
	}
}
