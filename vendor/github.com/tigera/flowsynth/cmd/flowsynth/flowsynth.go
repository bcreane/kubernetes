// Copyright (c) 2018-2019 Tigera, Inc. All rights reserved.

package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"runtime/pprof"
	"runtime/trace"

	log "github.com/sirupsen/logrus"

	"github.com/tigera/flowsynth/pkg/scheduler"
	"github.com/tigera/flowsynth/pkg/synthesizer"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "./config.yaml", "Config file path")
	flag.Parse()

	log.SetLevel(log.InfoLevel)

	config := parseConfig(configPath)

	if config.CPUProfilePath != "" {
		p, err := os.Create("cpuprofile")
		if err != nil {
			log.Fatal(err)
		}
		defer p.Close()
		pprof.StartCPUProfile(p)
		defer pprof.StopCPUProfile()
	}
	if config.TracePath != "" {
		t, err := os.Create("trace.out")
		if err != nil {
			log.Fatal(err)
		}
		defer t.Close()
		trace.Start(t)
		defer trace.Stop()
	}
	nodes := []string{}
	for i := 0; i < config.NumNodes; i++ {
		nodes = append(nodes, fmt.Sprintf("node%02d", i))
	}
	_, cidr, err := net.ParseCIDR(config.PodNetwork)
	if err != nil {
		log.Panic(err)
	}
	s := scheduler.NewCIDRScheduler(
		nodes,
		*cidr,
	)
	sy := synthesizer.NewSynthesizer(nodes)

	for _, aCfg := range config.Apps {
		a := aCfg.New(s)
		sy.RegisterApp(a)
	}
	for _, oCfg := range config.Outs {
		o := oCfg.New()
		sy.RegisterOutput(o)
		o.Start(context.Background())
	}
	defer sy.StopOutputs()

	start, end := config.getTimes()
	sy.Synthesize(start, end)
}
