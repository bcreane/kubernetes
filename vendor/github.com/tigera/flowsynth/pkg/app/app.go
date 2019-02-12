// Copyright (c) 2018-2019 Tigera, Inc. All rights reserved.

package app

import (
	"net"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/tigera/flowsynth/pkg/flows"
	"github.com/tigera/flowsynth/pkg/scheduler"
)

type Application interface {
	GetFlowData(start time.Time, end time.Time, updates map[string]chan<- *flows.FlowUpdate)
}

type WrappableApp interface {
	Application
	GetPodByServiceName(name string) Pod
}

type Pod struct {
	Name           string
	AggregatedName string
	Namespace      string
	Node           string
	IP             net.IP
}

type AppConfig struct {
	Type string      `yaml:"Type"`
	Spec interface{} `yaml:"Spec"`
}

func (cfg AppConfig) New(s scheduler.Scheduler) Application {
	switch cfg.Type {
	case "Basic":
		return NewBasic(cfg.Spec.(BasicConfig), s)
	case "Scaling":
		return NewScaling(cfg.Spec.(ScalingConfig), s)
	case "WrappedApp":
		return NewWrappedApp(cfg.Spec.(WrappedAppConfig), s)
	case "MultiService":
		return NewMultiService(cfg.Spec.(MultiServiceConfig), s)
	default:
		log.WithField("type", cfg.Type).Fatal("Unknown App type")
	}
	return nil
}

func (a *AppConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	ts := struct {
		Type string `yaml:"Type"`
	}{}
	err := unmarshal(&ts)
	if err != nil {
		return err
	}
	a.Type = ts.Type
	switch ts.Type {
	case "Basic":
		ss := struct {
			Spec BasicConfig `yaml:"Spec"`
		}{}
		err = unmarshal(&ss)
		a.Spec = ss.Spec
	case "Scaling":
		ss := struct {
			Spec ScalingConfig `yaml:"Spec"`
		}{}
		err = unmarshal(&ss)
		a.Spec = ss.Spec
	case "WrappedApp":
		ss := struct {
			Spec WrappedAppConfig `yaml:"Spec"`
		}{}
		err = unmarshal(&ss)
		a.Spec = ss.Spec
	case "MultiService":
		ss := struct {
			Spec MultiServiceConfig `yaml:"Spec"`
		}{}
		err = unmarshal(&ss)
		a.Spec = ss.Spec
	default:
		log.WithField("type", ts.Type).Fatal("Unrecognized app type")
	}
	if err != nil {
		return err
	}
	return nil
}
