//go:build integration

package batch_test

import (
	"encoding/json"
	"testing"
	"time"

	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/batch"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestJobCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Batch::Job",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "batch/v1",
				"kind":       "Job",
				"metadata": map[string]any{
					"name":      "test-job",
					"namespace": ns,
				},
				"spec": map[string]any{
					"template": map[string]any{
						"spec": map[string]any{
							"containers": []map[string]any{
								{"name": "worker", "image": "busybox:1.37", "command": []string{"echo", "hello"}},
							},
							"restartPolicy": "Never",
						},
					},
					"backoffLimit": 1,
				},
			})
		},
		SkipUpdate:           true, // Job spec is immutable
		ExpectedCreateStatus: resource.OperationStatusInProgress,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        60 * time.Second,
	})
}

func TestCronJobCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Batch::CronJob",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "batch/v1",
				"kind":       "CronJob",
				"metadata": map[string]any{
					"name":      "test-cronjob",
					"namespace": ns,
				},
				"spec": map[string]any{
					"schedule": "*/5 * * * *",
					"jobTemplate": map[string]any{
						"spec": map[string]any{
							"template": map[string]any{
								"spec": map[string]any{
									"containers": []map[string]any{
										{"name": "worker", "image": "busybox:1.37", "command": []string{"echo", "hello"}},
									},
									"restartPolicy": "OnFailure",
								},
							},
						},
					},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "batch/v1",
				"kind":       "CronJob",
				"metadata": map[string]any{
					"name":      "test-cronjob",
					"namespace": ns,
				},
				"spec": map[string]any{
					"schedule": "*/10 * * * *",
					"jobTemplate": map[string]any{
						"spec": map[string]any{
							"template": map[string]any{
								"spec": map[string]any{
									"containers": []map[string]any{
										{"name": "worker", "image": "busybox:1.37", "command": []string{"echo", "hello"}},
									},
									"restartPolicy": "OnFailure",
								},
							},
						},
					},
				},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
	})
}
