#!/bin/bash
# © 2026 Platform Engineering Labs Inc.
# SPDX-License-Identifier: Apache-2.0
#
# formae <-> Helm interop suite: agent lifecycle plus the Go test run.
#
# The suite needs an agent with discovery switched on, which no default
# configuration has. Rather than requiring a human to have started the right one
# — the failure mode being step 3 quietly waiting out its timeout — this owns the
# agent for the length of the run.
#
# The agent runs under its own profile and its own SQLite file, so nothing here
# disturbs ~/.config/formae/formae.conf.pkl or an agent already serving other
# work. Set INTEROP_PROFILE to change the profile name.
#
# Usage:
#   ./scripts/run-helm-interop-test.sh                 # every chart
#   ./scripts/run-helm-interop-test.sh velero          # one, by subtest name
#
# Honoured: FORMAE_BINARY, INTEROP_TIMEOUT, INTEROP_HELM, INTEROP_KEEP,
# INTEROP_KUBE_VERSION, GO_TEST_TIMEOUT.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
SUITE_DIR="${PROJECT_ROOT}/test/helm-interop"

FORMAE="${FORMAE_BINARY:-$(command -v formae || true)}"
if [[ -z "${FORMAE}" ]] || [[ ! -x "${FORMAE}" ]]; then
    echo "Error: formae binary not found; set FORMAE_BINARY" >&2
    exit 1
fi

PROFILE="${INTEROP_PROFILE:-helm-interop-ci}"
PROFILE_DIR="${HOME}/.config/formae/profiles"
CHART_FILTER="${1:-}"

agent_running() {
    "${FORMAE}" --profile "${PROFILE}" status agent >/dev/null 2>&1
}

stop_agent() {
    # Best-effort: a run that never got the agent up must not fail in cleanup and
    # mask why it could not start.
    "${FORMAE}" --profile "${PROFILE}" agent stop >/dev/null 2>&1 || true
}

echo "=== formae <-> Helm interop ==="
echo "binary:  ${FORMAE}"
echo "profile: ${PROFILE}"

# Pkl deps for the generated bootstrap forma. The suite writes a PklProject
# pointing at schema/pkl, which has to be resolvable before any apply.
echo "Resolving schema/pkl dependencies..."
pkl project resolve "${PROJECT_ROOT}/schema/pkl" >/dev/null

mkdir -p "${PROFILE_DIR}"
cp "${SUITE_DIR}/agent-config.pkl" "${PROFILE_DIR}/${PROFILE}.pkl"

echo "Starting agent..."
stop_agent
"${FORMAE}" --profile "${PROFILE}" agent start >/dev/null 2>&1 &
trap stop_agent EXIT

for _ in $(seq 1 30); do
    if agent_running; then break; fi
    sleep 2
done
if ! agent_running; then
    echo "Error: agent did not come up under profile ${PROFILE}" >&2
    exit 1
fi
echo "Agent up."

RUN_PATTERN="TestHelmInterop"
if [[ -n "${CHART_FILTER}" ]]; then
    RUN_PATTERN="TestHelmInterop/${CHART_FILTER}"
fi

echo ""
echo "Running ${RUN_PATTERN}..."

# Output is teed rather than streamed straight through, so the run can be
# checked for having actually run something. A -run pattern that matches no
# chart exits zero and prints "no tests to run", and go test reports the parent
# as PASS — a green result for a suite that did nothing. That is a live risk now
# the patterns are generated per CI group rather than typed: one renamed chart
# and a whole group silently stops testing.
OUTPUT="$(mktemp)"
trap 'stop_agent; rm -f "${OUTPUT}"' EXIT

set +e
FORMAE_BINARY="${FORMAE}" \
INTEROP_FORMAE_PROFILE="${PROFILE}" \
    go test -tags integration -count=1 -v \
        -timeout "${GO_TEST_TIMEOUT:-90m}" \
        -run "${RUN_PATTERN}" \
        "${SUITE_DIR}/..." 2>&1 | tee "${OUTPUT}"
STATUS="${PIPESTATUS[0]}"
set -e

if grep -q "no tests to run" "${OUTPUT}"; then
    echo ""
    echo "Error: ${RUN_PATTERN} matched no chart. Nothing ran." >&2
    echo "Charts available: $(ls "${SUITE_DIR}/charts"/*.yaml | grep -v migrate | xargs -n1 basename | sed 's/.yaml//' | tr '\n' ' ')" >&2
    exit 1
fi

exit "${STATUS}"
