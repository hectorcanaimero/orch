# Releasing `orch`

Two release lines share this repo's tag namespace, distinguished by tag
suffix rather than prefix (see the note on why, below).

| Tag shape | Builds | Workflow |
|---|---|---|
| `v*-py` | The archived Python wheel | `.github/workflows/release.yml` **on the `python-legacy` branch** |
| `v*` (no suffix) | The Go binary | `.github/workflows/release-go.yml` |

Since G7.5 `main` has no Python tree and no `release.yml`. A tag push runs the
workflows of the commit it points at, so a `-py` tag cut from `python-legacy`
still builds with that branch's own `release.yml`; nothing on `main` is
involved.

## Why a suffix, not a prefix

The Python line froze at `v0.11.0-py` (ADR-G0) — that tag already exists,
already has a release attached to it, and is what `python-legacy` points
at. Continuing the Python series with a `-py` suffix keeps that tag's
own shape as the pattern (`v0.11.0-py`, `v0.11.1-py`, …) rather than
inventing a second one after the fact. The Go binary picks up the bare
`v*` series where Python's numbering left off before the freeze — the
next tag is `v0.12.0`, not `v1.0.0` — so `git tag --list 'v*' --sort=v:refname`
still reads as one continuous version history for the project, with the
freeze point visible as the moment `-py` tags start appearing alongside
it rather than the numbering resetting.

A distinct prefix (`go-v*`) was considered and rejected: one product,
one series. The suffix says "this is the frozen line," which is the
thing actually worth calling out — a Go release isn't a parallel product,
it's what the numbering was always going to become.

**`.github/workflows/release.yml`'s trigger is `tags: 'v*-py'`,
`release-go.yml`'s is `tags: 'v[0-9]*'` — and release-go.yml's job also
checks `!endsWith(github.ref, '-py')` explicitly at runtime.** The glob
alone doesn't exclude `-py` tags (`v0.11.0-py` matches `v[0-9]*` too,
since `*` matches the suffix); the runtime check is the real guard against
both workflows firing on the same tag. If you ever see both fire on one
push, that check has a bug — file it as a bug in this doc, not a reason
to trust the glob alone next time.

## Cutting a Go release

```bash
git tag v0.12.0
git push origin v0.12.0
```

Pushing the tag triggers `release-go.yml`, which:

1. Runs `make web` (builds the operator SPA `internal/dashboard` embeds —
   skipping this ships a binary whose dashboard is the placeholder, not a
   real one).
2. Cross-compiles `linux`/`darwin` × `amd64`/`arm64` (four binaries, no
   cgo — `modernc.org/sqlite` is pure Go) via `goreleaser`.
3. Archives each as a `.tar.gz`, computes `checksums.txt`, and attaches
   both to a new GitHub Release under that tag.
4. Pushes an updated Homebrew formula to
   `github.com/hectorcanaimero/homebrew-orch` — **that repo has to exist
   first**; goreleaser pushes a commit to it, it doesn't create it. Not
   created by this PR (creating a new public repo under the maintainer's
   GitHub account is a real, visible action outside a PR's scope) — do
   this once, by hand, before the first real `v*` tag:

   ```bash
   gh repo create hectorcanaimero/homebrew-orch --public \
     --description "Homebrew tap for orch"
   ```

   Also needs a `HOMEBREW_TAP_GITHUB_TOKEN` repo secret — a PAT with
   `contents:write` on that tap repo specifically; the default
   `GITHUB_TOKEN` this workflow otherwise uses only has permissions on
   *this* repo. `.goreleaser.yaml` passes it as `brews.repository.token`.

   **Until that secret exists the tap push is skipped** (`skip_upload`
   is templated on it), so a release still publishes its GitHub archives
   and `install.sh` works; `brew install` does not until a release runs
   with the secret set. `v0.12.0` shipped this way.

Nothing publishes on an ordinary push or PR — only an actual `v*` tag
push triggers a real release. Every other event (PR, push to `main`)
only runs the dry-run job in `.github/workflows/go.yml`
(`goreleaser release --snapshot --clean`, which skips every publish
step — no GitHub release, no Homebrew push, no checksums uploaded
anywhere) so a broken `.goreleaser.yaml` or build fails a PR check
immediately instead of at the next real tag.

## Testing locally

```bash
goreleaser release --snapshot --clean
```

Builds every OS/arch pair, archives, and writes the Homebrew formula
under `dist/`, all without touching GitHub or the tap repo. `dist/` is
gitignored (the same `/dist/` rule the Python wheel build uses).

## `install.sh`

`scripts/install.sh` (also served at a stable URL for `curl | sh` once a
release exists) detects OS/arch, downloads the matching tarball from the
latest `v*` GitHub Release, and installs the binary to
`~/.local/bin` (or `$INSTALL_DIR` if set). See its own header comment for
the exact resolution order.

## G7.2 — the clean-install smoke test

`release-go.yml`'s `smoke-install` job (`needs: goreleaser`) runs after
a real release publishes: on a bare `ubuntu-latest` runner with no Go
toolchain installed at all, it downloads that release's Linux amd64
tarball (the same way `install.sh` would), extracts it, and runs
`orch init` → `orch validate` → `orch doctor` against a scratch project —
proving the shipped binary actually works standalone, not just that it
compiled.
