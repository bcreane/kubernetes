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
	"fmt"
	"time"

	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/utils/calico"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
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

	defaultPodSgIpPermissions []*ec2.IpPermission
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

func (a *Awsctl) GetTigeraPodDefaultSG() (*ec2.SecurityGroup, error) {
	filter := map[string][]string{
		"tag:aws:cloudformation:logical-id": []string{"TigeraPodDefault"},
		"vpc-id": []string{a.Client.Info.VPCInfo.ID},
	}
	sgs, err := a.Client.DescribeFilteredSecurityGroups(filter)

	if err != nil {
		return nil, fmt.Errorf("Failed to query AWS SecurityGroup the TigeraPodDefault: %v", err)
	}
	if len(sgs) != 1 {
		return nil, fmt.Errorf("Querying for TigeraPodDefault resulted in multiple SGs: %v", sgs)
	}
	return sgs[0], nil
}

func (a *Awsctl) RemoveIngressRulesInTigeraPodDefaultSG() error {
	sg, err := a.GetTigeraPodDefaultSG()
	if err != nil {
		return err
	}

	ipPerms := []*ec2.IpPermission{}
	for _, p := range sg.IpPermissions {
		copy := *p
		ipPerms = append(ipPerms, &copy)
	}

	if err = a.Client.RevokeSecurityGroupsIngress(aws.StringValue(sg.GroupId)); err != nil {
		return fmt.Errorf("Failed to remove Ingress rules from TigeraPodDefault SG %s: %v",
			sg.GroupId, err)
	}

	a.defaultPodSgIpPermissions = ipPerms

	// Allow DNS ingress (or else DNS pod won't work)
	err = a.Client.AuthorizeSGIngressIPRange(aws.StringValue(sg.GroupId), "udp", 53, 53, []string{"0.0.0.0/0"})
	if err != nil {
		return fmt.Errorf("Failed to add DNS ingress to TigeraPodDefault: %v", err)
	}

	return nil
}

func (a *Awsctl) RestoreIngressRulesInTigeraPodDefaultSG() error {
	sg, err := a.GetTigeraPodDefaultSG()
	if err != nil {
		return err
	}

	//Revoke Ingress
	if a.defaultPodSgIpPermissions == nil {
		return fmt.Errorf("No Ingress rules stored, unable to restore the TigeraPodDefault SG")
	}
	err = a.Client.RevokeSGIngressIPRange(aws.StringValue(sg.GroupId), "udp", 53, 53, []string{"0.0.0.0/0"})
	if err != nil {
		return fmt.Errorf("Failed to revoke DNS ingress on TigeraPodDefault: %v", err)
	}

	err = a.Client.AuthorizeSGIngressIpPermissions(aws.StringValue(sg.GroupId), a.defaultPodSgIpPermissions)
	if err != nil {
		return fmt.Errorf("Failed to restore Ingress rules to TigeraPodDefault SG %s",
			aws.StringValue(sg.GroupId))
	}

	return nil
}

func (a *Awsctl) CreateTestVpcSG(name string, desc string) (string, error) {
	sgId, err := a.Client.CreateVpcSG(name, desc)
	if err != nil {
		return "", err
	}

	waitForSGTimeout := time.NewTimer(4 * time.Minute)
	defer waitForSGTimeout.Stop()
	for {
		// It is not helpful to do a DescribeSecurityGroups before trying to create
		// the tag because even if the Describe 'finds' the security group it is still
		// possible to get InvalidGroup.NotFound when doing the CreateTags.
		cti := ec2.CreateTagsInput{
			Resources: []*string{aws.String(sgId)},
			Tags: []*ec2.Tag{&ec2.Tag{
				Key:   aws.String("e2e-test"),
				Value: aws.String(a.Client.Info.VPCInfo.ID)}},
		}
		_, err = a.Client.EC2().CreateTags(&cti)
		if err == nil {
			break
		}
		if ErrorCode(err) != "InvalidGroup.NotFound" {
			return "", fmt.Errorf("Unable to tag SG: %v", err)
		}
		select {
		case <-time.After(1 * time.Second):
			// Sleep for 1 second before checking again.
		case <-waitForSGTimeout.C:
			return "", fmt.Errorf("Unable to tag SG %s we created: %v", sgId, err)
		}
	}

	return sgId, nil
}

func (a *Awsctl) CleanupTestSGs() error {
	sgs, err := a.Client.DescribeFilteredSecurityGroups(map[string][]string{
		"tag:e2e-test": []string{a.Client.Info.VPCInfo.ID},
	})
	framework.Logf("Cleaning up SecurityGroups")

	if err != nil {
		return fmt.Errorf("Failed to pull AWS SecurityGroups for cleanup: %v", err)
	}
	for _, sg := range sgs {
		if err = a.Client.RevokeSecurityGroupsIngress(aws.StringValue(sg.GroupId)); err != nil {
			return err
		}
		if err = a.Client.RevokeSecurityGroupsEgress(aws.StringValue(sg.GroupId)); err != nil {
			return err
		}
	}
	for _, sg := range sgs {
		if err = a.Client.DeleteVpcSG(aws.StringValue(sg.GroupId)); err != nil {
			return fmt.Errorf("Failed to clean up Security Group %s: %v",
				aws.StringValue(sg.GroupId), err)
		}
	}

	return nil
}

func (a *Awsctl) DumpSGsInVpc() error {
	sgs, err := a.Client.DescribeFilteredSecurityGroups(map[string][]string{
		"vpc-id": []string{a.Client.Info.VPCInfo.ID},
	})
	framework.Logf("Dumping %d SecurityGroups", len(sgs))

	if err != nil {
		return fmt.Errorf("Failed to pull AWS SecurityGroups for dumping: %v", err)
	}
	for _, sg := range sgs {
		framework.Logf(sg.String())
	}

	return nil
}

func (a *Awsctl) RunSSHCommandOnInstance(instanceId, cmd string, timeout time.Duration) (stdout, stderr string, rc int, err error) {
	user, err := a.Client.GetInstanceSshUser(instanceId)
	if err != nil {
		return "", "", 1, fmt.Errorf("Failed to get instance ssh user: %v", err)
	}

	ip, err := a.Client.GetInstancePublicIp(instanceId)
	if err != nil {
		return "", "", 1, fmt.Errorf("Failed to get instance IP to SSH: %v", err)
	}
	// Create ssh connection
	host := ip + ":22"

	return calico.RunSSHCommand(cmd, host, user, timeout)
}
