# Architecture diagrams

Five diagrams of the Go binary, authored with archify (a Claude Code skill)
as small typed JSON specs and rendered to self-contained interactive HTML.
The rendered pages are published with the project site:
<https://hectorcanaimero.github.io/orch/architecture/> (sources for the HTML:
`site/architecture/`).

| Diagram | Type | Spec | Page |
| --- | --- | --- | --- |
| Packages of the binary | architecture | [`packages.architecture.json`](packages.architecture.json) | [`site/architecture/packages.html`](../../site/architecture/packages.html) |
| Data through a project | dataflow | [`dataflow.dataflow.json`](dataflow.dataflow.json) | [`site/architecture/dataflow.html`](../../site/architecture/dataflow.html) |
| Dispatching one task | sequence | [`dispatch.sequence.json`](dispatch.sequence.json) | [`site/architecture/dispatch.html`](../../site/architecture/dispatch.html) |
| A task's lifecycle | lifecycle | [`task-lifecycle.lifecycle.json`](task-lifecycle.lifecycle.json) | [`site/architecture/task-lifecycle.html`](../../site/architecture/task-lifecycle.html) |
| Where the client page lives | architecture | [`publish.architecture.json`](publish.architecture.json) | [`site/architecture/publish.html`](../../site/architecture/publish.html) |

The diagram text is in Spanish; archify does not translate its own viewer
controls, which stay in English.

## Where the facts come from

Every node and edge traces to [`FACTS.md`](FACTS.md), which was written from
the code on `main` at `723351e`, not from the migration plan:

- the package graph from `go list` (internal imports only) — which is how the
  diagrams know that `internal/engine` imports neither `router` nor `notify`;
- task statuses and legal transitions from `internal/model/status.go`;
- the dispatch order from `internal/engine/scheduler.go` (`Refill`,
  `spawnOne`) and `internal/engine/reaper.go`;
- the publish destinations and the `orch-cloud` contract from
  `internal/publish` and `docs/CLOUD.md`.

**When the code changes, these go stale silently.** Update `FACTS.md` first,
then the spec, then re-render.

## How they were checked

Each page passed, on the exact bytes committed here:

- `archify validate <type> <spec> --quality showcase` — 9/9 artifact checks,
  0 composition errors, 0 warnings;
- `archify visual-check` in a real Chromium at 1440×900, 1600×1000, 1920×1080
  and 2048×1320, light and dark — no overflow, text above the readability
  floor;
- a review of the screenshots, which found three factual errors the automated
  checks could not: `internal/mcp` drawn as read-only (it writes state — it is
  how agents report), `SetTaskPR` drawn as a call to `gh` (it writes
  `orch.db`), and a route crossing in the publish diagram.

## Re-rendering

```bash
cd <archify skill directory>
node bin/archify.mjs validate architecture docs/architecture/packages.architecture.json --quality showcase --json
node bin/archify.mjs deliver  architecture docs/architecture/packages.architecture.json site/architecture/packages.html --quality showcase --json
```

`visual-check` needs a Chrome; on a machine without one, the Playwright image
works unmodified:

```bash
docker run --rm --shm-size=1g --user "$(id -u):$(id -g)" -e HOME=/tmp \
  -e ARCHIFY_CHROME=/ms-playwright/chromium-1194/chrome-linux/chrome \
  -e ARCHIFY_CHROME_NO_SANDBOX=1 \
  -v <archify skill directory>:/archify:ro -v "$PWD/site/architecture":/out \
  mcr.microsoft.com/playwright:v1.56.1-noble \
  node /archify/bin/archify.mjs visual-check /out/packages.html --json
```
