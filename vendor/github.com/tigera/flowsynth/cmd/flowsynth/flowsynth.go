// Copyright (c) 2018-2019 Tigera, Inc. All rights reserved.

package main

import (
	"context"
	"flag"
	"fmt"
	"net"

	"github.com/tigera/flowsynth/pkg/synthesizer"

	log "github.com/sirupsen/logrus"

	"github.com/tigera/flowsynth/pkg/scheduler"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "./config.yaml", "Config file path")
	flag.Parse()

	log.SetLevel(log.InfoLevel)

	config := parseConfig(configPath)
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
