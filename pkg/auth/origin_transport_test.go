//go:build unit

package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func originURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
func sourceWithCount(calls *atomic.Int64) TokenSource {
	return tokenFunc(func(context.Context) (string, time.Time, error) {
		return fmt.Sprintf("token-%d", calls.Add(1)), time.Now().Add(time.Hour), nil
	})
}

func TestOriginTransportRejectsOtherOriginsBeforeMint(t *testing.T) {
	var hits, mints atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer server.Close()
	configured := originURL(t, server.URL)
	tr := NewOriginTokenTransport(server.Client().Transport, sourceWithCount(&mints), configured)
	for _, raw := range []string{"https://example.invalid/", strings.Replace(server.URL, "https:", "http:", 1), "https://user:SECRET@" + configured.Host + "/"} {
		req, _ := http.NewRequest("GET", raw, nil)
		if _, err := tr.RoundTrip(req); err == nil {
			t.Errorf("accepted forbidden origin %s", raw)
		}
	}
	if hits.Load() != 0 || mints.Load() != 0 {
		t.Fatal("forbidden request reached credential source or network")
	}
	// Copy origin at construction: callers cannot widen authorization afterward.
	configured.Host = "other.invalid"
	req, _ := http.NewRequest("GET", server.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if hits.Load() != 1 || mints.Load() != 1 {
		t.Fatal("configured origin not usable")
	}
}

func TestOriginTransportRejectsSameAndCrossOriginRedirects(t *testing.T) {
	for _, cross := range []bool{false, true} {
		t.Run(fmt.Sprint(cross), func(t *testing.T) {
			var mints, hits, otherHits atomic.Int64
			other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { otherHits.Add(1) }))
			defer other.Close()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.Header.Get("Authorization") != "Bearer token-1" {
					t.Error("missing initial bearer")
				}
				location := "/destination"
				if cross {
					location = other.URL
				}
				http.Redirect(w, r, location, http.StatusFound)
			}))
			defer server.Close()
			client := server.Client()
			client.Transport = NewOriginTokenTransport(client.Transport, sourceWithCount(&mints), originURL(t, server.URL))
			if resp, err := client.Get(server.URL); err == nil {
				resp.Body.Close()
				t.Fatal("redirect should be rejected")
			}
			if hits.Load() != 1 || otherHits.Load() != 0 || mints.Load() != 1 {
				t.Fatalf("redirect minted or escaped: initial=%d other=%d mint=%d", hits.Load(), otherHits.Load(), mints.Load())
			}
		})
	}
}

func TestOriginTransportReplayRulesAndRequestOwnership(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		replay bool
		want   int
	}{
		{"replay 401 once", 401, true, 2}, {"nonreplayable 401", 401, false, 1}, {"never retry 403", 403, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits, mints atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := hits.Add(1)
				body, _ := io.ReadAll(r.Body)
				if string(body) != "payload" {
					t.Errorf("attempt %d body=%q", n, body)
				}
				if r.Header.Get("Authorization") != fmt.Sprintf("Bearer token-%d", n) {
					t.Errorf("wrong token on attempt %d", n)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, "response")
			}))
			defer server.Close()
			source := NewCachedTokenSource(sourceWithCount(&mints))
			tr := NewOriginTokenTransport(server.Client().Transport, source, originURL(t, server.URL))
			req, _ := http.NewRequest("POST", server.URL, strings.NewReader("payload"))
			if !tc.replay {
				req.GetBody = nil
			}
			req.Header.Set("Authorization", "caller-value")
			req.Header.Set("X-Original", "present")
			bodyPtr := req.Body
			resp, err := tr.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || string(body) != "response" {
				t.Fatalf("caller response prematurely closed: %q %v", body, err)
			}
			if resp.StatusCode != tc.status || hits.Load() != int64(tc.want) || mints.Load() != int64(tc.want) {
				t.Fatalf("wrong retries: hits=%d mints=%d", hits.Load(), mints.Load())
			}
			if req.Header.Get("Authorization") != "caller-value" || req.Body != bodyPtr {
				t.Fatal("mutated caller request")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type closeSpy struct {
	io.Reader
	closed bool
}

func (b *closeSpy) Close() error { b.closed = true; return nil }

func TestOriginTransportClosesDiscardedResponsesAndRedactsFailures(t *testing.T) {
	denied := errors.New("denied")
	var mints atomic.Int64
	first := &closeSpy{Reader: strings.NewReader("unauthorized")}
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if mints.Load() == 1 {
			return &http.Response{StatusCode: 401, Body: first}, nil
		}
		return nil, fmt.Errorf("https://signed.invalid/?sig=SECRET: %w", denied)
	})
	tr := NewOriginTokenTransport(base, NewCachedTokenSource(sourceWithCount(&mints)), originURL(t, "https://cluster.invalid"))
	req, _ := http.NewRequest("GET", "https://cluster.invalid", nil)
	_, err := tr.RoundTrip(req)
	if !first.closed {
		t.Fatal("discarded 401 body leaked")
	}
	if err == nil || strings.Contains(err.Error(), "SECRET") || !errors.Is(err, denied) {
		t.Fatalf("unsafe/untyped error %v", err)
	}
	source := tokenFunc(func(context.Context) (string, time.Time, error) {
		return "", time.Time{}, fmt.Errorf("assertion=SECRET: %w", denied)
	})
	tr = NewOriginTokenTransport(base, source, originURL(t, "https://cluster.invalid"))
	_, err = tr.RoundTrip(req)
	if err == nil || strings.Contains(err.Error(), "SECRET") || !errors.Is(err, denied) {
		t.Fatalf("unsafe source error %v", err)
	}
}

func TestOriginTransportClosesBodiesWhenReplayMintFails(t *testing.T) {
	first := &closeSpy{Reader: strings.NewReader("401 response")}
	replayBody := &closeSpy{Reader: strings.NewReader("payload")}
	calls := 0
	denied := errors.New("broker denied")
	source := NewCachedTokenSource(tokenFunc(func(context.Context) (string, time.Time, error) {
		calls++
		if calls == 2 {
			return "", time.Time{}, fmt.Errorf("SECRET: %w", denied)
		}
		return "initial", time.Now().Add(time.Hour), nil
	}))
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.Body.Close()
		return &http.Response{StatusCode: 401, Body: first}, nil
	})
	req, _ := http.NewRequest("POST", "https://cluster.invalid", strings.NewReader("payload"))
	req.GetBody = func() (io.ReadCloser, error) { return replayBody, nil }
	_, err := NewOriginTokenTransport(base, source, originURL(t, "https://cluster.invalid")).RoundTrip(req)
	if !first.closed || !replayBody.closed {
		t.Fatal("retry failure leaked a body")
	}
	if !errors.Is(err, denied) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("unsafe replay error %v", err)
	}
}

func TestOriginTransportKeeps401WhenBodyCannotBeReopened(t *testing.T) {
	first := &closeSpy{Reader: strings.NewReader("original unauthorized response")}
	var mints atomic.Int64
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.Body.Close()
		return &http.Response{StatusCode: 401, Body: first}, nil
	})
	req, _ := http.NewRequest("POST", "https://cluster.invalid", strings.NewReader("payload"))
	req.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("cannot reopen") }
	resp, err := NewOriginTokenTransport(base, NewCachedTokenSource(sourceWithCount(&mints)), originURL(t, "https://cluster.invalid")).RoundTrip(req)
	if err != nil || resp.StatusCode != 401 || first.closed || mints.Load() != 1 {
		t.Fatalf("lost original response: %v", err)
	}
	resp.Body.Close()
}
