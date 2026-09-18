// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/repo"
)

// NewCallbackContext starts the broker Helm allowance at the plugin boundary,
// before version discovery. Release's child allowance intersects this deadline
// instead of giving chart preparation a second full callback budget.
func NewCallbackContext(ctx context.Context, cfg *config.Config, properties []byte) (context.Context, context.CancelFunc, error) {
	if !cfg.UsesOidc() {
		return ctx, func() {}, nil
	}
	info, ok := plugin.OidcOperationMetadata(ctx)
	if !ok {
		return nil, nil, fmt.Errorf("helm OIDC requires trusted operation metadata")
	}

	bounded, cancel, err := brokerCallbackContext(ctx, info)
	if err != nil {
		return nil, nil, err
	}
	if properties != nil {
		props, err := decodeProperties(properties)
		if err == nil {
			err = validateActionAllowance(info, props.timeout())
		}
		if err != nil {
			cancel()
			return nil, nil, err
		}
	}
	return bounded, cancel, nil
}

func brokerCallbackContext(ctx context.Context, info plugin.OidcOperationInfo) (context.Context, context.CancelFunc, error) {
	if _, err := bridgeWindow(info); err != nil {
		return nil, nil, err
	}
	if info.CallTimeout <= 10*time.Second {
		return nil, nil, fmt.Errorf("helm OIDC call timeout must exceed the 10s scheduling reserve")
	}
	bounded, cancel := context.WithTimeout(ctx, info.CallTimeout-10*time.Second)
	return bounded, cancel, nil
}

type boundedGetter struct {
	ctx    context.Context
	getter getter.Getter
}

func (g boundedGetter) Get(url string, options ...getter.Option) (*bytes.Buffer, error) {
	if err := g.ctx.Err(); err != nil {
		return nil, err
	}
	deadline, ok := g.ctx.Deadline()
	if !ok {
		return nil, fmt.Errorf("chart retrieval requires a callback deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil, context.DeadlineExceeded
	}
	options = append(options, getter.WithTimeout(remaining))
	return g.getter.Get(url, options...)
}

// Helm LocateChart does not expose getter timeouts. This equivalent downloader
// path bounds each index/archive request by the same callback deadline. OCI's
// registry requests additionally bind that deadline directly to every request.
func loadChartWithin(ctx context.Context, _ *action.Configuration, props *releaseProperties) (*chart.Chart, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateChartRef(props.Chart, props.RepoURL); err != nil {
		return nil, err
	}
	if _, err := os.Stat(props.Chart); err == nil {
		chrt, err := loader.Load(props.Chart)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return chrt, err
	}
	if filepath.IsAbs(props.Chart) || len(props.Chart) > 0 && props.Chart[0] == '.' {
		return nil, fmt.Errorf("chart path does not exist")
	}
	settings := cli.New()
	providers := getter.Providers{
		{Schemes: []string{"http", "https"}, New: func(options ...getter.Option) (getter.Getter, error) {
			g, err := getter.NewHTTPGetter(options...)
			return boundedGetter{ctx, g}, err
		}},
		{Schemes: []string{"oci"}, New: func(options ...getter.Option) (getter.Getter, error) {
			g, err := getter.NewOCIGetter(options...)
			return boundedGetter{ctx, g}, err
		}},
	}
	ref, repoURL := resolveChartRef(props.Chart, props.RepoURL)
	authorizer, err := newRegistryAuthorizer(ctx, settings)
	if err != nil {
		return nil, err
	}
	client, err := registry.NewClient(registry.ClientOptEnableCache(true), registry.ClientOptAuthorizer(authorizer), registry.ClientOptCredentialsFile(settings.RegistryConfig))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if repoURL != "" {
		ref, err = repo.FindChartInAuthAndTLSAndPassRepoURL(repoURL, "", "", ref, props.Version, "", "", "", false, false, providers)
		if err != nil {
			return nil, err
		}
	}
	dl := downloader.ChartDownloader{Out: os.Stdout, Getters: providers, RepositoryConfig: settings.RepositoryConfig, RepositoryCache: settings.RepositoryCache, RegistryClient: client}
	if registry.IsOCI(ref) {
		dl.Options = append(dl.Options, getter.WithRegistryClient(client))
	}
	if err := os.MkdirAll(settings.RepositoryCache, 0755); err != nil {
		return nil, err
	}
	path, _, err := dl.DownloadTo(ref, props.Version, settings.RepositoryCache)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	chrt, err := loader.Load(path)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return chrt, err
}

// This direct configuration belongs to the active callback, never to the
// worker. Waiting for its storage record also services queued worker refreshes.
func awaitRecordedWithService(ctx context.Context, conf *action.Configuration, name string, target int, done <-chan error, b *authBridge, source auth.TokenSource) error {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			return err
		case <-b.requests:
			if err := b.Service(ctx, source); err != nil {
				return err
			}
		case <-tick.C:
			rel, err := lastRelease(conf, name)
			if err != nil {
				return err
			}
			if rel != nil && rel.Version >= target {
				return nil
			}
		}
	}
}
