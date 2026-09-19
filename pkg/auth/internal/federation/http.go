// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

// Package federation constrains the HTTP clients used by explicit cloud exchanges.
package federation

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

// Client permits only the exact canonical URLs supplied by a provider. Redirects
// are rejected before a body or Authorization header can be replayed.
func Client(base http.RoundTripper, urls ...string) *http.Client {
	if base == nil {
		base = http.DefaultTransport
	}
	allowed := make(map[string]bool, len(urls))
	for _, u := range urls {
		allowed[u] = true
	}
	return &http.Client{Transport: &transport{base: base, allowed: allowed}, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("credential exchange redirect rejected") }}
}

type transport struct {
	base    http.RoundTripper
	allowed map[string]bool
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL == nil || r.URL.Scheme != "https" || r.URL.User != nil || r.URL.Fragment != "" || r.Response != nil || (r.Host != "" && r.Host != r.URL.Host) || !t.allowed[strings.TrimSuffix(r.URL.String(), "?")] {
		return nil, errors.New("credential exchange endpoint rejected")
	}
	return t.base.RoundTrip(r)
}
