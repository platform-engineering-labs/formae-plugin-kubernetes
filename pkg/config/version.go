// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"k8s.io/client-go/discovery"
)

// Supported Kubernetes version range. Updated in lockstep with client-go bumps.
const (
	MinSupportedK8sVersion = "1.31"
	MaxSupportedK8sVersion = "1.34"
	ClientGoVersion        = "0.34.0"
)

// EnvK8sVersion is the env var that overrides the K8s version used for
// field-gate preflight checks. Useful in CI matrix runs.
const EnvK8sVersion = "FORMAE_K8S_VERSION"

// ResolveK8sVersion returns the K8s minor version string ("1.32") used for
// @K8sVersion gate checks. Resolution priority:
//
//  1. cfg.KubernetesVersion (target config override)
//  2. FORMAE_K8S_VERSION environment variable
//  3. Live cluster discovery via discovery.ServerVersion()
//
// The returned string is always normalized to MAJOR.MINOR (no patch).
func ResolveK8sVersion(ctx context.Context, cfg *Config, disc discovery.DiscoveryInterface) (string, error) {
	if cfg != nil && cfg.KubernetesVersion != "" {
		return NormalizeK8sVersion(cfg.KubernetesVersion)
	}
	if v := os.Getenv(EnvK8sVersion); v != "" {
		return NormalizeK8sVersion(v)
	}
	if disc == nil {
		return "", fmt.Errorf("cannot resolve K8s version: no override set and no discovery client provided")
	}
	info, err := disc.ServerVersion()
	if err != nil {
		return "", fmt.Errorf("cannot resolve K8s version via discovery (set %s or kubernetesVersion in target config): %w", EnvK8sVersion, err)
	}
	return NormalizeK8sVersion(info.Major + "." + info.Minor)
}

// NormalizeK8sVersion strips a leading "v", trims a "+" suffix (used by GKE
// and other distros), and reduces "1.32.5" or "1.32+" to "1.32".
func NormalizeK8sVersion(v string) (string, error) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimSuffix(v, "+")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid K8s version %q: expected MAJOR.MINOR", v)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return "", fmt.Errorf("invalid K8s major %q: %w", parts[0], err)
	}
	minor, err := strconv.Atoi(strings.TrimSuffix(parts[1], "+"))
	if err != nil {
		return "", fmt.Errorf("invalid K8s minor %q: %w", parts[1], err)
	}
	return fmt.Sprintf("%d.%d", major, minor), nil
}

// CompareK8sVersions returns -1, 0, or +1 for a vs b on MAJOR.MINOR semantics.
// Both inputs must already be normalized (use NormalizeK8sVersion).
func CompareK8sVersions(a, b string) int {
	am, an := splitMinor(a)
	bm, bn := splitMinor(b)
	switch {
	case am < bm:
		return -1
	case am > bm:
		return 1
	case an < bn:
		return -1
	case an > bn:
		return 1
	}
	return 0
}

func splitMinor(v string) (int, int) {
	parts := strings.SplitN(v, ".", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	return major, minor
}
