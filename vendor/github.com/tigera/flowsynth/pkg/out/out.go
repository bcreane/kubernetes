// Copyright (c) 2018-2019 Tigera, Inc. All rights reserved.

package out

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	log "github.com/sirupsen/logrus"
	"github.com/tigera/flowsynth/pkg/flows"
)

type Output interface {
	Write(flowLog *flows.FlowLog)
	Start(ctx context.Context)
	Stop()
}

type stdOut struct{}

func NewStdOut() Output {
	return stdOut{}
}

func (s stdOut) Write(flowLog *flows.FlowLog) {
	fmt.Println(*flowLog)
}

func (s stdOut) Start(_ context.Context) {
	return
}

func (s stdOut) Stop() {
	return
}

type jsonOut struct {
	out io.Writer
}

func NewJsonOut(out io.Writer) Output {
	return jsonOut{out}
}

func (j jsonOut) Write(flowLog *flows.FlowLog) {
	b, err := json.Marshal(flowLog)
	if err != nil {
		// nothing
		return
	}

	_, err = j.out.Write(append(b, '\n'))
	if err != nil {
		// nothing
		return
	}
}

func (j jsonOut) Start(_ context.Context) {
	return
}

func (j jsonOut) Stop() {

}

type OutConfig struct {
	Type string      `yaml:"Type"`
	Spec interface{} `yaml:"Spec"`
}

type JsonOutConfig struct {
	StdOut bool
	Path   string
}

func JsonOutConfigFromSpec(spec interface{}) JsonOutConfig {
	cfg := JsonOutConfig{}
	m := spec.(map[interface{}]interface{})
	stdout, ok := m["Stdout"]
	if ok {
		cfg.StdOut = stdout.(bool)
		return cfg
	}
	p, ok := m["Path"]
	if !ok {
		log.Fatalf("Couldn't parse JsonOutConfig %v", spec)
	}
	cfg.Path = p.(string)
	return cfg
}

func (o *OutConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	m := make(map[string]interface{})
	err := unmarshal(m)
	if err != nil {
		return err
	}
	t, ok := m["Type"].(string)
	if !ok {
		return fmt.Errorf("OutConfig missing Type %v", m)
	}
	o.Type = t
	switch t {
	case "JSON":
		o.Spec = JsonOutConfigFromSpec(m["Spec"])
	case "Stdout":
		o.Spec = nil
	case "Elastic":
		o.Spec = ElasticOutConfigFromSpec(m["Spec"])
	default:
		return fmt.Errorf("Unrecognized Out Type %s", t)
	}
	return nil
}

func (cfg OutConfig) New() Output {
	switch cfg.Type {
	case "JSON":
		return NewJsonOutFromConfig(cfg.Spec.(JsonOutConfig))
	case "Stdout":
		return NewStdOut()
	case "Elastic":
		return NewElasticOutFromConfig(cfg.Spec.(ElasticOutConfig))
	}
	log.WithField("type", cfg.Type).Fatal("unknown output type")
	return nil
}

func NewJsonOutFromConfig(spec JsonOutConfig) Output {
	if spec.StdOut {
		return NewJsonOut(os.Stdout)
	}
	f, err := os.Create(spec.Path)
	if err != nil {
		log.Fatal(err)
	}
	return NewJsonOut(f)
}
