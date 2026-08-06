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

func TestDaemonSetOperationStatus_OnDeleteReadyIsSuccess(t *testing.T) {
	ds := &DaemonSet{}
	d := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 2},
		Spec:       appsv1.DaemonSetSpec{UpdateStrategy: appsv1.DaemonSetUpdateStrategy{Type: appsv1.OnDeleteDaemonSetStrategyType}},
		Status: appsv1.DaemonSetStatus{
			ObservedGeneration:     2,
			DesiredNumberScheduled: 3,
			NumberReady:            3,
			UpdatedNumberScheduled: 0, // OnDelete: not auto-rolled
		},
	}
	if got := ds.operationStatus(d); got != resource.OperationStatusSuccess {
		t.Fatalf("OnDelete ready: expected Success, got %s", got)
	}
}
