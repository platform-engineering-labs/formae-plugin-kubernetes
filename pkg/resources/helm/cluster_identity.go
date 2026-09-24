// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"context"
	"fmt"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Flight identity must be authenticated cluster data, not a selector, DNS name
// or certificate heuristic. Do not cache: a replaced cluster can reuse an URL.
// Mutating/recovering Helm calls require get namespaces/kube-system permission.
func resolveFlightScope(ctx context.Context, client *transport.Client) (string, error) {
	live, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	namespace, err := client.CoreV1().Namespaces().Get(live, "kube-system", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("identify Helm cluster: get namespace kube-system (requires get namespaces resourceNames=[kube-system]): %w", err)
	}
	if namespace.UID == "" {
		return "", fmt.Errorf("identify Helm cluster: namespace kube-system has no UID")
	}
	return "UID|" + string(namespace.UID), nil
}
