// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package apps

import (
	"testing"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ptrInt32(v int32) *int32 { return &v }

func TestDeploymentOperationStatus_PausedIsSuccess(t *testing.T) {
	d := &Deployment{}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 2},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptrInt32(3),
			Paused:   true,
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 2, // controller has observed the paused spec
			UpdatedReplicas:    0, // paused → new RS never created
			ReadyReplicas:      0,
			AvailableReplicas:  0,
		},
	}
	if got := d.operationStatus(deploy); got != resource.OperationStatusSuccess {
		t.Fatalf("paused deployment: expected Success, got %s", got)
	}
}

func TestDeploymentOperationStatus_PausedButUnobservedIsInProgress(t *testing.T) {
	d := &Deployment{}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 3},
		Spec:       appsv1.DeploymentSpec{Replicas: ptrInt32(3), Paused: true},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 2},
	}
	if got := d.operationStatus(deploy); got != resource.OperationStatusInProgress {
		t.Fatalf("paused but unobserved: expected InProgress, got %s", got)
	}
}
