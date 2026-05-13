//go:build integration

package storage_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/storage"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestStorageClassCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Storage::StorageClass",
		IsNamespaced: false,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "storage.k8s.io/v1",
				"kind":       "StorageClass",
				"metadata": map[string]any{
					"name":   "formae-int-sc-" + ns,
					"labels": map[string]string{"app": "test"},
				},
				"provisioner":       "kubernetes.io/no-provisioner",
				"volumeBindingMode": "WaitForFirstConsumer",
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "storage.k8s.io/v1",
				"kind":       "StorageClass",
				"metadata": map[string]any{
					"name":   "formae-int-sc-" + ns,
					"labels": map[string]string{"app": "updated"},
				},
				"provisioner":       "kubernetes.io/no-provisioner",
				"volumeBindingMode": "WaitForFirstConsumer",
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
		CleanupExtra: func(t *testing.T, env *testutil.TestEnv, nativeID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			name := strings.TrimPrefix(nativeID, "/")
			_ = env.Client.StorageV1().StorageClasses().Delete(ctx, name, metav1.DeleteOptions{})
		},
	})
}

func TestCSIDriverCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Storage::CSIDriver",
		IsNamespaced: false,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "storage.k8s.io/v1",
				"kind":       "CSIDriver",
				"metadata": map[string]any{
					"name":   "formae-int-csi-" + ns + ".example.com",
					"labels": map[string]string{"app": "test"},
				},
				"spec": map[string]any{
					"attachRequired": false,
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "storage.k8s.io/v1",
				"kind":       "CSIDriver",
				"metadata": map[string]any{
					"name":   "formae-int-csi-" + ns + ".example.com",
					"labels": map[string]string{"app": "updated"},
				},
				"spec": map[string]any{
					"attachRequired": false,
				},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
		CleanupExtra: func(t *testing.T, env *testutil.TestEnv, nativeID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			name := strings.TrimPrefix(nativeID, "/")
			_ = env.Client.StorageV1().CSIDrivers().Delete(ctx, name, metav1.DeleteOptions{})
		},
	})
}
