# Telemetry (G8.6 / F4.9)

`orch` never phones home unless you turn it on. This page is the complete,
exact list of what one usage ping carries when you do — kept in step with
`internal/telemetry.Event`; if this list and that struct ever disagree, the
struct is the bug.

---

## Off by default

Nothing is sent unless a project's own `.orchestrator/config.yaml` has:

```yaml
telemetry:
  enabled: true
```

There is no global, machine-wide switch — enabling it is a per-project
decision, the same as every other `config.yaml` key.

## `DO_NOT_TRACK` always wins

Regardless of `telemetry.enabled`, setting the standard
[`DO_NOT_TRACK`](https://consoledonottrack.com) environment variable turns
telemetry off:

```bash
export DO_NOT_TRACK=1
```

Its mere presence is enough — any value other than `0` opts out, matching
every other tool that honours this variable. A project cannot re-enable
telemetry against an operator's own `DO_NOT_TRACK`; the environment always
overrides `config.yaml`, never the other way around.

## Exactly what one event carries

One JSON object, sent once per `orch` invocation, over HTTPS, as soon as
the command finishes:

| Field | Type | What it is |
|---|---|---|
| `install_id` | string | A random id generated once per machine/user account (see below) — identifies an **installation**, never a person or a project. |
| `version` | string | The `orch` binary's own version (`orch --version`). |
| `os` | string | `runtime.GOOS` — e.g. `linux`, `darwin`. |
| `arch` | string | `runtime.GOARCH` — e.g. `amd64`, `arm64`. |
| `command` | string | Which subcommand ran, e.g. `status`, `run`, `dashboard token rotate`. Never its arguments or flag values. |
| `duration_ms` | integer | How long the invocation took, in milliseconds. |
| `success` | boolean | Whether it exited 0. |

**That is the whole list.** Never sent, on principle, not as an oversight:

- A file path, a project id, or a project name.
- Anything from `tasks.json` — task ids, titles, models, specs.
- Prompt content, agent output, or log lines.
- Flag values or command arguments (only which subcommand ran).
- IP address, hostname, or anything else the collector wasn't explicitly
  handed above (an HTTP request necessarily has a source IP; this package
  puts nothing extra there beyond the JSON body).

## The install id

Generated once and stored at `~/.orch/install_id` (mode `0600`) the first
time telemetry actually sends an event. 16 random bytes, hex-encoded — not
a UUID, not derived from your machine's hardware or hostname, not a
secret. It exists so a collector can tell "the same install reported N
events" from "N different installs reported once each" — nothing more.
Delete the file and a new id is generated next time; nothing else changes.

## Where it goes

`telemetry.endpoint` in `config.yaml` overrides the built-in default. Most
projects never need to set it — it exists for a self-hosted collector.

```yaml
telemetry:
  enabled: true
  endpoint: "https://your-collector.example/collect"  # optional
```

## Failure is always silent

A telemetry send that fails — network down, collector unreachable,
timeout — never surfaces as an error, never changes the command's exit
code, and never prints anything. The command you ran already finished by
the time a report is even attempted; telemetry riding on its result is a
side effect, not a dependency.

## Implementation

`internal/telemetry` is the whole implementation — `Reporter.Report` builds
the `Event` above and POSTs it as JSON. `internal/cli.Run` calls it once
per invocation, after the command has already returned, with the decision
(`telemetry.enabled` AND NOT `DO_NOT_TRACK`) already resolved. See that
package's own doc comment for the field-for-field reasoning.
