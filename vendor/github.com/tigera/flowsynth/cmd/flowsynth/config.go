// Copyright (c) 2018-2019 Tigera, Inc. All rights reserved.

package main

import (
	"io/ioutil"
	"time"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"

	"github.com/tigera/flowsynth/pkg/app"
	"github.com/tigera/flowsynth/pkg/out"
	"github.com/tigera/flowsynth/pkg/util"
)

type Config struct {
	NumNodes   int             `yaml:"NumNodes"`
	PodNetwork string          `yaml:"PodNetwork"`
	StartTime  string          `yaml:"StartTime"`
	EndTime    string          `yaml:"EndTime"`
	Apps       []app.AppConfig `yaml:"Apps"`
	Outs       []out.OutConfig `yaml:"Outs"`
}

func parseConfig(path string) Config {
	cfg := Config{}
	data, err := ioutil.ReadFile(path)
	if err != nil {
		log.Panic(err)
	}
	err = yaml.Unmarshal(data, &cfg)
	if err != nil {
		log.Panic(err)
	}
	return cfg
}

func (c Config) getTimes() (time.Time, time.Time) {
	s, err := util.ParseANSITime(c.StartTime)
	if err != nil {
		log.Panic(err)
	}
	e, err := util.ParseANSITime(c.EndTime)
	if err != nil {
		log.Panic(err)
	}
	return s, e
}
