# orch-cloud — the hosted stakeholder viewer (G8.1)

`orch publish --to cloud` puts the client-facing page on a Cloudflare Worker
**you deploy to your own Cloudflare account**. There is no shared orch
service: no sign-up, nobody else holding your clients' data, nothing to pay
for beyond your own Cloudflare usage (the free tier covers a stakeholder page).

- The Worker lives in its own repository: **`<orch-cloud repository URL — filled in when the Worker lands>`**.
- The stakeholder link is `https://<your worker>/v/<view token>/`.
- orch keeps the Worker's URL and tokens in `~/.orch/credentials` (mode `0600`).

## Operator walkthrough

> The Worker-side commands below are placeholders until the orch-cloud
> repository is published; the orch side is final.

**1. Deploy the Worker** (once per operator):

```bash
git clone <orch-cloud repository URL> && cd orch-cloud
npm ci
npx wrangler login --device          # or CLOUDFLARE_API_TOKEN for a scoped token
npx wrangler deploy                  # prints https://orch-cloud.<you>.workers.dev
ADMIN_TOKEN=$(openssl rand -hex 32)
printf %s "$ADMIN_TOKEN" | npx wrangler secret put ADMIN_TOKEN
```

**2. Point orch at it** (once per machine). The token is piped, never typed —
a prompt would echo it:

```bash
printf %s "$ADMIN_TOKEN" | orch cloud login --url https://orch-cloud.<you>.workers.dev
```

**3. Publish** (from a project):

```bash
orch publish --to cloud              # first time: creates the project, prints the link
orch publish --to cloud --watch      # re-publish while `orch run` works
```

**4. Manage the link:**

```bash
orch cloud status                    # URL, reachability, which tokens are stored — never the tokens
orch cloud rotate                    # new stakeholder link; the old one stops working at once
orch cloud rotate --publish          # re-issue the project's publish token (leaked CI token, lost credentials)
orch cloud logout                    # forget the Worker on this machine; published pages keep serving
```

**CI:** publish one project with only its publish token — no admin token, no
credentials file:

```bash
ORCH_CLOUD_URL=https://orch-cloud.<you>.workers.dev \
ORCH_CLOUD_PUBLISH_TOKEN=<that project's publish token> \
orch publish --to cloud
```

A CI publish cannot print the stakeholder link: the view token stays with the
operator (`orch cloud status` shows it).

## What orch does on a publish

1. Exports the site exactly as `--to dir` does, into a fresh temporary
   directory (never reused: the Worker serves the exact file set it receives).
2. Reads it back and checks the contract's limits **before any request** — a
   site the Worker would refuse fails here, and never leaves a registered
   project behind.
3. If `~/.orch/credentials` has no publish token for the project, creates the
   project with the admin token and saves both tokens **before** uploading
   (the file is proven writable first; the Worker cannot show them again).
4. `PUT`s the site. An identical upload (same `digest`) is a no-op.

**The upload digest is over the files, not over the snapshot.** `--watch`
compares snapshots (ignoring `generated_at`) to decide whether to publish at
all; once it does, the digest sent to the Worker is SHA-256 over every path and
the SHA-256 of its bytes. So an orch upgrade that ships a new stakeholder
bundle over unchanged data is a new site, a manual re-publish moves the page's
freshness stamp (as `--to git` does), and a retried upload is still a no-op.

Project ids are orch's project id lowercased and must match
`^[a-z0-9][a-z0-9-]{0,62}$`; anything else is refused, not rewritten
(`--project-id` publishes under another id).

---

# HTTP contract — v1

Shared by the Worker (repo `orch-cloud`) and the Go client (`internal/publish/cloud.go`).
Change it in both places or neither. `internal/publish/cloudfake` is an
in-memory implementation of it that orch's tests run against.

## Model

- **Self-hosted per operator.** Each operator deploys their own `orch-cloud` Worker to
  their own Cloudflare account. There are no user accounts; there is one operator.
- **Three kinds of secret, each a random 32-byte hex string (64 chars):**
  - **admin token** — a Worker secret (`ADMIN_TOKEN`, set with `wrangler secret put`).
    Creates and deletes projects and re-issues publish tokens. Lives on the operator's
    machine only.
  - **publish token** — one per project. Uploads that project's site and rotates its
    view token. Safe to hand to a CI job for that project alone.
  - **view token** — one per project, the only secret in the stakeholder URL
    `https://<worker>/v/<view_token>/`. Rotating it kills the old link.
- **The Worker stores only SHA-256 hex digests of publish and view tokens**, never the
  tokens. Comparing digests is the constant-time comparison (fixed length).
- Tokens are returned exactly once, in the response that creates them.
- The view URL carries no project id: a leaked link reveals one project and nothing
  about the others.

## Storage (one KV namespace, binding `ORCH`)

| Key | Value |
| --- | --- |
| `project:<id>` | JSON `{"id","publish_hash","view_hash","created_at","updated_at","site_version"}` |
| `view:<view_hash>` | the project id |
| `manifest:<id>` | JSON `{"version":<int>,"digest":"<client digest>","files":{"<path>":{"blob":"<sha256>","type":"<mime>","size":<int>}}}` |
| `blob:<sha256>` | the file bytes (content-addressed; identical files across versions share a key) |

A site upload writes every blob first and the manifest last, so a reader sees the old
site or the new one, never a mix. Old blobs are not garbage-collected in v1.

## Project ids

`^[a-z0-9][a-z0-9-]{0,62}$` — orch's project id, lowercased; anything else is `400`.

## Errors

Every non-2xx API response is JSON `{"error":"<code>","message":"<human sentence>"}`.
Codes: `unauthorized` (401: missing/wrong token), `not_found` (404), `conflict` (409),
`invalid` (400), `too_large` (413), `method_not_allowed` (405).

Viewer routes (`/v/...`) never return JSON and never distinguish "bad token" from
"missing file": both are the same plain `404 Not Found`.

## Endpoints

All API requests authenticate with `Authorization: Bearer <token>`.

### `GET /api/v1/health` — no auth
`200 {"ok":true,"version":"<orch-cloud version>","api":1}`

### `GET /api/v1/whoami` — admin
`200 {"role":"admin"}` — lets `orch cloud login` verify the admin token before saving it.

### `POST /api/v1/projects` — admin
Body `{"id":"<project id>"}`.
`201 {"id","publish_token","view_token"}`. `409 conflict` if the project exists.

### `DELETE /api/v1/projects/<id>` — admin
Deletes the project, its view key and its manifest. `204`. `404` if absent.

### `POST /api/v1/projects/<id>/publish-token` — admin
Issues a new publish token; the old one stops working. `200 {"publish_token"}`.

### `POST /api/v1/projects/<id>/view-token` — publish token of that project
Issues a new view token; the old URL stops working at once. `200 {"view_token"}`.

### `PUT /api/v1/projects/<id>/site` — publish token of that project
Body (JSON, at most 10 MiB):
`{"digest":"<client content digest>","files":{"<relative path>":"<base64 bytes>", ...}}`

`digest` is opaque to the Worker. orch sends a digest of the file set (SHA-256 over each path and the SHA-256 of its bytes), not of the snapshot alone: a new stakeholder bundle over unchanged data must still upload.

- Paths: relative, `/`-separated, no empty, `.` or `..` segment, no leading `/`,
  at most 200 files, at most 5 MiB per file. `index.html` must be present.
- Content-Type is derived by the Worker from the extension (html, js, css, json, svg,
  png, ico, txt, webmanifest, woff2; anything else `application/octet-stream`).
- If `digest` equals the stored manifest's digest, nothing is written:
  `200 {"changed":false,"version":<n>}`.
- Otherwise `200 {"changed":true,"version":<n+1>}`.

`publish --watch` already skips unchanged snapshots client-side; the server check makes
a retry after a timeout harmless.

### `GET /v/<view_token>/` and `GET /v/<view_token>/<path>`
Serves the project's current site; `/v/<token>/` is `index.html`. `/v/<token>` (no
trailing slash) redirects `308` to `/v/<token>/` so relative asset URLs resolve.

Headers on every viewer response:
- `Referrer-Policy: no-referrer` — the token is in the URL; it must not leak to any
  link the page contains.
- `X-Robots-Tag: noindex, nofollow`
- `X-Content-Type-Options: nosniff`
- `Cache-Control: no-store` for `index.html`, `data.json`, `data.js`;
  `public, max-age=31536000, immutable` for paths under `assets/`;
  `no-cache` for everything else.

### `GET /robots.txt`
`User-agent: *\nDisallow: /\n`

## Consistency

Workers KV is eventually consistent: a publish can take up to about 60 seconds to be
visible from every location. The plan's "a state change shows up in under two minutes"
still holds.

## Client side (orch)

- Credentials: `~/.orch/credentials`, mode `0600`, JSON:
  `{"cloud":{"url":"https://…","admin_token":"…","projects":{"<id>":{"publish_token":"…","view_token":"…"}}}}`
  Other top-level blocks are preserved on save.
- Env overrides for CI: `ORCH_CLOUD_URL` and `ORCH_CLOUD_PUBLISH_TOKEN` (publish only;
  no admin token needed, no credentials file written). Both or neither: one without
  the other is refused rather than mixed with the credentials file.
- `orch publish --to cloud` exports the site exactly as `--to dir` does (same
  `publish.Export`, same `--token`-less layout: the view token plays that role), then
  uploads the directory with `PUT …/site`. First publish of a project with no stored
  publish token creates the project with the admin token and stores both tokens.
  Prints the viewer URL.
- The client refuses a plain-`http` Worker URL unless the host is loopback
  (`wrangler dev`): every request carries a bearer token.
