#!/bin/bash
# © 2026 Platform Engineering Labs Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Helm-side Drift Test
# ====================
# Deploy chart version A with formae, upgrade to version B with the Helm CLI,
# then show what formae makes of that.
#
# The point: a K8S::Helm::Release is not opaque to drift detection. `Read`
# returns the chart version from the release record, so a Helm-side version bump
# is an ordinary field diff on `version` — the same machinery as any other
# resource. A reconcile pulls it back to the pin in the forma.
#
# Steps:
#   1. formae apply       -> release at version A, revision 1
#   2. helm upgrade       -> release at version B, revision 2 (drift)
#   3. formae Read        -> reports version B, so the drift is visible
#   4. plain reconcile    -> REFUSED, because the stack changed out-of-band
#   5. reconcile --force  -> back to version A, revision 3
#
# Usage:
#   ./scripts/run-helm-drift-test.sh
#
# Requires: a reachable cluster, the plugin installed (`make install`) and a
# running agent, plus `helm` and `kubectl` on PATH.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DEMO_DIR="${PROJECT_ROOT}/examples/helm/drift-helm-upgrade"
FORMA_FILE="${DEMO_DIR}/kratos.pkl"

FORMAE="${FORMAE_BINARY:-$(command -v formae || true)}"
if [[ -z "${FORMAE}" ]] || [[ ! -x "${FORMAE}" ]]; then
    echo "Error: formae binary not found; set FORMAE_BINARY" >&2
    exit 1
fi

NS="formae-helm-drift"
RELEASE="kratos"
VERSION_A="0.62.1"
VERSION_B="0.63.0"
REPO_URL="https://k8s.ory.sh/helm/charts"

pass() { echo "  ✓ $1"; }
fail() { echo "  ✗ $1" >&2; exit 1; }

assert_eq() {
    local label="$1" expected="$2" actual="$3"
    [[ "${actual}" == "${expected}" ]] \
        && pass "${label}: ${actual}" \
        || fail "${label}: expected '${expected}', got '${actual}'"
}

# Chart version and revision straight from Helm's own release record, so the
# assertions do not depend on formae agreeing.
helm_field() {
    helm list -n "${NS}" -o json 2>/dev/null | python3 -c '
import json,sys
field = sys.argv[1]
try:
    rels = json.load(sys.stdin)
except Exception:
    sys.exit(0)
for r in rels:
    if r.get("name") == sys.argv[2]:
        v = str(r.get(field, ""))
        # "chart" comes back as "kratos-0.62.1"; we want just the version.
        print(v.rsplit("-", 1)[-1] if field == "chart" else v)
        break
' "$1" "${RELEASE}"
}
helm_version()  { helm_field chart; }
helm_revision() { helm_field revision; }

# The version formae currently believes, read back out of its own inventory.
formae_version() {
    "${FORMAE}" inventory resources --output-consumer machine 2>/dev/null \
        | python3 -c '
import json,sys
for r in json.load(sys.stdin)["Resources"]:
    if r["Type"] == "K8S::Helm::Release":
        print(r.get("Properties", {}).get("version", ""))
        break
'
}

# formae apply returns as soon as the agent accepts the command. Swallowing its
# output hides both CLI errors and agent-side failures, so capture the command id
# and poll it to a terminal state.
# Extracts the command id formae prints on submit.
command_id() { printf '%s' "$1" | grep -oE "id:[A-Za-z0-9]+" | head -1 | cut -d: -f2; }

# Polls one command to a terminal state. Both apply and destroy are accepted
# asynchronously, so anything that does not wait here races the next step.
wait_command() {
    local id="$1" quiet="${2:-}" state tries=0
    while :; do
        state="$("${FORMAE}" status command --query="id:${id}" --output-consumer machine 2>/dev/null \
            | python3 -c '
import json,sys
try: cs = json.load(sys.stdin).get("Commands", [])
except Exception: cs = []
print(cs[0].get("State","") if cs else "")
')"
        case "${state}" in
            Success) return 0 ;;
            Failed|Rejected|Canceled)
                [[ -n "${quiet}" ]] && return 1
                "${FORMAE}" status command --query="id:${id}" 2>&1 | sed 's/^/     /' >&2
                fail "command ${id} ended ${state}" ;;
        esac
        tries=$((tries + 1))
        if [[ ${tries} -gt 120 ]]; then
            [[ -n "${quiet}" ]] && return 1
            fail "command ${id} did not finish (last state '${state}')"
        fi
        sleep 3
    done
}

# formae apply returns as soon as the agent accepts the command. Swallowing its
# output hides both CLI errors and agent-side failures, so capture the command id
# and poll it to a terminal state.
# A bare `Rejected` with no ErrorMessage and no attempt count never reached the
# plugin — the agent turned the submit down up front. Besides drift (which
# --force covers) that happens when a background sync lands between the CLI's
# pre-flight read and its submit, so the stack version the CLI carried is already
# stale. That is an optimistic-concurrency conflict and the client's job to
# retry, so retry a few times before calling it a failure.
apply_and_wait() {
    local out id attempt
    for attempt in 1 2 3 4; do
        if ! out="$("${FORMAE}" apply --mode reconcile --yes "$@" 2>&1)"; then
            # A pre-flight refusal prints to stderr and exits nonzero; that is
            # the drift guard, which the caller may be asserting on purpose.
            echo "${out}" | sed 's/^/     /' >&2
            fail "formae apply exited nonzero"
        fi
        id="$(command_id "${out}")"
        if [[ -z "${id}" ]]; then
            echo "${out}" | sed 's/^/     /' >&2
            fail "formae apply submitted no command"
        fi
        if wait_command "${id}" quiet; then
            return 0
        fi
        echo "     submit ${id} was turned down (attempt ${attempt}); retrying" >&2
        sleep 10
    done
    "${FORMAE}" status command --query="id:${id}" 2>&1 | sed 's/^/     /' >&2
    fail "apply kept being rejected after 4 attempts"
}

# formae apply is asynchronous; block until the release settles or time out.
wait_for_helm() {
    local want_version="$1" want_revision="$2" tries=0
    until [[ "$(helm_version)" == "${want_version}" && "$(helm_revision)" == "${want_revision}" ]]; do
        tries=$((tries + 1))
        [[ ${tries} -gt 100 ]] && fail "timed out waiting for ${want_version} rev ${want_revision} (saw $(helm_version) rev $(helm_revision))"
        sleep 3
    done
}

# formae records a release only once it is fully deployed, so its inventory can
# lag Helm's record by the readiness check.
wait_for_formae() {
    local want="$1" tries=0
    until [[ "$(formae_version)" == "${want}" ]]; do
        tries=$((tries + 1))
        [[ ${tries} -gt 100 ]] && fail "timed out waiting for formae to report ${want} (saw '$(formae_version)')"
        sleep 3
    done
}

# Destroy is asynchronous too. Returning before it finishes leaves the stack
# half-torn-down, and the next run's apply is then rejected as drift against a
# stack that is still being removed — which looks like a plugin fault and is not.
cleanup() {
    local out id
    echo ""
    echo "Cleaning up..."
    if out="$("${FORMAE}" destroy --yes "${FORMA_FILE}" 2>&1)"; then
        id="$(command_id "${out}")"
        [[ -n "${id}" ]] && wait_command "${id}" quiet || true
    fi
    helm uninstall "${RELEASE}" -n "${NS}" >/dev/null 2>&1 || true
    kubectl delete namespace "${NS}" --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "Resolving Pkl dependencies..."
pkl project resolve "${PROJECT_ROOT}/schema/pkl" >/dev/null
pkl project resolve "${DEMO_DIR}" >/dev/null

echo ""
echo "1) formae apply — deploy version A (${VERSION_A})"
cd "${DEMO_DIR}"
apply_and_wait "${FORMA_FILE}"
wait_for_helm "${VERSION_A}" 1
assert_eq "helm chart version" "${VERSION_A}" "$(helm_version)"
assert_eq "helm revision"      "1"            "$(helm_revision)"
wait_for_formae "${VERSION_A}"
assert_eq "formae reports version" "${VERSION_A}" "$(formae_version)"

echo ""
echo "2) helm upgrade — move the release to version B (${VERSION_B}) behind formae's back"
# Helm 4 dropped `helm upgrade --repo`, so the chart has to come from a repo
# alias. formae itself needs no such alias — the plugin's repoURL field resolves
# the index directly — but the CLI does.
helm repo add ory "${REPO_URL}" >/dev/null 2>&1 || true
helm repo update ory >/dev/null 2>&1 || true
helm upgrade "${RELEASE}" ory/kratos \
    --version "${VERSION_B}" \
    --namespace "${NS}" \
    --reuse-values \
    --wait --timeout 10m >/dev/null
assert_eq "helm chart version" "${VERSION_B}" "$(helm_version)"
assert_eq "helm revision"      "2"            "$(helm_revision)"

echo ""
echo "3) formae Read picks the change up — the release is not opaque to drift"
# No CLI verb forces a sync; the agent runs one on its own interval (30s in the
# helm-interop profile). The wait below simply outlasts that.
wait_for_formae "${VERSION_B}"
assert_eq "formae reports version" "${VERSION_B}" "$(formae_version)"
echo "     the forma still pins ${VERSION_A}, so this is a diff on 'version'"

echo ""
echo "4) a plain reconcile is refused — out-of-band change, formae will not clobber it silently"
if "${FORMAE}" apply --mode reconcile --yes "${FORMA_FILE}" >/dev/null 2>&1; then
    fail "plain reconcile succeeded; the drift guard did not fire"
fi
pass "refused, as it should be (use --force, or extract the change into the forma)"

echo ""
echo "5) reconcile --force — back to the pinned version A"
apply_and_wait --force "${FORMA_FILE}"
wait_for_helm "${VERSION_A}" 3
assert_eq "helm chart version" "${VERSION_A}" "$(helm_version)"
assert_eq "helm revision"      "3"            "$(helm_revision)"
wait_for_formae "${VERSION_A}"
assert_eq "formae reports version" "${VERSION_A}" "$(formae_version)"

echo ""
echo "Helm history — every step is a real Helm revision:"
helm history "${RELEASE}" -n "${NS}" 2>/dev/null | sed 's/^/     /'

echo ""
echo "PASS: formae detected a Helm-side upgrade and reconciled it back."
