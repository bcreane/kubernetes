// Copyright (c) 2018-2019 Tigera, Inc. All rights reserved.

package app

import (
	"fmt"
	"math/rand"
	"net"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/tigera/flowsynth/pkg/flows"
	"github.com/tigera/flowsynth/pkg/scheduler"
)

type MultiServiceConfig struct {
	Namespace string          `yaml:"Namespace"`
	Services  []ServiceConfig `yaml:"Services"`
}

// ServiceConfig defines the operation of a single service in a multiservice app.
// We adopt the terminology from the Envoy Proxy, where downstream and upstream
// are from the perspective of responses from the service.
//
//  "Downstream"                 "Upstream"
//              +------------+
// Ingress ---->|            |-----> ExternalNets
//              | Service A  |
// ...--------->|            |---+
//              +------------+   |     +-----------+
//                               +---->| Service B |---->...
//                                     +-----------+
//
// The traffic model is that a service gets flows in from outside the cluster
// via "Ingress".  It can also get flows from other services (...). Each
// downstream flow can trigger upstream flows in proportion, by setting their
// weights.  Upstream flows can be either to external IPs, or to other services
// in the application.  Take care not to introduce loops in the service graph
// as this will cause a crash.
type ServiceConfig struct {
	Name string
	Port int

	// Number of incoming flows per second handled by each pod at full capacity
	FlowsPerSecPod float64
	// Proportion above or below capacity to trigger pod scaling.  For example
	// if set to 0.5, there would need to be 50% too many pods to trigger
	// killing some, or 50% too few to trigger creating some.
	Threshold float64

	// Number of flows from outside the cluster coming into the service at
	// "normal" scale.
	IngressFlowsPerSec float64
	// Scales the ingress traffic.
	Scaler TrafficScaler
	// IngressNets determines the networks Ingress flows come from.  Empty
	// list uses the default of 35.32.0.0/16
	IngressNets []*net.IPNet

	Upstreams []UpstreamConfig
}

func (s ServiceConfig) MarshalYAML() (interface{}, error) {
	var ingressNets []string
	for _, net := range s.IngressNets {
		ingressNets = append(ingressNets, net.String())
	}
	sc := struct {
		Name               string           `yaml:"Name"`
		Port               int              `yaml:"Port"`
		FlowsPerSecPod     float64          `yaml:"FlowsPerSecPod"`
		Threshold          float64          `yaml:"Threshold"`
		IngressFlowsPerSec float64          `yaml:"IngressFlowsPerSec"`
		Scaler             TrafficScaler    `yaml:"Scaler"`
		IngressNets        []string         `yaml:"IngressNets"`
		Upstreams          []UpstreamConfig `yaml:"Upstreams"`
	}{
		s.Name,
		s.Port,
		s.FlowsPerSecPod,
		s.Threshold,
		s.IngressFlowsPerSec,
		s.Scaler,
		ingressNets,
		s.Upstreams,
	}
	return &sc, nil
}

func (s *ServiceConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {

	sc := struct {
		Name               string           `yaml:"Name"`
		Port               int              `yaml:"Port"`
		FlowsPerSecPod     float64          `yaml:"FlowsPerSecPod"`
		Threshold          float64          `yaml:"Threshold"`
		IngressFlowsPerSec float64          `yaml:"IngressFlowsPerSec"`
		Scaler             TrafficScaler    `yaml:"Scaler"`
		IngressNets        []string         `yaml:"IngressNets"`
		Upstreams          []UpstreamConfig `yaml:"Upstreams"`
	}{}
	err := unmarshal(&sc)
	if err != nil {
		return err
	}
	s.Name = sc.Name
	s.Port = sc.Port
	s.FlowsPerSecPod = sc.FlowsPerSecPod
	s.Threshold = sc.Threshold
	s.IngressFlowsPerSec = sc.IngressFlowsPerSec
	s.Scaler = sc.Scaler
	s.Upstreams = sc.Upstreams
	for _, cidr := range sc.IngressNets {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("Could not parse CIDR %v: %v", cidr, err)
		}

		s.IngressNets = append(s.IngressNets, n)
	}
	return nil
}

// UpstreamConfig specifies how a service calls out to other services within
// or outside the cluster.
type UpstreamConfig struct {
	// Weight determines the rate of upstreamFlowScale flows based on downstream
	// requests.  A value of 1.0 means every downstream flow generates 1.0
	// upstreamFlowScale flows to this target.
	Weight float64

	// ConstantFlowsPerSec is the rate of flows to the upstreamFlowScale, independent of
	// and on top of those triggered by downstream requests.  Used to create
	// a service that calls another service at a constant rate, independent
	// of traffic.
	ConstantFlowsPerSec float64

	// If set, means that the upstreamFlowScale target is a named service in this
	// application.
	// TODO: Call in-cluster services in other applications?
	Service string

	// If set, means that the target is an external service.  Cannot be combined
	// with `Service`
	ExternalNets []*net.IPNet
	ExternalPort int
}

func (c *UpstreamConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var es struct {
		Weight              float64  `yaml:"Weight"`
		ConstantFlowsPerSec float64  `yaml:"ConstantFlowsPerSec"`
		Service             string   `yaml:"Service"`
		ExternalNets        []string `yaml:"ExternalNets"`
		ExternalPort        int      `yaml:"ExternalPort"`
	}

	err := unmarshal(&es)
	if err != nil {
		return err
	}

	c.Weight = es.Weight
	c.ConstantFlowsPerSec = es.ConstantFlowsPerSec
	c.Service = es.Service
	c.ExternalPort = es.ExternalPort

	for _, cidr := range es.ExternalNets {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("Could not parse CIDR %v: %v", cidr, err)
		}

		c.ExternalNets = append(c.ExternalNets, n)
	}

	return nil
}

type multiService struct {
	s         scheduler.Scheduler
	namespace string
	services  map[string]*service
}

type service struct {
	thresholdPodScaler
	cfg              ServiceConfig
	upstreamServices []serviceTarget
	external         []externalTarget

	// used to compute downstream flow rate at each timestep
	curFlowsPerSec float64
	curScale       float64
}

type serviceTarget struct {
	weight   float64
	constant float64
	service  *service
}

type externalTarget struct {
	weight   float64
	constant float64
	nets     []*net.IPNet
	port     int
}

func NewMultiService(cfg MultiServiceConfig, s scheduler.Scheduler) WrappableApp {
	a := &multiService{namespace: cfg.Namespace, s: s, services: make(map[string]*service)}
	// First pass creates all the services and stores in a map.
	for _, sCfg := range cfg.Services {
		s := &service{
			thresholdPodScaler: thresholdPodScaler{
				podScheduler: podScheduler{
					name:      sCfg.Name,
					namespace: cfg.Namespace,
					s:         s,
				},
				threshold:      sCfg.Threshold,
				flowsPerSecPod: sCfg.FlowsPerSecPod,
			},
			cfg: sCfg}
		if s.flowsPerSecPod <= 0 {
			log.WithField("service", sCfg.Name).Fatal("missing FlowsPerSecPod")
		}
		a.services[sCfg.Name] = s
	}

	// Second pass links up all the targets
	for _, svc := range a.services {
		for _, target := range svc.cfg.Upstreams {
			if target.Service != "" {
				// Targets a service in app
				ts, ok := a.services[target.Service]
				if !ok {
					log.WithFields(log.Fields{
						"downstream":        svc.cfg.Name,
						"upstreamFlowScale": target.Service}).Fatal("unable to resolve service")
				}
				svc.upstreamServices = append(svc.upstreamServices, serviceTarget{
					weight:   target.Weight,
					constant: target.ConstantFlowsPerSec,
					service:  ts,
				})
			} else {
				svc.external = append(svc.external, externalTarget{
					weight:   target.Weight,
					constant: target.ConstantFlowsPerSec,
					nets:     target.ExternalNets,
					port:     target.ExternalPort,
				})
			}
		}
	}
	return a
}

func (a *multiService) GetFlowData(start time.Time, end time.Time, updates map[string]chan<- *flows.FlowUpdate) {
	// Reset all svc flow numbers
	for _, svc := range a.services {
		svc.resetFlows()
	}
	// Compute flows per second to all services
	for _, svc := range a.services {
		svc.ingressFlowScale(start)
		svc.constantFlowScale()
	}
	// Scale each service
	for _, svc := range a.services {
		svc.scale()
	}

	numSec := end.Sub(start).Seconds()
	for _, svc := range a.services {
		svc.addIngressFlows(updates, numSec)
		svc.addConstantFlows(updates, numSec)
	}
	return
}

func (a *multiService) GetPodByServiceName(name string) Pod {
	svc, ok := a.services[name]
	if !ok {
		log.WithField("service", name).Panic("service not found")
	}
	p, _ := svc.randPod()
	return p
}

func (svc *service) resetFlows() {
	svc.curFlowsPerSec = 0
}

// ingress adds flow scale to account for ingress
func (svc *service) ingressFlowScale(start time.Time) {
	if svc.cfg.IngressFlowsPerSec <= 0 {
		return
	}
	svc.curScale = svc.cfg.Scaler.Scale(start)
	newFlowsPerSec := svc.cfg.IngressFlowsPerSec * svc.curScale
	svc.curFlowsPerSec += newFlowsPerSec

	// propagate upstreamFlowScale
	for _, st := range svc.upstreamServices {
		st.service.upstreamFlowScale(newFlowsPerSec * st.weight)
	}
}

// constantFlowScale adds flow scale to account for constant flows to upstreams
func (svc *service) constantFlowScale() {
	// propagate upstreamFlowScale
	for _, st := range svc.upstreamServices {
		if st.constant > 0 {
			st.service.upstreamFlowScale(st.constant)
		}
	}
}

func (svc *service) upstreamFlowScale(fps float64) {
	svc.curFlowsPerSec += fps

	// propagate upstreamFlowScale
	for _, st := range svc.upstreamServices {
		st.service.upstreamFlowScale(fps * st.weight)
	}
}

func (svc *service) scale() {
	log.WithFields(log.Fields{"svc": svc.cfg.Name, "flowspersec": svc.curFlowsPerSec})
	svc.scalePods(svc.curFlowsPerSec)
}

func (svc *service) addIngressFlows(updates map[string]chan<- *flows.FlowUpdate, numSec float64) {
	if svc.cfg.IngressFlowsPerSec <= 0 {
		return
	}
	numFlowsFloat := numSec * svc.cfg.IngressFlowsPerSec * svc.curScale
	log.WithFields(log.Fields{
		"svc":   svc.cfg.Name,
		"flows": numFlowsFloat,
	}).Debug("ingress flows")
	numFlows := int(numFlowsFloat)
	for i := 0; i < numFlows; i++ {
		dst, _ := svc.randPod()
		update := MakeFlowUpdates(FlowUpdatesConfig{
			Src:      ExternalHost(randIP(svc.cfg.IngressNets...)),
			Dest:     dst,
			DestPort: svc.cfg.Port,
		})
		for _, u := range update {
			updates[dst.Node] <- u
		}
	}
	svc.addUpstreamFlows(updates, numFlowsFloat)
}

func (svc *service) addUpstreamFlows(updates map[string]chan<- *flows.FlowUpdate, numFlows float64) {
	for _, et := range svc.external {
		eFlows := numFlows * et.weight
		addExternalFlows(updates, eFlows, svc, et)
	}
	for _, st := range svc.upstreamServices {
		sFlows := numFlows * st.weight
		addInternalFlows(updates, sFlows, svc, st.service)
	}
}

func (svc *service) addConstantFlows(updates map[string]chan<- *flows.FlowUpdate, numSec float64) {
	for _, et := range svc.external {
		if et.constant > 0 {
			eFlows := numSec * et.constant
			addExternalFlows(updates, eFlows, svc, et)
		}
	}
	for _, st := range svc.upstreamServices {
		if st.constant > 0 {
			sFlows := numSec * st.constant
			addInternalFlows(updates, sFlows, svc, st.service)
		}
	}
}

func addExternalFlows(updates map[string]chan<- *flows.FlowUpdate, numFlows float64, src *service, dst externalTarget) {
	log.WithFields(log.Fields{
		"svc":   src.cfg.Name,
		"flows": numFlows,
	}).Debug("external flows")
	n := int(numFlows)
	for i := 0; i < n; i++ {
		srcPod, _ := src.randPod()
		update := MakeFlowUpdates(FlowUpdatesConfig{
			Src:      srcPod,
			Dest:     ExternalHost(randIP(dst.nets...)),
			DestPort: dst.port,
			Reporter: flows.FlowLogReporterSrc,
		})
		for _, u := range update {
			updates[srcPod.Node] <- u
		}
	}
}

func addInternalFlows(updates map[string]chan<- *flows.FlowUpdate, numFlows float64, src, dst *service) {
	log.WithFields(log.Fields{
		"src":   src.cfg.Name,
		"dest":  dst.cfg.Name,
		"flows": numFlows,
	}).Debug("internal flows")
	n := int(numFlows)
	for i := 0; i < n; i++ {
		srcPod, _ := src.randPod()
		dstPod, _ := dst.randPod()
		srcPort := rand.Intn(2 << 15)
		fCfg := FlowUpdatesConfig{
			Src:      srcPod,
			SrcPort:  srcPort,
			Dest:     dstPod,
			DestPort: dst.cfg.Port,
			Reporter: flows.FlowLogReporterSrc,
		}
		fCfg.SetDefaults()
		// Src Reported
		update := MakeFlowUpdates(fCfg)
		for _, u := range update {
			updates[srcPod.Node] <- u
		}
		// Dst Reported
		fCfg.Reporter = flows.FlowLogReporterDst
		update = MakeFlowUpdates(fCfg)
		for _, u := range update {
			updates[dstPod.Node] <- u
		}
	}

	dst.addUpstreamFlows(updates, numFlows)
}
