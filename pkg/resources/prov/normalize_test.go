// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"encoding/json"
	"testing"
)

func TestNormalizeMetadata_FlatLabelsUnchanged(t *testing.T) {
	in := []byte(`{"metadata":{"labels":{"app":"nginx","managed-by":"formae"}}}`)
	out, err := NormalizeMetadata(in)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !jsonEqual(t, in, out) {
		t.Fatalf("flat input should be a no-op\n in:  %s\n out: %s", in, out)
	}
}

func TestNormalizeMetadata_FlattensDotExpandedLabels(t *testing.T) {
	// Mirrors the shape Formae's state-DB produces for a Helm-rendered
	// resource: `app.kubernetes.io/name` was split on `.` and stored as a
	// nested object.
	in := []byte(`{"metadata":{"labels":{
		"app":           {"kubernetes":{"io/name":"nginx","io/instance":"my-nginx"}},
		"helm":          {"sh/chart":"nginx-1.0.0"},
		"managed-by":    "formae"
	}}}`)
	want := map[string]string{
		"app.kubernetes.io/name":     "nginx",
		"app.kubernetes.io/instance": "my-nginx",
		"helm.sh/chart":              "nginx-1.0.0",
		"managed-by":                 "formae",
	}

	out, err := NormalizeMetadata(in)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	var got struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal normalized json into map[string]string: %v\nout: %s", err, out)
	}
	if !mapsEqual(got.Metadata.Labels, want) {
		t.Fatalf("labels mismatch\n got:  %#v\n want: %#v", got.Metadata.Labels, want)
	}
}

func TestNormalizeMetadata_FlattensAnnotationsAndMatchLabels(t *testing.T) {
	in := []byte(`{
		"metadata":{"annotations":{"prometheus":{"io/scrape":"true"}}},
		"spec":{"selector":{"matchLabels":{"app":{"kubernetes":{"io/name":"nginx"}}}}}
	}`)

	out, err := NormalizeMetadata(in)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	var got struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			Selector struct {
				MatchLabels map[string]string `json:"matchLabels"`
			} `json:"selector"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal normalized: %v\nout: %s", err, out)
	}
	if got.Metadata.Annotations["prometheus.io/scrape"] != "true" {
		t.Fatalf("annotation not flattened: %#v", got.Metadata.Annotations)
	}
	if got.Spec.Selector.MatchLabels["app.kubernetes.io/name"] != "nginx" {
		t.Fatalf("matchLabels not flattened: %#v", got.Spec.Selector.MatchLabels)
	}
}

func TestNormalizeMetadata_PreservesLabelSelectorShape(t *testing.T) {
	// Deployment.spec.selector is a LabelSelector — must NOT be collapsed
	// into a flat map. Inner matchLabels should still be flattened.
	in := []byte(`{"spec":{"selector":{
		"matchLabels":{"app":{"kubernetes":{"io/name":"nginx"}}},
		"matchExpressions":[{"key":"tier","operator":"In","values":["frontend"]}]
	}}}`)

	out, err := NormalizeMetadata(in)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	var got struct {
		Spec struct {
			Selector struct {
				MatchLabels      map[string]string        `json:"matchLabels"`
				MatchExpressions []map[string]interface{} `json:"matchExpressions"`
			} `json:"selector"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("LabelSelector should survive normalization unscathed: %v\nout: %s", err, out)
	}
	if got.Spec.Selector.MatchLabels["app.kubernetes.io/name"] != "nginx" {
		t.Fatalf("inner matchLabels not flattened: %#v", got.Spec.Selector.MatchLabels)
	}
	if len(got.Spec.Selector.MatchExpressions) != 1 {
		t.Fatalf("matchExpressions lost: %#v", got.Spec.Selector.MatchExpressions)
	}
}

func TestNormalizeMetadata_FlatServiceSelector(t *testing.T) {
	// Service.spec.selector — flat string map at the same JSON key
	// "selector". Must be collapsed when dot-expanded.
	in := []byte(`{"spec":{"selector":{"app":{"kubernetes":{"io/name":"nginx"}}}}}`)
	out, err := NormalizeMetadata(in)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	var got struct {
		Spec struct {
			Selector map[string]string `json:"selector"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("Service selector should unmarshal into map[string]string after normalize: %v\nout: %s", err, out)
	}
	if got.Spec.Selector["app.kubernetes.io/name"] != "nginx" {
		t.Fatalf("Service selector not flattened: %#v", got.Spec.Selector)
	}
}

func TestNormalizeMetadata_EmptyInput(t *testing.T) {
	out, err := NormalizeMetadata(nil)
	if err != nil {
		t.Fatalf("nil input should not error: %v", err)
	}
	if out != nil {
		t.Fatalf("nil input should round-trip as nil, got %s", out)
	}
}

// jsonEqual compares two JSON documents semantically (ignoring key order).
func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var av, bv interface{}
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("parse a: %v", err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("parse b: %v", err)
	}
	am, _ := json.Marshal(av)
	bm, _ := json.Marshal(bv)
	return string(am) == string(bm)
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
