#!/bin/bash
# © 2026 Platform Engineering Labs Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Immutable-field upgrade test.
#
# Shows an upgrade that cannot be performed — and, more to the point, that
# nothing else can perform it either. The failure arrives through formae, so it
# reads as formae's, and the whole value of this demo is the two steps at the end
# that rule that out.
#
#   1. formae apply at 5.1.1        -> installed
#   2. repin to 6.0.0, apply        -> refused, spec.selector is immutable
#   3. release inspected            -> untouched, still 5.1.1
#   4. plain helm upgrade to 6.0.0  -> identical error, no formae involved
#   5. helm --force-replace         -> identical error again
#
# Usage: ./scripts/run-helm-immutable-test.sh
# Requires a reachable cluster, `make install`, a running agent, helm and kubectl.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DEMO_DIR="${PROJECT_ROOT}/examples/helm/immutable-field-upgrade"
FORMA="${DEMO_DIR}/flowise.pkl"
WORK_DIR="$(mktemp -d)"

FORMAE="${FORMAE_BINARY:-$(command -v formae || true)}"
if [[ -z "${FORMAE}" ]] || [[ ! -x "${FORMAE}" ]]; then
    echo "Error: formae binary not found; set FORMAE_BINARY" >&2
    exit 1
fi

NS="formae-helm-immutable"
RELEASE="flowise"
STACK="helm-immutable"
REPO="https://cowboysysop.github.io/charts/"
VERSION_A="5.1.1"
VERSION_B="6.0.0"

pass() { echo "  ✓ $1"; }
fail() { echo "  ✗ $1" >&2; exit 1; }

# The chart version of the latest revision, whatever its status. A failed
# upgrade still writes a revision, so this alone does not say the upgrade
# worked — see deployed_version.
helm_version() {
    helm list -n "${NS}" -o json 2>/dev/null \
        | python3 -c 'import json,sys
try: rels = json.load(sys.stdin)
except Exception: sys.exit(0)
for r in rels:
    if r.get("name") == sys.argv[1]:
        print(str(r.get("chart","")).rsplit("-",1)[-1]); break' "${RELEASE}"
}

# The chart version actually serving: the newest revision whose status is
# deployed. This is the one that matters. A failed upgrade leaves a revision at
# the new version and the old one still deployed, so reading `helm list` alone
# reports the upgrade as having taken when nothing changed.
deployed_version() {
    helm history "${RELEASE}" -n "${NS}" -o json 2>/dev/null \
        | python3 -c 'import json,sys
try: revs = json.load(sys.stdin)
except Exception: sys.exit(0)
best = [r for r in revs if r.get("status") == "deployed"]
if best:
    print(str(best[-1].get("chart","")).rsplit("-",1)[-1])'
}

# Status of the newest revision.
latest_status() {
    helm history "${RELEASE}" -n "${NS}" -o json 2>/dev/null \
        | python3 -c 'import json,sys
try: revs = json.load(sys.stdin)
except Exception: sys.exit(0)
if revs: print(revs[-1].get("status",""))'
}

cleanup() {
    echo ""
    echo "Cleaning up..."
    "${FORMAE}" destroy --yes --query "stack:${STACK}" >/dev/null 2>&1 || true
    helm uninstall "${RELEASE}" -n "${NS}" >/dev/null 2>&1 || true
    kubectl delete namespace "${NS}" --wait=false >/dev/null 2>&1 || true
    rm -rf "${WORK_DIR}"
}
trap cleanup EXIT

echo "Resolving Pkl dependencies..."
pkl project resolve "${PROJECT_ROOT}/schema/pkl" >/dev/null
pkl project resolve "${DEMO_DIR}" >/dev/null

# A namespace left Terminating by a previous run is not the same as one that is
# gone. formae creates it, Helm then installs into it, and the install fails with
# "namespaces not found" while kubectl still lists it. Wait it out rather than
# race it.
if kubectl get namespace "${NS}" >/dev/null 2>&1; then
    echo "Waiting for a previous ${NS} to finish deleting..."
    kubectl delete namespace "${NS}" --wait=false >/dev/null 2>&1 || true
    for _ in $(seq 1 60); do
        kubectl get namespace "${NS}" >/dev/null 2>&1 || break
        sleep 5
    done
    kubectl get namespace "${NS}" >/dev/null 2>&1 \
        && fail "namespace ${NS} is still there; delete it and re-run"
fi

echo ""
echo "1) formae apply — install at ${VERSION_A}"
"${FORMAE}" apply --mode reconcile --yes "${FORMA}" >/dev/null 2>&1 || true
for _ in $(seq 1 60); do
    [[ "$(helm_version)" == "${VERSION_A}" ]] && break
    sleep 5
done
[[ "$(helm_version)" == "${VERSION_A}" ]] || fail "release never reached ${VERSION_A}"

# The release being deployed is not the same as the command being finished.
# Applying again while the first is still settling is turned down for being
# concurrent — which looks like the refusal this demo is about, and is not it.
for _ in $(seq 1 60); do
    "${FORMAE}" status command --query 'status:InProgress' --output-consumer machine 2>/dev/null \
        | grep -q '"CommandId"' || break
    sleep 5
done
pass "installed at ${VERSION_A}"

echo ""
echo "2) repin to ${VERSION_B} and apply — expected to be refused"
# The forma is copied rather than edited in place: this demo must not leave a
# modified file behind in the repo.
cp "${FORMA}" "${WORK_DIR}/flowise.pkl"
cp "${DEMO_DIR}/PklProject" "${WORK_DIR}/PklProject"
sed_i=(-i); [[ "$(uname)" == "Darwin" ]] && sed_i=(-i '')
sed "${sed_i[@]}" "s/local chartVersion = \"${VERSION_A}\"/local chartVersion = \"${VERSION_B}\"/" \
    "${WORK_DIR}/flowise.pkl"
# The project's schema dependency is written relative to the demo directory, and
# nothing resolves it from a temp dir. Absolutise it on the way over.
sed "${sed_i[@]}" "s#import(\"../../../schema/pkl/PklProject\")#import(\"${PROJECT_ROOT}/schema/pkl/PklProject\")#" \
    "${WORK_DIR}/PklProject"
pkl project resolve "${WORK_DIR}" >/dev/null

out="$("${FORMAE}" apply --mode reconcile --yes "${WORK_DIR}/flowise.pkl" 2>&1)" || true
echo "${out}" | sed 's/^/     /' | tail -3
# A concurrency rejection would leave the release unchanged too, and step 3
# would pass without this demo having shown anything.
echo "${out}" | grep -qi "wait for it to finish" \
    && fail "the apply was turned down as concurrent, not for the immutable field"
pass "apply submitted"

echo ""
echo "3) the upgrade failed and left ${VERSION_A} serving"
for _ in $(seq 1 36); do
    [[ "$(latest_status)" == "failed" ]] && break
    sleep 5
done
[[ "$(latest_status)" == "failed" ]] \
    || fail "newest revision is $(latest_status), not failed; this demo assumes the upgrade cannot succeed"
pass "newest revision is failed"
[[ "$(deployed_version)" == "${VERSION_A}" ]] \
    && pass "still serving ${VERSION_A} — a failed upgrade writes a revision but changes nothing" \
    || fail "deployed version is $(deployed_version), expected ${VERSION_A}"

echo ""
echo "4) plain helm upgrade — the same error, with formae out of the picture"
if err="$(helm upgrade "${RELEASE}" flowise --repo "${REPO}" --version "${VERSION_B}" \
        -n "${NS}" --wait --timeout 4m 2>&1)"; then
    fail "plain helm upgrade succeeded; the premise of this demo is wrong"
fi
echo "${err}" | grep -qi "immutable" \
    && pass "helm refuses too: $(echo "${err}" | grep -oiE '[^ ]+: field is immutable' | head -1)" \
    || fail "helm failed for some other reason: ${err}"

echo ""
echo "5) helm --force-replace — still refused"
# Helm 4 renamed --force to --force-replace, and it will not run alongside
# server-side apply, which is now the default. Hence --server-side=false.
if err="$(helm upgrade "${RELEASE}" flowise --repo "${REPO}" --version "${VERSION_B}" \
        -n "${NS}" --force-replace --server-side=false --wait --timeout 4m 2>&1)"; then
    fail "--force-replace succeeded; this demo's conclusion needs revisiting"
fi
echo "${err}" | grep -qi "immutable" \
    && pass "a replace is not a delete-and-recreate, so it fails the same way" \
    || fail "--force-replace failed for some other reason: ${err}"

echo ""
echo "========================================"
echo "An immutable field ends an upgrade for everyone."
echo "Deleting the object is the only way through, and nothing does that for you."
echo "========================================"
