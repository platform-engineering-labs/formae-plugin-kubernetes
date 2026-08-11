# Helm stability: what a crash costs

What happens to a `K8S::Helm::Release` when the thing driving it dies, and what
that means for the forma command and for whoever is watching it.

```bash
make helm-stability-test                                  # every scenario
make helm-stability-test TEST=TestPluginSigtermMidInstall # one
make helm-stability-test SAMPLES=10                       # + measure the drain
```

Runs on every PR via `.github/workflows/helm-stability-pr.yml`, and nightly.

## Why a crash is a problem at all

Every other provider formae talks to has a server-side async API: you submit, you
get an operation id, and the provider carries on without you. A stateless plugin
is free there — `Status` just asks the remote API.

Helm has no such thing. The install runs **inside the plugin process**, so when
that process dies the work dies with it, and Helm's `pending-install` status is
left behind as a lock with no owner. Helm then refuses **both install and
upgrade** on that release. Its documented way out is `helm uninstall`, which
destroys the objects and re-runs `pre-install` hooks to get back to where it
already was.

And this is the ordinary case, not an exotic one:

```
formae agent stop
  → SIGTERM to the agent                        internal/agent/agent.go:277
  → supervisor SendExitMeta to the plugin port   plugin_process_supervisor.go:534
  → port calls cmd.Process.Kill() = SIGKILL      ergo meta/port.go:144
```

The plugin gets SIGKILL with no chance to unwind. So recovery cannot happen on
the way down; it has to happen on the next operation.

## The guarantee

Whatever died, three things hold. These are what the suite asserts, and they are
the contract to rely on:

1. **The command reaches a verdict.** It never sits in `InProgress` reporting
   progress on work that nothing is doing.
2. **A failure says why**, naming the specific object that is missing.
3. **The next apply converges** — no operator, no `helm uninstall`.

## How recovery decides

Helm writes `deployed` **last**, after the hooks have run and every object
exists:

```
Releases.Create(pending-install) → hooks → create objects → SetStatus(deployed)
```

Dying anywhere in that middle stretch leaves an identical record whether the work
finished or never started. So recovery inspects the cluster rather than trusting
the record:

| Objects the release renders | Record becomes | Why |
|---|---|---|
| All present and ready | `deployed` | The install *did* finish; only the final write was lost. Reported as success — no second Helm operation, no hooks re-run |
| Anything missing | `failed` | An upgrade is allowed to run over `failed`, three-way merging what is absent |

Getting this backwards is expensive, which is why it is checked rather than
assumed: marking a finished install `failed` sends it through an upgrade, and
re-runs `pre-upgrade` hooks — on a chart like kratos, a second database
migration.

"Is an operation still running?" is a fact, not a guess: Helm operations for
formae run in exactly one process, so if this plugin is not running one, nothing
is. No timeout is involved and recovery is immediate. A release this plugin is
*actively* installing is never touched, however long its hooks take.

## Scenarios, and what the user sees

| Scenario | Release ends | Forma command | What the user does |
|---|---|---|---|
| **`TestAgentCrashMidInstall`**<br>agent SIGKILLed mid-install | `failed`, then `deployed` after re-apply | **Failed**, with `objects are incomplete (ConfigMap/x is absent)` | Re-apply. Converges to `deployed`, revision 2 |
| **`TestAgentRestartAfterInstall`**<br>agent restarted after install finished | `deployed`, revision **1** | **No command submitted** — "No changes needed" | Nothing. The re-apply is a genuine no-op, and the `pre-install` hook does not re-run |
| **`TestPluginSigtermMidInstall`**<br>`SIGTERM` direct to the plugin | `failed` | Failed | Re-apply. Upgrades over it to `deployed` |
| **`TestPluginSigkillMidInstall`**<br>`SIGKILL` direct to the plugin | `pending-install` until the next apply | Failed, **with no message** (see below) | Re-apply. Clears the lock and completes the install |
| **`TestDrainWinRate`**<br>`SIGTERM` × N | — | — | Reports how often the drain wins its race. Skipped unless `SAMPLES` is set |

Every scenario additionally asserts, at teardown, that the release is **not left
holding a Helm lock** — no test can pass while leaving one wedged.

### What this means in practice

- **A crash never requires `helm uninstall`.** The worst case costs one re-apply.
- **A crash may cost a revision.** If the install had not finished, recovery
  marks it `failed` and the next apply upgrades over it, so you land on revision
  2 rather than 1. The objects are correct either way.
- **A completed install is never redone.** If everything was already applied, the
  re-apply runs no Helm operation and no hooks fire again. This is the case that
  matters for charts whose hooks are migrations.
- **Someone else's release is never touched.** A stuck release this plugin did
  not install is reported with the recovery command in the message, and left
  alone.
- **An interrupted hook leaves its Job behind.** See below — this is Helm's
  behaviour, not the plugin's, and an interrupted `helm install` from the CLI does
  the same.

### Hook residue after a crash

Helm deletes a hook only on an outcome. From `pkg/action/hooks.go`, all three
deletions live inside `execHook`:

```
 61  deleteHookByPolicy(h, HookBeforeHookCreation)   before creating the hook
103  deleteHookByPolicy(h, HookFailed)               the hook failed
127  deleteHookByPolicy(h, HookSucceeded)            the hook succeeded
```

Kill the process while a hook is running and `execHook` reaches none of them, so
the Job and its Pod survive. Helm's design assumes this: line 58 defaults every
hook to `before-hook-creation`, meaning stale hooks are cleaned when that hook is
next *created* rather than reaped eagerly.

The catch is that "next created" means the **same** hook. Recovery here is an
upgrade, which runs `pre-upgrade` hooks — so:

| Chart's hook | After a crash |
|---|---|
| `pre-install` only | The Job persists. The recovery upgrade never re-creates it, so it is cleaned only by a fresh install of that release |
| `pre-install,pre-upgrade` (e.g. kratos' migration) | The recovery upgrade deletes the stale Job via `before-hook-creation` and re-runs it |

The suite logs surviving hook objects rather than failing on them, because
matching Helm here is correct and overriding it would mean deleting evidence — a
half-run migration Job holds the logs someone may need. Worth knowing if you run
charts with `pre-install`-only hooks: a crash can leave a completed-or-partial
hook Job with nothing owning it.

## Two limitations, and why they exist

**A plugin crash defers recovery to the next apply.** The agent's PluginOperator
runs on the plugin's node and dies with it, so the agent's ResourceUpdater sees
silence (`PluginOperatorMissingInAction`), fails the resource update, and never
calls `Status` again. The plugin is therefore never asked to recover, and the
release stays `pending-install` until something applies. Nothing in the plugin
can change this — it is dead at the moment the decision is made.

**That failure carries no message.** It comes from formae's
`pluginOperationMissingInAction`, which calls `MarkAsFailed()` without a reason,
so the user sees a failed command with an empty diagnostic. Worth fixing
upstream; not fixable here.

### The drain, and its race

A `SIGTERM` sent *directly* to the plugin is handled: in-flight operations are
cancelled, so Helm runs `failRelease` and records `failed` instead of leaving
`pending-install`. That is one Secret write, and it turns a wedge into a plain
upgrade.

It is best-effort. The SDK installs its own signal handler and calls
`node.Stop()`, and Go delivers to every `signal.Notify` receiver concurrently
with no ordering hook — so the record write races node teardown. **Losing costs
nothing**: the outcome is then what it was before the drain existed, and the next
apply recovers it anyway.

`TestDrainWinRate` measures the rate and fails only below 50%. Under that, the
drain is not buying what it claims and a pre-stop hook on the SDK's `RunConfig`
stops being a nice-to-have.

## Why this is not in `pkg/`

`pkg/resources/helm/stability_integration_test.go` covers the same decisions by
calling `Create` twice in one process. That is a good test of the logic and a bad
test of the behaviour, because the three things that actually have to work are
not exercised by it:

- a **real agent** re-driving or re-polling a **real command**,
- a **real signal** reaching the plugin process,
- the **drain** racing the SDK's node teardown.

None of those is a Go function you can call twice. This suite kills processes.

The in-package tests stay where they are because they reach unexported internals
(`planSubmit`, `abandoned`, `recoverAbandoned`, `fingerprint`); moving them here
would mean exporting all of that purely so tests could reach it.

## Safety

This suite sends signals, so isolation is load-bearing rather than tidy.

- **Its own profile** (`helm-stability-ci`) with **its own SQLite file**, wiped
  per run. Every scenario ends with a command interrupted on purpose, and formae
  persists those — an `InProgress` row from the last run would refuse this run's
  first apply with "another command is already working on the same resources".
- **The plugin PID is the child of this suite's own agent**, never a name match.
  `formae agent start` carries no distinguishing arguments, so
  `pgrep -f "formae.*agent start"` matches every agent on the machine equally.
  Fine for reading, fatal for signalling.
- **Orphaned plugins are reaped** at suite start, after each crash, and on exit.
  Not tidiness: an orphan keeps its Ergo node name registered, so the next
  agent's replacement plugin dies with `unable to register node: resource is
  taken`, the supervisor exhausts `MaxPluginRestarts`, and that namespace is left
  with no plugin at all. One leaked orphan breaks the next run — and any other
  formae work on the machine. This is the cleanup Linux gets from `Pdeathsig` and
  macOS does not.
- **Namespaces prefixed `formae-helm-stability-`**, and cleanup matches only that
  prefix. Stacks likewise, one per scenario, so an interrupted command in one
  cannot block another's apply.
- **Discovery is on**, because off is a configuration nobody runs. Discovery
  lists releases, which builds the release inventory, which takes the plugin's
  read path through a release that is pending or wedged on purpose — exactly the
  combination a crash produces in the field. A suite that switched it off would
  be proving recovery works in a setup the customer does not have.
- **Sync is off.** Unlike discovery it is not a read path through the release: it
  writes its own view, which turns every assertion about a deliberately
  mid-operation release into a race.
- **The profile is global CLI state** in formae (`formae profile use`, not a
  flag), so this suite cannot run alongside other formae work. The runner script
  restores the previous profile on exit.

## Platform differences

The crash scenarios behave differently on Linux and macOS, and the suite reads
which happened off the machine rather than off `runtime.GOOS`:

| | Agent SIGKILLed |
|---|---|
| **Linux** (`Pdeathsig: SIGKILL`) | The kernel kills the plugin with the agent. The install dies mid-flight; recovery marks the release `failed` and the next apply upgrades over it |
| **macOS** (no `Pdeathsig`) | The plugin is orphaned onto pid 1 and finishes the install. Recovery recognises the completed state and runs no second operation |

Both are legitimate and both must converge, which is why the assertion is the
invariant rather than the route. A restarted agent never reattaches to a
surviving plugin on either platform — it spawns a fresh one — so an interrupted
operation is never *resumed*; it is recognised as done, or redone.

## Environment

| Variable | Default | |
|---|---|---|
| `FORMAE_BINARY` | `formae` on `PATH` | |
| `STABILITY_PROFILE` | `helm-stability-ci` | Agent profile name |
| `STABILITY_KUBE_VERSION` | `1.33` | Schema version the generated forma imports |
| `STABILITY_FORMAE_PKL` | `0.88.1` | formae Pkl package version |
| `STABILITY_DRAIN_SAMPLES` | unset | Samples for `TestDrainWinRate` (`SAMPLES=` via make) |
| `STABILITY_KEEP` | unset | Leave namespaces behind for inspection |
| `GO_TEST_TIMEOUT` | `60m` | |

The suite skips rather than fails when `formae`, `kubectl` or a cluster is
missing.
