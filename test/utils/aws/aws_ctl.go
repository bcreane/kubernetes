/*
Copyright (c) 2018 Tigera, Inc. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package aws

import (
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"

	. "github.com/onsi/gomega"
)

type AwsctlOptions struct {
	option string // Option for awsctl.
}

type AwsConfig struct {
	Region     string
	DefaultSG  string
	EnforcedSG string
	PodSG      string
	TrustSG    string
	Vpc        string
}

type Awsctl struct {
	Config AwsConfig // Current aws config for anx cluster.

	Client *Cloud // Aws client
}

func CheckAnxInstalled(f *framework.Framework) bool {
	_, err := calico.GetCalicoConfigMapData(f, []string{"tigera-aws-config"})
	return err == nil
}

func ConfigureAwsctl(f *framework.Framework, opts ...AwsctlOptions) *Awsctl {
	var ctl Awsctl

	// Get config info from configmap.
	cfg, err := calico.GetCalicoConfigMapData(f, []string{"tigera-aws-config"})
	Expect(err).NotTo(HaveOccurred(), "Unable to get config map: %v", err)
	getAwsConfigFromMap(*cfg, &ctl.Config)

	framework.Logf("Got aws config %#v", ctl.Config)

	// Create Cloud client handler
	ctl.Client, err = NewCloudHandler(f.BaseName, ctl.Config.Region, framework.Logf)
	Expect(err).NotTo(HaveOccurred(), "Unable to create aws cloud handler: %v", err)

	// Get aws info
	err = ctl.Client.GetVPCInfo(ctl.Config.Vpc)
	Expect(err).NotTo(HaveOccurred(), "Unable to get aws vpc info: %v", err)

	return &ctl
}

func getAwsConfigFromMap(cfg map[string]string, awsCfg *AwsConfig) {
	Expect(cfg).Should(HaveKey("aws_region"))
	awsCfg.Region = cfg["aws_region"]

	Expect(cfg).Should(HaveKey("vpcs"))
	awsCfg.Vpc = cfg["vpcs"]

	Expect(cfg).Should(HaveKey("pod_sg"))
	awsCfg.PodSG = cfg["pod_sg"]

	Expect(cfg).Should(HaveKey("trust_sg"))
	awsCfg.TrustSG = cfg["trust_sg"]

	Expect(cfg).Should(HaveKey("enforced_sg"))
	awsCfg.EnforcedSG = cfg["enforced_sg"]

	Expect(cfg).Should(HaveKey("default_sgs"))
	awsCfg.DefaultSG = cfg["default_sgs"]
}
