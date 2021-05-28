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

package machine

import (
	"context"

	machinev1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	awsclient "sigs.k8s.io/cluster-api-provider-aws/pkg/client"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	scopeFailFmt      = "%s: failed to create scope for machine: %w"
	reconcilerFailFmt = "%s: reconciler failed to %s machine: %w"
	createEventAction = "Create"
	updateEventAction = "Update"
	deleteEventAction = "Delete"
	noEventAction     = ""
)

// Actuator is responsible for performing machine reconciliation.
type Actuator struct {
	client              runtimeclient.Client
	eventRecorder       record.EventRecorder
	awsClientBuilder    awsclient.AwsClientBuilderFuncType
	ibmClientBuilder    awsclient.IbmClientBuilderFuncType
	configManagedClient runtimeclient.Client
}

// ActuatorParams holds parameter information for Actuator.
type ActuatorParams struct {
	Client              runtimeclient.Client
	EventRecorder       record.EventRecorder
	AwsClientBuilder    awsclient.AwsClientBuilderFuncType
	IbmClientBuilder    awsclient.IbmClientBuilderFuncType
	ConfigManagedClient runtimeclient.Client
}

// NewActuator returns an actuator.
func NewActuator(params ActuatorParams) *Actuator {
	return &Actuator{
		client:              params.Client,
		eventRecorder:       params.EventRecorder,
		awsClientBuilder:    params.AwsClientBuilder,
		ibmClientBuilder:    params.IbmClientBuilder,
		configManagedClient: params.ConfigManagedClient,
	}
}

// Set corresponding event based on error. It also returns the original error
// for convenience, so callers can do "return handleMachineError(...)".
func (a *Actuator) handleMachineError(machine *machinev1.Machine, err error, eventAction string) error {
	klog.Errorf("%v error: %v", machine.GetName(), err)
	if eventAction != noEventAction {
		a.eventRecorder.Eventf(machine, corev1.EventTypeWarning, "Failed"+eventAction, "%v", err)
	}
	return err
}

// Create creates a machine and is invoked by the machine controller.
func (a *Actuator) Create(ctx context.Context, machine *machinev1.Machine) error {
	klog.Info("src:act:Create > entry")
	klog.Infof("%s: actuator creating machine", machine.GetName())

	scopeIBM, errIBM := newMachineScopeIBM(machineScopeParamsIBM{
		Context:             ctx,
		client:              a.client,
		machine:             machine,
		ibmClientBuilder:    a.ibmClientBuilder,
		configManagedClient: a.configManagedClient,
	})

	if errIBM != nil {
		klog.Info("errIBM: ", errIBM)
	}

	if err := newReconcilerIBM(scopeIBM).createIBM(); err != nil {
		klog.Error("src: newReconcilerIBM(scopeIBM).createIBM(): ", err)
	}
	return scopeIBM.patchMachine()

	/*
		scope, err := newMachineScope(machineScopeParams{
			Context:             ctx,
			client:              a.client,
			machine:             machine,
			awsClientBuilder:    a.awsClientBuilder,
			configManagedClient: a.configManagedClient,
		})
		if err != nil {
			fmtErr := fmt.Errorf(scopeFailFmt, machine.GetName(), err)
			return a.handleMachineError(machine, fmtErr, createEventAction)
		}
		if err := newReconciler(scope).create(); err != nil {
			if err := scope.patchMachine(); err != nil {
				return err
			}
			fmtErr := fmt.Errorf(reconcilerFailFmt, machine.GetName(), createEventAction, err)
			return a.handleMachineError(machine, fmtErr, createEventAction)
		}
		a.eventRecorder.Eventf(machine, corev1.EventTypeNormal, createEventAction, "Created Machine %v", machine.GetName())
		return scope.patchMachine()
	*/
}

// Exists determines if the given machine currently exists.
// A machine which is not terminated is considered as existing.
func (a *Actuator) Exists(ctx context.Context, machine *machinev1.Machine) (bool, error) {
	klog.Info("src:act:Exists 0 > entry")
	klog.Infof("%s: actuator checking if machine exists", machine.GetName())

	scopeIBM, errIBM := newMachineScopeIBM(machineScopeParamsIBM{
		Context:             ctx,
		client:              a.client,
		machine:             machine,
		ibmClientBuilder:    a.ibmClientBuilder,
		configManagedClient: a.configManagedClient,
	})

	if errIBM != nil {
		klog.Error("src:scopeIBM err ", errIBM)
	}

	return newReconcilerIBM(scopeIBM).exists()

	//return newReconcilerIBM(scopeIBM).exists()

	/*
		scope, err := newMachineScope(machineScopeParams{
			Context:             ctx,
			client:              a.client,
			machine:             machine,
			awsClientBuilder:    a.awsClientBuilder,
			configManagedClient: a.configManagedClient,
		})
		if err != nil {
			return false, fmt.Errorf(scopeFailFmt, machine.GetName(), err)
		}
		return newReconciler(scope).exists()
	*/
}

// Update attempts to sync machine state with an existing instance.
func (a *Actuator) Update(ctx context.Context, machine *machinev1.Machine) error {
	klog.Infof("src:rec:Update > entry")
	klog.Info("src: machine: ", machine.GetName())

	scopeIBM, errIBM := newMachineScopeIBM(machineScopeParamsIBM{
		Context:             ctx,
		client:              a.client,
		machine:             machine,
		ibmClientBuilder:    a.ibmClientBuilder,
		configManagedClient: a.configManagedClient,
	})

	if errIBM != nil {
		klog.Error("src:rec:update err: ", errIBM)
	}

	return newReconcilerIBM(scopeIBM).update()

	/*
		scope, err := newMachineScope(machineScopeParams{
			Context:             ctx,
			client:              a.client,
			machine:             machine,
			awsClientBuilder:    a.awsClientBuilder,
			configManagedClient: a.configManagedClient,
		})
		if err != nil {
			fmtErr := fmt.Errorf(scopeFailFmt, machine.GetName(), err)
			return a.handleMachineError(machine, fmtErr, updateEventAction)
		}
		if err := newReconciler(scope).update(); err != nil {
			// Update machine and machine status in case it was modified
			if err := scope.patchMachine(); err != nil {
				return err
			}
			fmtErr := fmt.Errorf(reconcilerFailFmt, machine.GetName(), updateEventAction, err)
			return a.handleMachineError(machine, fmtErr, updateEventAction)
		}

		previousResourceVersion := scope.machine.ResourceVersion

		if err := scope.patchMachine(); err != nil {
			return err
		}

		currentResourceVersion := scope.machine.ResourceVersion

		// Create event only if machine object was modified
		if previousResourceVersion != currentResourceVersion {
			a.eventRecorder.Eventf(machine, corev1.EventTypeNormal, updateEventAction, "Updated Machine %v", machine.GetName())
		}

		return nil
	*/
}

// Delete deletes a machine and updates its finalizer
func (a *Actuator) Delete(ctx context.Context, machine *machinev1.Machine) error {
	klog.Info("src:act:Delete > entry")
	klog.Infof("%s: actuator deleting machine", machine.GetName())

	scopeIBM, errIBM := newMachineScopeIBM(machineScopeParamsIBM{
		Context:             ctx,
		client:              a.client,
		machine:             machine,
		ibmClientBuilder:    a.ibmClientBuilder,
		configManagedClient: a.configManagedClient,
	})

	if errIBM != nil {
		klog.Info("src:errIBM ", errIBM)
	}

	newReconcilerIBM(scopeIBM).delete()

	return scopeIBM.patchMachine()

	/*
		scope, err := newMachineScope(machineScopeParams{
			Context:             ctx,
			client:              a.client,
			machine:             machine,
			awsClientBuilder:    a.awsClientBuilder,
			configManagedClient: a.configManagedClient,
		})
		if err != nil {
			fmtErr := fmt.Errorf(scopeFailFmt, machine.GetName(), err)
			return a.handleMachineError(machine, fmtErr, deleteEventAction)
		}
		if err := newReconciler(scope).delete(); err != nil {
			if err := scope.patchMachine(); err != nil {
				return err
			}
			fmtErr := fmt.Errorf(reconcilerFailFmt, machine.GetName(), deleteEventAction, err)
			return a.handleMachineError(machine, fmtErr, deleteEventAction)
		}
		a.eventRecorder.Eventf(machine, corev1.EventTypeNormal, deleteEventAction, "Deleted machine %v", machine.GetName())
		return scope.patchMachine()
	*/

}
