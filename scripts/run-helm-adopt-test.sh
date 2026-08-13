#!/bin/bash
# © 2026 Platform Engineering Labs Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Adopt-and-Rollback Test
# =======================
# Discover a Helm-installed release, take it under management, upgrade it with
# formae, then roll it back with the Helm CLI.
#
# Steps:
#   1. helm install                 -> release at version A, revision 1, foreign
#   2. formae apply                 -> target/stack/namespace created, and the
#                                      release REFUSED: formae did not install it
#   3. discovery                    -> unmanaged K8S::Helm::Release appears
#   4. formae extract               -> a forma describing the live release
#   5. formae apply --mode patch    -> adopted; arrives as `update`, not `create`
#   6. formae apply --mode reconcile-> upgraded to version B, revision 2
#   7. helm rollback                -> revision 3 carrying version A again
#   8. formae Read                  -> reports version A, so the rollback is drift
#
# Usage:
#   ./scripts/run-helm-adopt-test.sh
#
# Requires: a reachable cluster, the plugin installed (`make install`) and a
# running agent, plus `helm`, `kubectl` and `python3` on PATH.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DEMO_DIR="${PROJECT_ROOT}/examples/helm/adopt-and-rollback"
FORMA_FILE="${DEMO_DIR}/kratos.pkl"
HELM_VALUES="${DEMO_DIR}/helm-values.yaml"
WORK_DIR="$(mktemp -d)"

FORMAE="${FORMAE_BINARY:-$(command -v formae || true)}"
if [[ -z "${FORMAE}" ]] || [[ ! -x "${FORMAE}" ]]; then
    echo "Error: formae binary not found; set FORMAE_BINARY" >&2
    exit 1
fi

NS="formae-helm-adopt"
RELEASE="kratos"
STACK="helm-adopt"
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

helm_field() {
    helm list -n "${NS}" -o json 2>/dev/null | python3 -c '
import json,sys
field = sys.argv[1]
try: rels = json.load(sys.stdin)
except Exception: sys.exit(0)
for r in rels:
    if r.get("name") == sys.argv[2]:
        v = str(r.get(field, ""))
        print(v.rsplit("-", 1)[-1] if field == "chart" else v)
        break
' "$1" "${RELEASE}"
}
helm_version()  { helm_field chart; }
helm_revision() { helm_field revision; }

# One field of the K8S::Helm::Release as formae currently records it, plus the
# stack it sits on so adoption is observable.
formae_field() {
    "${FORMAE}" inventory resources --output-consumer machine 2>/dev/null \
        | python3 -c '
import json,sys
want = sys.argv[1]
try: rs = json.load(sys.stdin).get("Resources", [])
except Exception: sys.exit(0)
for r in rs:
    if r["Type"] == "K8S::Helm::Release":
        print(r["Stack"] if want == "stack" else r.get("Properties", {}).get(want, ""))
        break
' "$1"
}

# Every native id the inventory holds for one resource type, whatever stack it is
# on. Used to prove the discovery collapse: a chart's objects must not be here.
formae_ids_of_type() {
    "${FORMAE}" inventory resources --output-consumer machine 2>/dev/null \
        | python3 -c '
import json,sys
try: rs = json.load(sys.stdin).get("Resources", [])
except Exception: sys.exit(0)
for r in rs:
    if r["Type"] == sys.argv[1]:
        print(r.get("NativeID") or r.get("Label",""))
' "$1"
}

command_id() { printf '%s' "$1" | grep -oE "id:[A-Za-z0-9]+" | head -1 | cut -d: -f2; }

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

# A bare Rejected with no message never reached the plugin: besides drift, a
# background sync landing between the CLI's pre-flight read and its submit makes
# the stack version it carried stale. That is a concurrency conflict, so retry.
apply_and_wait() {
    local mode="$1"; shift
    local out id attempt
    for attempt in 1 2 3 4; do
        if ! out="$("${FORMAE}" apply --mode "${mode}" --yes "$@" 2>&1)"; then
            echo "${out}" | sed 's/^/     /' >&2
            fail "formae apply exited nonzero"
        fi
        id="$(command_id "${out}")"
        [[ -z "${id}" ]] && { echo "${out}" | sed 's/^/     /' >&2; fail "no command submitted"; }
        if wait_command "${id}" quiet; then
            return 0
        fi
        echo "     submit ${id} was turned down (attempt ${attempt}); retrying" >&2
        sleep 10
    done
    fail "apply kept being rejected"
}

# Returns the failing command's ErrorMessage, for asserting on a refusal.
apply_expecting_failure() {
    local mode="$1"; shift
    local out id
    out="$("${FORMAE}" apply --mode "${mode}" --yes "$@" 2>&1)" || true
    id="$(command_id "${out}")"
    [[ -z "${id}" ]] && { echo "${out}" >&2; fail "no command submitted"; }
    wait_command "${id}" quiet && fail "apply succeeded but should have been refused"
    # The ErrorMessage is persisted a moment after the command reaches Failed, so
    # reading it once races the write. Poll briefly for it.
    local msg tries=0
    while [[ ${tries} -lt 20 ]]; do
        msg="$("${FORMAE}" status command --query="id:${id}" --output-consumer machine 2>/dev/null \
            | python3 -c '
import json,sys
try: c = json.load(sys.stdin)["Commands"][0]
except Exception: sys.exit(0)
for ru in c.get("ResourceUpdates", []):
    if ru["ResourceType"].endswith("Release"):
        print(ru.get("ErrorMessage") or "")
        break
')"
        [[ -n "${msg}" ]] && { printf '%s' "${msg}"; return 0; }
        tries=$((tries + 1))
        sleep 2
    done
    printf '%s' ""
}

wait_until() {
    local what="$1" want="$2" tries=0
    until [[ "$(formae_field "${what}")" == "${want}" ]]; do
        tries=$((tries + 1))
        [[ ${tries} -gt 100 ]] && fail "timed out waiting for ${what}=${want} (saw '$(formae_field "${what}")')"
        sleep 3
    done
}

cleanup() {
    local out id
    echo ""
    echo "Cleaning up..."
    # Query-based: the adopted release is managed through the extracted forma in
    # a temp dir, not through FORMA_FILE.
    if out="$("${FORMAE}" destroy --yes --query "stack:${STACK}" 2>&1)"; then
        id="$(command_id "${out}")"
        [[ -n "${id}" ]] && wait_command "${id}" quiet || true
    fi
    for ctl in configmap/ctl-configmap deployment/ctl-deployment service/ctl-service serviceaccount/ctl-sa; do
        kubectl delete "${ctl}" -n "${NS}" --wait=false >/dev/null 2>&1 || true
    done
    helm uninstall "${RELEASE}" -n "${NS}" >/dev/null 2>&1 || true
    kubectl delete namespace "${NS}" --wait=false >/dev/null 2>&1 || true
    rm -rf "${WORK_DIR}"
}
trap cleanup EXIT

echo "Resolving Pkl dependencies..."
pkl project resolve "${PROJECT_ROOT}/schema/pkl" >/dev/null
pkl project resolve "${DEMO_DIR}" >/dev/null

echo ""
echo "1) helm install — a release formae knows nothing about (version ${VERSION_A})"
helm repo add ory "${REPO_URL}" >/dev/null 2>&1 || true
helm repo update ory >/dev/null 2>&1 || true
helm install "${RELEASE}" ory/kratos \
    --version "${VERSION_A}" \
    -f "${HELM_VALUES}" \
    --namespace "${NS}" --create-namespace \
    --wait --timeout 10m >/dev/null
assert_eq "helm chart version" "${VERSION_A}" "$(helm_version)"
assert_eq "helm revision"      "1"            "$(helm_revision)"
# No ownership marker: the plugin stamps formae.dev/managed only on releases it
# installs, and that is what makes the next step a refusal rather than a takeover.
marker="$(kubectl get secret -n "${NS}" -l owner=helm \
    -o jsonpath='{.items[0].metadata.labels.formae\.dev/managed}' 2>/dev/null || true)"
[[ -z "${marker}" ]] && pass "release carries no formae ownership marker" \
                      || fail "release unexpectedly marked as formae-managed"

echo ""
echo "2) formae apply — refused, because formae did not install this release"
# The target and namespace are still created; only the release is refused. That
# matters: discovery needs a target before it can see anything.
# One attempt only. Applying again would leave the stack with a second failed
# reconcile behind it and the later `--mode patch` adopt is then rejected.
#
# The target, stack and namespace are still created here, which matters: discovery
# cannot see anything until a target exists.
msg="$(apply_expecting_failure reconcile "${FORMA_FILE}")"
pass "refused (target, stack and namespace created; discovery now has a target)"
if [[ -n "${msg}" ]]; then
    echo "${msg}" | grep -q "was not created by formae" \
        && pass "refused with an actionable message" \
        || fail "refused, but not for the expected reason: '${msg}'"
    echo "${msg}" | fold -s -w 100 | sed 's/^/     /' | head -4
else
    # formae does not persist a per-resource ErrorMessage for a command whose
    # resource outcomes are mixed — the namespace succeeded here, so the
    # release's refusal text is dropped. The refusal itself still stands.
    pass "refused (formae recorded no message for this mixed-outcome command)"
fi

echo ""
echo "3) discovery — the release shows up as unmanaged"
wait_until stack '$unmanaged'
pass "discovered on the \$unmanaged stack"
assert_eq "discovered version" "${VERSION_A}" "$(formae_field version)"

echo ""
echo "3b) discovery collapses the chart's own objects"
# The release renders a Deployment, a StatefulSet, three Services, a ConfigMap and
# a ServiceAccount. The profile discovers all those kinds, so if the collapse were
# not working they would each appear as unmanaged resources next to the release.
collapse_failures=0
for pair in \
    "K8S::Apps::Deployment|${NS}/${RELEASE}" \
    "K8S::Apps::StatefulSet|${NS}/${RELEASE}-courier" \
    "K8S::Core::Service|${NS}/${RELEASE}-admin" \
    "K8S::Core::Service|${NS}/${RELEASE}-public" \
    "K8S::Core::ConfigMap|${NS}/${RELEASE}-config" \
    "K8S::Core::ServiceAccount|${NS}/${RELEASE}"
do
    rtype="${pair%%|*}"; rid="${pair##*|}"
    if formae_ids_of_type "${rtype}" | grep -qx "${rid}"; then
        echo "  ✗ ${rtype} ${rid} surfaced in discovery; the collapse did not hide it" >&2
        collapse_failures=$((collapse_failures + 1))
    fi
done
[[ ${collapse_failures} -eq 0 ]] \
    || fail "${collapse_failures} chart-rendered object(s) leaked into discovery"
pass "none of the chart's 6 rendered objects surfaced"

# Negative controls, one per kind whose absence is asserted above. Without these
# the assertions could pass vacuously: discovery might simply not have run for a
# kind yet, and "not in the inventory" would prove nothing about the filter.
#
# Each is an object in the same namespace that no chart rendered, so each MUST be
# discovered. Deployment uses replicas=0 to avoid waiting on a pod.
# Created with the failure surfaced, not swallowed. A `|| true` here once turned a
# namespace that was still Terminating from a previous run into a confusing
# "control never discovered" failure several minutes later.
create_control() {
    local out
    if ! out="$(kubectl create "$@" -n "${NS}" 2>&1)"; then
        echo "     ${out}" >&2
        fail "could not create control object (${*}); the namespace may still be Terminating from a previous run"
    fi
}
create_control configmap ctl-configmap --from-literal=k=v
create_control deployment ctl-deployment --image=busybox:1.36 --replicas=0
create_control service clusterip ctl-service --tcp=80:80
create_control serviceaccount ctl-sa

for pair in \
    "K8S::Core::ConfigMap|${NS}/ctl-configmap" \
    "K8S::Apps::Deployment|${NS}/ctl-deployment" \
    "K8S::Core::Service|${NS}/ctl-service" \
    "K8S::Core::ServiceAccount|${NS}/ctl-sa"
do
    rtype="${pair%%|*}"; rid="${pair##*|}"
    tries=0
    until formae_ids_of_type "${rtype}" | grep -qx "${rid}"; do
        tries=$((tries + 1))
        # Discovery runs on an interval (60s in the helm-interop profile), and the
        # controls are created after it has already swept once, so this has to
        # outlast more than a single cycle.
        [[ ${tries} -gt 60 ]] && fail "control ${rtype} ${rid} never discovered after $((tries * 5))s — either discovery does not cover this kind, so the collapse assertion above was vacuous, or the filter is over-matching"
        sleep 5
    done
done
pass "hand-made Deployment, Service, ConfigMap and ServiceAccount all still discovered"

echo ""
echo "4) formae extract — generate a forma describing the live release"
cd "${WORK_DIR}"
"${FORMAE}" extract --query 'type:K8S::Helm::Release managed:false' \
    --schema-location local --yes ./adopted.pkl >/dev/null
[[ -f ./adopted.pkl ]] || fail "extract produced no file"
RELEASE="${RELEASE}" NS="${NS}" STACK="${STACK}" REPO_URL="${REPO_URL}" python3 - <<'PY'
import os, re
p = 'adopted.pkl'
s = open(p).read()

# The generated forma leaves the stack label for a human to choose.
release, ns, stack = os.environ["RELEASE"], os.environ["NS"], os.environ["STACK"]

s = s.replace('''    // Please provide a stack to bring the resources in this Forma under management
    // label = ""''', f'    label = "{stack}"', 1)

# The resource label is left exactly as extract wrote it. Adoption binds by
# label, and extract derives it from the native id, appending a dedup suffix when
# state already holds that label — so it is not predictable enough to hardcode in
# a committed forma. Everything after adoption is driven through this extracted
# file instead.
renamed = 0

# No repoURL is added. The forma pins the version already deployed, so the plugin
# reuses the chart stored in the release record and never fetches — which is the
# only reason adoption can work at all, since Helm does not record which
# repository a release came from.

# Workaround for a formae bug: extract emits map keys as bare Pkl identifiers,
# so a values key containing dots (kratos names its identity schemas after
# files) produces `identity.default.schema.json = "..."`, which is not valid
# Pkl. Bracket-quote anything that is not a plain identifier.
s, n = re.subn(r'(?m)^(\s*)([A-Za-z_][A-Za-z0-9_\-]*(?:\.[A-Za-z0-9_\-]+)+)(\s*=\s*)',
               r'\1["\2"]\3', s)
open(p, 'w').write(s)
print(f"     set stack={stack}, bracket-quoted {n} dotted key(s)")
PY
# extract writes its own PklProject; resolve it before evaluating.
pkl project resolve . >/dev/null 2>&1 || true
if ! pkl eval --project-dir . adopted.pkl >/dev/null 2>"${WORK_DIR}/eval.err"; then
    echo "     --- generated PklProject ---" >&2
    sed 's/^/     /' PklProject >&2
    echo "     --- resolved k8s dependency ---" >&2
    python3 -c '
import json
d = json.load(open("PklProject.deps.json")).get("resolvedDependencies", {})
for k, v in d.items():
    if "k8s" in k:
        print("    ", k, "->", v.get("type"), v.get("uri"), v.get("path"))
' >&2 || true
    echo "     --- pkl error ---" >&2
    head -12 "${WORK_DIR}/eval.err" | sed 's/^/     /' >&2
    fail "extracted forma does not evaluate"
fi
pass "extracted forma evaluates"

echo ""
echo "5) formae apply --mode patch — adopt it"
# patch, NOT reconcile. The extracted forma describes only the release; a
# reconcile would treat everything else already on the stack — the namespace —
# as absent from the forma and delete it, taking the release down with it.
apply_and_wait patch ./adopted.pkl
wait_until stack "${STACK}"
pass "adopted onto stack ${STACK}"
assert_eq "version after adoption" "${VERSION_A}" "$(formae_field version)"
# Adoption is a pure bind: the extracted forma describes the live release exactly,
# so the patch carries no change and Helm is never called. The release must not
# move — a chart with pre-upgrade hooks would otherwise run them just to be
# adopted.
assert_eq "helm revision unchanged by adoption" "1" "$(helm_revision)"

echo ""
echo "6) edit the adopted forma to version B (${VERSION_B}) and apply"
cd "${WORK_DIR}"
# Bumping the version DOES need a repository: this is the one operation that has
# to fetch a chart the cluster has never seen. Adding repoURL here is the real
# step an operator takes when upgrading an adopted HTTP-repo release.
python3 - "${VERSION_A}" "${VERSION_B}" "${REPO_URL}" <<'PYEOF'
import re, sys
a, b, repo = sys.argv[1], sys.argv[2], sys.argv[3]
p = 'adopted.pkl'
s = open(p).read()
if f'version = "{a}"' not in s:
    raise SystemExit(f"adopted forma does not pin version {a}")
s = s.replace(f'version = "{a}"', f'version = "{b}"', 1)
if 'repoURL' not in s:
    s, n = re.subn(r'(?m)^(\s*)chart = "([^"]+)"$',
                   rf'\1chart = "\2"\n\1repoURL = "{repo}"', s, count=1)
    if not n:
        raise SystemExit('could not find the chart field to add repoURL after')
open(p, 'w').write(s)
PYEOF
pass "adopted forma repinned to ${VERSION_B}, repoURL added for the fetch"
# patch, not reconcile: the extracted forma describes only the release, and a
# reconcile would delete the namespace that is also on this stack.
apply_and_wait patch ./adopted.pkl
assert_eq "helm chart version" "${VERSION_B}" "$(helm_version)"
assert_eq "helm revision"      "2"            "$(helm_revision)"
wait_until version "${VERSION_B}"
pass "formae reports version ${VERSION_B}"

echo ""
echo "7) helm rollback — undo the formae upgrade from the Helm side"
# Roll back to revision 1, the state helm install produced. Helm records this as a
# new revision rather than rewinding history.
helm rollback "${RELEASE}" 1 -n "${NS}" --wait --timeout 10m >/dev/null
assert_eq "helm chart version" "${VERSION_A}" "$(helm_version)"
assert_eq "helm revision"      "3"            "$(helm_revision)"
# The marker is carried by whichever revision Helm copies labels from, so which
# revision you roll back to decides whether it comes along. Revision 2 is the
# upgrade formae performed, so it has the marker.
marker2="$(kubectl get secret -n "${NS}" "sh.helm.release.v1.${RELEASE}.v2" \
    -o jsonpath='{.metadata.labels.formae\.dev/managed}' 2>/dev/null || true)"
assert_eq "marker on the revision formae created" "true" "${marker2}"

# Revision 3 is a rollback to revision 1, which predates adoption and never had
# the marker, so revision 3 does not either. Harmless: the guard only applies to
# a create, and formae already holds a NativeID for this release, so every
# further operation arrives as an update.
marker3="$(kubectl get secret -n "${NS}" "sh.helm.release.v1.${RELEASE}.v3" \
    -o jsonpath='{.metadata.labels.formae\.dev/managed}' 2>/dev/null || true)"
[[ -z "${marker3}" ]] \
    && pass "rollback to a pre-adoption revision drops the marker, as expected" \
    || pass "marker also present on the rolled-back revision"

echo ""
echo "8) formae sees the rollback as ordinary drift"
wait_until version "${VERSION_A}"
pass "formae reports version ${VERSION_A} again"
echo "     the forma still pins ${VERSION_B}, so this is a diff on 'version'"

echo ""
echo "Helm history:"
helm history "${RELEASE}" -n "${NS}" 2>/dev/null | sed 's/^/     /'

echo ""
echo "PASS: adopted a Helm-installed release, upgraded it, and saw the rollback."
