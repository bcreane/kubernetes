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
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/rds"
	"golang.org/x/time/rate"
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

type Cloud struct {
	Name string // name of the cloud cluster, used as prefix to generate resource names.

	ec2 *ec2.EC2
	rds *rds.RDS

	Info awsInfo // Holds current aws resource info.

	logger LogFunc

	limiter *rate.Limiter
}

type LogFunc func(format string, args ...interface{})

func NewCloudHandler(name string, region string, logger LogFunc) (*Cloud, error) {
	session, err := session.NewSession(&aws.Config{
		Region: aws.String(region)},
	)
	if err != nil {
		return nil, err
	}

	return &Cloud{
		Name:    name,
		ec2:     ec2.New(session),
		rds:     rds.New(session),
		logger:  logger,
		limiter: rate.NewLimiter(20, 20),
	}, nil
}

func (a *Cloud) EC2() *ec2.EC2 {
	a.limiter.Wait(context.Background())
	return a.ec2
}

func (a *Cloud) RDS() *rds.RDS {
	a.limiter.Wait(context.Background())
	return a.rds
}

func (a *Cloud) ResourceName(name string) string {
	return a.Name + "-" + name
}

func (a *Cloud) subnetIDs() []*string {
	var result []*string
	for _, subnet := range a.Info.VPCInfo.Subnets {
		result = append(result, aws.String(subnet.ID))
	}
	return result
}

func (a *Cloud) firstSubnetID() string {
	return a.Info.VPCInfo.Subnets[0].ID
}

func (a *Cloud) findVPC(vpcID string) (*ec2.Vpc, error) {
	a.logger("Calling DescribeVPC for VPC %s", vpcID)

	request := &ec2.DescribeVpcsInput{
		VpcIds: []*string{&vpcID},
	}

	var response *ec2.DescribeVpcsOutput
	var err error
	err = a.retryDueToRequestLimiting(func() error {
		response, err = a.EC2().DescribeVpcs(request)
		return err
	})
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

func (a *Cloud) GetVPCInfo(vpcID string) error {
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

	var response *ec2.DescribeSubnetsOutput
	err = a.retryDueToRequestLimiting(func() error {
		response, err = a.EC2().DescribeSubnets(request)
		return err
	})
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

func (a *Cloud) CreateVpcSG(name string, desc string) (string, error) {
	vpcID := a.Info.VPCInfo.ID

	var result *ec2.CreateSecurityGroupOutput
	var err error
	err = a.retryDueToRequestLimiting(func() error {
		result, err = a.EC2().CreateSecurityGroup(&ec2.CreateSecurityGroupInput{
			GroupName:   aws.String(a.ResourceName(name)),
			Description: aws.String(desc),
			VpcId:       aws.String(vpcID),
		})
		return err
	})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case "InvalidVpcID.NotFound":
				return "", fmt.Errorf("Unable to find VPC with ID %s.", vpcID)
			case "InvalidGroup.Duplicate":
				return "", fmt.Errorf("Security group %s already exists in %s.", name, vpcID)
			}
		}
		return "", fmt.Errorf("Unable to create security group %q, %v", name, err)
	}

	groupID := aws.StringValue(result.GroupId)

	a.logger("Created security group %s with VPC %s.\n",
		groupID, vpcID)

	return groupID, nil
}

func (a *Cloud) DeleteVpcSG(groupID string) error {
	deleteRequest := &ec2.DeleteSecurityGroupInput{
		GroupId: aws.String(groupID),
	}

	var err error
	err = a.retryDueToRequestLimiting(func() error {
		_, err = a.EC2().DeleteSecurityGroup(deleteRequest)
		return err
	})
	if err != nil {
		a.logger("Could not delete vpc security group %v", err)
		return err
	}

	a.logger("Deleted security group %s", groupID)
	return nil
}

func (a *Cloud) DescribeSecurityGroup(groupID string) ([]*ec2.SecurityGroup, error) {
	input := &ec2.DescribeSecurityGroupsInput{
		GroupIds: []*string{
			aws.String(groupID),
		},
	}

	var result *ec2.DescribeSecurityGroupsOutput
	var err error
	err = a.retryDueToRequestLimiting(func() error {
		result, err = a.EC2().DescribeSecurityGroups(input)
		return err
	})

	if err != nil {
		return nil, err
	}

	return result.SecurityGroups, nil
}

func (a *Cloud) DescribeFilteredSecurityGroups(filter map[string][]string) ([]*ec2.SecurityGroup, error) {
	f := []*ec2.Filter{}
	for k, v := range filter {
		f = append(f, newEC2Filter(k, v...))
	}
	input := &ec2.DescribeSecurityGroupsInput{Filters: f}

	var result *ec2.DescribeSecurityGroupsOutput
	var err error
	err = a.retryDueToRequestLimiting(func() error {
		result, err = a.EC2().DescribeSecurityGroups(input)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("Unable to discribe security group with filter %v, %v", f, err)
	}

	return result.SecurityGroups, nil
}

// Remove all ingress rules in an SG.
func (a *Cloud) RevokeSecurityGroupsIngress(groupID string) error {
	sgs, err := a.DescribeSecurityGroup(groupID)
	if err != nil {
		return err
	}

	for _, sg := range sgs {
		if len(sg.IpPermissions) > 0 {
			// Has to use GroupId for non-default VPC.
			input := &ec2.RevokeSecurityGroupIngressInput{
				GroupId:       sg.GroupId,
				IpPermissions: sg.IpPermissions,
			}
			var err error
			err = a.retryDueToRequestLimiting(func() error {
				_, err = a.EC2().RevokeSecurityGroupIngress(input)
				return err
			})
			if err != nil {
				a.logger("Got err %v", err)
				return fmt.Errorf("Unable to revoke security group %s ingress, %v", aws.StringValue(sg.GroupId), err)
			}

			a.logger("Successfully revoke security group ingress for %s", aws.StringValue(sg.GroupId))
		}
	}

	return nil
}

// Remove all egress rules in an SG.
func (a *Cloud) RevokeSecurityGroupsEgress(groupID string) error {
	sgs, err := a.DescribeSecurityGroup(groupID)
	if err != nil {
		return err
	}

	for _, sg := range sgs {
		if len(sg.IpPermissionsEgress) > 0 {
			// Has to use GroupId for non-default VPC.
			input := &ec2.RevokeSecurityGroupEgressInput{
				GroupId:       sg.GroupId,
				IpPermissions: sg.IpPermissionsEgress,
			}
			var err error
			err = a.retryDueToRequestLimiting(func() error {
				_, err := a.EC2().RevokeSecurityGroupEgress(input)
				return err
			})
			if err != nil {
				a.logger("Got err %v", err)
				return fmt.Errorf("Unable to revoke security group %s egress, %v", aws.StringValue(sg.GroupId), err)
			}
		}

		a.logger("Successfully revoke security group egress for %s", aws.StringValue(sg.GroupId))
	}

	return nil
}

func (a *Cloud) AuthorizeSGEgressDstSG(groupID string, protocol string, fromPort, toPort int64, dstSGs []string) error {
	// From dst sg names to userIDGroupPairs.
	ids := []*ec2.UserIdGroupPair{}
	for _, sg := range dstSGs {
		ids = append(ids, &ec2.UserIdGroupPair{GroupId: aws.String(sg)})
	}

	// Has to use GroupId for non-default VPC.
	var err error
	err = a.retryDueToRequestLimiting(func() error {
		_, err = a.EC2().AuthorizeSecurityGroupEgress(&ec2.AuthorizeSecurityGroupEgressInput{
			GroupId: aws.String(groupID),
			IpPermissions: []*ec2.IpPermission{
				(&ec2.IpPermission{}).
					SetIpProtocol(protocol).
					SetFromPort(fromPort).
					SetToPort(toPort).
					SetUserIdGroupPairs(ids),
			},
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("Unable to set security group %s egress, %v", groupID, err)
	}

	a.logger("Successfully set security group egress for %s", groupID)
	return nil
}

func (a *Cloud) RevokeSGIngressSrcSG(groupID string, protocol string, fromPort, toPort int64, srcSGs []string) error {
	// From source sg names to userIDGroupPairs.
	ids := []*ec2.UserIdGroupPair{}
	for _, sg := range srcSGs {
		ids = append(ids, &ec2.UserIdGroupPair{GroupId: aws.String(sg)})
	}

	var err error
	err = a.retryDueToRequestLimiting(func() error {
		// Has to use GroupId for non-default VPC.
		_, err = a.EC2().RevokeSecurityGroupIngress(&ec2.RevokeSecurityGroupIngressInput{
			GroupId: aws.String(groupID),
			IpPermissions: []*ec2.IpPermission{
				(&ec2.IpPermission{}).
					SetIpProtocol(protocol).
					SetFromPort(fromPort).
					SetToPort(toPort).
					SetUserIdGroupPairs(ids),
			},
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("Unable to revoke security group %s ingress, %v", groupID, err)
	}

	a.logger("Successfully revoke security group ingress for %s", groupID)
	return nil
}

func (a *Cloud) RevokeSGIngressIPRange(groupID string, protocol string, fromPort, toPort int64, ipRanges []string) error {
	// From source ip ranges strings, e.g "0.0.0.0/0" to CIDRs.
	rgs := []*ec2.IpRange{}
	for _, rg := range ipRanges {
		rgs = append(rgs, &ec2.IpRange{CidrIp: aws.String(rg)})
	}

	var err error
	err = a.retryDueToRequestLimiting(func() error {
		// Has to use GroupId for non-default VPC.
		_, err = a.EC2().RevokeSecurityGroupIngress(&ec2.RevokeSecurityGroupIngressInput{
			GroupId: aws.String(groupID),
			IpPermissions: []*ec2.IpPermission{
				(&ec2.IpPermission{}).
					SetIpProtocol(protocol).
					SetFromPort(fromPort).
					SetToPort(toPort).
					SetIpRanges(rgs),
			},
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("Unable to revoke security group %s ingress, %v", groupID, err)
	}

	a.logger("Successfully revoke security group ingress for %s", groupID)
	return nil
}

func (a *Cloud) AuthorizeSGIngressSrcSG(groupID string, protocol string, fromPort, toPort int64, srcSGs []string) error {
	// From source sg names to userIDGroupPairs.
	ids := []*ec2.UserIdGroupPair{}
	for _, sg := range srcSGs {
		ids = append(ids, &ec2.UserIdGroupPair{GroupId: aws.String(sg)})
	}

	var err error
	err = a.retryDueToRequestLimiting(func() error {
		// Has to use GroupId for non-default VPC.
		_, err = a.EC2().AuthorizeSecurityGroupIngress(&ec2.AuthorizeSecurityGroupIngressInput{
			GroupId: aws.String(groupID),
			IpPermissions: []*ec2.IpPermission{
				(&ec2.IpPermission{}).
					SetIpProtocol(protocol).
					SetFromPort(fromPort).
					SetToPort(toPort).
					SetUserIdGroupPairs(ids),
			},
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("Unable to set security group %s ingress, %v", groupID, err)
	}

	a.logger("Successfully set security group ingress for %s", groupID)
	return nil
}

func (a *Cloud) AuthorizeSGIngressIPRange(groupID string, protocol string, fromPort, toPort int64, ipRanges []string) error {
	// From source ip ranges strings, e.g "0.0.0.0/0" to CIDRs.
	rgs := []*ec2.IpRange{}
	for _, rg := range ipRanges {
		rgs = append(rgs, &ec2.IpRange{CidrIp: aws.String(rg)})
	}

	var err error
	err = a.retryDueToRequestLimiting(func() error {
		// Has to use GroupId for non-default VPC.
		_, err = a.EC2().AuthorizeSecurityGroupIngress(&ec2.AuthorizeSecurityGroupIngressInput{
			GroupId: aws.String(groupID),
			IpPermissions: []*ec2.IpPermission{
				(&ec2.IpPermission{}).
					SetIpProtocol(protocol).
					SetFromPort(fromPort).
					SetToPort(toPort).
					SetIpRanges(rgs),
			},
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("Unable to set security group %s ingress, %v", groupID, err)
	}

	a.logger("Successfully set security group ingress for %s", groupID)
	return nil
}

func (a *Cloud) AuthorizeSGIngressIpPermissions(groupID string, ipPermissions []*ec2.IpPermission) error {
	var err error
	err = a.retryDueToRequestLimiting(func() error {
		// Has to use GroupId for non-default VPC.
		_, err = a.EC2().AuthorizeSecurityGroupIngress(&ec2.AuthorizeSecurityGroupIngressInput{
			GroupId:       aws.String(groupID),
			IpPermissions: ipPermissions,
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("Unable to set security group %s ingress, %v", groupID, err)
	}

	a.logger("Successfully set security group ingress for %s", groupID)
	return nil
}

func (a *Cloud) CreateDBSubnetGroup(name string) (string, error) {
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

func (a *Cloud) DeleteDBSubnetGroup(idString string) error {
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

func (a *Cloud) CreateRDSInstance(name, subnetGroup, vpcSgID, password, dbName string) (string, string, int64, error) {
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

func (a *Cloud) GetRDSInstanceSecurityGroups(idString string) ([]string, error) {

	describeInput := &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(idString),
	}

	response, err := a.RDS().DescribeDBInstances(describeInput)
	if err != nil {
		return nil, fmt.Errorf("error listing rdb instance: %v", err)
	}

	if len(response.DBInstances) != 1 {
		return nil, fmt.Errorf("found multiple instances for %s", idString)
	}

	db := response.DBInstances[0]

	sgs := []string{}
	for _, sgm := range db.VpcSecurityGroups {
		sgs = append(sgs, aws.StringValue(sgm.VpcSecurityGroupId))
	}

	return sgs, nil
}

func (a *Cloud) DeleteRDSInstance(idString string) error {
	a.logger("RDS instance %s start deleting", idString)
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

// To debug the instance you can set AWS_INSTANCE_KEY_NAME with a key in AWS
// that will be loaded to the instance and then it can be ssh'd to as ec2-user.
func (a *Cloud) CreateInstance(name, sgID string, instanceCommand string) (instanceId string, err error) {
	// Specify the details of the instance that you want to create.
	request := &ec2.RunInstancesInput{
		// An Amazon Linux AMI ID for t2.micro instances in the us-west-2 region
		ImageId:      aws.String("ami-e7527ed7"),
		InstanceType: aws.String("t2.micro"),
		MinCount:     aws.Int64(1),
		MaxCount:     aws.Int64(1),
		NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
			&ec2.InstanceNetworkInterfaceSpecification{
				AssociatePublicIpAddress: aws.Bool(true),
				DeviceIndex:              aws.Int64(0),
				Groups: []*string{
					aws.String(sgID),
				},
				SubnetId: aws.String(a.firstSubnetID()),
			}},
		UserData: aws.String(base64.StdEncoding.EncodeToString([]byte(instanceCommand))),
	}

	key_pair_name := os.Getenv("AWS_INSTANCE_KEY_NAME")
	if key_pair_name == "" {
		key_pair_name = "wavetank"
	}
	request.KeyName = aws.String(key_pair_name)

	result, err := a.EC2().RunInstances(request)

	if err != nil {
		a.logger("Could not create instance: %s", err)
		return "", err
	}

	instanceID := result.Instances[0].InstanceId
	idString := aws.StringValue(instanceID)

	a.logger("Created instance %s", idString)

	a.logger("Wait for instance %s to run.", idString)

	describeInput := &ec2.DescribeInstancesInput{
		InstanceIds: []*string{instanceID},
	}

	if err := a.EC2().WaitUntilInstanceRunning(describeInput); err != nil {
		return "", err
	}

	// Add tags to the created instance
	errtag := a.CreateTag(idString, "Name", a.ResourceName(name))
	if errtag != nil {
		a.logger("Could not create tags for instance %s: %v", idString, errtag)
		return "", errtag
	}

	a.logger("Successfully tagged instance %s.", idString)

	a.logger("EC2 instance %s is available", idString)
	return idString, nil
}

func (a *Cloud) GetInstanceSshUser(instanceId string) (string, error) {
	return "ec2-user", nil
}

func (a *Cloud) GetInstancePrivateIp(instanceId string) (privateIp string, err error) {
	result, err := a.EC2().DescribeInstances(&ec2.DescribeInstancesInput{
		InstanceIds: []*string{aws.String(instanceId)},
	})
	if err != nil {
		return "", fmt.Errorf("Failed to describe instance %s: %v", instanceId, err)
	}
	if len(result.Reservations) != 1 {
		return "", fmt.Errorf(
			"Describe instance returned incorrect number of reservations: %v",
			result.Reservations)
	}
	if len(result.Reservations[0].Instances) != 1 {
		return "", fmt.Errorf(
			"Describe instance returned incorrect number of instances: %v",
			result.Reservations[0].Instances)
	}
	return aws.StringValue(result.Reservations[0].Instances[0].PrivateIpAddress), nil
}
func (a *Cloud) GetInstancePublicIp(instanceId string) (privateIp string, err error) {
	result, err := a.EC2().DescribeInstances(&ec2.DescribeInstancesInput{
		InstanceIds: []*string{aws.String(instanceId)},
	})
	if err != nil {
		return "", fmt.Errorf("Failed to describe instance %s: %v", instanceId, err)
	}
	if len(result.Reservations) != 1 {
		return "", fmt.Errorf(
			"Describe instance returned incorrect number of reservations: %v",
			result.Reservations)
	}
	if len(result.Reservations[0].Instances) != 1 {
		return "", fmt.Errorf(
			"Describe instance returned incorrect number of instances: %v",
			result.Reservations[0].Instances)
	}
	return aws.StringValue(result.Reservations[0].Instances[0].PublicIpAddress), nil
}

func (a *Cloud) DeleteInstance(idString string) error {
	log.Printf("Deleting EC2 instance %s", idString)
	request := &ec2.TerminateInstancesInput{
		InstanceIds: []*string{aws.String(idString)},
	}
	_, err := a.EC2().TerminateInstances(request)
	if err != nil {
		if ErrorCode(err) == "InvalidInstanceID.NotFound" {
			log.Printf("Got InvalidInstanceID.NotFound error deleting instance %s; will treat as already-deleted", idString)
		} else {
			return fmt.Errorf("error deleting Instance %s: %v", idString, err)
		}
	}

	a.logger("Wait for instance %s to be deleted", idString)

	describeInput := &ec2.DescribeInstancesInput{
		InstanceIds: []*string{aws.String(idString)},
	}

	if err := a.EC2().WaitUntilInstanceTerminated(describeInput); err != nil {
		return err
	}

	a.logger("EC2 instance %s deleted.", idString)

	return nil
}

func (a *Cloud) CreateTag(id, key, value string) error {
	cti := ec2.CreateTagsInput{
		Resources: []*string{aws.String(id)},
		Tags: []*ec2.Tag{&ec2.Tag{
			Key:   aws.String(key),
			Value: aws.String(value)}},
	}
	_, err := a.EC2().CreateTags(&cti)
	return err
}

// ErrorCode returns the aws error code, if it is an awserr.Error, otherwise ""
func ErrorCode(err error) string {
	if awsError, ok := err.(awserr.Error); ok {
		return awsError.Code()
	}
	return ""
}

// ErrorMessage returns the aws error message, if it is an awserr.Error, otherwise ""
func ErrorMessage(err error) string {
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

func (a *Cloud) retryDueToRequestLimiting(theFunc func() error) error {
	delay := time.Second
	for err := theFunc(); err != nil; err = theFunc() {
		code := ErrorCode(err)
		if code != "RequestLimitExceeded" {
			return err
		}
		if delay.Minutes() < 5 {
			time.Sleep(delay)
			if delay.Minutes() > 1 {
				a.logger("Delaying %d seconds due to API Rate Limiting", delay.Seconds())
			}
		} else {
			return fmt.Errorf("API backoff timed out: %v", err)
		}

		delay = 2 * delay
	}
	return nil
}
