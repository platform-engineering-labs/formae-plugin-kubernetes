#!/bin/bash
# © 2025 Platform Engineering Labs Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Credential setup hook for conformance tests.
#
# The K8S plugin authenticates via the local kubeconfig (KubeconfigAuth), so no
# cloud credentials need to be provisioned for local or kind/k3s/OrbStack CI
# runs — this is a no-op. Cloud-auth conformance matrices (EKS/GKE/AKS/OVH/OCI)
# can replace or extend this script to mint the provider credentials they need
# before the test run.
set -euo pipefail

echo "setup-credentials.sh: no credentials required (kubeconfig auth)"
