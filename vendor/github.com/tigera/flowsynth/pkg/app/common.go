// Copyright (c) 2018-2019 Tigera, Inc. All rights reserved.

package app

import (
	"math"
	"math/rand"
	"net"
	"time"

	"github.com/tigera/flowsynth/pkg/flows"
)

const letters = "abcdefghijklmnopqrstuvwxyz1234567890"

func init() {
	rand.Seed(time.Now().UnixNano())
}

func randString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

const SecPerDay = 24 * 60 * 60
const SecPerWeek = 7 * SecPerDay

// First Sunday in 2010 --- serves as our local reference for start of the week.
var LocalSunday = time.Date(2010, time.January, 3, 0, 0, 0, 0, time.Local)

type TrafficScaler struct {
	Weekly   []Phaser `yaml:"Weekly"`
	Daily    []Phaser `yaml:"Daily"`
	Constant float64  `yaml:"Constant"`
	Noise    float64  `yaml:"Noise"`
}

type Phaser struct {
	Amp   float64 `yaml:"Amp"`
	Phase float64 `yaml:"Phase"` // in radians
}

func (s *TrafficScaler) Scale(t time.Time) float64 {
	secs := t.Sub(LocalSunday).Seconds()
	var out float64 = s.Constant
	for i, phaser := range s.Weekly {
		x := (secs / SecPerWeek * 2 * math.Pi * float64(i+1)) + phaser.Phase
		out += phaser.Amp * (1.0 - math.Cos(x)) / 2.0
	}
	for i, phaser := range s.Daily {
		x := (secs / SecPerDay * 2 * math.Pi * float64(i+1)) + phaser.Phase
		out += phaser.Amp * (1.0 - math.Cos(x)) / 2.0
	}
	out += s.Noise * rand.NormFloat64() * out
	if out > 0 {
		return out
	}
	return 0
}

func RandScaler() TrafficScaler {
	s := TrafficScaler{}
	var a float64 = 1
	s.Constant = 0.3 * rand.Float64()
	a -= s.Constant
	s.Noise = 0.5 * rand.Float64()
	for a > 0.01 {
		dp := Phaser{a * rand.Float64() * 0.5, 2 * math.Pi * rand.Float64()}
		a -= dp.Amp
		s.Daily = append(s.Daily, dp)
		wp := Phaser{a * rand.Float64() * 0.5, 2 * math.Pi * rand.Float64()}
		a -= wp.Amp
		s.Weekly = append(s.Weekly, wp)
	}
	return s
}

type FlowUpdatesConfig struct {
	Src        FlowEndpoint
	SrcPort    int
	Dest       FlowEndpoint
	DestPort   int
	PacketsIn  int
	PacketsOut int
	BytesIn    int
	BytesOut   int
	Reporter   flows.FlowLogReporter
	Action     flows.FlowLogAction
}

type FlowEndpoint interface {
	getIP() net.IP
	getMeta() flows.EndpointMetadata
}

func (p Pod) getIP() net.IP {
	return p.IP
}

func (p Pod) getMeta() flows.EndpointMetadata {
	return flows.EndpointMetadata{
		Type:           flows.FlowLogEndpointTypeWep,
		Name:           p.Name,
		Namespace:      p.Namespace,
		AggregatedName: p.AggregatedName,
	}
}

type ExternalHost net.IP

func (e ExternalHost) getIP() net.IP {
	return net.IP(e)
}

func (e ExternalHost) getMeta() flows.EndpointMetadata {
	return flows.EndpointMetadata{
		Type:           flows.FlowLogEndpointTypeNet,
		Name:           flows.FlowLogFieldNotIncluded,
		Namespace:      flows.FlowLogFieldNotIncluded,
		AggregatedName: string(flows.PublicNet),
	}
}

const (
	DefaultPacketsIn  = 300
	DefaultPacketsOut = 200
	DefaultBytesIn    = 30000
	DefaultBytesOut   = 20000
)

func (c *FlowUpdatesConfig) SetDefaults() {
	if c.SrcPort <= 0 {
		c.SrcPort = rand.Intn(2 << 15)
	}
	if c.PacketsIn <= 0 {
		c.PacketsIn = rand.Intn(DefaultPacketsIn)
	}
	if c.PacketsOut <= 0 {
		c.PacketsOut = rand.Intn(DefaultPacketsOut)
	}
	if c.BytesIn <= 0 {
		c.BytesIn = rand.Intn(DefaultBytesIn)
	}
	if c.BytesOut <= 0 {
		c.BytesOut = rand.Intn(DefaultBytesOut)
	}
	if c.Reporter == "" {
		c.Reporter = flows.FlowLogReporterDst
	}
	if c.Action == "" {
		c.Action = flows.FlowLogActionAllow
	}
}

func MakeFlowUpdates(c FlowUpdatesConfig) []*flows.FlowUpdate {
	c.SetDefaults()

	t := flows.Tuple{
		Proto: 6, // TCP
		L4Src: c.SrcPort,
		L4Dst: c.DestPort,
	}
	copy(t.Src[:], c.Src.getIP().To16())
	copy(t.Dst[:], c.Dest.getIP().To16())
	m := flows.FlowMeta{
		Tuple:    t,
		SrcMeta:  c.Src.getMeta(),
		DstMeta:  c.Dest.getMeta(),
		Action:   c.Action,
		Reporter: c.Reporter,
	}
	l := flows.FlowLabels{make(map[string]string), make(map[string]string)}
	p := make(flows.FlowPolicies)
	stats := flows.FlowStats{
		FlowReportedStats: flows.FlowReportedStats{
			PacketsIn:  c.PacketsIn,
			PacketsOut: c.PacketsOut,
			BytesIn:    c.BytesIn,
			BytesOut:   c.BytesOut,
		},
	}

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
