//go:build integration

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package interop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	if len(payload.Simulation.Command.ResourceUpdates) != 1 {
		return "", fmt.Errorf("resolution review contains %d resource updates, want 1", len(payload.Simulation.Command.ResourceUpdates))
	}
	update := payload.Simulation.Command.ResourceUpdates[0]
	if update.ResourceID != decision.ResourceID || update.StackName != expected.Stack || update.ResourceType != expected.Type || update.ResourceLabel != expected.Label || update.Operation != "update" {
		return "", fmt.Errorf("resolution review update does not match the drifted release")
	}
	return payload.Review.ReviewID, nil
}

func parseStaleDriftReviewRejection(raw []byte) (string, error) {
	payload, err := decodeJSONObject(raw)
	if err != nil {
		return "", fmt.Errorf("decode drift resolution rejection: %w", err)
	}
	if len(payload) != 2 {
		return "", fmt.Errorf("drift resolution rejection has unexpected fields")
	}
	errorType, err := requiredJSONString(payload, "error")
	if err != nil {
		return "", fmt.Errorf("drift resolution rejection error: %w", err)
	}
	if errorType != "DriftResolutionRejected" {
		return "", fmt.Errorf("submission error = %q, want DriftResolutionRejected", errorType)
	}

	dataRaw, ok := payload["data"]
	if !ok {
		return "", fmt.Errorf("drift resolution rejection has no data")
	}
	data, err := decodeJSONObject(dataRaw)
	if err != nil {
		return "", fmt.Errorf("decode drift resolution rejection data: %w", err)
	}
	for name := range data {
		switch name {
		case "Code", "Reason", "ResourceID", "CommandId":
		default:
			return "", fmt.Errorf("drift resolution rejection data has unexpected field %q", name)
		}
	}
	code, err := requiredJSONString(data, "Code")
	if err != nil {
		return "", fmt.Errorf("drift resolution rejection code: %w", err)
	}
	if code != "stale-review" {
		return "", fmt.Errorf("drift resolution rejection code = %q, want stale-review", code)
	}
	reason, err := requiredJSONString(data, "Reason")
	if err != nil {
		return "", fmt.Errorf("drift resolution rejection reason: %w", err)
	}
	if reason == "" {
		return "", fmt.Errorf("drift resolution rejection has no reason")
	}
	if resourceID, ok := data["ResourceID"]; ok {
		if _, err := jsonString(resourceID); err != nil {
			return "", fmt.Errorf("drift resolution rejection ResourceID: %w", err)
		}
	}
	if commandIDRaw, ok := data["CommandId"]; ok {
		commandID, err := jsonString(commandIDRaw)
		if err != nil {
			return "", fmt.Errorf("drift resolution rejection CommandId: %w", err)
		}
		if commandID != "" {
			return "", fmt.Errorf("drift resolution rejection admitted command %q", commandID)
		}
	}
	return reason, nil
}

func decodeJSONObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("got %v, want JSON object", opening)
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("got object key %T, want string", key)
		}
		if _, exists := fields[name]; exists {
			return nil, fmt.Errorf("duplicate field %q", name)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("unexpected JSON after object")
		}
		return nil, err
	}
	return fields, nil
}

func requiredJSONString(fields map[string]json.RawMessage, name string) (string, error) {
	raw, ok := fields[name]
	if !ok {
		return "", fmt.Errorf("missing %s", name)
	}
	return jsonString(raw)
}

func jsonString(raw json.RawMessage) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("got %T, want string", value)
	}
	return text, nil
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
