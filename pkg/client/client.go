/*
Copyright 2018 The Kubernetes Authors.

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

package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/cluster-api-provider-aws/pkg/version"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/IBM/go-sdk-core/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
	"github.com/aws/aws-sdk-go/service/elb"
	"github.com/aws/aws-sdk-go/service/elb/elbiface"
	"github.com/aws/aws-sdk-go/service/elbv2"
	"github.com/aws/aws-sdk-go/service/elbv2/elbv2iface"
	configv1 "github.com/openshift/api/config/v1"
	machineapiapierrors "github.com/openshift/machine-api-operator/pkg/controller/machine"
	apimachineryerrors "k8s.io/apimachinery/pkg/api/errors"
)

//go:generate go run ../../vendor/github.com/golang/mock/mockgen -source=./client.go -destination=./mock/client_generated.go -package=mock

const (
	// AwsCredsSecretIDKey is secret key containing AWS KeyId
	AwsCredsSecretIDKey = "aws_access_key_id"
	// AwsCredsSecretAccessKey is secret key containing AWS Secret Key
	AwsCredsSecretAccessKey = "aws_secret_access_key"

	// GlobalInfrastuctureName default name for infrastructure object
	GlobalInfrastuctureName = "cluster"

	// KubeCloudConfigNamespace is the namespace where the kube cloud config ConfigMap is located
	KubeCloudConfigNamespace = "openshift-config-managed"
	// kubeCloudConfigName is the name of the kube cloud config ConfigMap
	kubeCloudConfigName = "kube-cloud-config"
	// cloudCABundleKey is the key in the kube cloud config ConfigMap where the custom CA bundle is located
	cloudCABundleKey = "ca-bundle.pem"
)

// AwsClientBuilderFuncType is function type for building aws client
type AwsClientBuilderFuncType func(client client.Client, secretName, namespace, region string, configManagedClient client.Client) (Client, error)

type IbmClientBuilderFuncType func(client client.Client, secretName, namespace, region string, configManagedClient client.Client) (ClientIBM, error)

// Client is a wrapper object for actual AWS SDK clients to allow for easier testing.
type Client interface {
	DescribeImages(*ec2.DescribeImagesInput) (*ec2.DescribeImagesOutput, error)
	DescribeDHCPOptions(input *ec2.DescribeDhcpOptionsInput) (*ec2.DescribeDhcpOptionsOutput, error)
	DescribeVpcs(*ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error)
	DescribeSubnets(*ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error)
	DescribeAvailabilityZones(*ec2.DescribeAvailabilityZonesInput) (*ec2.DescribeAvailabilityZonesOutput, error)
	DescribeSecurityGroups(*ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error)
	RunInstances(*ec2.RunInstancesInput) (*ec2.Reservation, error)
	DescribeInstances(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error)
	TerminateInstances(*ec2.TerminateInstancesInput) (*ec2.TerminateInstancesOutput, error)
	DescribeVolumes(*ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error)
	CreateTags(*ec2.CreateTagsInput) (*ec2.CreateTagsOutput, error)

	RegisterInstancesWithLoadBalancer(*elb.RegisterInstancesWithLoadBalancerInput) (*elb.RegisterInstancesWithLoadBalancerOutput, error)
	ELBv2DescribeLoadBalancers(*elbv2.DescribeLoadBalancersInput) (*elbv2.DescribeLoadBalancersOutput, error)
	ELBv2DescribeTargetGroups(*elbv2.DescribeTargetGroupsInput) (*elbv2.DescribeTargetGroupsOutput, error)
	ELBv2RegisterTargets(*elbv2.RegisterTargetsInput) (*elbv2.RegisterTargetsOutput, error)
	ELBv2DeregisterTargets(*elbv2.DeregisterTargetsInput) (*elbv2.DeregisterTargetsOutput, error)
}

type ClientIBM interface {
	DescribeVpcs() error
	CreateVsi(name string, userData []byte) (*vpcv1.Instance, error)
	Exists(name string) (bool, error)
	DeleteVsi(name string) error
	GetVsi(name string) (*vpcv1.Instance, error)
}

func (c *ibmClient) GetVsi(name string) (*vpcv1.Instance, error) {
	klog.Info("src:GetVsi > entry")
	klog.Info("src: name: ", name)

	listOptions := &vpcv1.ListInstancesOptions{
		Name: &name,
	}
	instances, listResp, listErr := c.ibClient.ListInstances(listOptions)

	if listErr != nil {
		klog.Error("src:getvsi returned err: ", listErr)
		if listResp != nil {
			klog.Error("src:getvsi rc: ", listResp.StatusCode)
		}
		return nil, listErr
	}

	if listResp != nil {
		klog.Info("src:listinstances rc: ", listResp.StatusCode)
	}

	if listResp.StatusCode != 200 {
		klog.Error("src:GetVsi err rc getting instances, code: ", listResp.StatusCode, " : ", listErr)
		if listErr != nil {
			return nil, listErr
		}
	}

	//var instId string
	if instances != nil {
		klog.Info("src:getvsi inst count returned: ", instances.TotalCount)
	}

	for _, inst := range instances.Instances {
		strname := *inst.Name
		klog.Info("src:getvsi comparing: ", name, " w: ", strname)
		if name == strname {
			klog.Info("src:GetVsi machine found: ", name)
			return &inst, nil
		}
	}

	return nil, errors.New("src:GetVsi err, unable to retrieve instance for machine: " + name)
}

func (c *ibmClient) DeleteVsi(name string) error {
	listOptions := &vpcv1.ListInstancesOptions{
		Name: &name,
	}
	instances, listResp, listErr := c.ibClient.ListInstances(listOptions)
	if listResp.StatusCode != 200 {
		klog.Error("src:DeleteVsi err rc getting instances, code: ", listResp.StatusCode, " : ", listErr)
		if listErr != nil {
			return listErr
		}
	}

	var instId string = ""

	for _, inst := range instances.Instances {
		if name == *inst.Name {
			klog.Info("src:DelVsi machine found: ", name)
			instId = *inst.ID
			break
		}
	}

	if instId == "" {
		klog.Info("src:DelVsi no vsi found for ", name)
		return nil
	}

	klog.Info("src: deleting instance: ", instId)
	delOptions := &vpcv1.DeleteInstanceOptions{
		ID: &instId,
	}

	delResp, delErr := c.ibClient.DeleteInstance(delOptions)

	if delErr != nil {
		klog.Error("src:DelVsi err deleting instance: ", instId, " - ", delErr)
		if delResp != nil {
			klog.Error("src: del status code: ", delResp.StatusCode)
		}
		return delErr
	}

	return nil
}

func (c *ibmClient) Exists(name string) (bool, error) {
	options := &vpcv1.ListInstancesOptions{}
	instances, resp, err := c.ibClient.ListInstances(options)

	if err != nil {
		klog.Info("src:c:Exists err: ", err)
	}

	klog.Info("src:c:Exists resp code: ", resp.GetStatusCode())
	klog.Info("src: total: ", instances.TotalCount)
	for _, inst := range instances.Instances {
		klog.Info("comparing ", name, " with ", inst.Name)
		if name == *inst.Name {
			klog.Info("src: ", name, " exists, returning true")
			return true, nil
		}
	}

	return false, nil
}

func (c *ibmClient) CreateVsi(name string, userData []byte) (*vpcv1.Instance, error) {
	klog.Info("src:CreateVsi > entry")

	//name := "src-0"
	profile := "bx2-4x16"
	image := "r014-69281f26-bb55-42b5-ba32-855d03b13233"
	zone := "us-east-1"
	subnet := "0757-375fe20d-2480-44f8-aff8-ec11ad771136"
	//userData := "{\"ignition\":{\"config\":{\"merge\":[{\"source\":\"https://api-int.ocp.mao.satellite.test.appdomain.cloud:22623/config/worker\"}]},\"security\":{\"tls\":{\"certificateAuthorities\":[{\"source\":\"data:text/plain;charset=utf-8;base64,LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSURFRENDQWZpZ0F3SUJBZ0lJRytTYkQ2WlFKQ1V3RFFZSktvWklodmNOQVFFTEJRQXdKakVTTUJBR0ExVUUKQ3hNSmIzQmxibk5vYVdaME1SQXdEZ1lEVlFRREV3ZHliMjkwTFdOaE1CNFhEVEl4TURVeE5URXlNVFl4TVZvWApEVE14TURVeE16RXlNVFl4TVZvd0pqRVNNQkFHQTFVRUN4TUpiM0JsYm5Ob2FXWjBNUkF3RGdZRFZRUURFd2R5CmIyOTBMV05oTUlJQklqQU5CZ2txaGtpRzl3MEJBUUVGQUFPQ0FROEFNSUlCQ2dLQ0FRRUF4N2QzL2tGS1Z2dEIKQ1dPZXlkUyswTjdBZllmSmFUTnZJL0ZXR1B0SjVrMXQ5TXhiWmNNSnpoUGNlWVIwL2lZZHEzVlArN2k5Mi9lcwpyQTd3dWVPdm41cXloZ2VtNkFzRHVXQlJEV1paaHQ3dmZLckJ3N1VQVWhzVmJLL3l5azc2c3dNckF3am1hUHlpCnBTMFpPSlpjR2UzUnZZMW4vSW9MeEhBdVVIODBwcHFKYVkwR0JMcEQ4Wk9idk1lTVdBZm9kSzdXaFZBY3Rad2MKTHV0ckFuMjJoMnZCYzVFOWVRRW56QzF1VStxcGx5RWYxaG4yNE9qVU03WTFVU3VPQ003TU5obXBCL3RDMGwrYQpsUFU4MEd0NnhsOWw0NHorbEFQL3Z0OTMyT05jQTBwSUF5cTNMdFh6em02eGhSSFYzL3JRVUZUTVhIWDliRTZYCmtXMy9PWjBQdHdJREFRQUJvMEl3UURBT0JnTlZIUThCQWY4RUJBTUNBcVF3RHdZRFZSMFRBUUgvQkFVd0F3RUIKL3pBZEJnTlZIUTRFRmdRVVVkUW9TbFh2K3J1REpDVVRWTFNHd25kcVovWXdEUVlKS29aSWh2Y05BUUVMQlFBRApnZ0VCQUNiaWo5VG9vTHdlaEQwek5NWHZjd2ZSbG95eEZvbmJwb0JnNWJHUXljcERPc1BEVkEzSlFrdlVDZGJuCmRaRVZNVTFWVmtvOGQ1RjRHQkVOL2lvWHEwQzYzSUVOR1Y3b0p3bFZDTEtYdUxnektITEtaMCtYdkphcmYrNE0KcG10OVRWcHVleXdDZm1QNUpjdytJYUtXL2tMdWxWUnQwMXNXdStDU2JhMlhKcmpWYkI5ekRPOG8rTmpWSmtRVwo2Y29KZmhRSy84WmhHajBnRWxFL29LRi9yYVNaTmdTSkUwNUZwcjNBbU9pVXVUQWtwOFlkSmFoNWMzeFJCYW5kCjZEa3hxWC85bHB6dHRkQ3YvRzRqMThBTVJkV0NackEreTFCczFCbVBOZEhuMUt1OE5Db1JtMDJmUlQrWFBrakEKcnlkYzRUVllwQWtJeEVZcVdpWjhVb1hBUmVJPQotLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tCg==\"}]}},\"version\":\"3.2.0\"}}"
	userDataEnc := base64.StdEncoding.EncodeToString(userData)
	ssh := "r014-bf027d6a-8622-4f4b-89b8-7abbaf778891"

	klog.Info("src:CreateVsi:CreateInstanceOptions > about-to")
	opts := &vpcv1.CreateInstanceOptions{}

	klog.Info("src:CreateVsi:InstancePrototype > about-to")
	instPrototype := &vpcv1.InstancePrototype{
		Name: &name,
		Image: &vpcv1.ImageIdentity{
			ID: &image,
		},
		Profile: &vpcv1.InstanceProfileIdentity{
			Name: &profile,
		},
		Zone: &vpcv1.ZoneIdentity{
			Name: &zone,
		},
		PrimaryNetworkInterface: &vpcv1.NetworkInterfacePrototype{
			Subnet: &vpcv1.SubnetIdentity{
				ID: &subnet,
			},
		},
		UserData: &userDataEnc,
	}

	klog.Info("src:CreateVsi:KeyIdentityIntf > about-to")
	instPrototype.Keys = []vpcv1.KeyIdentityIntf{}
	key := &vpcv1.KeyIdentity{
		ID: &ssh,
	}

	instPrototype.Keys = append(instPrototype.Keys, key)

	klog.Info("src:CreateVsi:SetInstancePrototype > about-to")
	opts.SetInstancePrototype(instPrototype)

	klog.Info("src:CreateVsi:CreateInstance > about-to")
	inst, resp, err := c.ibClient.CreateInstance(opts)
	klog.Info("src:CreateVsi:CreateInstance > after")

	if err != nil {
		klog.Info("src:createVSI:err: ", err)
	}

	klog.Info("src:CreateVsi:CreateInstance:returncode: ", resp.GetStatusCode())

	if resp.GetStatusCode() != 201 {
		klog.Error("src:client:cvsi err response: ", resp.GetStatusCode())
		rawresp := string(resp.RawResult)
		return nil, errors.New(rawresp)
	}

	klog.Info("src:crn: ", inst.CRN)

	return inst, nil
}

func (c *ibmClient) DescribeVpcs() error {
	klog.Info("src:DescribeVpcs > entry")
	listFloatingIpsOptions := c.ibClient.NewListFloatingIpsOptions()
	floatingIPs, response, err := c.ibClient.ListFloatingIps(listFloatingIpsOptions)
	klog.Info("src:total FIPs: ", floatingIPs.TotalCount)
	klog.Info("src:resp code: ", response.StatusCode)
	klog.Info("src:err: ", err)
	return nil
}

type ibmClient struct {
	ibClient vpcv1.VpcV1
}

type awsClient struct {
	ec2Client   ec2iface.EC2API
	elbClient   elbiface.ELBAPI
	elbv2Client elbv2iface.ELBV2API
}

func (c *awsClient) DescribeDHCPOptions(input *ec2.DescribeDhcpOptionsInput) (*ec2.DescribeDhcpOptionsOutput, error) {
	return c.ec2Client.DescribeDhcpOptions(input)
}

func (c *awsClient) DescribeImages(input *ec2.DescribeImagesInput) (*ec2.DescribeImagesOutput, error) {
	return c.ec2Client.DescribeImages(input)
}

func (c *awsClient) DescribeVpcs(input *ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error) {
	return c.ec2Client.DescribeVpcs(input)
}

func (c *awsClient) DescribeSubnets(input *ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error) {
	return c.ec2Client.DescribeSubnets(input)
}

func (c *awsClient) DescribeAvailabilityZones(input *ec2.DescribeAvailabilityZonesInput) (*ec2.DescribeAvailabilityZonesOutput, error) {
	return c.ec2Client.DescribeAvailabilityZones(input)
}

func (c *awsClient) DescribeSecurityGroups(input *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error) {
	return c.ec2Client.DescribeSecurityGroups(input)
}

func (c *awsClient) RunInstances(input *ec2.RunInstancesInput) (*ec2.Reservation, error) {
	return c.ec2Client.RunInstances(input)
}

func (c *awsClient) DescribeInstances(input *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
	return c.ec2Client.DescribeInstances(input)
}

func (c *awsClient) TerminateInstances(input *ec2.TerminateInstancesInput) (*ec2.TerminateInstancesOutput, error) {
	return c.ec2Client.TerminateInstances(input)
}

func (c *awsClient) DescribeVolumes(input *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
	return c.ec2Client.DescribeVolumes(input)
}

func (c *awsClient) CreateTags(input *ec2.CreateTagsInput) (*ec2.CreateTagsOutput, error) {
	return c.ec2Client.CreateTags(input)
}

func (c *awsClient) RegisterInstancesWithLoadBalancer(input *elb.RegisterInstancesWithLoadBalancerInput) (*elb.RegisterInstancesWithLoadBalancerOutput, error) {
	return c.elbClient.RegisterInstancesWithLoadBalancer(input)
}

func (c *awsClient) ELBv2DescribeLoadBalancers(input *elbv2.DescribeLoadBalancersInput) (*elbv2.DescribeLoadBalancersOutput, error) {
	return c.elbv2Client.DescribeLoadBalancers(input)
}

func (c *awsClient) ELBv2DescribeTargetGroups(input *elbv2.DescribeTargetGroupsInput) (*elbv2.DescribeTargetGroupsOutput, error) {
	return c.elbv2Client.DescribeTargetGroups(input)
}

func (c *awsClient) ELBv2RegisterTargets(input *elbv2.RegisterTargetsInput) (*elbv2.RegisterTargetsOutput, error) {
	return c.elbv2Client.RegisterTargets(input)
}

func (c *awsClient) ELBv2DeregisterTargets(input *elbv2.DeregisterTargetsInput) (*elbv2.DeregisterTargetsOutput, error) {
	return c.elbv2Client.DeregisterTargets(input)
}

// NewClient creates our client wrapper object for the actual AWS clients we use.
// For authentication the underlying clients will use either the cluster AWS credentials
// secret if defined (i.e. in the root cluster),
// otherwise the IAM profile of the master where the actuator will run. (target clusters)
func NewClient(ctrlRuntimeClient client.Client, secretName, namespace, region string, configManagedClient client.Client) (Client, error) {
	klog.Info("src:NewClient > entry")
	s, err := newAWSSession(ctrlRuntimeClient, secretName, namespace, region, configManagedClient)
	if err != nil {
		return nil, err
	}

	return &awsClient{
		ec2Client:   ec2.New(s),
		elbClient:   elb.New(s),
		elbv2Client: elbv2.New(s),
	}, nil
}

func NewClientIBM(ctrlRuntimeClient client.Client, secretName, namespace, region string, configManagedClient client.Client) (ClientIBM, error) {
	klog.Info("src:NewIBMClient > entry")
	var secret corev1.Secret
	if err := ctrlRuntimeClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: secretName}, &secret); err != nil {
		if apimachineryerrors.IsNotFound(err) {
			return nil, machineapiapierrors.InvalidMachineConfiguration("ibm credentials secret %s/%s: %v not found", namespace, secretName, err)
		}
		return nil, err
	}

	apikey := string(secret.Data["key"])

	klog.Info("src:key: ", apikey)

	// Instantiate the service with an API key based IAM authenticator
	vpcService, err := vpcv1.NewVpcV1(&vpcv1.VpcV1Options{
		Authenticator: &core.IamAuthenticator{
			ApiKey: apikey,
			URL:    "https://iam.cloud.ibm.com/identity/token",
		},
		URL: "https://us-east.iaas.cloud.ibm.com/v1",
	})

	if err != nil {

	}

	return &ibmClient{
		ibClient: *vpcService,
	}, nil

}

// NewClientFromKeys creates our client wrapper object for the actual AWS clients we use.
// For authentication the underlying clients will use AWS credentials.
func NewClientFromKeys(accessKey, secretAccessKey, region string) (Client, error) {
	awsConfig := &aws.Config{
		Region: aws.String(region),
		Credentials: credentials.NewStaticCredentials(
			accessKey,
			secretAccessKey,
			"",
		),
	}

	s, err := session.NewSession(awsConfig)
	if err != nil {
		return nil, err
	}
	s.Handlers.Build.PushBackNamed(addProviderVersionToUserAgent)

	return &awsClient{
		ec2Client:   ec2.New(s),
		elbClient:   elb.New(s),
		elbv2Client: elbv2.New(s),
	}, nil
}

// NewValidatedClient creates our client wrapper object for the actual AWS clients we use.
// This should behave the same as NewClient except it will validate the client configuration
// (eg the region) before returning the client.
func NewValidatedClient(ctrlRuntimeClient client.Client, secretName, namespace, region string, configManagedClient client.Client) (Client, error) {
	klog.Info("src:NewValidatedClient > entry")
	klog.Info("src:NewValidatedClient region: ", region)
	if len(region) < 1 {
		region = "us-east-1"
	}
	s, err := newAWSSession(ctrlRuntimeClient, secretName, namespace, region, configManagedClient)
	if err != nil {
		return nil, err
	}

	// Check that the endpoint can be resolved by the endpoint resolver.
	// If the endpoint is not known, it is not a standard or configured custom region.
	// If this is the case, the client will likely not be able to connect
	_, err = s.Config.EndpointResolver.EndpointFor("ec2", region, func(opts *endpoints.Options) {
		opts.StrictMatching = true
	})
	if err != nil {
		return nil, fmt.Errorf("region %q not resolved: %w", region, err)
	}

	return &awsClient{
		ec2Client:   ec2.New(s),
		elbClient:   elb.New(s),
		elbv2Client: elbv2.New(s),
	}, nil
}

func newAWSSession(ctrlRuntimeClient client.Client, secretName, namespace, region string, configManagedClient client.Client) (*session.Session, error) {
	sessionOptions := session.Options{
		Config: aws.Config{
			Region: aws.String(region),
		},
	}

	if secretName != "" {
		var secret corev1.Secret
		if err := ctrlRuntimeClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: secretName}, &secret); err != nil {
			if apimachineryerrors.IsNotFound(err) {
				return nil, machineapiapierrors.InvalidMachineConfiguration("aws credentials secret %s/%s: %v not found", namespace, secretName, err)
			}
			return nil, err
		}
		sharedCredsFile, err := sharedCredentialsFileFromSecret(&secret)
		if err != nil {
			return nil, fmt.Errorf("failed to create shared credentials file from Secret: %v", err)
		}
		sessionOptions.SharedConfigState = session.SharedConfigEnable
		sessionOptions.SharedConfigFiles = []string{sharedCredsFile}
	}

	// Resolve custom endpoints
	if err := resolveEndpoints(&sessionOptions.Config, ctrlRuntimeClient, region); err != nil {
		return nil, err
	}

	if err := useCustomCABundle(&sessionOptions, configManagedClient); err != nil {
		return nil, fmt.Errorf("failed to set the custom CA bundle: %w", err)
	}

	// Otherwise default to relying on the IAM role of the masters where the actuator is running:
	s, err := session.NewSessionWithOptions(sessionOptions)
	if err != nil {
		return nil, err
	}

	// Remove any temporary shared credentials files after session creation so they don't accumulate
	if len(sessionOptions.SharedConfigFiles) > 0 {
		os.Remove(sessionOptions.SharedConfigFiles[0])
	}

	s.Handlers.Build.PushBackNamed(addProviderVersionToUserAgent)

	return s, nil
}

// addProviderVersionToUserAgent is a named handler that will add cluster-api-provider-aws
// version information to requests made by the AWS SDK.
var addProviderVersionToUserAgent = request.NamedHandler{
	Name: "openshift.io/cluster-api-provider-aws",
	Fn:   request.MakeAddToUserAgentHandler("openshift.io cluster-api-provider-aws", version.Version.String()),
}

func resolveEndpoints(awsConfig *aws.Config, ctrlRuntimeClient client.Client, region string) error {
	infra := &configv1.Infrastructure{}
	infraName := client.ObjectKey{Name: GlobalInfrastuctureName}

	if err := ctrlRuntimeClient.Get(context.Background(), infraName, infra); err != nil {
		return err
	}

	// Do nothing when custom endpoints are missing
	if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.AWS == nil {
		return nil
	}

	customEndpointsMap := buildCustomEndpointsMap(infra.Status.PlatformStatus.AWS.ServiceEndpoints)

	if len(customEndpointsMap) == 0 {
		return nil
	}

	customResolver := func(service, region string, optFns ...func(*endpoints.Options)) (endpoints.ResolvedEndpoint, error) {
		if url, ok := customEndpointsMap[service]; ok {
			return endpoints.ResolvedEndpoint{
				URL:           url,
				SigningRegion: region,
			}, nil

		}
		return endpoints.DefaultResolver().EndpointFor(service, region, optFns...)
	}

	awsConfig.EndpointResolver = endpoints.ResolverFunc(customResolver)

	return nil
}

// buildCustomEndpointsMap constructs a map that links endpoint name and it's url
func buildCustomEndpointsMap(customEndpoints []configv1.AWSServiceEndpoint) map[string]string {
	customEndpointsMap := make(map[string]string)

	for _, customEndpoint := range customEndpoints {
		customEndpointsMap[customEndpoint.Name] = customEndpoint.URL
	}

	return customEndpointsMap
}

// sharedCredentialsFileFromSecret returns a location (path) to the shared credentials
// file that was created using the provided secret
func sharedCredentialsFileFromSecret(secret *corev1.Secret) (string, error) {
	var data []byte
	switch {
	case len(secret.Data["credentials"]) > 0:
		data = secret.Data["credentials"]
	case len(secret.Data["aws_access_key_id"]) > 0 && len(secret.Data["aws_secret_access_key"]) > 0:
		data = newConfigForStaticCreds(
			string(secret.Data["aws_access_key_id"]),
			string(secret.Data["aws_secret_access_key"]),
		)
	default:
		return "", fmt.Errorf("invalid secret for aws credentials")
	}

	f, err := ioutil.TempFile("", "aws-shared-credentials")
	if err != nil {
		return "", fmt.Errorf("failed to create file for shared credentials: %v", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return "", fmt.Errorf("failed to write credentials to %s: %v", f.Name(), err)
	}
	return f.Name(), nil
}

func newConfigForStaticCreds(accessKey string, accessSecret string) []byte {
	buf := &bytes.Buffer{}
	fmt.Fprint(buf, "[default]\n")
	fmt.Fprintf(buf, "aws_access_key_id = %s\n", accessKey)
	fmt.Fprintf(buf, "aws_secret_access_key = %s\n", accessSecret)
	return buf.Bytes()
}

// useCustomCABundle will set up a custom CA bundle in the AWS options if a CA bundle is configured in the
// kube cloud config.
func useCustomCABundle(awsOptions *session.Options, configManagedClient client.Client) error {
	cm := &corev1.ConfigMap{}
	switch err := configManagedClient.Get(
		context.Background(),
		client.ObjectKey{Namespace: KubeCloudConfigNamespace, Name: kubeCloudConfigName},
		cm,
	); {
	case apimachineryerrors.IsNotFound(err):
		// no cloud config ConfigMap, so no custom CA bundle
		return nil
	case err != nil:
		return fmt.Errorf("failed to get kube-cloud-config ConfigMap: %w", err)
	}
	caBundle, ok := cm.Data[cloudCABundleKey]
	if !ok {
		// no "ca-bundle.pem" key in the ConfigMap, so no custom CA bundle
		return nil
	}
	klog.Info("using a custom CA bundle")
	awsOptions.CustomCABundle = strings.NewReader(caBundle)
	return nil
}
