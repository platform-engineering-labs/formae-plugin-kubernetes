//go:build integration

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package interop

import (
	"strings"
	"testing"
)

const (
	expectedDriftStack = "ci-nginx-123456"
	expectedDriftLabel = "ci-nginx-123456/ingress-nginx-a1b2c3"
)

var expectedDriftRelease = driftResource{
	Stack: expectedDriftStack,
	Type:  "K8S::Helm::Release",
	Label: expectedDriftLabel,
}

func TestParseReconcileRejectionBuildsOneRevertDecision(t *testing.T) {
	raw := `{
  "error": "ReconcileRejected",
  "data": {
    "ObservationID": "observation-1",
    "ModifiedStacks": {
      "ci-nginx-123456": {
        "ModifiedResources": [{
          "ExternalChangesOnly": true,
          "ResourceID": "resource-1",
          "Stack": "ci-nginx-123456",
          "Type": "K8S::Helm::Release",
          "Label": "ci-nginx-123456/ingress-nginx-a1b2c3",
          "Operation": "update"
        }]
      }
    }
  }
}`

	controls, err := parseReconcileRejection([]byte(raw), expectedDriftRelease)
	if err != nil {
		t.Fatal(err)
	}
	if controls.ObservationID != "observation-1" {
		t.Fatalf("ObservationID = %q", controls.ObservationID)
	}
	if len(controls.Decisions) != 1 || controls.Decisions[0].ResourceID != "resource-1" || controls.Decisions[0].Action != "revert" {
		t.Fatalf("Decisions = %#v", controls.Decisions)
	}
}

func TestParseResolutionReviewRequiresMatchingReceiptAndUpdate(t *testing.T) {
	raw := `{
  "Review": {
    "ObservationID": "observation-1",
    "ReviewID": "review-1",
    "Decisions": [{"ResourceID":"resource-1","Action":"revert"}],
    "Observations": [{
      "ResourceID": "resource-1",
      "Stack": "ci-nginx-123456",
      "Type": "K8S::Helm::Release",
      "Label": "ci-nginx-123456/ingress-nginx-a1b2c3",
      "Kind": "update"
    }]
  },
  "Description": {},
  "Simulation": {
    "ChangesRequired": true,
    "Command": {
      "ResourceUpdates": [{
        "ResourceId": "resource-1",
        "ResourceType": "K8S::Helm::Release",
        "ResourceLabel": "ci-nginx-123456/ingress-nginx-a1b2c3",
        "StackName": "ci-nginx-123456",
        "Operation": "update"
      }]
    }
  }
}`
	controls := driftResolution{
		ObservationID: "observation-1",
		Decisions:     []driftDecision{{ResourceID: "resource-1", Action: "revert"}},
	}

	reviewID, err := parseResolutionReview([]byte(raw), controls, expectedDriftRelease)
	if err != nil {
		t.Fatal(err)
	}
	if reviewID != "review-1" {
		t.Fatalf("ReviewID = %q", reviewID)
	}
}

func TestParseSubmittedCommand(t *testing.T) {
	commandID, err := parseSubmittedCommand([]byte(`{"CommandId":"command-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if commandID != "command-1" {
		t.Fatalf("CommandId = %q", commandID)
	}
}

func TestParseReconcileRejectionRejectsWrongOrExtraDrift(t *testing.T) {
	tests := map[string]string{
		"wrong resource":    `{"error":"ReconcileRejected","data":{"ObservationID":"observation-1","ModifiedStacks":{"ci-nginx-123456":{"ModifiedResources":[{"ExternalChangesOnly":true,"ResourceID":"resource-1","Stack":"ci-nginx-123456","Type":"K8S::Core::ConfigMap","Label":"wrong","Operation":"update"}]}}}}`,
		"extra resource":    `{"error":"ReconcileRejected","data":{"ObservationID":"observation-1","ModifiedStacks":{"ci-nginx-123456":{"ModifiedResources":[{"ExternalChangesOnly":true,"ResourceID":"resource-1","Stack":"ci-nginx-123456","Type":"K8S::Helm::Release","Label":"ci-nginx-123456/ingress-nginx-a1b2c3","Operation":"update"},{"ExternalChangesOnly":true,"ResourceID":"resource-2","Stack":"ci-nginx-123456","Type":"K8S::Core::ConfigMap","Label":"extra","Operation":"update"}]}}}}`,
		"not external only": `{"error":"ReconcileRejected","data":{"ObservationID":"observation-1","ModifiedStacks":{"ci-nginx-123456":{"ModifiedResources":[{"ResourceID":"resource-1","Stack":"ci-nginx-123456","Type":"K8S::Helm::Release","Label":"ci-nginx-123456/ingress-nginx-a1b2c3","Operation":"update"}]}}}}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseReconcileRejection([]byte(raw), expectedDriftRelease); err == nil {
				t.Fatal("accepted invalid drift payload")
			}
		})
	}
}

func TestParseResolutionReviewRejectsMissingOrMismatchedReviewData(t *testing.T) {
	valid := `{"Review":{"ObservationID":"observation-1","ReviewID":"review-1","Decisions":[{"ResourceID":"resource-1","Action":"revert"}],"Observations":[{"ResourceID":"resource-1","Stack":"ci-nginx-123456","Type":"K8S::Helm::Release","Label":"ci-nginx-123456/ingress-nginx-a1b2c3","Kind":"update"}]},"Simulation":{"ChangesRequired":true,"Command":{"ResourceUpdates":[{"ResourceId":"resource-1","ResourceType":"K8S::Helm::Release","ResourceLabel":"ci-nginx-123456/ingress-nginx-a1b2c3","StackName":"ci-nginx-123456","Operation":"update"}]}}}`
	controls := driftResolution{ObservationID: "observation-1", Decisions: []driftDecision{{ResourceID: "resource-1", Action: "revert"}}}
	tests := map[string]string{
		"missing review id":      strings.Replace(valid, `"ReviewID":"review-1"`, `"ReviewID":""`, 1),
		"wrong observation":      strings.Replace(valid, `"ObservationID":"observation-1"`, `"ObservationID":"other"`, 1),
		"wrong decision":         strings.Replace(valid, `"Action":"revert"`, `"Action":"absorb"`, 1),
		"no changes":             strings.Replace(valid, `"ChangesRequired":true`, `"ChangesRequired":false`, 1),
		"no resource updates":    strings.Replace(valid, `"ResourceUpdates":[{"ResourceId":"resource-1","ResourceType":"K8S::Helm::Release","ResourceLabel":"ci-nginx-123456/ingress-nginx-a1b2c3","StackName":"ci-nginx-123456","Operation":"update"}]`, `"ResourceUpdates":[]`, 1),
		"missing release update": strings.Replace(valid, `"Operation":"update"`, `"Operation":"delete"`, 1),
		"extra resource update": strings.Replace(valid,
			`"ResourceUpdates":[{"ResourceId":"resource-1","ResourceType":"K8S::Helm::Release","ResourceLabel":"ci-nginx-123456/ingress-nginx-a1b2c3","StackName":"ci-nginx-123456","Operation":"update"}]`,
			`"ResourceUpdates":[{"ResourceId":"resource-1","ResourceType":"K8S::Helm::Release","ResourceLabel":"ci-nginx-123456/ingress-nginx-a1b2c3","StackName":"ci-nginx-123456","Operation":"update"},{"ResourceId":"resource-2","ResourceType":"K8S::Core::ConfigMap","ResourceLabel":"extra","StackName":"ci-nginx-123456","Operation":"update"}]`, 1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseResolutionReview([]byte(raw), controls, expectedDriftRelease); err == nil {
				t.Fatal("accepted invalid review payload")
			}
		})
	}
}

func TestParseSubmittedCommandRejectsMissingID(t *testing.T) {
	if _, err := parseSubmittedCommand([]byte(`{"CommandId":""}`)); err == nil {
		t.Fatal("accepted empty CommandId")
	}
}

func TestParseStaleDriftReviewRejectionAcceptsOnlyNoCommandStale(t *testing.T) {
	for _, raw := range []string{
		`{"error":"DriftResolutionRejected","data":{"Code":"stale-review","Reason":"final plan or relevant state changed"}}`,
		`{"error":"DriftResolutionRejected","data":{"Code":"stale-review","Reason":"final plan or relevant state changed","CommandId":""}}`,
	} {
		reason, err := parseStaleDriftReviewRejection([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if reason != "final plan or relevant state changed" {
			t.Fatalf("Reason = %q", reason)
		}
	}
}

func TestParseStaleDriftReviewRejectionRejectsOtherResponses(t *testing.T) {
	tests := map[string]string{
		"malformed JSON":       `{`,
		"malformed data shape": `{"error":"DriftResolutionRejected","data":[]}`,
		"duplicate error":      `{"error":"ReconcileRejected","error":"DriftResolutionRejected","data":{"Code":"stale-review","Reason":"changed"}}`,
		"duplicate command id": `{"error":"DriftResolutionRejected","data":{"Code":"stale-review","Reason":"changed","CommandId":"command-1","CommandId":""}}`,
		"wrong error":          `{"error":"ReconcileRejected","data":{"Code":"stale-review","Reason":"changed"}}`,
		"wrong code":           `{"error":"DriftResolutionRejected","data":{"Code":"invalid-resolution","Reason":"changed"}}`,
		"empty reason":         `{"error":"DriftResolutionRejected","data":{"Code":"stale-review","Reason":""}}`,
		"non-string reason":    `{"error":"DriftResolutionRejected","data":{"Code":"stale-review","Reason":true}}`,
		"nonempty command id":  `{"error":"DriftResolutionRejected","data":{"Code":"stale-review","Reason":"changed","CommandId":"command-1"}}`,
		"null command id":      `{"error":"DriftResolutionRejected","data":{"Code":"stale-review","Reason":"changed","CommandId":null}}`,
		"recovery required":    `{"error":"DriftResolutionRejected","data":{"Code":"recovery-required","Reason":"recover admitted command","CommandId":"command-1"}}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseStaleDriftReviewRejection([]byte(raw)); err == nil {
				t.Fatal("accepted non-retryable submission response")
			}
		})
	}
}
