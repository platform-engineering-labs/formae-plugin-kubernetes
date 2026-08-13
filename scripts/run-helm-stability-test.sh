#!/bin/bash
# © 2026 Platform Engineering Labs Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Crash-recovery suite: what happens to a Helm release when the agent or the
# plugin dies mid-install.
#
# The suite kills processes on purpose, so isolation is not a nicety here. It
# runs under its own profile with its own SQLite file, and the Go harness
# resolves the plugin PID as a child of its OWN agent — never by name. Several
# agents can be running on one machine and they all spawn a process from the
# same plugins/k8s path, so `pkill -f k8s` would take out somebody else's work.
#
# The harness owns the agent lifecycle itself (TestMain), because half the
# scenarios restart it. This script only installs the profile and runs the tests.
#
# Usage:
#   ./scripts/run-helm-stability-test.sh                      # the four scenarios
#   ./scripts/run-helm-stability-test.sh TestPluginSigterm    # one, by name
#
# Honoured: FORMAE_BINARY, STABILITY_PROFILE, STABILITY_KUBE_VERSION, STABILITY_FORMAE_PKL,
#           STABILITY_DRAIN_SAMPLES, STABILITY_KEEP, GO_TEST_TIMEOUT.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
SUITE_DIR="${PROJECT_ROOT}/test/helm-stability"

FORMAE="${FORMAE_BINARY:-$(command -v formae || true)}"
if [[ -z "${FORMAE}" ]] || [[ ! -x "${FORMAE}" ]]; then
    echo "Error: formae binary not found; set FORMAE_BINARY" >&2
    exit 1
fi

PROFILE="${STABILITY_PROFILE:-helm-stability-ci}"
PROFILE_DIR="${HOME}/.config/formae/profiles"
TEST_FILTER="${1:-}"

# The profile is global CLI state in formae — `formae profile use <name>`, not a
# per-invocation flag. So this suite cannot run alongside other formae work, and
# it has to put the previous profile back when it is done. Captured before
# anything switches.
PREVIOUS_PROFILE="$("${FORMAE}" profile current 2>/dev/null | tr -d '[:space:]' || true)"

echo "=== K8S::Helm::Release stability ==="
echo "binary:   ${FORMAE}"
echo "profile:  ${PROFILE} (restoring ${PREVIOUS_PROFILE:-none} afterwards)"

# The generated formae reference schema/pkl, which has to be resolvable before
# any apply.
echo "Resolving schema/pkl dependencies..."
pkl project resolve "${PROJECT_ROOT}/schema/pkl" >/dev/null

# Best-effort: a previous run killed mid-scenario leaves an agent behind, and it
# would still be re-driving that run's commands when this one starts. Done before
# the profile switch so it stops the agent that is actually running.
"${FORMAE}" agent stop >/dev/null 2>&1 || true

# reap_orphaned_plugins kills plugin processes whose parent is gone.
#
# An orphan keeps its Ergo node name registered, so the next agent cannot start
# that plugin at all — it dies with "unable to register node: resource is taken",
# the supervisor exhausts its restart budget, and every command against that
# namespace then fails. One leaked orphan breaks the next run, and any other
# formae work on this machine with it.
#
# Orphans only (ppid 1). A plugin whose agent is alive belongs to that agent.
reap_orphaned_plugins() {
    local pid ppid reaped=0
    for pid in $(pgrep -f "/formae/plugins/" 2>/dev/null || true); do
        ppid=$(ps -o ppid= -p "${pid}" 2>/dev/null | tr -d ' ')
        if [[ "${ppid}" == "1" ]]; then
            kill -9 "${pid}" 2>/dev/null && reaped=$((reaped + 1))
        fi
    done
    [[ ${reaped} -gt 0 ]] && echo "Reaped ${reaped} orphaned plugin process(es)"
    return 0
}

# Wipe this suite's datastore.
#
# Not hygiene — correctness. Every scenario here ends with a command that was
# interrupted on purpose, and formae persists those: the next agent start
# re-drives them, and an InProgress command from the last run then refuses this
# run's very first apply with "another command is already working on the same
# resources". The suite is only meaningful from an empty datastore.
#
# Must match the filePath in agent-config.pkl.
DATASTORE="${HOME}/.pel/formae/data/helm-stability-ci.db"
rm -f "${DATASTORE}" "${DATASTORE}-wal" "${DATASTORE}-shm"

# Clear anything a previously crashed run orphaned, or the agent we are about to
# start will be unable to spawn its own plugins.
reap_orphaned_plugins

mkdir -p "${PROFILE_DIR}"
cp "${SUITE_DIR}/agent-config.pkl" "${PROFILE_DIR}/${PROFILE}.pkl"
"${FORMAE}" profile use "${PROFILE}" >/dev/null

cleanup() {
    "${FORMAE}" agent stop >/dev/null 2>&1 || true
    reap_orphaned_plugins
    if [[ -n "${PREVIOUS_PROFILE}" ]] && [[ "${PREVIOUS_PROFILE}" != "${PROFILE}" ]]; then
        echo "Restoring profile ${PREVIOUS_PROFILE}"
        "${FORMAE}" profile use "${PREVIOUS_PROFILE}" >/dev/null 2>&1 || true
    fi
    if [[ -z "${STABILITY_KEEP:-}" ]]; then
        # Namespaces, not releases: a scenario ends with the release deliberately
        # wedged about half the time, and `helm uninstall` is exactly what these
        # tests prove you should not have to reach for.
        #
        # Matched on this suite's own prefix and nothing else. Other suites and
        # other people share this cluster.
        for ns in $(kubectl get ns -o name 2>/dev/null | grep -o 'formae-helm-stability-[a-z0-9-]*' || true); do
            kubectl delete namespace "${ns}" --wait=false --ignore-not-found >/dev/null 2>&1 || true
        done
    fi
}
trap cleanup EXIT

RUN_ARGS=()
if [[ -n "${TEST_FILTER}" ]]; then
    RUN_ARGS=(-run "${TEST_FILTER}")
fi

echo "Running suite..."
FORMAE_BINARY="${FORMAE}" \
    go test -tags stability -count=1 -v \
    -timeout "${GO_TEST_TIMEOUT:-60m}" \
    ${RUN_ARGS[@]+"${RUN_ARGS[@]}"} \
    "${SUITE_DIR}/..."
