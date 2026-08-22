# Sprint E-5 — Tunnel Manager (spec)

> Status: **draft** · Source: `docs/design/sprint-e-5-tunnel-manager.md` · Mode: file (engram unavailable)

Delta spec against the current dashboard behavior. Assumes the reader has already read the proposal for context on intent, scope, and rollback.

---

## Domain: `dashboard/tunnel` (NEW)

### ADDED Requirements

#### Requirement: TUN-1 — Tunnel config schema

The dashboard MUST accept a `tunnel:` section in `dashboard.yaml` with the following fields and defaults:

| Field | Type | Default | Validation |
|-------|------|---------|------------|
| `enabled` | bool | `false` | required |
| `provider` | string | `"autossh"` | MUST be in provider allowlist (see TUN-2) |
| `command` | string | `"autossh"` | MUST match `provider`'s pinned binary (see TUN-2) |
| `args` | list[string] | provider default | forwarded verbatim to subprocess |
| `auto_start` | bool | `false` | see TUN-9 |
| `url_regex` | string | provider default | compiled once at load |
| `startup_probe_timeout_s` | int | `3` | 1–30 |
| `url_parse_timeout_s` | int | `30` | 5–300 |

If `enabled` is absent the whole section MUST be treated as `enabled:false`. Any field failing validation MUST prevent dashboard startup with a clear stderr message pointing at the offending key. (Proposal §Scope, §Risks row 2.)

##### Scenario: valid config loads

- GIVEN `dashboard.yaml` contains a valid `tunnel:` block with `provider:autossh`
- WHEN the dashboard boots
- THEN startup succeeds
- AND `/api/tunnel/*` routes are registered

##### Scenario: unknown provider rejected

- GIVEN `tunnel.provider: ngrok` (not on allowlist)
- WHEN the dashboard boots
- THEN startup fails with exit code non-zero
- AND stderr names `tunnel.provider` as invalid

#### Requirement: TUN-2 — Provider allowlist

The dashboard MUST maintain a hard-coded provider registry. In v1, the registry MUST contain exactly one entry: `autossh`. The registry MUST bind each provider to a pinned binary name (`command`) and a default URL-extraction regex. When loading config the dashboard MUST reject any `command` value that does not equal the pinned binary for the declared `provider`. (Proposal §Risks row 2.)

##### Scenario: command override blocked

- GIVEN `tunnel.provider: autossh` and `tunnel.command: rm`
- WHEN the dashboard boots
- THEN startup fails
- AND stderr states `tunnel.command must equal "autossh" for provider "autossh"`

#### Requirement: TUN-3 — Three-layer gate order

Every request to `/api/tunnel/*` MUST pass three gates in this fixed order, short-circuiting on the first failure:

| Order | Gate | Failure response |
|-------|------|------------------|
| 1 | Config: `tunnel.enabled` is true | `404 Not Found` |
| 2 | Profile: caller has `operator` profile token | `403 Forbidden` |
| 3 | Host: request `Host` header host equals `127.0.0.1`, `localhost`, or `[::1]` (port ignored) | `403 Forbidden` |

The dashboard MUST NOT consult `X-Forwarded-For` or any proxy header when evaluating gate 3. Documentation MUST state that reverse-proxy fronting is unsupported for tunnel control. (Proposal §Approach, resolved decision 2.)

`GET /api/tunnel/capabilities` MUST evaluate all three gates but MUST always respond `200` with a JSON body reporting which gates passed (see TUN-4).

##### Scenario: config gate closed

- GIVEN `tunnel.enabled: false`
- WHEN any client calls `GET /api/tunnel/status`
- THEN the response is `404`

##### Scenario: profile gate rejects stakeholder

- GIVEN `tunnel.enabled: true` AND caller presents a `stakeholder` token from loopback
- WHEN the client calls `POST /api/tunnel/start`
- THEN the response is `403`
- AND no subprocess is spawned

##### Scenario: host gate rejects tunnel-origin operator

- GIVEN `tunnel.enabled: true` AND caller presents a valid `operator` token
- AND the request `Host` header is `abc123.a.pinggy.link`
- WHEN the client calls `POST /api/tunnel/start`
- THEN the response is `403`
- AND `/api/tunnel/capabilities` for the same caller reports `can_control: false` with `reason: "host_gate"`

#### Requirement: TUN-4 — `GET /api/tunnel/capabilities`

The endpoint MUST return `200` with body:

```
{
  "enabled": bool,
  "provider": string | null,
  "can_control": bool,
  "reason": "ok" | "config_disabled" | "profile_gate" | "host_gate" | "autossh_missing"
}
```

`can_control` MUST be `true` only when all three gates pass AND the provider binary is present on `PATH`. `reason` MUST report the FIRST failing check in gate order (config → profile → host → binary). (Proposal §Success Criteria bullet 3, §Risks row 3.)

##### Scenario: missing binary surfaces

- GIVEN `tunnel.enabled: true`, valid operator token, loopback host, AND `autossh` not on PATH
- WHEN the client calls `GET /api/tunnel/capabilities`
- THEN response `200` with `can_control: false` AND `reason: "autossh_missing"`

#### Requirement: TUN-5 — `GET /api/tunnel/status`

The endpoint MUST return `200` with body:

```
{
  "state": "idle" | "starting" | "running" | "stopping" | "error",
  "url": string | null,
  "pid": int | null,
  "started_at": ISO-8601 | null,
  "restart_count": int,
  "last_error": string | null
}
```

`state` transitions MUST follow: `idle → starting → running → stopping → idle`, with `error` reachable from `starting` or `running`. `url` MUST be `null` until URL extraction succeeds (see TUN-7) and MUST persist for the lifetime of the current `running` state. On transition to `idle` all volatile fields (`url`, `pid`, `started_at`) MUST reset to `null` / `0` while `restart_count` and `last_error` MUST persist for post-mortem.

##### Scenario: idle after fresh boot

- GIVEN dashboard just booted with no prior tunnel state
- WHEN the client calls `GET /api/tunnel/status`
- THEN body reports `state: "idle"`, `url: null`, `pid: null`

#### Requirement: TUN-6 — `POST /api/tunnel/start` and `/stop`

`POST /api/tunnel/start` MUST return `202 Accepted` when it successfully transitions state from `idle` to `starting` and spawns the subprocess. When called while state is `starting` or `running` it MUST return `409 Conflict` with body `{"error":"already_running","state":<current>}`. It MUST NOT be idempotent — the operator gets an explicit conflict.

`POST /api/tunnel/stop` MUST return `202 Accepted` when it transitions state from `running` or `starting` to `stopping` and signals the subprocess. When called while state is `idle` it MUST return `409 Conflict` with body `{"error":"not_running"}`. Stop signaling MUST use `SIGTERM` first and MUST escalate to `SIGKILL` after 5 s if the process has not exited.

##### Scenario: double start rejected

- GIVEN state is `running`
- WHEN client calls `POST /api/tunnel/start`
- THEN response is `409` with `error: "already_running"`
- AND no additional subprocess spawns

##### Scenario: stop while idle rejected

- GIVEN state is `idle`
- WHEN client calls `POST /api/tunnel/stop`
- THEN response is `409` with `error: "not_running"`

##### Scenario: happy path start

- GIVEN operator on loopback, `enabled:true`, `autossh` present, state `idle`
- WHEN client calls `POST /api/tunnel/start`
- THEN response is `202`
- AND within `startup_probe_timeout_s + url_parse_timeout_s` seconds `GET /api/tunnel/status` reports `state:"running"` with a non-null `url`

#### Requirement: TUN-7 — URL extraction contract

The manager MUST tail subprocess stdout line-by-line, applying the provider's compiled URL regex to each line. On first match the manager MUST update in-memory `status.url` to the captured group and leave `state` at `running`. If no match occurs within `url_parse_timeout_s` seconds after the process reports listening (or after spawn if no listen signal exists) the manager MUST populate `status.last_error` with `"url_parse_timeout"` while keeping `state:"running"` and `url:null`. Regex compile failure at config load MUST fail dashboard startup (see TUN-1). (Proposal §Risks row 5.)

##### Scenario: URL extracted from stdout

- GIVEN autossh subprocess writes `https://abc123.a.pinggy.link` to stdout
- WHEN the manager reads that line
- THEN `status.url` is set to `https://abc123.a.pinggy.link`
- AND subsequent `GET /api/tunnel/status` returns that URL

##### Scenario: URL never appears

- GIVEN subprocess is `running` but writes no URL-matching line for `url_parse_timeout_s`
- WHEN the timeout elapses
- THEN `status.last_error` equals `"url_parse_timeout"`
- AND `status.url` remains `null`
- AND `status.state` remains `"running"`

#### Requirement: TUN-8 — Lock and PID lifecycle

The manager MUST maintain a lock file at `state/tunnel/lock` and a PID file at `state/tunnel/pid`. Acquiring the lock MUST be a precondition for `POST /start`; failure to acquire (lock held by another process) MUST yield `409` with `error:"locked"`. On successful spawn the child PID MUST be written to the PID file atomically. On process exit (observed or via `SIGCHLD`) the manager MUST remove both files and reset `state → idle`.

At dashboard startup the manager MUST run a reconciler:

1. If `state/tunnel/pid` exists AND that PID is alive AND its exe basename matches the allowlisted `command` → adopt as `running`.
2. Otherwise (PID missing, dead, or mismatched exe) → remove `pid` and `lock`, reset state to `idle`, and increment nothing.

##### Scenario: zombie recovery

- GIVEN `state/tunnel/pid` contains PID 12345 AND PID 12345 is not running
- WHEN the dashboard boots
- THEN the reconciler deletes `state/tunnel/pid` AND `state/tunnel/lock`
- AND `GET /api/tunnel/status` reports `state:"idle"`

#### Requirement: TUN-9 — `auto_start:true` boot sequence

When `tunnel.enabled:true` AND `tunnel.auto_start:true`, the dashboard MUST perform this sequence at boot:

1. Bind uvicorn to configured host/port.
2. Begin serving.
3. Self-probe: issue `GET /` against `http://127.0.0.1:<port>/` with timeout `startup_probe_timeout_s`.
4. On probe success → invoke the same code path as `POST /api/tunnel/start`.
5. On probe failure → log an error `auto_start_skipped: self_probe_failed`, do NOT spawn the tunnel, leave state `idle`. Dashboard startup itself MUST NOT fail.

The tunnel subprocess MUST NOT be spawned before step 3 completes. (Proposal §Open questions #4, resolved decision 4.)

##### Scenario: auto-start after successful probe

- GIVEN `enabled:true`, `auto_start:true`, port `7420` free
- WHEN the dashboard boots
- THEN uvicorn serves before any tunnel spawn
- AND self-probe `GET http://127.0.0.1:7420/` returns `200`
- AND within `url_parse_timeout_s` `status.state` transitions to `running`

##### Scenario: auto-start skipped on probe failure

- GIVEN `enabled:true`, `auto_start:true`, AND app root returns `500` during self-probe
- WHEN the dashboard boots
- THEN no tunnel subprocess is spawned
- AND stderr contains `auto_start_skipped: self_probe_failed`
- AND `status.state` remains `"idle"`

#### Requirement: TUN-10 — `GET /api/tunnel/logs` SSE stream

The endpoint MUST be gated identically to `POST /api/tunnel/start` (config → operator profile → loopback host). It MUST emit an SSE stream where each `data:` frame is one line of tailed subprocess stdout/stderr, most recent 200 lines replayed on connect then live-tailed. When the subprocess is not running the stream MUST still open and emit only the replay buffer, ending with a `event: idle` frame. Known token patterns (`Bearer <hex>`, `token=<hex>`) MUST be redacted with `***REDACTED***` before emission. (Proposal §Risks row 6, resolved decision 3.)

##### Scenario: log stream refused from tunnel origin

- GIVEN operator token AND `Host: abc123.a.pinggy.link`
- WHEN client opens `GET /api/tunnel/logs`
- THEN response is `403`
- AND no SSE frames are emitted

#### Requirement: TUN-11 — `orch doctor` extension

`orch doctor` MUST report two new checks:

| Check | PASS condition | FAIL condition |
|-------|----------------|----------------|
| `tunnel.config` | `dashboard.yaml → tunnel` absent OR validates per TUN-1/TUN-2 | any validation error (message from TUN-1) |
| `tunnel.binary` | `tunnel.provider` provider's pinned binary is on PATH, OR `tunnel.enabled:false`, OR section absent | binary missing AND `enabled:true` |

`tunnel.binary` MUST report `WARN` (not `FAIL`) when `enabled:false` and the binary is missing, so users can prep config before installing the binary.

##### Scenario: doctor flags missing binary

- GIVEN `tunnel.enabled:true` AND `autossh` not on PATH
- WHEN user runs `orch doctor`
- THEN `tunnel.binary` reports `FAIL` with remediation string mentioning `autossh`
- AND exit code is non-zero

---

## Non-Functional Requirements

### Requirement: TUN-NFR-1 — Test suite green

The existing ~680 pytest suite MUST remain green. New tests MUST cover, at minimum:

- Three-gate matrix (config × profile × host, all 8 combinations for at least one endpoint).
- Allowlist rejection at config load (unknown provider, mismatched command).
- URL parsing: happy match, no-match timeout, regex compile error.
- Zombie recovery: stale-PID cleanup on boot.
- `auto_start` sequencing: probe success spawns, probe failure skips.
- `/logs` SSE gating parity with `/start`.
- `orch doctor` new checks.

### Requirement: TUN-NFR-2 — No new runtime deps

The orch Python package MUST NOT gain a new runtime dependency for this feature. `autossh` is a host-side binary check only. Any subprocess management MUST use stdlib (`subprocess`, `signal`, `os`, `asyncio`) already available.

### Requirement: TUN-NFR-3 — Rollback via config

Setting `tunnel.enabled:false` (default) MUST fully disable the feature: routes return `404`, no subprocess management runs, no reconciler runs, no SPA panel renders. No code path added by this sprint may execute when disabled beyond the config gate itself.

---

## Cross-references to proposal

- Config schema, allowlist: proposal §Scope, §Risks rows 2, §Rollback Plan.
- Three-layer gate + rationale: proposal §Approach, §Success Criteria bullets 2–4.
- URL extraction risk: proposal §Risks row 5.
- Lock/PID + zombie recovery: proposal §Approach, §Risks row 4.
- SSE gating decision: proposal §Open questions #3 (resolved: identical gating).
- `auto_start` sequencing: proposal §Open questions #4 (resolved: bind → serve → probe → spawn).
- `orch doctor`: proposal §Dependencies, §Success Criteria bullet 5.
- Reverse-proxy exclusion: proposal §Open questions #2 (resolved: unsupported in v1, no XFF).

---

## Open questions (blocking `sdd-tasks`? — NO)

None of the below block task breakdown; all can be resolved at design time.

1. **Reconciler exe-basename check on macOS vs Linux** — `/proc/<pid>/exe` doesn't exist on macOS; design must pick a cross-platform PID→exe check (`psutil` is a dep concern; `ps -p <pid> -o comm=` is portable). Flag for `sdd-design`.
2. **SSE replay buffer bound** — TUN-10 specifies 200 lines; design should decide whether it lives in-memory only or is backed by `state/tunnel/stdout.log` (proposal names the file but doesn't specify rotation).
3. **Signal delivery to autossh child ssh** — `SIGTERM` to autossh may leave the underlying `ssh` child. Design must decide process-group handling (mirror the killpg pattern from Sprint A).
4. **Concurrent `/status` reads during state transitions** — design must specify the concurrency primitive (asyncio `Lock` vs threadsafe `RLock`) protecting the state dict.

---

## Envelope

- **status**: `draft`
- **artifacts**: `docs/design/sprint-e-5-tunnel-manager-spec.md`
- **next_recommended**: `sdd-design` (resolve the 4 open questions), then `sdd-tasks`
- **risks**: If reverse-proxy exclusion (TUN-3) is later relaxed, TUN-3 host-gate scenarios must be re-specified with trusted-proxy allowlist semantics — a breaking spec change, not additive.
