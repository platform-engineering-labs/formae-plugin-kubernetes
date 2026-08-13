#!/bin/bash
# © 2026 Platform Engineering Labs Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Remove what a Helm interop run leaves behind when it does not get to finish.
#
# The suite tears down after itself, but t.Cleanup does not run when a run is
# killed — Ctrl-C, a CI timeout, a laptop lid. What survives is not inert: a
# chart's cluster-scoped objects carry fixed names, so the next run of that same
# chart cannot install at all ("ClusterRole X exists and cannot be imported"),
# and an orphaned admission webhook keeps enforcing against everything else on
# the cluster.
#
# Scope is every namespace matching ci-*, which is the naming the suite uses,
# plus the cluster-scoped objects whose Helm ownership annotation points at one
# of them. Nothing without that annotation is touched: an unannotated
# ClusterRole may well be a real installation, and it is not this script's.
#
# Usage:
#   ./scripts/clean-helm-interop.sh          # show what would go
#   ./scripts/clean-helm-interop.sh --yes    # actually remove it

set -euo pipefail

APPLY=false
[[ "${1:-}" == "--yes" ]] && APPLY=true

FORMAE="${FORMAE_BINARY:-$(command -v formae || true)}"
PROFILE="${INTEROP_PROFILE:-helm-interop-ci}"

CLUSTER_KINDS="clusterrole,clusterrolebinding,customresourcedefinition,apiservice,validatingwebhookconfiguration,mutatingwebhookconfiguration"

namespaces=$(kubectl get namespace -o name 2>/dev/null | grep -oE 'ci-[a-z0-9-]+' || true)

echo "=== namespaces ==="
if [[ -z "${namespaces}" ]]; then
    echo "  (none)"
else
    echo "${namespaces}" | sed 's/^/  /'
fi

echo "=== cluster-scoped objects owned by them ==="
owned=""
while IFS= read -r line; do
    [[ -z "${line}" ]] && continue
    kind="${line%%|*}"
    rest="${line#*|}"
    name="${rest%%|*}"
    owner="${rest##*|}"
    case "${owner}" in
        ci-*) owned+="${kind}/${name}"$'\n'; echo "  ${kind}/${name}  (release namespace ${owner})" ;;
    esac
done < <(kubectl get ${CLUSTER_KINDS} -o \
    'jsonpath={range .items[*]}{.kind}|{.metadata.name}|{.metadata.annotations.meta\.helm\.sh/release-namespace}{"\n"}{end}' 2>/dev/null || true)
[[ -z "${owned}" ]] && echo "  (none)"

if [[ "${APPLY}" != true ]]; then
    echo ""
    echo "Nothing removed. Re-run with --yes to apply."
    exit 0
fi

echo ""
# Stacks first: destroying through formae keeps its datastore consistent with
# the cluster. Best-effort — an agent that is not running is not a reason to
# stop cleaning up Kubernetes.
if [[ -n "${FORMAE}" ]] && [[ -x "${FORMAE}" ]]; then
    for ns in ${namespaces}; do
        "${FORMAE}" --profile "${PROFILE}" destroy --yes --query "stack:${ns}" >/dev/null 2>&1 || true
    done
fi

while IFS= read -r obj; do
    [[ -z "${obj}" ]] && continue
    kind="${obj%%/*}"
    name="${obj#*/}"
    kubectl delete "$(echo "${kind}" | tr '[:upper:]' '[:lower:]')" "${name}" --ignore-not-found >/dev/null 2>&1 \
        && echo "removed ${obj}"
done <<< "${owned}"

for ns in ${namespaces}; do
    # --wait=false: a namespace with a stuck finalizer should not hold up the
    # rest of the sweep, and the next run generates a fresh name anyway.
    kubectl delete namespace "${ns}" --wait=false >/dev/null 2>&1 && echo "removed namespace ${ns}"
done

echo "Done."
