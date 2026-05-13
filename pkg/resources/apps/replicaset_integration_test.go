//go:build integration

// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package apps_test

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/apps"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplicaSetList_SkipsOwnedReplicaSets(t *testing.T) {
	env := testutil.SetupEnv(t)
	ctx := context.Background()

	// Standalone ReplicaSet (no owner reference) — should appear in List.
	replicas := int32(0)
	standalone := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "standalone-rs",
			Namespace: env.Namespace,
		},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "standalone"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "standalone"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "registry.k8s.io/pause:3.9"}},
				},
			},
		},
	}
	_, err := env.Client.AppsV1().ReplicaSets(env.Namespace).Create(ctx, standalone, metav1.CreateOptions{})
	require.NoError(t, err)

	// Deployment — the controller creates a child ReplicaSet with an ownerReference.
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "owner-dep",
			Namespace: env.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "owned"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "owned"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "registry.k8s.io/pause:3.9"}},
				},
			},
		},
	}
	_, err = env.Client.AppsV1().Deployments(env.Namespace).Create(ctx, deployment, metav1.CreateOptions{})
	require.NoError(t, err)

	// Wait until the deployment controller materializes a ReplicaSet.
	require.Eventually(t, func() bool {
		list, err := env.Client.AppsV1().ReplicaSets(env.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false
		}
		for _, rs := range list.Items {
			if len(rs.OwnerReferences) > 0 {
				return true
			}
		}
		return false
	}, 30*time.Second, 250*time.Millisecond, "Deployment did not produce an owned ReplicaSet")

	rsProv := &apps.ReplicaSet{Client: env.Client, Config: env.Config}
	result, err := rsProv.List(ctx, &resource.ListRequest{AdditionalProperties: map[string]string{"namespace": env.Namespace}})
	require.NoError(t, err)

	assert.Contains(t, result.NativeIDs, env.Namespace+"/standalone-rs",
		"standalone ReplicaSet must appear in List")
	for _, id := range result.NativeIDs {
		assert.NotContains(t, id, "owner-dep-",
			"owned ReplicaSets must be filtered from List")
	}
}
