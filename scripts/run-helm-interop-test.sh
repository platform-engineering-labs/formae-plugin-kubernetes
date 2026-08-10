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

# The profile's own datastore, read out of the config so the two cannot drift.
DB="$(sed -n 's#.*filePath = "\(.*\.db\)".*#\1#p' "${SUITE_DIR}/agent-config.pkl" | head -1)"
DB="${DB/#\~/${HOME}}"

echo "Starting agent..."
stop_agent
"${FORMAE}" --profile "${PROFILE}" agent start >/dev/null 2>&1 &
trap stop_agent EXIT

# Wait for the agent to prove it is *this* profile's agent, not merely that some
# agent answers.
#
# `agent start` refuses with "agent is already running (PID n)" when any agent
# holds the port, whatever profile it belongs to, and still exits 0. `status
# agent` then answers happily - from that other agent, against its datastore. So
# agent_running() alone passes while the whole sweep runs somewhere else, which is
# how three separate readings in this suite's history turned out to be false. The
# datastore file appearing is the cheapest thing that can only be true if the
# intended agent is the one running.
for _ in $(seq 1 30); do
    if [[ -f "${DB}" ]] && agent_running; then break; fi
    sleep 2
done
if [[ ! -f "${DB}" ]]; then
    echo "Error: no agent came up under profile ${PROFILE} (${DB} was never created)." >&2
    echo "       Something else holds the agent port, so every formae call in this run" >&2
    echo "       would go to its datastore instead. Stop it and re-run: formae agent stop" >&2
    # `|| running=` matters: pipefail plus head closing the pipe makes this
    # substitution fail, and under set -e that kills the script before it can
    # print any of this. A diagnostic that exits silently is worse than none.
    running="$("${FORMAE}" status agent 2>&1 | head -3 | tr '\n' ' ')" || running="(no answer)"
    echo "       Currently answering: ${running}" >&2
    exit 1
fi
if ! agent_running; then
    echo "Error: agent did not come up under profile ${PROFILE}" >&2
    exit 1
fi
echo "Agent up."

# Clear commands left InProgress by an earlier run, and treat this as
# load-bearing rather than tidiness.
#
# A killed run leaves its apply InProgress in this profile's datastore, against a
# namespace the cleanup then deletes, so it can never finish. The agent re-drives
# incomplete commands on every start, and a user changeset holds discovery paused
# for as long as it runs — so one leftover command keeps discovery paused for the
# entire life of every later agent. Every cell then waits out its timeout at step
# 3 with nothing wrong with it, and the suite reads as a regression in whatever
# changed most recently. That is exactly how a schema change was blamed for two
# charts failing.
#
# --force is the right hammer: whatever it is mid-update on lives in a namespace
# from a finished run, so there is no cluster-side work worth waiting for.
if "${FORMAE}" --profile "${PROFILE}" status command --query 'status:InProgress' \
        --output-consumer machine 2>/dev/null | grep -q '"CommandId"'; then
    echo "Cancelling commands left InProgress by an earlier run..."
    "${FORMAE}" --profile "${PROFILE}" cancel --force --yes \
        --query 'status:InProgress' >/dev/null 2>&1 || true
fi

RUN_PATTERN="TestHelmInterop"
if [[ -n "${CHART_FILTER}" ]]; then
    # An unparenthesised alternation silently under-tests, which is worse than
    # the empty-match case guarded below because it still reports green.
    # `go test -run` splits the pattern on `/` per name level, but a top-level
    # `|` alternates the *whole* pattern: `TestHelmInterop/a|b` reads as
    # "TestHelmInterop/a" or "b", and `b` matches no top-level test — so only `a`
    # ever runs. Parenthesise and it means what it looks like.
    if [[ "${CHART_FILTER}" == *"|"* ]] && [[ "${CHART_FILTER}" != "("*")" ]]; then
        echo "Error: parenthesise the alternation: '(${CHART_FILTER})'." >&2
        echo "       Without it, go test runs only the first chart and still reports PASS." >&2
        exit 1
    fi
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
