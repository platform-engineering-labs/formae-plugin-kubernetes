//go:build integration

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package interop

import (
	"encoding/json"
	"fmt"
)

type driftResource struct {
	ResourceID          string `json:"ResourceID,omitempty"`
	Stack               string `json:"Stack"`
	Type                string `json:"Type"`
	Label               string `json:"Label"`
	Operation           string `json:"Operation"`
	ExternalChangesOnly bool   `json:"ExternalChangesOnly,omitempty"`
}

type driftDecision struct {
	ResourceID string `json:"ResourceID"`
	Action     string `json:"Action"`
}

type driftResolution struct {
	ObservationID  string          `json:"ObservationID"`
	ReviewID       string          `json:"ReviewID,omitempty"`
	Decisions      []driftDecision `json:"Decisions"`
	IdempotencyKey string          `json:"IdempotencyKey,omitempty"`
}

func parseReconcileRejection(raw []byte, expected driftResource) (driftResolution, error) {
	var payload struct {
		Error string `json:"error"`
		Data  struct {
			ObservationID  string `json:"ObservationID"`
			ModifiedStacks map[string]struct {
				ModifiedResources []driftResource `json:"ModifiedResources"`
			} `json:"ModifiedStacks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return driftResolution{}, fmt.Errorf("decode reconcile rejection: %w", err)
	}
	if payload.Error != "ReconcileRejected" {
		return driftResolution{}, fmt.Errorf("apply error = %q, want ReconcileRejected", payload.Error)
	}
	if payload.Data.ObservationID == "" {
		return driftResolution{}, fmt.Errorf("reconcile rejection has no ObservationID")
	}
	var changes []driftResource
	for _, stack := range payload.Data.ModifiedStacks {
		changes = append(changes, stack.ModifiedResources...)
	}
	if len(changes) != 1 {
		return driftResolution{}, fmt.Errorf("reconcile rejection contains %d changed resources, want 1", len(changes))
	}
	change := changes[0]
	if change.ResourceID == "" {
		return driftResolution{}, fmt.Errorf("changed release has no ResourceID")
	}
	if change.Stack != expected.Stack || change.Type != expected.Type || change.Label != expected.Label {
		return driftResolution{}, fmt.Errorf("changed resource = %s/%s %s, want %s/%s %s",
			change.Stack, change.Label, change.Type, expected.Stack, expected.Label, expected.Type)
	}
	if change.Operation != "update" {
		return driftResolution{}, fmt.Errorf("changed release operation = %q, want update", change.Operation)
	}
	if !change.ExternalChangesOnly {
		return driftResolution{}, fmt.Errorf("changed release is not marked ExternalChangesOnly")
	}
	return driftResolution{
		ObservationID: payload.Data.ObservationID,
		Decisions:     []driftDecision{{ResourceID: change.ResourceID, Action: "revert"}},
	}, nil
}

func parseResolutionReview(raw []byte, controls driftResolution, expected driftResource) (string, error) {
	var payload struct {
		Review struct {
			ObservationID string          `json:"ObservationID"`
			ReviewID      string          `json:"ReviewID"`
			Decisions     []driftDecision `json:"Decisions"`
			Observations  []struct {
				ResourceID string `json:"ResourceID"`
				Stack      string `json:"Stack"`
				Type       string `json:"Type"`
				Label      string `json:"Label"`
				Kind       string `json:"Kind"`
			} `json:"Observations"`
		} `json:"Review"`
		Simulation struct {
			ChangesRequired bool `json:"ChangesRequired"`
			Command         struct {
				ResourceUpdates []struct {
					ResourceID    string `json:"ResourceId"`
					ResourceType  string `json:"ResourceType"`
					ResourceLabel string `json:"ResourceLabel"`
					StackName     string `json:"StackName"`
					Operation     string `json:"Operation"`
				} `json:"ResourceUpdates"`
			} `json:"Command"`
		} `json:"Simulation"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("decode resolution review: %w", err)
	}
	if payload.Review.ReviewID == "" {
		return "", fmt.Errorf("resolution review has no ReviewID")
	}
	if payload.Review.ObservationID != controls.ObservationID {
		return "", fmt.Errorf("review ObservationID = %q, want %q", payload.Review.ObservationID, controls.ObservationID)
	}
	if len(controls.Decisions) != 1 || len(payload.Review.Decisions) != 1 || payload.Review.Decisions[0] != controls.Decisions[0] {
		return "", fmt.Errorf("review did not receipt the requested decision")
	}
	decision := controls.Decisions[0]
	if len(payload.Review.Observations) != 1 {
		return "", fmt.Errorf("review contains %d observations, want 1", len(payload.Review.Observations))
	}
	observation := payload.Review.Observations[0]
	if observation.ResourceID != decision.ResourceID || observation.Stack != expected.Stack || observation.Type != expected.Type || observation.Label != expected.Label || observation.Kind != "update" {
		return "", fmt.Errorf("review observation does not match the drifted release")
	}
	if !payload.Simulation.ChangesRequired {
		return "", fmt.Errorf("resolution review says no changes are required")
	}
	found := false
	for _, update := range payload.Simulation.Command.ResourceUpdates {
		if update.ResourceID == decision.ResourceID && update.StackName == expected.Stack && update.ResourceType == expected.Type && update.ResourceLabel == expected.Label && update.Operation == "update" {
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("resolution review has no matching release update")
	}
	return payload.Review.ReviewID, nil
}

func parseSubmittedCommand(raw []byte) (string, error) {
	var payload struct {
		CommandID string `json:"CommandId"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("decode submitted command: %w", err)
	}
	if payload.CommandID == "" {
		return "", fmt.Errorf("submission returned no CommandId")
	}
	return payload.CommandID, nil
}
