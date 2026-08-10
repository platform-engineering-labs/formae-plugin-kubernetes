# Crash recovery for `K8S::Helm::Release`

What happens to a Helm release when the thing driving it dies.

```bash
make install
make helm-stability-test                                  # the four scenarios
make helm-stability-test TEST=TestPluginSigtermMidInstall # one
make helm-stability-test SAMPLES=10                       # + measure the drain
```

## Why this is not in `pkg/`

`pkg/resources/helm/stability_integration_test.go` covers the same four
cases by calling `Create` twice in one process. That is a good test of the
decision logic and a bad test of the behaviour, because the three things that
actually have to work are not exercised by it:

- a **real agent** re-driving a **real command**,
- a **real signal** reaching the plugin process,
- the **drain** racing the SDK's node teardown.

None of those is a Go function you can call twice. This suite kills processes.

## The problem being tested

Helm runs inside the plugin process, which is a separate OS process from the
agent. The agent does not *resume* an interrupted operation, it *re-drives* it —
`ReRunIncompleteCommands` resets an in-flight resource update to `NotStarted` and
calls `Create` again from scratch, discarding the RequestID
(`formae/internal/metastructure/metastructure.go:1262`). So an agent restart
usually leaves the install goroutine running and hands it a duplicate `Create`.

## What each scenario proves

| Test | Kills | Asserts |
|---|---|---|
| `TestAgentRestartMidInstall` | agent, mid-install | The duplicate `Create` rejoins the running install. Command succeeds, **revision stays 1** — a second revision means it upgraded over its own in-flight install. |
| `TestAgentRestartAfterInstall` | agent, after install | Re-applying the identical forma runs no Helm operation. **Revision stays 1 and the pre-install hook does not re-run** — on a chart like kratos that hook is a database migration. |
| `TestPluginSigtermMidInstall` | plugin, `SIGTERM` | The release lands **`failed`**, not `pending-install`, and a plain re-apply recovers it with no operator involved. |
| `TestPluginSigkillMidInstall` | plugin, `SIGKILL` | The documented floor: release stays `pending-install`, the command **fails rather than claiming success**, and the message names `helm uninstall`. |
| `TestDrainWinRate` | plugin, `SIGTERM` × N | How often the drain actually wins its race. Off unless `SAMPLES` is set. |

The `SIGKILL` case sets `timeoutSeconds = 20` so the stall window — twice the
release's own timeout — is something a test can wait out. At the 600s default
the same assertion would take twenty minutes. That the window is now derived
from the release rather than a package constant is what makes it testable at
all.

### The drain measurement

The `SIGTERM` drain cancels in-flight operations so Helm records `failed`
instead of leaving `pending-install`. It is best-effort: the SDK installs its
own signal handler and calls `node.Stop()`, and Go delivers to every
`signal.Notify` receiver concurrently with no ordering hook. So the
release-record write is in a footrace with node teardown.

`TestDrainWinRate` runs the scenario N times and reports the rate. **Losing is
not a failure** — the outcome then is exactly what it was before the drain
existed — so it only fails if the rate falls below 50%. Below that, the drain
is not buying what it claims to and a pre-stop hook on the SDK's `RunConfig`
stops being a nice-to-have.

## Safety

This suite sends signals, so isolation is load-bearing rather than tidy.

- **Its own profile** (`helm-stability-ci`) with **its own SQLite file**. The suite
  restarts the agent on purpose to watch what `ReRunIncompleteCommands` does
  with the rows left behind; a shared datastore would mean re-driving somebody
  else's commands too.
- **The plugin PID is resolved as a child of this suite's own agent**, never by
  name. Several agents can run on one machine — a developer's, CI's, another
  suite's — and they all spawn a process from the same
  `~/.pel/formae/plugins/k8s/<version>/k8s` path, so `pkill -f k8s` would take
  out unrelated work.
- **Namespaces are prefixed `formae-helm-stability-`** and cleanup matches only that
  prefix.
- Sync and discovery are **off** in the profile. Every scenario asserts on a
  release that is deliberately mid-operation or deliberately wedged, and a
  background pass writing its own view of that release turns each assertion into
  a race.

## Environment

| Variable | Default | |
|---|---|---|
| `FORMAE_BINARY` | `formae` on `PATH` | |
| `STABILITY_PROFILE` | `helm-stability-ci` | Agent profile name |
| `STABILITY_KUBE_VERSION` | `1.33` | Schema version the generated forma imports |
| `STABILITY_FORMAE_PKL` | `0.88.1` | formae Pkl package version |
| `STABILITY_DRAIN_SAMPLES` | unset | Samples for `TestDrainWinRate` |
| `STABILITY_KEEP` | unset | Leave namespaces behind for inspection |
| `GO_TEST_TIMEOUT` | `60m` | |

The suite skips rather than fails when `formae`, `kubectl` or a cluster is
missing.
