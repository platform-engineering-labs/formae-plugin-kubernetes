#!/bin/bash
# © 2025 Platform Engineering Labs Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Drift Detection Test
# ====================
# Verifies that formae detects and reconciles external changes (drift)
# introduced via kubectl.
#
# Steps:
#   1. Apply the drift-demo forma (Namespace + ConfigMap + Deployment)
#   2. Verify initial state
#   3. Introduce drift via kubectl (scale, patch, label)
#   4. Force reconcile to detect and fix drift
#   5. Verify reconciliation restored the original state
#   6. Cleanup
#
# Usage:
#   ./scripts/run-drift-test.sh
#
# Environment variables:
#   FORMAE_BINARY - Path to formae binary (required)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
FORMA_FILE="${PROJECT_ROOT}/examples/drift-demo.pkl"

# Require formae binary
if [[ -z "${FORMAE_BINARY:-}" ]] || [[ ! -x "${FORMAE_BINARY}" ]]; then
    echo "Error: FORMAE_BINARY must be set to a valid formae binary"
    exit 1
fi

# Namespace used by the drift demo
NS="drift-demo"

cleanup() {
    echo ""
    echo "Cleaning up..."
    "${FORMAE_BINARY}" destroy --yes --watch "${FORMA_FILE}" 2>&1 || true
    kubectl delete namespace "${NS}" --wait=false 2>/dev/null || true
    "${FORMAE_BINARY}" agent stop 2>/dev/null || true
}
trap cleanup EXIT

assert_eq() {
    local label="$1" expected="$2" actual="$3"
    if [[ "${actual}" == "${expected}" ]]; then
        echo "  ✓ ${label}: ${actual}"
    else
        echo "  ✗ ${label}: expected '${expected}', got '${actual}'"
        exit 1
    fi
}

# Resolve PKL dependencies (schema first since examples imports it)
if [[ -f "${PROJECT_ROOT}/schema/pkl/PklProject" ]]; then
    echo "Resolving schema/pkl dependencies..."
    pkl project resolve "${PROJECT_ROOT}/schema/pkl"
fi
if [[ -f "${PROJECT_ROOT}/examples/PklProject" ]]; then
    echo "Resolving examples/ PKL dependencies..."
    pkl project resolve "${PROJECT_ROOT}/examples"
fi

echo "========================================"
echo "Drift Detection Test"
echo "========================================"

# --- Step 0: Install drift-test config and start agent ---
echo ""
echo "[0/6] Starting formae agent (sync interval: 5s)..."
FORMAE_CONFIG_DIR="${HOME}/.config/formae"
mkdir -p "${FORMAE_CONFIG_DIR}"
cp "${SCRIPT_DIR}/ci/drift-test-config.pkl" "${FORMAE_CONFIG_DIR}/formae.conf.pkl"
"${FORMAE_BINARY}" agent stop 2>/dev/null || true
"${FORMAE_BINARY}" agent start &
sleep 2

# --- Step 1: Initial apply ---
echo ""
echo "[1/6] Applying drift-demo forma..."
"${FORMAE_BINARY}" apply --yes --mode reconcile --watch "${FORMA_FILE}" 2>&1

# --- Step 2: Verify initial state ---
echo ""
echo "[2/6] Verifying initial state..."
assert_eq "replicas" "2" "$(kubectl -n "${NS}" get deployment drift-demo-app -o jsonpath='{.spec.replicas}')"
assert_eq "log_level" "info" "$(kubectl -n "${NS}" get configmap drift-demo-config -o jsonpath='{.data.log_level}')"
assert_eq "max_connections" "100" "$(kubectl -n "${NS}" get configmap drift-demo-config -o jsonpath='{.data.max_connections}')"

# --- Step 3: Introduce drift ---
echo ""
echo "[3/6] Introducing drift via kubectl..."
kubectl -n "${NS}" scale deployment/drift-demo-app --replicas=5
kubectl -n "${NS}" patch configmap/drift-demo-config --type merge -p '{"data":{"log_level":"debug"}}'
kubectl -n "${NS}" label deployment/drift-demo-app env=production

echo "  Drift introduced:"
echo "    - Deployment replicas: 2 → 5"
echo "    - ConfigMap log_level: info → debug"
echo "    - Deployment label added: env=production"

# Wait for formae sync to detect drift (agent config uses 5s sync interval).
sleep 15

# Verify drift exists
echo ""
echo "[4/6] Verifying drift exists..."
assert_eq "replicas (drifted)" "5" "$(kubectl -n "${NS}" get deployment drift-demo-app -o jsonpath='{.spec.replicas}')"
assert_eq "log_level (drifted)" "debug" "$(kubectl -n "${NS}" get configmap drift-demo-config -o jsonpath='{.data.log_level}')"
assert_eq "env label (drifted)" "production" "$(kubectl -n "${NS}" get deployment drift-demo-app -o jsonpath='{.metadata.labels.env}')"

# --- Step 5: Force reconcile ---
echo ""
echo "[5/6] Force reconciling to fix drift..."
"${FORMAE_BINARY}" apply --yes --mode reconcile --force --watch "${FORMA_FILE}" 2>&1

# Brief pause to let K8S propagate changes
sleep 2

# --- Step 6: Verify reconciliation ---
echo ""
echo "[6/6] Verifying drift was reconciled..."
assert_eq "replicas (restored)" "2" "$(kubectl -n "${NS}" get deployment drift-demo-app -o jsonpath='{.spec.replicas}')"
assert_eq "log_level (restored)" "info" "$(kubectl -n "${NS}" get configmap drift-demo-config -o jsonpath='{.data.log_level}')"
assert_eq "max_connections (unchanged)" "100" "$(kubectl -n "${NS}" get configmap drift-demo-config -o jsonpath='{.data.max_connections}')"

# env label should be removed by SSA force apply
env_label="$(kubectl -n "${NS}" get deployment drift-demo-app -o jsonpath='{.metadata.labels.env}' 2>/dev/null || echo "")"
if [[ -z "${env_label}" ]]; then
    echo "  ✓ env label: removed (SSA reclaimed ownership)"
else
    echo "  ✗ env label: expected removed, got '${env_label}'"
    exit 1
fi

echo ""
echo "========================================"
echo "Drift Detection Test: PASSED"
echo "========================================"
