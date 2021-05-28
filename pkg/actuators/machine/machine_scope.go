package machine

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go/service/ec2"
	machinev1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	machineapierros "github.com/openshift/machine-api-operator/pkg/controller/machine"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	awsproviderv1 "sigs.k8s.io/cluster-api-provider-aws/pkg/apis/awsprovider/v1beta1"
	awsclient "sigs.k8s.io/cluster-api-provider-aws/pkg/client"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	userDataSecretKey = "userData"
)

// dhcpDomainKeyName is a variable so we can reference it in unit tests.
var dhcpDomainKeyName = "domain-name"

// machineScopeParams defines the input parameters used to create a new MachineScope.
type machineScopeParams struct {
	context.Context

	awsClientBuilder awsclient.AwsClientBuilderFuncType
	// api server controller runtime client
	client runtimeclient.Client
	// machine resource
	machine *machinev1.Machine
	// api server controller runtime client for the openshift-config-managed namespace
	configManagedClient runtimeclient.Client
}

type machineScopeParamsIBM struct {
	context.Context

	ibmClientBuilder awsclient.IbmClientBuilderFuncType

	// api server controller runtime client
	client runtimeclient.Client
	// machine resource
	machine *machinev1.Machine
	// api server controller runtime client for the openshift-config-managed namespace
	configManagedClient runtimeclient.Client
}

type machineScope struct {
	context.Context

	// client for interacting with AWS
	awsClient awsclient.Client
	// api server controller runtime client
	client runtimeclient.Client
	// machine resource
	machine            *machinev1.Machine
	machineToBePatched runtimeclient.Patch
	providerSpec       *awsproviderv1.AWSMachineProviderConfig
	providerStatus     *awsproviderv1.AWSMachineProviderStatus
}

type IBMMachineProviderStatus struct {
	metav1.TypeMeta `json:",inline"`

	// InstanceID is the instance ID of the machine created in AWS
	// +optional
	InstanceID *string `json:"instanceId,omitempty"`

	// InstanceState is the state of the AWS instance for this machine
	// +optional
	InstanceState *string `json:"instanceState,omitempty"`
}

type machineScopeIBM struct {
	context.Context

	//client for interacting with IBMCloud
	ibmClient awsclient.ClientIBM
	// api server controller runtime client
	client runtimeclient.Client
	//Logger        logr.Logger
	//Cluster       *clusterv1.Cluster
	//Machine *clusterv1.Machine
	// machine resource
	machine            *machinev1.Machine
	machineToBePatched runtimeclient.Patch
	//providerSpec       *awsproviderv1.AWSMachineProviderConfig
	providerStatus *IBMMachineProviderStatus
	//IBMVPCCluster *infrav1.IBMVPCCluster
	//IBMVPCMachine *machinev1.IBMVPCMachine
}

func newMachineScopeIBM(params machineScopeParamsIBM) (*machineScopeIBM, error) {

	klog.Info("src:m_s:newMachineScopeIBM > entry")

	ibmClient, err := params.ibmClientBuilder(params.client, "ibmcloud-mao-key", params.machine.Namespace, "us-east", params.configManagedClient)

	if err != nil {
		return nil, machineapierros.InvalidMachineConfiguration("failed to create aws client: %v", err.Error())
	}

	return &machineScopeIBM{
		Context:            params.Context,
		ibmClient:          ibmClient,
		client:             params.client,
		machine:            params.machine,
		machineToBePatched: runtimeclient.MergeFrom(params.machine.DeepCopy()),
	}, nil
}

func newMachineScope(params machineScopeParams) (*machineScope, error) {
	providerSpec, err := awsproviderv1.ProviderSpecFromRawExtension(params.machine.Spec.ProviderSpec.Value)
	if err != nil {
		return nil, machineapierros.InvalidMachineConfiguration("failed to get machine config: %v", err)
	}

	providerStatus, err := awsproviderv1.ProviderStatusFromRawExtension(params.machine.Status.ProviderStatus)
	if err != nil {
		return nil, machineapierros.InvalidMachineConfiguration("failed to get machine provider status: %v", err.Error())
	}

	credentialsSecretName := ""
	if providerSpec.CredentialsSecret != nil {
		credentialsSecretName = providerSpec.CredentialsSecret.Name
	}

	awsClient, err := params.awsClientBuilder(params.client, credentialsSecretName, params.machine.Namespace, providerSpec.Placement.Region, params.configManagedClient)
	if err != nil {
		return nil, machineapierros.InvalidMachineConfiguration("failed to create aws client: %v", err.Error())
	}

	return &machineScope{
		Context:            params.Context,
		awsClient:          awsClient,
		client:             params.client,
		machine:            params.machine,
		machineToBePatched: runtimeclient.MergeFrom(params.machine.DeepCopy()),
		providerSpec:       providerSpec,
		providerStatus:     providerStatus,
	}, nil
}

func (s *machineScopeIBM) patchMachine() error {
	klog.Info("src:*machineScopeIBM > entry")
	klog.Info("src:machine: ", s.machine.GetName())

	statusCopy := *s.machine.Status.DeepCopy()

	// patch machine
	if err := s.client.Patch(context.Background(), s.machine, s.machineToBePatched); err != nil {
		klog.Errorf("Failed to patch machine %q: %v", s.machine.GetName(), err)
		return err
	}

	s.machine.Status = statusCopy

	// patch status
	if err := s.client.Status().Patch(context.Background(), s.machine, s.machineToBePatched); err != nil {
		klog.Errorf("Failed to patch machine status %q: %v", s.machine.GetName(), err)
		return err
	}

	return nil
}

// Patch patches the machine spec and machine status after reconciling.
func (s *machineScope) patchMachine() error {
	klog.V(3).Infof("%v: patching machine", s.machine.GetName())

	providerStatus, err := awsproviderv1.RawExtensionFromProviderStatus(s.providerStatus)
	if err != nil {
		return machineapierros.InvalidMachineConfiguration("failed to get machine provider status: %v", err.Error())
	}
	s.machine.Status.ProviderStatus = providerStatus

	statusCopy := *s.machine.Status.DeepCopy()

	// patch machine
	if err := s.client.Patch(context.Background(), s.machine, s.machineToBePatched); err != nil {
		klog.Errorf("Failed to patch machine %q: %v", s.machine.GetName(), err)
		return err
	}

	s.machine.Status = statusCopy

	// patch status
	if err := s.client.Status().Patch(context.Background(), s.machine, s.machineToBePatched); err != nil {
		klog.Errorf("Failed to patch machine status %q: %v", s.machine.GetName(), err)
		return err
	}

	return nil
}

func (s *machineScopeIBM) getUserData() ([]byte, error) {
	klog.Info("src:(s *machineScopeIBM) getUserData() > entry")
	userDataSecret := &corev1.Secret{}

	objKey := runtimeclient.ObjectKey{
		Namespace: s.machine.Namespace,
		Name:      "worker-user-data",
	}

	if err := s.client.Get(s.Context, objKey, userDataSecret); err != nil {
		return nil, err
	}

	userData, exists := userDataSecret.Data[userDataSecretKey]
	if !exists {
		return nil, fmt.Errorf("src:IBM secret %s missing %s key", objKey, userDataSecretKey)
	}

	klog.Info("src:userData: ", userData)

	return userData, nil
}

// getUserData fetches the user-data from the secret referenced in the Machine's
// provider spec, if one is set.
func (s *machineScope) getUserData() ([]byte, error) {
	if s.providerSpec == nil || s.providerSpec.UserDataSecret == nil {
		return nil, nil
	}

	userDataSecret := &corev1.Secret{}

	objKey := runtimeclient.ObjectKey{
		Namespace: s.machine.Namespace,
		Name:      s.providerSpec.UserDataSecret.Name,
	}

	if err := s.client.Get(s.Context, objKey, userDataSecret); err != nil {
		return nil, err
	}

	userData, exists := userDataSecret.Data[userDataSecretKey]
	if !exists {
		return nil, fmt.Errorf("secret %s missing %s key", objKey, userDataSecretKey)
	}

	return userData, nil
}

func (s *machineScopeIBM) setProviderStatus(instanceId string, instanceState string) error {
	klog.Info("src:rec:setProviderStatus > entry")
	if s.providerStatus == nil {
		klog.Info("src: providerStatus is nil, assigning new instance")
		s.providerStatus = new(IBMMachineProviderStatus)
	}
	s.providerStatus.InstanceID = &instanceId
	s.providerStatus.InstanceState = &instanceState
	return nil
}

func (s *machineScope) setProviderStatus(instance *ec2.Instance, condition awsproviderv1.AWSMachineProviderCondition) error {
	klog.Infof("%s: Updating status", s.machine.Name)

	networkAddresses := []corev1.NodeAddress{}

	// TODO: remove 139-141, no need to clean instance id ands state
	// Instance may have existed but been deleted outside our control, clear it's status if so:
	if instance == nil {
		s.providerStatus.InstanceID = nil
		s.providerStatus.InstanceState = nil
	} else {
		s.providerStatus.InstanceID = instance.InstanceId
		s.providerStatus.InstanceState = instance.State.Name

		domainNames, err := s.getCustomDomainFromDHCP(instance.VpcId)

		if err != nil {
			return err
		}

		addresses, err := extractNodeAddresses(instance, domainNames)
		if err != nil {
			klog.Errorf("%s: Error extracting instance IP addresses: %v", s.machine.Name, err)
			return err
		}

		networkAddresses = append(networkAddresses, addresses...)
	}
	klog.Infof("%s: finished calculating AWS status", s.machine.Name)

	s.machine.Status.Addresses = networkAddresses
	s.providerStatus.Conditions = setAWSMachineProviderCondition(condition, s.providerStatus.Conditions)

	return nil
}

func (s *machineScope) getCustomDomainFromDHCP(vpcID *string) ([]string, error) {
	vpc, err := s.awsClient.DescribeVpcs(&ec2.DescribeVpcsInput{
		VpcIds: []*string{vpcID},
	})
	if err != nil {
		klog.Errorf("%s: error describing vpc: %v", s.machine.Name, err)
		return nil, err
	}

	if len(vpc.Vpcs) == 0 || vpc.Vpcs[0] == nil || vpc.Vpcs[0].DhcpOptionsId == nil {
		return nil, nil
	}

	dhcp, err := s.awsClient.DescribeDHCPOptions(&ec2.DescribeDhcpOptionsInput{
		DhcpOptionsIds: []*string{vpc.Vpcs[0].DhcpOptionsId},
	})
	if err != nil {
		klog.Errorf("%s: error describing dhcp: %v", s.machine.Name, err)
		return nil, err
	}

	if dhcp == nil || len(dhcp.DhcpOptions) == 0 || dhcp.DhcpOptions[0] == nil {
		return nil, nil
	}

	for _, i := range dhcp.DhcpOptions[0].DhcpConfigurations {
		if i.Key != nil && *i.Key == dhcpDomainKeyName && len(i.Values) > 0 && i.Values[0] != nil && i.Values[0].Value != nil {
			return strings.Split(*i.Values[0].Value, " "), nil
		}
	}
	return nil, nil
}
