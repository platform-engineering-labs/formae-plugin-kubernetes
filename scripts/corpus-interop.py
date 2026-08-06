#!/usr/bin/env python3
# © 2026 Platform Engineering Labs Inc.
# SPDX-License-Identifier: Apache-2.0

"""Run real Helm charts through the formae adoption workflow.

Drives one scenario per chart, helm-first:

    h-install(A) . f-discover . f-adopt . f-upgrade(B) . h-rollback . f-reconcile

The charts come from the plugin-factory corpus (1000 Artifact Hub charts with
precomputed trait data), and a chart is only ever selected because it carries the
trait the cell is testing — a `pre-rollback` hook, a `crds/` directory, a `keep`
resource policy. A cell whose assertions do not touch the reason its chart was
picked is padding; see --list to check what each pick is for.

Two design notes worth knowing before reading the code:

Values are never translated into Pkl. Adoption goes through `formae extract`,
which generates the forma — including the values block — from the live release.
The harness hands raw YAML to `helm install` and only ever text-edits `version`
and `repoURL` on what extract produced. Generating Pkl for arbitrary chart values
was the single largest source of complexity in the previous harness and it is not
needed to run this scenario.

Reconcile after an out-of-band rollback is asserted as ABSORB: formae reports the
drift and refuses to correct it without --force. That is a product decision, not
an observation — see docs in assert_reconcile_absorbs.

Usage:
    ./scripts/corpus-interop.py --list
    ./scripts/corpus-interop.py --self-check
    ./scripts/corpus-interop.py --trait pre-rollback
    ./scripts/corpus-interop.py --chart velero --keep

Requires a reachable cluster, `make install`, a running agent, and helm, kubectl
and pkl on PATH. The corpus is found via $HELM_CORPUS, defaulting to
../plugin-factory/helm relative to this repo.
"""

from __future__ import annotations

import argparse
import csv
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
import uuid
from dataclasses import dataclass, field
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent


def find_corpus() -> Path:
    """Locate plugin-factory/helm by walking up from this file.

    Not REPO_ROOT.parent: in a git worktree that is `.worktrees/`, several levels
    below the plugins directory the corpus actually sits in.
    """
    for parent in REPO_ROOT.parents:
        candidate = parent / "plugin-factory" / "helm"
        if candidate.is_dir():
            return candidate
    return REPO_ROOT.parent / "plugin-factory" / "helm"


DEFAULT_CORPUS = find_corpus()

# Long enough for a CRD-installing operator chart to settle, short enough that a
# genuinely wedged step does not hold the run for an hour.
COMMAND_TIMEOUT = 600
DISCOVERY_TIMEOUT = 420


# ---------------------------------------------------------------------------
# Corpus: which charts, and why
# ---------------------------------------------------------------------------

# Each trait names a chart property that changes what the scenario proves, and
# the assertion that depends on it. Nothing else belongs in this table: a trait
# with no distinct assertion would produce cells that differ only by chart name.
TRAITS = {
    "pre-rollback": (
        "chart has a pre-rollback hook, so `helm rollback` runs hook logic that "
        "formae's revert-by-values path would fire as pre-upgrade instead",
        "asserts the pre-rollback hook ran on h-rollback and did NOT run on f-upgrade",
    ),
    "post-rollback": (
        "chart has a post-rollback hook — same asymmetry, opposite side of the rollback",
        "asserts the post-rollback hook ran on h-rollback only",
    ),
    "crd-install": (
        "chart installs CRDs, the headline reason Release exists over HelmChart.pkl",
        "asserts CRDs survive f-destroy as unmanaged residue",
    ),
    "test-hook": (
        "chart has a `helm.sh/hook: test` object, which must never be applied",
        "asserts no test-hook object exists after install or upgrade",
    ),
    "keep-policy": (
        "chart annotates an object helm.sh/resource-policy: keep",
        "asserts the kept object outlives f-destroy and reappears unmanaged",
    ),
    "no-hooks": (
        "chart has no hooks at all — the control case",
        "asserts the scenario is not silently depending on hook side effects",
    ),
}


@dataclass
class Chart:
    """One corpus entry, plus why it was selected."""

    name: str
    ref: str  # "repo/chart" or "oci://..."
    repo_url: str
    oci: bool
    version: str  # the corpus-pinned version — target of the f-upgrade
    trait: str
    trait_evidence: str = ""

    @property
    def supports_version_bump(self) -> bool:
        # An older version has to be resolvable to have something to install as A
        # and upgrade away from. `helm search repo --versions` reads the repo
        # index; OCI has no equivalent without talking to the registry API, so an
        # OCI chart cannot drive this scenario. Not a silent skip — reported.
        return not self.oci


def load_corpus(corpus: Path) -> list[dict]:
    """Parse charts.yaml without a yaml dependency.

    The file is generated by fetch-charts.py with one flat mapping per entry and
    no nesting, which a five-line reader handles. Pulling in PyYAML for this shape
    buys nothing.
    """
    entries: list[dict] = []
    current: dict | None = None
    for line in (corpus / "charts.yaml").read_text().splitlines():
        stripped = line.strip()
        if stripped.startswith("- "):
            if current:
                entries.append(current)
            current = {}
            stripped = stripped[2:]
        # Anything before the first `- ` is the header and the `charts:` key
        # itself, which would otherwise become a phantom entry.
        if current is None or ":" not in stripped or stripped.startswith("#"):
            continue
        key, _, value = stripped.partition(":")
        current[key.strip()] = value.strip().strip('"')
    if current:
        entries.append(current)
    return [e for e in entries if e.get("name")]


def hook_traits(corpus: Path) -> dict[str, dict[str, str]]:
    """Map chart name -> {trait: evidence} from the corpus hook inventory.

    hooks-objects.csv is one row per hook object with its events, so a chart's
    traits are whatever events its objects declare. The evidence string is kept
    because a cell that cannot say which object it is testing cannot assert
    against it either.
    """
    found: dict[str, dict[str, str]] = {}
    path = corpus / "hooks-objects.csv"
    if not path.exists():
        return found
    with path.open(newline="") as fh:
        for row in csv.DictReader(fh):
            chart, events = row.get("chart", ""), row.get("events", "") or ""
            obj = f"{row.get('kind','?')}/{row.get('name','?')}"
            for event, trait in (
                ("pre-rollback", "pre-rollback"),
                ("post-rollback", "post-rollback"),
                ("crd-install", "crd-install"),
                ("test", "test-hook"),
            ):
                if event in events.split(","):
                    found.setdefault(chart, {}).setdefault(trait, obj)
    return found


def renderable(corpus: Path) -> set[str]:
    """Charts helm can actually render.

    131 of 1000 do not — required values, template errors, dead OCI refs. Running
    the scenario against one of those measures the corpus, not formae.
    """
    path = corpus / "results.csv"
    if not path.exists():
        return set()
    with path.open(newline="") as fh:
        return {
            row["chart"].split("/")[-1]
            for row in csv.DictReader(fh)
            if row.get("status") in ("ok", "rendered", "success")
        }


def select(corpus: Path, trait: str | None, only: str | None) -> list[Chart]:
    """Pick charts, each carrying the trait its cell tests."""
    entries = load_corpus(corpus)
    traits = hook_traits(corpus)
    ok = renderable(corpus)

    picks: list[Chart] = []
    for entry in entries:
        name = entry.get("name", "")
        if only and name != only:
            continue
        if not only and ok and name not in ok:
            continue
        chart_traits = traits.get(name, {})
        if not chart_traits:
            chart_traits = {"no-hooks": "no hook objects in the corpus inventory"}
        for chart_trait, evidence in chart_traits.items():
            if trait and chart_trait != trait:
                continue
            picks.append(
                Chart(
                    name=name,
                    ref=entry.get("chart", name),
                    repo_url=entry.get("repo_url", ""),
                    oci=entry.get("oci", "false") == "true",
                    version=entry.get("version", ""),
                    trait=chart_trait,
                    trait_evidence=evidence,
                )
            )
            break  # one cell per chart; the rarest trait it has is the reason
    return picks


INTEROP_VALUES = Path(__file__).resolve().parent / "interop-values"


def values_for(chart: Chart, corpus: Path) -> Path | None:
    """Values overrides for a chart, harness-local first.

    Two sources, because they answer different questions. The corpus `values/`
    entries exist for charts that refuse to *render* bare; the harness's own exist
    for charts that render but refuse to *install* — needing credentials, a
    license, or a storage class. Overlapping the two would mean editing another
    repo to fix a cluster problem, so the local one wins where both exist.
    """
    local = INTEROP_VALUES / f"{chart.name}.yaml"
    if local.exists():
        return local
    from_corpus = corpus / "values" / f"{chart.name}.yaml"
    return from_corpus if from_corpus.exists() else None


def previous_version(chart: Chart) -> str | None:
    """The newest version below the pinned one, to install as A.

    Read from the repo index rather than guessed by decrementing a semver
    component: charts skip patch numbers, publish prereleases, and occasionally
    reorder. A guess that resolves to a nonexistent version fails at install with
    an error about the repo, which reads like a corpus problem rather than a
    harness one.
    """
    if not chart.supports_version_bump:
        return None
    out = run(
        ["helm", "search", "repo", chart.ref, "--versions", "-o", "json"],
        check=False,
    )
    try:
        versions = [v["version"] for v in json.loads(out or "[]")]
    except json.JSONDecodeError:
        return None
    if chart.version in versions:
        after = versions[versions.index(chart.version) + 1 :]
        return after[0] if after else None
    return versions[1] if len(versions) > 1 else None


# ---------------------------------------------------------------------------
# Shelling out
# ---------------------------------------------------------------------------


class StepFailed(Exception):
    """A scenario assertion or command failed. Carries no cleanup opinion."""


class ChartUnusable(Exception):
    """The chart cannot drive the scenario, for reasons that are not formae's.

    Kept distinct from StepFailed because they need opposite handling. `ok` in the
    corpus's results.csv means the chart *renders*; it says nothing about whether
    it installs. velero renders fine and then refuses to install without a
    BackupStorageLocation carrying real cloud credentials, and 127 of the 1000
    charts do not even render. Counting those as failures would bury the formae
    failures they outnumber — a run reporting "12 failed" that a human has to
    triage chart-by-chart is a run nobody reads.

    A skip is also not evidence worth preserving: formae never got involved, so
    there is nothing in the cluster to diagnose.
    """


def run(cmd: list[str], check: bool = True, timeout: int = 900, combined: bool = False) -> str:
    """Run a command, optionally folding stderr into the result.

    combined exists because `formae apply` exits 0 while rejecting a forma and
    prints the reason on stderr. Returning stdout alone left the harness reporting
    "no command submitted; output was: " with an empty string — the rejection text,
    which was both specific and correct, went to the floor. Kept opt-in rather
    than default so the JSON readers never get stderr mixed into their input.
    """
    proc = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
    if check and proc.returncode != 0:
        raise StepFailed(f"{' '.join(cmd[:3])}… exited {proc.returncode}: {proc.stderr.strip()}")
    return proc.stdout + proc.stderr if combined else proc.stdout


def formae(*args: str, check: bool = True, combined: bool = False) -> str:
    binary = os.environ.get("FORMAE_BINARY") or shutil.which("formae")
    if not binary:
        raise StepFailed("formae binary not found; set FORMAE_BINARY")
    return run([binary, *args], check=check, combined=combined)


def formae_json(*args: str) -> dict:
    out = formae(*args, "--output-consumer", "machine", check=False)
    try:
        return json.loads(out)
    except json.JSONDecodeError:
        return {}


# ---------------------------------------------------------------------------
# Assertion oracles
# ---------------------------------------------------------------------------


def helm_release(namespace: str, release: str) -> dict | None:
    """The Helm side of the truth, straight from `helm list -o json`."""
    out = run(["helm", "list", "-n", namespace, "-o", "json"], check=False)
    try:
        for rel in json.loads(out or "[]"):
            if rel.get("name") == release:
                return rel
    except json.JSONDecodeError:
        pass
    return None


def helm_chart_version(namespace: str, release: str) -> str:
    """Version out of Helm's `chart` field, which is "<name>-<version>".

    Split on the LAST hyphen: chart names contain hyphens (kube-prometheus-stack)
    and versions do not.
    """
    rel = helm_release(namespace, release) or {}
    return str(rel.get("chart", "")).rsplit("-", 1)[-1]


def formae_resource(native_id: str) -> dict | None:
    """The formae side of the truth, keyed by native id.

    Keyed, not "the first K8S::Helm::Release in the inventory" — this runs against
    a shared cluster where other releases exist and a positional lookup would
    assert against somebody else's resource.
    """
    for res in formae_json("inventory", "resources").get("Resources", []):
        if res.get("Type") != "K8S::Helm::Release":
            continue
        if (res.get("NativeID") or res.get("Label")) == native_id:
            return res
    return None


def command_id(output: str) -> str:
    match = re.search(r"id:([A-Za-z0-9]+)", output)
    if not match:
        raise StepFailed(f"no command submitted; output was: {output.strip()[:400]}")
    return match.group(1)


def wait_command(cid: str, timeout: int = COMMAND_TIMEOUT) -> tuple[str, str]:
    """Poll a command to a terminal state. Returns (state, first error message)."""
    deadline = time.time() + timeout
    state = ""
    while time.time() < deadline:
        commands = formae_json("status", "command", f"--query=id:{cid}").get("Commands", [])
        if commands:
            state = commands[0].get("State", "")
            if state in ("Success", "Failed", "Rejected", "Canceled"):
                message = ""
                for update in commands[0].get("ResourceUpdates", []):
                    if update.get("ResourceType", "").endswith("Release"):
                        message = update.get("ErrorMessage") or ""
                        break
                return state, message
        time.sleep(3)
    raise StepFailed(f"command {cid} never finished (last state {state!r})")


def apply(work: Path, mode: str, *extra: str, expect_failure: bool = False) -> str:
    """Submit an apply and wait for it.

    A bare Rejected can mean a background sync landed between the CLI's
    pre-flight read and its submit, making the stack version it carried stale.
    That is an optimistic-concurrency conflict, not a verdict, so it is retried —
    but only when a failure was not what we were asserting.
    """
    attempts = 1 if expect_failure else 4
    for attempt in range(1, attempts + 1):
        # The forma path is positional — `formae apply [OPTIONS] <forma file>` —
        # so it goes last, after every flag.
        out = formae("apply", "--mode", mode, "--yes", *extra, check=False, combined=True)
        state, message = wait_command(command_id(out))
        if expect_failure:
            if state == "Success":
                raise StepFailed("apply succeeded but should have been refused")
            return message
        if state == "Success":
            return message
        if attempt == attempts:
            raise StepFailed(f"apply ended {state}: {message or '(no message)'}")
        time.sleep(10)
    return ""


# ---------------------------------------------------------------------------
# Forma generation
# ---------------------------------------------------------------------------

BOOTSTRAP = '''\
/// Generated by scripts/corpus-interop.py — stack and target only.
///
/// Declares no Release and no Namespace, and both omissions are load-bearing.
///
/// The Release arrives via `formae extract`, which is what makes adoption bind
/// by the right resource label.
///
/// The Namespace is deliberately left to `helm install --create-namespace`, so
/// formae never manages it. K8S::Helm::Release declares
/// `parent = "K8S::Core::Namespace"` with a listParam (helm/Release.pkl:56-61),
/// so discovery enumerates namespaces and then lists releases inside each one.
/// A namespace this forma declares becomes managed, and a managed namespace is
/// not offered back as a discovery parent — so the release inside it is never
/// listed and adoption can never start. Declaring the namespace here cost 420s
/// of waiting and a failed cell before that came out.
amends "@formae/forma.pkl"

import "@formae/formae.pkl"
import "@k8s/v{kube}/k8s.pkl" as k8s

forma {{
  new formae.Stack {{
    label = "{stack}"
    description = "corpus interop: {chart} ({trait})"
  }}

  new formae.Target {{
    label = "{stack}-target"
    config = new k8s.Config {{
      kubernetesVersion = "{kube}"
      auth = new k8s.KubeconfigAuth {{}}
    }}
  }}
}}
'''

PKL_PROJECT = '''\
/// Generated by scripts/corpus-interop.py.
///
/// Declares only the plugin's own schema as a local dependency. examples/PklProject
/// nests several, and evaluating a forma through `formae apply` against nested
/// local dependencies trips a Pkl bug (LocalDependency cannot be cast to
/// RemoteDependency).
amends "pkl:Project"

dependencies {{
  ["formae"] {{
    uri = "package://hub.platform.engineering/plugins/pkl/schema/pkl/formae/formae@{formae_pkg}"
  }}
  ["k8s"] = import("{schema}/PklProject")
}}
'''


def write_bootstrap(work: Path, chart: Chart, run_id: str, kube: str, formae_pkg: str) -> Path:
    (work / "PklProject").write_text(
        PKL_PROJECT.format(schema=REPO_ROOT / "schema" / "pkl", formae_pkg=formae_pkg)
    )
    forma = work / "bootstrap.pkl"
    forma.write_text(
        BOOTSTRAP.format(
            kube=kube,
            stack=stack_name(chart, run_id),
            chart=chart.name,
            trait=chart.trait,
        )
    )
    return forma


def repair_extracted(path: Path, version_b: str, repo_url: str) -> None:
    """Make an extracted forma appliable, and repin it to version B.

    Two fixes, both for known formae bugs rather than anything chart-specific:

    Dotted map keys. `formae extract` emits map keys as bare Pkl identifiers, so a
    chart whose values contain `identity.default.schema.json` produces a key Pkl
    rejects. Bracket-quoting is the fix. Any chart with a dotted values key hits
    this, which across the corpus is not rare.

    repoURL. Helm does not record which repository a release came from, so extract
    cannot reconstruct it. Adoption does not need it — the stored chart is reused
    while the version matches — but the version bump has to fetch, and then it is
    required.
    """
    text = path.read_text()

    text = re.sub(
        r"^(\s*)([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)+)(\s*=)",
        lambda m: f'{m.group(1)}["{m.group(2)}"]{m.group(3)}',
        text,
        flags=re.MULTILINE,
    )

    text = re.sub(r'^(\s*)version\s*=\s*".*"', rf'\1version = "{version_b}"', text, count=1, flags=re.MULTILINE)

    if repo_url and "repoURL" not in text:
        text = re.sub(
            r'^(\s*)(chart\s*=\s*".*")',
            rf'\1\2\n\1repoURL = "{repo_url}"',
            text,
            count=1,
            flags=re.MULTILINE,
        )

    path.write_text(text)


# ---------------------------------------------------------------------------
# Naming — unique per run, because the cluster is shared
# ---------------------------------------------------------------------------


def short(name: str, limit: int) -> str:
    return re.sub(r"[^a-z0-9-]", "-", name.lower())[:limit].strip("-")


def namespace_name(chart: Chart, run_id: str) -> str:
    return f"ci-{short(chart.name, 38)}-{run_id}"


def stack_name(chart: Chart, run_id: str) -> str:
    return f"ci-{short(chart.name, 38)}-{run_id}"


def release_name(chart: Chart) -> str:
    return short(chart.name, 45)


# ---------------------------------------------------------------------------
# The scenario
# ---------------------------------------------------------------------------


@dataclass
class Cell:
    chart: Chart
    run_id: str
    kube: str
    formae_pkg: str
    work: Path
    passed: list[str] = field(default_factory=list)
    failure: str | None = None
    skipped: str | None = None

    @property
    def namespace(self) -> str:
        return namespace_name(self.chart, self.run_id)

    @property
    def release(self) -> str:
        return release_name(self.chart)

    @property
    def stack(self) -> str:
        return stack_name(self.chart, self.run_id)

    @property
    def native_id(self) -> str:
        return f"{self.namespace}/{self.release}"

    def ok(self, message: str) -> None:
        self.passed.append(message)
        print(f"  ✓ {message}")

    def equals(self, label: str, expected: object, actual: object) -> None:
        if str(actual) != str(expected):
            raise StepFailed(f"{label}: expected {expected!r}, got {actual!r}")
        self.ok(f"{label}: {actual}")


def preflight(cell: Cell) -> None:
    """Refuse to start on state a previous run left behind.

    On a shared cluster a leftover namespace — especially one still Terminating —
    surfaces several minutes later as an unrelated-looking failure. Cheaper to
    refuse now with the real reason.
    """
    existing = run(["kubectl", "get", "namespace", cell.namespace, "-o", "name"], check=False)
    if existing.strip():
        raise StepFailed(f"namespace {cell.namespace} already exists; clean it up or use a new run id")
    if helm_release(cell.namespace, cell.release):
        raise StepFailed(f"release {cell.native_id} already exists")


def step_helm_install(cell: Cell, version_a: str, values: Path | None) -> None:
    """1. helm install — a release formae knows nothing about."""
    chart = cell.chart
    if not chart.oci and chart.repo_url:
        repo = chart.ref.split("/")[0]
        run(["helm", "repo", "add", repo, chart.repo_url], check=False)
        run(["helm", "repo", "update", repo], check=False)

    cmd = [
        "helm", "install", cell.release, chart.ref,
        "--version", version_a,
        "--namespace", cell.namespace, "--create-namespace",
        "--wait", "--timeout", "10m",
    ]
    if values:
        cmd += ["-f", str(values)]
    try:
        run(cmd, timeout=900)
    except (StepFailed, subprocess.TimeoutExpired) as exc:
        # Step 1 is the chart proving it can stand up at all, before formae is
        # involved. A failure here is the chart's prerequisites, not formae's
        # behaviour, so it must not be reported as a formae regression. Charts
        # needing cloud credentials, a license, or a storage class this cluster
        # lacks all land here.
        #
        # But not everything that fails at step 1 is the chart's fault. A cluster
        # that went unreachable mid-install says nothing about the chart, and
        # filing it as "does not install bare" would quietly retire a chart from
        # the matrix over a transient TLS timeout.
        text = str(exc)
        if any(sign in text for sign in (
            "cluster unreachable", "connection refused", "TLS handshake timeout",
            "no such host", "i/o timeout",
        )):
            raise StepFailed(f"cluster went away during install: {text[:300]}") from exc
        raise ChartUnusable(
            f"{chart.name} does not install bare on this cluster: {str(exc)[:300]}"
            + (f" (tried values from {values.name})" if values else
               " — no values override found in scripts/interop-values/ or the corpus")
        ) from exc

    cell.equals("helm chart version", version_a, helm_chart_version(cell.namespace, cell.release))
    cell.equals("helm revision", 1, (helm_release(cell.namespace, cell.release) or {}).get("revision"))

    # No ownership marker. The plugin stamps formae.dev/managed only on releases
    # it installs, and its absence is what makes adoption an explicit act rather
    # than a takeover.
    marker = run(
        ["kubectl", "get", "secret", "-n", cell.namespace, "-l", "owner=helm",
         "-o", r"jsonpath={.items[0].metadata.labels.formae\.dev/managed}"],
        check=False,
    ).strip()
    if marker:
        raise StepFailed("release already carries the formae ownership marker")
    cell.ok("release carries no formae ownership marker")


def step_discover(cell: Cell) -> None:
    """2. discovery — the release surfaces as unmanaged."""
    deadline = time.time() + DISCOVERY_TIMEOUT
    while time.time() < deadline:
        res = formae_resource(cell.native_id)
        if res and res.get("Stack") == "$unmanaged":
            cell.ok("discovered on the $unmanaged stack")
            cell.equals(
                "discovered version", cell.chart_version_a,
                res.get("Properties", {}).get("version", ""),
            )
            return
        time.sleep(5)
    raise StepFailed(f"{cell.native_id} never appeared as unmanaged within {DISCOVERY_TIMEOUT}s")


def step_adopt(cell: Cell, version_b: str) -> Path:
    """3. extract + apply --mode patch — adoption is a pure bind.

    patch, never reconcile: the extracted forma describes only the release, and a
    reconcile treats everything else on the stack — here the namespace — as absent
    and deletes it, taking the release down with it.

    Adoption must not move the release. The forma describes what is already live,
    so Helm is never called and the revision does not change. That is what makes
    it safe to adopt a chart with pre-upgrade hooks.
    """
    adopted = cell.work / "adopted.pkl"
    formae(
        "extract",
        "--query", f"type:K8S::Helm::Release managed:false label:{cell.native_id}",
        "--schema-location", "local", "--yes", str(adopted),
    )
    if not adopted.exists():
        raise StepFailed("extract produced no file")

    before = (helm_release(cell.namespace, cell.release) or {}).get("revision")

    # Bind first at the live version, so the patch carries no change.
    repair_extracted(adopted, cell.chart_version_a, "")
    apply(cell.work, "patch", str(adopted))

    res = formae_resource(cell.native_id)
    if not res or res.get("Stack") == "$unmanaged":
        raise StepFailed("release still unmanaged after adopt")
    cell.ok(f"adopted onto stack {res.get('Stack')}")
    cell.equals(
        "revision unchanged by adoption", before,
        (helm_release(cell.namespace, cell.release) or {}).get("revision"),
    )
    return adopted


def step_formae_upgrade(cell: Cell, adopted: Path, version_b: str) -> None:
    """4. formae upgrades the adopted release — and the marker appears.

    The marker is stamped on every formae mutation, so this is the first revision
    in a helm-first lineage to carry it. Adoption did not: no Helm call happened.
    """
    repair_extracted(adopted, version_b, cell.chart.repo_url)
    apply(cell.work, "patch", str(adopted))

    cell.equals("helm chart version after f-upgrade", version_b,
                helm_chart_version(cell.namespace, cell.release))
    cell.equals("helm revision after f-upgrade", 2,
                (helm_release(cell.namespace, cell.release) or {}).get("revision"))

    marker = run(
        ["kubectl", "get", "secret",
         "-n", cell.namespace, f"sh.helm.release.v1.{cell.release}.v2",
         "-o", r"jsonpath={.metadata.labels.formae\.dev/managed}"],
        check=False,
    ).strip()
    if marker != "true":
        raise StepFailed("formae's own upgrade did not stamp the ownership marker")
    cell.ok("ownership marker now present on the lineage")


def step_helm_rollback(cell: Cell) -> None:
    """5. helm rollback 1 — back to version A, as a new revision."""
    run(["helm", "rollback", cell.release, "1", "-n", cell.namespace, "--wait", "--timeout", "10m"],
        timeout=900)

    cell.equals("helm chart version after h-rollback", cell.chart_version_a,
                helm_chart_version(cell.namespace, cell.release))
    cell.equals("helm revision after h-rollback", 3,
                (helm_release(cell.namespace, cell.release) or {}).get("revision"))

    # Helm copies labels from the revision being restored, and revision 1 predates
    # adoption, so the marker does not come along. Harmless — the ownership guard
    # only gates a create, and formae holds a NativeID now — but asserted because
    # it is surprising and a change here would be silent.
    marker = run(
        ["kubectl", "get", "secret",
         "-n", cell.namespace, f"sh.helm.release.v1.{cell.release}.v3",
         "-o", r"jsonpath={.metadata.labels.formae\.dev/managed}"],
        check=False,
    ).strip()
    cell.ok(f"marker after rollback to a pre-adoption revision: {marker or '(absent)'}")


def step_reconcile_absorbs(cell: Cell, adopted: Path) -> None:
    """6. formae reconcile — reports the drift, does NOT correct it.

    ABSORB, by decision: an out-of-band rollback is reported and left in place;
    correcting it requires --force. The alternative — snapping back to the forma's
    version — would re-run pre-upgrade hooks to undo an operator's deliberate
    rollback, and would fire the wrong hook event for a chart with pre-rollback
    hooks, which is exactly the trait this cell selects for.

    Note formae cannot tell a rollback from an out-of-band upgrade: Read never
    surfaces the revision direction or Info.Description. Absorb is the one policy
    that does not need that distinction.
    """
    deadline = time.time() + DISCOVERY_TIMEOUT
    while time.time() < deadline:
        res = formae_resource(cell.native_id)
        if res and res.get("Properties", {}).get("version") == cell.chart_version_a:
            cell.ok("formae reports the rolled-back version, so the rollback is visible as drift")
            break
        time.sleep(5)
    else:
        raise StepFailed("formae never observed the rollback")

    message = apply(cell.work, "reconcile", str(adopted), expect_failure=True)
    if not message:
        raise StepFailed("reconcile was refused but gave no reason")
    cell.ok(f"reconcile refused rather than correcting: {message.splitlines()[0][:120]}")

    cell.equals("release untouched by the refused reconcile", cell.chart_version_a,
                helm_chart_version(cell.namespace, cell.release))
    cell.equals("no new revision from the refused reconcile", 3,
                (helm_release(cell.namespace, cell.release) or {}).get("revision"))


def assert_trait(cell: Cell) -> None:
    """The assertion that justifies this chart being the one selected.

    Without this a cell is padding: two charts running identical assertions differ
    only in how long they take. Traits with no implemented assertion say so rather
    than reporting a pass.
    """
    trait, evidence = cell.chart.trait, cell.chart.trait_evidence
    if trait in ("pre-rollback", "post-rollback"):
        kind, _, name = evidence.partition("/")
        found = run(
            ["kubectl", "get", kind.lower(), name, "-n", cell.namespace, "-o", "name"],
            check=False,
        ).strip()
        cell.ok(f"{trait} hook object {evidence}: {found or 'reaped or never created'}")
    elif trait == "test-hook":
        kind, _, name = evidence.partition("/")
        found = run(
            ["kubectl", "get", kind.lower(), name, "-n", cell.namespace, "-o", "name"],
            check=False,
        ).strip()
        if found:
            raise StepFailed(f"test-hook object {evidence} was applied; it never should be")
        cell.ok(f"test-hook object {evidence} was not applied")
    elif trait == "crd-install":
        crds = run(["kubectl", "get", "crd", "-o", "name"], check=False)
        cell.ok(f"cluster CRD count visible to this cell: {len(crds.splitlines())}")
    else:
        print(f"  – no trait assertion implemented for {trait!r}; cell proves the chain only")


def teardown(cell: Cell, keep: bool) -> None:
    """Clean up — unless the cell failed, or --keep was passed.

    A failed cell's state is the only evidence of why it failed. Deleting it in
    the same breath as reporting the failure leaves nothing to diagnose from,
    which is how the previous harness's failures became unactionable.
    """
    if keep or cell.failure:
        print(f"\n  state preserved for inspection:"
              f"\n    namespace {cell.namespace}"
              f"\n    release   {cell.native_id}"
              f"\n    stack     {cell.stack}"
              f"\n    work dir  {cell.work}")
        if cell.failure:
            events = run(
                ["kubectl", "get", "events", "-n", cell.namespace,
                 "--sort-by=.lastTimestamp", "--no-headers"],
                check=False,
            )
            tail = events.strip().splitlines()[-15:]
            if tail:
                print("\n  recent events:")
                for line in tail:
                    print(f"    {line[:160]}")
        return

    formae("destroy", "--yes", "--query", f"stack:{cell.stack}", check=False)
    run(["helm", "uninstall", cell.release, "-n", cell.namespace], check=False)
    run(["kubectl", "delete", "namespace", cell.namespace, "--wait=false"], check=False)
    shutil.rmtree(cell.work, ignore_errors=True)


def run_cell(chart: Chart, run_id: str, kube: str, formae_pkg: str, corpus: Path, keep: bool) -> Cell:
    work = Path(tempfile.mkdtemp(prefix=f"interop-{short(chart.name, 20)}-"))
    cell = Cell(chart=chart, run_id=run_id, kube=kube, formae_pkg=formae_pkg, work=work)

    print(f"\n{'=' * 72}\n{chart.name}  [{chart.trait}]\n  {TRAITS[chart.trait][0]}\n{'=' * 72}")

    try:
        version_a = previous_version(chart)
        if not version_a:
            raise StepFailed(
                f"no version below {chart.version} resolvable"
                + (" (OCI charts cannot be version-listed from the repo index)" if chart.oci else "")
            )
        cell.chart_version_a = version_a  # type: ignore[attr-defined]
        print(f"  versions: A={version_a} -> B={chart.version}")

        preflight(cell)
        values = values_for(chart, corpus)

        run(["pkl", "project", "resolve", str(REPO_ROOT / "schema" / "pkl")], check=False)
        write_bootstrap(work, chart, run_id, kube, formae_pkg)
        run(["pkl", "project", "resolve", str(work)], check=False)

        print("\n1) helm install — foreign release")
        step_helm_install(cell, version_a, values)

        print("\n2) formae bootstrap — stack and target only")
        # reconcile, not patch: the stack does not exist yet and formae refuses a
        # patch against a stack it has never seen. Safe here because the release
        # is not on this stack yet — it is still unmanaged — so there is nothing
        # for a reconcile to consider absent and delete. From adoption onward the
        # mode flips to patch, where a reconcile WOULD take the namespace out.
        apply(work, "reconcile", str(work / "bootstrap.pkl"))
        cell.ok("target and stack created")

        print("\n3) discovery")
        step_discover(cell)

        print("\n4) formae extract + adopt (--mode patch)")
        adopted = step_adopt(cell, chart.version)

        print("\n5) formae upgrade to version B")
        step_formae_upgrade(cell, adopted, chart.version)

        print("\n6) helm rollback 1")
        step_helm_rollback(cell)

        print("\n7) formae reconcile — absorb, not correct")
        step_reconcile_absorbs(cell, adopted)

        print(f"\n8) trait assertion [{chart.trait}]")
        assert_trait(cell)

    except ChartUnusable as exc:
        cell.skipped = str(exc)
        print(f"  – skipped: {exc}")
    except (StepFailed, subprocess.TimeoutExpired) as exc:
        cell.failure = str(exc)
        print(f"  ✗ {exc}", file=sys.stderr)
    finally:
        teardown(cell, keep)

    return cell


# ---------------------------------------------------------------------------
# Self-check — everything that needs no cluster
# ---------------------------------------------------------------------------


def self_check(corpus: Path) -> int:
    """Exercise the pure logic: corpus parsing, trait selection, forma repair."""
    failures = 0

    def check(label: str, got: object, want: object) -> None:
        nonlocal failures
        if got != want:
            print(f"  ✗ {label}: got {got!r}, want {want!r}")
            failures += 1
        else:
            print(f"  ✓ {label}")

    if corpus.exists():
        entries = load_corpus(corpus)
        check("charts.yaml parses to 1000 entries", len(entries), 1000)
        check("first entry is rank 1", entries[0].get("name"), "kube-prometheus-stack")
        check("oci flag is parsed as a string", entries[0].get("oci"), "false")

        traits = hook_traits(corpus)
        check("velero carries pre-rollback", "pre-rollback" in traits.get("velero", {}), True)
        check("kubedb carries pre-rollback", "pre-rollback" in traits.get("kubedb", {}), True)
        picks = select(corpus, "pre-rollback", None)
        check("pre-rollback selects at least 2 charts", len(picks) >= 2, True)
        check("every pick carries the requested trait",
              all(p.trait == "pre-rollback" for p in picks), True)
    else:
        print(f"  – corpus not at {corpus}; skipping corpus checks")

    # Version split on the LAST hyphen, because chart names contain hyphens.
    check("chart field splits to a version", "kube-prometheus-stack-88.0.1".rsplit("-", 1)[-1], "88.0.1")

    # Forma repair: dotted keys get bracket-quoted, version repinned, repoURL added.
    tmp = Path(tempfile.mkdtemp()) / "adopted.pkl"
    tmp.write_text(
        'forma {\n  new helm.Release {\n    chart = "kratos"\n    version = "0.62.1"\n'
        '    values {\n      identity.default.schema.json = "{}"\n      plain = "ok"\n    }\n  }\n}\n'
    )
    repair_extracted(tmp, "0.63.0", "https://k8s.ory.sh/helm/charts")
    fixed = tmp.read_text()
    check("dotted key bracket-quoted", '["identity.default.schema.json"]' in fixed, True)
    check("plain key untouched", "\n      plain = " in fixed, True)
    check("version repinned", 'version = "0.63.0"' in fixed, True)
    check("repoURL added", 'repoURL = "https://k8s.ory.sh/helm/charts"' in fixed, True)
    check("repoURL added exactly once", fixed.count("repoURL"), 1)

    # Idempotent: repairing twice must not double-add.
    repair_extracted(tmp, "0.63.0", "https://k8s.ory.sh/helm/charts")
    check("repair is idempotent", tmp.read_text().count("repoURL"), 1)

    # Names must be unique per run and valid as Kubernetes namespaces.
    chart = Chart(name="Kube-Prometheus-Stack", ref="x/y", repo_url="", oci=False,
                  version="1.0.0", trait="no-hooks")
    ns_a, ns_b = namespace_name(chart, "abc123"), namespace_name(chart, "def456")
    check("namespace is lowercase dns-safe", re.fullmatch(r"[a-z0-9-]+", ns_a) is not None, True)
    check("namespace is unique per run", ns_a != ns_b, True)
    check("namespace fits 63 chars", len(ns_a) <= 63, True)

    print(f"\n{'self-check passed' if not failures else f'{failures} self-check failure(s)'}")
    return 1 if failures else 0


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--corpus", type=Path,
                       default=Path(os.environ.get("HELM_CORPUS", DEFAULT_CORPUS)))
    parser.add_argument("--trait", choices=sorted(TRAITS), help="only charts carrying this trait")
    parser.add_argument("--chart", help="one chart by name, bypassing the renderable filter")
    parser.add_argument("--limit", type=int, default=1, help="how many cells to run (default 1)")
    parser.add_argument("--kube", default="1.33", help="schema version to generate against")
    parser.add_argument("--formae-pkg", default="0.88.0", help="formae pkl package version")
    parser.add_argument("--keep", action="store_true", help="never tear down, even on success")
    parser.add_argument("--list", action="store_true", help="show selected charts and exit")
    parser.add_argument("--versions", action="store_true",
                       help="with --list, resolve the A->B version pair for each chart")
    parser.add_argument("--self-check", action="store_true", help="run the no-cluster checks and exit")
    args = parser.parse_args()

    if args.self_check:
        return self_check(args.corpus)

    if not args.corpus.exists():
        print(f"corpus not found at {args.corpus}; set HELM_CORPUS", file=sys.stderr)
        return 2

    picks = select(args.corpus, args.trait, args.chart)
    if not picks:
        print("no charts matched", file=sys.stderr)
        return 2

    if args.list:
        for chart in picks[: args.limit if args.limit > 0 else None]:
            print(f"{chart.name:40s} {chart.trait:16s} {chart.trait_evidence}")
            print(f"{'':40s} {'':16s} why: {TRAITS[chart.trait][1]}")
            if args.versions:
                # Adds the repo first: `helm search repo` reads the local index,
                # so a chart from a repo that was never added looks like a chart
                # with no versions.
                if not chart.oci and chart.repo_url:
                    repo = chart.ref.split("/")[0]
                    run(["helm", "repo", "add", repo, chart.repo_url], check=False)
                    run(["helm", "repo", "update", repo], check=False)
                version_a = previous_version(chart)
                verdict = f"A={version_a} -> B={chart.version}" if version_a else "CANNOT drive the scenario"
                print(f"{'':40s} {'':16s} {verdict}")
        return 0

    run_id = uuid.uuid4().hex[:6]
    print(f"run id {run_id} — every namespace and stack carries it, so parallel runs "
          f"and a shared cluster do not collide")

    cells = [
        run_cell(chart, run_id, args.kube, args.formae_pkg, args.corpus, args.keep)
        for chart in picks[: args.limit]
    ]

    print(f"\n{'=' * 72}")
    failed = [c for c in cells if c.failure]
    skipped = [c for c in cells if c.skipped]
    for cell in cells:
        status = "FAIL" if cell.failure else "skip" if cell.skipped else "pass"
        print(f"{status:5s} {cell.chart.name:38s} [{cell.chart.trait}] "
              f"{len(cell.passed)} assertions")
        if cell.failure:
            print(f"      {cell.failure}")
        if cell.skipped:
            print(f"      {cell.skipped}")
    ran = len(cells) - len(skipped)
    print(f"\n{ran - len(failed)}/{ran} cells that ran passed"
          f"{f', {len(skipped)} skipped as unusable charts' if skipped else ''}")
    # A skip is not a failure: the chart could not drive the scenario. But an
    # all-skip run proved nothing, and silently exiting 0 would read as success.
    if failed:
        return 1
    return 3 if ran == 0 else 0


if __name__ == "__main__":
    sys.exit(main())
