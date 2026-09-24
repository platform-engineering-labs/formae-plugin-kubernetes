//go:build unit

package config

import (
	"context"
	"errors"
	"k8s.io/client-go/rest"
	"testing"
	"time"
)

type lookupValue struct{}
type contextDescriber struct{ t *testing.T }

func (d contextDescriber) ConfigureTransport(*rest.Config) error { return nil }
func (d contextDescriber) DescribeCluster(ctx context.Context) (string, []byte, error) {
	if ctx.Value(lookupValue{}) != "live" {
		d.t.Error("legacy metadata lookup lost caller values")
	}
	if _, ok := ctx.Deadline(); !ok {
		d.t.Error("legacy metadata lookup lost caller deadline")
	}
	return "", nil, ctx.Err()
}
func TestLegacyConnectionLookupReceivesLiveContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), lookupValue{}, "live"), time.Second)
	cancel()
	cfg := &Config{authType: "AKS"}
	_, _, err := cfg.connectionDetails(ctx, contextDescriber{t}, &CloudAuthConfig{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("legacy metadata cancellation lost: %v", err)
	}
}
