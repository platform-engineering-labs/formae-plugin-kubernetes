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

func TestStatefulSetOperationStatus_OnDeleteReadyIsSuccess(t *testing.T) {
	ss := &StatefulSet{}
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 2},
		Spec: appsv1.StatefulSetSpec{
			Replicas:       ptrInt32(3),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 2,
			ReadyReplicas:      3,
			UpdatedReplicas:    0, // OnDelete: controller won't recreate pods
		},
	}
	if got := ss.operationStatus(sts); got != resource.OperationStatusSuccess {
		t.Fatalf("OnDelete ready: expected Success, got %s", got)
	}
}

func TestStatefulSetOperationStatus_PartitionSuccessAtDesiredMinusPartition(t *testing.T) {
	ss := &StatefulSet{}
	part := int32(2)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 5},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptrInt32(3),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type:          appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &part},
			},
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 5,
			ReadyReplicas:      3,
			UpdatedReplicas:    1, // only ordinal >= partition(2) updates → 3-2 = 1
		},
	}
	if got := ss.operationStatus(sts); got != resource.OperationStatusSuccess {
		t.Fatalf("partitioned rollout complete: expected Success, got %s", got)
	}
}

func TestStatefulSetOperationStatus_PartitionInProgress(t *testing.T) {
	ss := &StatefulSet{}
	part := int32(2)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 5},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptrInt32(3),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type:          appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &part},
			},
		},
		Status: appsv1.StatefulSetStatus{ObservedGeneration: 5, ReadyReplicas: 3, UpdatedReplicas: 0},
	}
	if got := ss.operationStatus(sts); got != resource.OperationStatusInProgress {
		t.Fatalf("partitioned rollout mid-flight: expected InProgress, got %s", got)
	}
}
