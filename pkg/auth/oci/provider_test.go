// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

func TestTokenURL(t *testing.T) {
	got := tokenURL("us-chicago-1", "ocid1.cluster.oc1.us-chicago-1.aaaaaaaa")
	want := "https://containerengine.us-chicago-1.oraclecloud.com/cluster_request/ocid1.cluster.oc1.us-chicago-1.aaaaaaaa"
	if got != want {
		t.Errorf("tokenURL() = %q, want %q", got, want)
	}
}

func TestTokenURL_DifferentRegion(t *testing.T) {
	got := tokenURL("eu-frankfurt-1", "ocid1.cluster.oc1.eu-frankfurt-1.test")
	if !strings.Contains(got, "containerengine.eu-frankfurt-1.oraclecloud.com") {
		t.Errorf("tokenURL() should contain region, got %q", got)
	}
}

func TestBuildPresignedToken_Decodable(t *testing.T) {
	baseURL := "https://containerengine.us-chicago-1.oraclecloud.com/cluster_request/test-cluster"
	auth := `Signature algorithm="rsa-sha256",headers="date (request-target) host",keyId="tenancy/user/fingerprint",signature="abc123=="`
	date := "Mon, 21 Apr 2026 12:00:00 GMT"

	token := buildPresignedToken(baseURL, auth, date)

	decoded, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("token should be valid base64: %v", err)
	}

	decodedStr := string(decoded)
	if !strings.HasPrefix(decodedStr, baseURL+"?") {
		t.Errorf("decoded token should start with base URL, got %q", decodedStr)
	}
}

func TestBuildPresignedToken_ContainsAuthAndDate(t *testing.T) {
	baseURL := "https://containerengine.us-chicago-1.oraclecloud.com/cluster_request/test-cluster"
	auth := `Signature algorithm="rsa-sha256"`
	date := "Mon, 21 Apr 2026 12:00:00 GMT"

	token := buildPresignedToken(baseURL, auth, date)

	decoded, _ := base64.StdEncoding.DecodeString(token)
	parsed, err := url.Parse(string(decoded))
	if err != nil {
		t.Fatalf("decoded token should be a valid URL: %v", err)
	}

	if parsed.Query().Get("authorization") == "" {
		t.Error("decoded token URL should have 'authorization' query param")
	}
	if parsed.Query().Get("date") == "" {
		t.Error("decoded token URL should have 'date' query param")
	}
}

func TestBuildPresignedToken_AuthorizationPreserved(t *testing.T) {
	baseURL := "https://containerengine.us-chicago-1.oraclecloud.com/cluster_request/test"
	auth := `Signature algorithm="rsa-sha256",headers="date (request-target) host",keyId="tenancy/user/fp",signature="sig==",version="1"`
	date := "Mon, 21 Apr 2026 12:00:00 GMT"

	token := buildPresignedToken(baseURL, auth, date)

	decoded, _ := base64.StdEncoding.DecodeString(token)
	parsed, _ := url.Parse(string(decoded))

	gotAuth := parsed.Query().Get("authorization")
	if gotAuth != auth {
		t.Errorf("authorization should round-trip through URL encoding\ngot:  %q\nwant: %q", gotAuth, auth)
	}

	gotDate := parsed.Query().Get("date")
	if gotDate != date {
		t.Errorf("date should round-trip through URL encoding\ngot:  %q\nwant: %q", gotDate, date)
	}
}
