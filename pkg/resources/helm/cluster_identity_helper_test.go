//go:build unit || integration

package helm

import (
	"context"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"testing"
)

func resolveTestFlightScope(ctx context.Context, cfg *config.Config) (string, error) {
	client, err := transport.NewClient(ctx, cfg)
	if err != nil {
		return "", err
	}
	return resolveFlightScope(ctx, client)
}

func testReleaseForConfig(t *testing.T, cfg *config.Config) *Release {
	t.Helper()
	client, err := transport.NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &Release{Config: cfg, Client: client}
}
