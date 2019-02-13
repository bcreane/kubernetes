// Copyright (c) 2018-2019 Tigera, Inc. All rights reserved.

package out

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"net/http"
	"net/url"

	log "github.com/sirupsen/logrus"

	"github.com/olivere/elastic"
	"github.com/tigera/flowsynth/pkg/flows"
)

const (
	FlowLogIndexPrefix = "tigera_secure_ee_flows.cluster."
)

const mapping = `{
  "mappings": {
    "fluentd": {
      "properties" : {
        "start_time": {
            "type": "date",
            "format": "epoch_second"
        },
        "end_time": {
            "type": "date",
            "format": "epoch_second"
        },
        "action": {
            "type": "keyword"
        },
        "bytes_in": {
            "type": "long"
        },
        "bytes_out": {
            "type": "long"
        },
        "dest_ip": {
            "type": "ip",
            "null_value": "0.0.0.0"
        },
        "dest_name": {
            "type": "keyword"
        },
        "dest_name_aggr": {
            "type": "keyword"
        },
        "dest_namespace": {
            "type": "keyword"
        },
        "dest_port": {
            "type": "long",
            "null_value": "0"
        },
        "dest_type": {
            "type": "keyword"
        },
        "dest_labels": {
                /* This is an array of keywords. It is not necessary to declare this as an array. Elastic will automatically accept a list of strings here */
                "type": "nested",
                "properties": {
                        "labels": {"type": "keyword"}
                }
        },
        "reporter": {
            "type": "keyword"
        },
        "num_flows": {
            "type": "long"
        },
        "num_flows_completed": {
            "type": "long"
        },
        "num_flows_started": {
            "type": "long"
        },
        "packets_in": {
            "type": "long"
        },
        "packets_out": {
            "type": "long"
        },
        "proto": {
            "type": "keyword"
        },
        "policies": {
                /* This is an array of keywords. It is not necessary to declare this as an array. Elastic will automatically accept a list of strings here */
                "type": "nested",
                "properties": {
                        "all_policies": {"type": "keyword"}
                }
        },
        "source_ip": {
            "type": "ip",
            "null_value": "0.0.0.0"
        },
        "source_name": {
            "type": "keyword"
        },
        "source_name_aggr": {
            "type": "keyword"
        },
        "source_namespace": {
            "type": "keyword"
        },
        "source_port": {
            "type": "long",
            "null_value": "0"
        },
        "source_type": {
            "type": "keyword"
        },
        "source_labels": {
                /* This is an array of keywords. It is not necessary to declare this as an array. Elastic will automatically accept a list of strings here */
                "type": "nested",
                "properties": {
                        "labels": {"type": "keyword"}
                }
        }
      }   
    }
  }
}`

type elasticOut struct {
	c           *elastic.Client
	indexExists map[string]bool
	p           *elastic.BulkProcessor
	numWorkers  int
}

type ElasticOutConfig struct {
	URL        *url.URL
	Username   string
	Password   string
	PathToCA   string
	NumWorkers int
}

func (c ElasticOutConfig) MarshalYAML() (interface{}, error) {
	ec := struct {
		URL        string `yaml:"URL"`
		Username   string `yaml:"Username"`
		Password   string `yaml:"Password"`
		PathToCA   string `yaml:"PathToCA"`
		NumWorkers int    `yaml:"NumWorkers"`
	}{
		c.URL.String(),
		c.Username,
		c.Password,
		c.PathToCA,
		c.NumWorkers,
	}
	return &ec, nil
}

func ElasticOutConfigFromSpec(spec interface{}) ElasticOutConfig {
	cfg := ElasticOutConfig{}
	m := spec.(map[interface{}]interface{})
	urlString, ok := m["URL"]
	if !ok {
		log.Fatal("missing URL from Elastic out spec")
	}
	url, err := url.Parse(urlString.(string))
	if err != nil {
		log.Fatalf("Could not parse URL %s: %v", urlString, err)
	}
	username, ok := m["Username"]
	if !ok {
		if url.Scheme == "https" {
			log.Fatal("missing Username from Elastic out spec")
		}
		username = ""
	}
	password, ok := m["Password"]
	if !ok {
		if url.Scheme == "https" {
			log.Fatal("missing Password from Elastic out spec")
		}
		password = ""
	}
	pathToCA, ok := m["PathToCA"]
	if !ok {
		if url.Scheme == "https" {
			log.Fatal("missing PathToCA from Elastic out spec")
		}
		pathToCA = ""
	}
	cfg.URL = url
	cfg.Username = username.(string)
	cfg.Password = password.(string)
	cfg.PathToCA = pathToCA.(string)
	num, ok := m["NumWorkers"]
	if ok {
		cfg.NumWorkers = num.(int)
	}
	return cfg
}

func NewElasticOut(url *url.URL, username, password, pathToCA string) *elasticOut {
	ca, err := x509.SystemCertPool()
	if err != nil {
		panic(err)
	}
	if pathToCA != "" {
		cert, err := ioutil.ReadFile(pathToCA)
		if err != nil {
			panic(err)
		}
		ok := ca.AppendCertsFromPEM(cert)
		if !ok {
			panic("failed to add CA")
		}
	}
	h := &http.Client{}
	if url.Scheme == "https" {
		h.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: ca}}
	}
	options := []elastic.ClientOptionFunc{
		elastic.SetURL(url.String()),
		elastic.SetHttpClient(h),
		elastic.SetErrorLog(log.StandardLogger()),
		elastic.SetSniff(false),
	}
	if username != "" {
		options = append(options, elastic.SetBasicAuth(username, password))
	}
	c, err := elastic.NewClient(options...)
	if err != nil {
		panic(err)
	}
	return &elasticOut{c: c, numWorkers: 16, indexExists: make(map[string]bool)}
}

func NewElasticOutFromConfig(spec ElasticOutConfig) *elasticOut {
	e := NewElasticOut(spec.URL, spec.Username, spec.Password, spec.PathToCA)
	if spec.NumWorkers > 0 {
		e.numWorkers = spec.NumWorkers
	}
	return e
}

func (e *elasticOut) Write(flowLog *flows.FlowLog) {
	idxName := getIndexName(flowLog)
	if !e.indexExists[idxName] {
		r, err := e.c.IndexExists(idxName).Do(context.Background())
		if err != nil {
			panic(err)
		}
		e.indexExists[idxName] = r
		if !e.indexExists[idxName] {
			r, err := e.c.CreateIndex(idxName).BodyString(mapping).Do(context.Background())
			if err != nil {
				panic(err)
			}
			if !r.Acknowledged {
				panic("not acknowledged index create")
			}
			e.indexExists[idxName] = true
		}
	}

	req := elastic.NewBulkIndexRequest().
		Index(idxName).
		Type("fluentd").
		Doc(flowLog)
	e.p.Add(req)
}

func (e *elasticOut) Start(ctx context.Context) {
	p, err := e.c.BulkProcessor().Workers(e.numWorkers).Do(context.Background())
	if err != nil {
		panic(err)
	}
	e.p = p
	p.Start(ctx)
}

func (e *elasticOut) Stop() {
	e.p.Flush()
	e.p.Close()
}

func getIndexName(f *flows.FlowLog) string {
	return FlowLogIndexPrefix + f.EndTime.Format("20060102")
}
