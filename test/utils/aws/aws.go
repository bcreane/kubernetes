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
	"log"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/rds"
)

type SubnetInfo struct {
	ID   string
	Zone string
	CIDR string
}

type VPCInfo struct {
	ID string

	// CIDR is the IP address range for the VPC
	CIDR string

	// Subnets is a list of subnets that are part of the VPC
	Subnets []*SubnetInfo
}

type awsInfo struct {
	VPCInfo *VPCInfo
}

type AwsCloud struct {
	Name string // name of the cloud cluster, used as prefix to generate resource names.

	ec2 *ec2.EC2
	rds *rds.RDS

	Info awsInfo // Holds current aws resource info.

	logger LogFunc
}

type LogFunc func(format string, args ...interface{})

func NewCloudHandler(name string, region string, logger LogFunc) (*AwsCloud, error) {
	session, err := session.NewSession(&aws.Config{
		Region: aws.String(region)},
	)
	if err != nil {
		return nil, err
	}

	return &AwsCloud{
		Name:   name,
		ec2:    ec2.New(session),
		rds:    rds.New(session),
		logger: logger,
	}, nil
}

func (a *AwsCloud) EC2() *ec2.EC2 {
	return a.ec2
}

func (a *AwsCloud) RDS() *rds.RDS {
	return a.rds
}

func (a *AwsCloud) ResourceName(name string) string {
	return a.Name + "-" + name
}

func (a *AwsCloud) subnetIDs() []*string {
	result := []*string{}
	for _, subnet := range a.Info.VPCInfo.Subnets {
		result = append(result, aws.String(subnet.ID))
	}
	return result
}

func (a *AwsCloud) firstSubnetID() string {
	return a.Info.VPCInfo.Subnets[0].ID
}

func (a *AwsCloud) findVPC(vpcID string) (*ec2.Vpc, error) {
	a.logger("Calling DescribeVPC for VPC %s", vpcID)

	request := &ec2.DescribeVpcsInput{
		VpcIds: []*string{&vpcID},
	}

	response, err := a.EC2().DescribeVpcs(request)
	if err != nil {
		return nil, fmt.Errorf("error listing VPCs: %v", err)
	}
	if response == nil || len(response.Vpcs) == 0 {
		return nil, nil
	}
	if len(response.Vpcs) != 1 {
		return nil, fmt.Errorf("found multiple VPCs for %s", vpcID)
	}

	vpc := response.Vpcs[0]
	a.logger("Found VPC %s", vpc)

	return vpc, nil
}

func (a *AwsCloud) GetVPCInfo(vpcID string) error {
	vpc, err := a.findVPC(vpcID)
	if err != nil {
		return err
	}
	if vpc == nil {
		return fmt.Errorf("Got nil VPC")
	}

	vpcInfo := &VPCInfo{
		ID:   vpcID,
		CIDR: aws.StringValue(vpc.CidrBlock),
	}

	// Find subnets in the VPC
	a.logger("Calling DescribeSubnets for subnets in VPC %s", vpcID)
	request := &ec2.DescribeSubnetsInput{
		Filters: []*ec2.Filter{newEC2Filter("vpc-id", vpcID)},
	}

	response, err := a.EC2().DescribeSubnets(request)
	if err != nil {
		return fmt.Errorf("error listing subnets in VPC %s: %v", vpcID, err)
	}
	if response != nil {
		for _, subnet := range response.Subnets {
			subnetInfo := &SubnetInfo{
				ID:   aws.StringValue(subnet.SubnetId),
				CIDR: aws.StringValue(subnet.CidrBlock),
				Zone: aws.StringValue(subnet.AvailabilityZone),
			}

			a.logger("Found subnet %#v\n", subnetInfo)
			vpcInfo.Subnets = append(vpcInfo.Subnets, subnetInfo)
		}
	}

	a.Info.VPCInfo = vpcInfo
	return nil
}

func (a *AwsCloud) CreateVpcSG(name string, desc string) (string, error) {
	vpcID := a.Info.VPCInfo.ID

	result, err := a.EC2().CreateSecurityGroup(&ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(a.ResourceName(name)),
		Description: aws.String(desc),
		VpcId:       aws.String(vpcID),
	})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case "InvalidVpcID.NotFound":
				return "", fmt.Errorf("Unable to find VPC with ID %s.", vpcID)
			case "InvalidGroup.Duplicate":
				return "", fmt.Errorf("Security group %s already exists.", name)
			}
		}
		return "", fmt.Errorf("Unable to create security group %q, %v", name, err)
	}

	groupID := aws.StringValue(result.GroupId)

	a.logger("Created security group %s with VPC %s.\n",
		groupID, vpcID)

	return groupID, nil
}

func (a *AwsCloud) DeleteVpcSG(groupID string) error {
	deleteRequest := &ec2.DeleteSecurityGroupInput{
		GroupId: aws.String(groupID),
	}

	_, err := a.EC2().DeleteSecurityGroup(deleteRequest)
	if err != nil {
		a.logger("Could not delete vpc security group %v", err)
		return err
	}

	a.logger("Deleted security group %s", groupID)
	return nil
}

func (a *AwsCloud) AuthorizeSGIngressSrcSG(groupID string, protocol string, fromPort, toPort int64, srcSGs []string) error {
	// From source sg names to userIDGroupPairs.
	ids := []*ec2.UserIdGroupPair{}
	for _, sg := range srcSGs {
		ids = append(ids, &ec2.UserIdGroupPair{GroupId: aws.String(sg)})
	}

	// Has to use GroupId for non-default VPC.
	_, err := a.EC2().AuthorizeSecurityGroupIngress(&ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(groupID),
		IpPermissions: []*ec2.IpPermission{
			(&ec2.IpPermission{}).
				SetIpProtocol(protocol).
				SetFromPort(fromPort).
				SetToPort(toPort).
				SetUserIdGroupPairs(ids),
		},
	})
	if err != nil {
		return fmt.Errorf("Unable to set security group %s ingress, %v", groupID, err)
	}

	a.logger("Successfully set security group ingress for %s", groupID)
	return nil
}

func (a *AwsCloud) AuthorizeSGIngressIPRange(groupID string, protocol string, fromPort, toPort int64, ipRanges []string) error {
	// From source ip ranges strings, e.g "0.0.0.0/0" to cidrs.
	rgs := []*ec2.IpRange{}
	for _, rg := range ipRanges {
		rgs = append(rgs, &ec2.IpRange{CidrIp: aws.String(rg)})
	}

	// Has to use GroupId for non-default VPC.
	_, err := a.EC2().AuthorizeSecurityGroupIngress(&ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(groupID),
		IpPermissions: []*ec2.IpPermission{
			(&ec2.IpPermission{}).
				SetIpProtocol(protocol).
				SetFromPort(fromPort).
				SetToPort(toPort).
				SetIpRanges(rgs),
		},
	})
	if err != nil {
		return fmt.Errorf("Unable to set security group %s ingress, %v", groupID, err)
	}

	a.logger("Successfully set security group ingress for %s", groupID)
	return nil
}

func (a *AwsCloud) CreateDBSubnetGroup(name string) (string, error) {
	createInput := &rds.CreateDBSubnetGroupInput{
		DBSubnetGroupDescription: aws.String(a.ResourceName("DB subnet group")),
		DBSubnetGroupName:        aws.String(a.ResourceName(name)),
		SubnetIds:                a.subnetIDs(),
	}

	result, err := a.RDS().CreateDBSubnetGroup(createInput)
	if err != nil {
		return "", err
	}

	groupName := aws.StringValue(result.DBSubnetGroup.DBSubnetGroupName)

	a.logger("Created db subnet group %s", groupName)
	return groupName, nil
}

func (a *AwsCloud) DeleteDBSubnetGroup(idString string) error {
	deleteRequest := &rds.DeleteDBSubnetGroupInput{
		DBSubnetGroupName: aws.String(idString),
	}

	_, err := a.RDS().DeleteDBSubnetGroup(deleteRequest)
	if err != nil {
		a.logger("Could not delete rds subnet group", err)
		return err
	}

	a.logger("Deleted db subnet group %s", idString)
	return nil
}

func (a *AwsCloud) CreateRDSInstance(name, subnetGroup, vpcSgID, password, dbName string) (string, string, int64, error) {
	createInput := &rds.CreateDBInstanceInput{
		AllocatedStorage:      aws.Int64(5),
		BackupRetentionPeriod: aws.Int64(0),
		DBInstanceClass:       aws.String("db.t2.micro"),
		DBInstanceIdentifier:  aws.String(a.ResourceName(name)),
		Engine:                aws.String("postgres"),
		MasterUserPassword:    aws.String(password),
		MasterUsername:        aws.String("master"),
		DBSubnetGroupName:     aws.String(subnetGroup),
		DBName:                aws.String(dbName),
		VpcSecurityGroupIds: []*string{
			aws.String(vpcSgID),
		},
	}

	result, err := a.RDS().CreateDBInstance(createInput)
	if err != nil {
		a.logger("Could not create rds instance", err)
		return "", "", 0, err
	}

	instanceID := result.DBInstance.DBInstanceIdentifier
	idString := aws.StringValue(instanceID)

	a.logger("Created rds instance %s", idString)

	describeInput := &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: instanceID,
	}

	if err := a.RDS().WaitUntilDBInstanceAvailable(describeInput); err != nil {
		return "", "", 0, err
	}

	a.logger("RDS instance available %s", idString)

	response, err := a.RDS().DescribeDBInstances(describeInput)
	if err != nil {
		return "", "", 0, fmt.Errorf("error listing rdb instance: %v", err)
	}

	if len(response.DBInstances) != 1 {
		return "", "", 0, fmt.Errorf("found multiple instances for %s", idString)
	}

	db := response.DBInstances[0]
	a.logger("Get db instance endpoint %s:%d\n", *db.Endpoint.Address, *db.Endpoint.Port)

	return idString, *db.Endpoint.Address, *db.Endpoint.Port, nil
}

func (a *AwsCloud) DeleteRDSInstance(idString string) error {
	a.logger("RDS instance start deleting", idString)
	skipFinalSnapshot := true
	deleteRequest := &rds.DeleteDBInstanceInput{
		DBInstanceIdentifier: aws.String(idString),
		SkipFinalSnapshot:    &skipFinalSnapshot,
	}

	_, err := a.RDS().DeleteDBInstance(deleteRequest)
	if err != nil {
		a.logger("Could not delete rds instance %s : %v", idString, err)
		return err
	}

	a.logger("RDS instance %s waiting for deletion.", idString)

	describeInput := &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(idString),
	}

	if err := a.RDS().WaitUntilDBInstanceDeleted(describeInput); err != nil {
		return err
	}

	a.logger("RDS instance %s deleted", idString)

	return nil
}

func (a *AwsCloud) createInstance(name, sgID string) (string, error) {
	// Specify the details of the instance that you want to create.
	request := &ec2.RunInstancesInput{
		// An Amazon Linux AMI ID for t2.micro instances in the us-west-2 region
		ImageId:      aws.String("ami-e7527ed7"),
		InstanceType: aws.String("t2.micro"),
		MinCount:     aws.Int64(1),
		MaxCount:     aws.Int64(1),
		SecurityGroupIds: []*string{
			aws.String(sgID),
		},
		SubnetId: aws.String(a.firstSubnetID()),
	}

	result, err := a.EC2().RunInstances(request)

	if err != nil {
		a.logger("Could not create instance: %s", err)
		return "", err
	}

	instanceID := result.Instances[0].InstanceId
	idString := aws.StringValue(instanceID)

	a.logger("Created instance %s", idString)

	// Add tags to the created instance
	_, errtag := a.EC2().CreateTags(&ec2.CreateTagsInput{
		Resources: []*string{instanceID},
		Tags: []*ec2.Tag{
			{
				Key:   aws.String("Name"),
				Value: aws.String(a.ResourceName(name)),
			},
		},
	})
	if errtag != nil {
		a.logger("Could not create tags for instance %s: %v", idString, errtag)
		return "", err
	}

	a.logger("Successfully tagged instance %s.", idString)

	a.logger("Wait for instance %s to run.", idString)

	describeInput := &ec2.DescribeInstancesInput{
		InstanceIds: []*string{instanceID},
	}

	if err := a.EC2().WaitUntilInstanceRunning(describeInput); err != nil {
		return "", err
	}

	a.logger("EC2 instance %s is available", idString)
	return idString, nil
}

func (a *AwsCloud) DeleteInstance(idString string) error {
	log.Printf("Deleting EC2 instance %s", idString)
	request := &ec2.TerminateInstancesInput{
		InstanceIds: []*string{aws.String(idString)},
	}
	_, err := a.EC2().TerminateInstances(request)
	if err != nil {
		if AWSErrorCode(err) == "InvalidInstanceID.NotFound" {
			log.Printf("Got InvalidInstanceID.NotFound error deleting instance %s; will treat as already-deleted", idString)
		} else {
			return fmt.Errorf("error deleting Instance %s: %v", idString, err)
		}
	}

	a.logger("Wait for instance %s to be deleted", idString)

	describeInput := &ec2.DescribeInstancesInput{
		InstanceIds: []*string{aws.String(idString)},
	}

	if err := a.EC2().WaitUntilInstanceExists(describeInput); err != nil {
		return err
	}

	a.logger("EC2 instance %s deleted.", idString)

	return nil
}

// AWSErrorCode returns the aws error code, if it is an awserr.Error, otherwise ""
func AWSErrorCode(err error) string {
	if awsError, ok := err.(awserr.Error); ok {
		return awsError.Code()
	}
	return ""
}

// AWSErrorMessage returns the aws error message, if it is an awserr.Error, otherwise ""
func AWSErrorMessage(err error) string {
	if awsError, ok := err.(awserr.Error); ok {
		return awsError.Message()
	}
	return ""
}

func newEC2Filter(name string, values ...string) *ec2.Filter {
	awsValues := []*string{}
	for _, value := range values {
		awsValues = append(awsValues, aws.String(value))
	}
	filter := &ec2.Filter{
		Name:   aws.String(name),
		Values: awsValues,
	}
	return filter
}
