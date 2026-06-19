#!/bin/bash
# © 2025 Platform Engineering Labs Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Clean Environment Hook
# ======================
# This script is called before AND after conformance tests to clean up
# test resources in the Kubernetes cluster.
#
# Purpose:
# - Before tests: Remove orphaned resources from previous failed runs
# - After tests: Clean up resources created during the test run
#
# The script should be idempotent - safe to run multiple times.

set -euo pipefail

# Prefix used for test resources
TEST_PREFIX="${TEST_PREFIX:-formae-test-}"

echo "clean-environment.sh: Cleaning K8S namespaces with prefix '${TEST_PREFIX}'"

# Remove the bootstrapped test CRD (and any leftover Widget instances). Runs
# regardless of namespace state; deleting the CRD cascades to its instances.
kubectl delete crd widgets.example.com --ignore-not-found --wait=false 2>/dev/null || true

# Get namespaces matching the prefix
NAMESPACES=$(kubectl get namespaces -o jsonpath="{.items[*].metadata.name}" 2>/dev/null | tr ' ' '\n' | grep "^${TEST_PREFIX}" || true)

if [[ -z "$NAMESPACES" ]]; then
    echo "No test namespaces found with prefix '${TEST_PREFIX}'"
    exit 0
fi

while IFS= read -r ns; do
    echo "  Deleting namespace: $ns"
    kubectl delete namespace "$ns" --wait=false 2>/dev/null || true
done <<< "$NAMESPACES"

echo "clean-environment.sh: Cleanup complete"
