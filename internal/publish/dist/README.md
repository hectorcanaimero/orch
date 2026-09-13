# Generated — do not edit

This directory exists so `go:embed` in `../bundle.go` always has at least one
real file to embed, even on a fresh checkout before the stakeholder bundle has
ever been built (a `go:embed` pattern that matches zero files is a compile
error, not a runtime one).

- **This file** is the only thing tracked directly under `dist/`.
- **`stakeholder/`** is the vite `build.outDir` of
  `../../../web/vite.stakeholder.config.ts` — the compiled client-facing page
  lands there. It's gitignored and rebuilt from scratch (`emptyOutDir: true`)
  every `pnpm build:stakeholder`, which never touches this file because it
  lives one level up, outside `stakeholder/`.

This is the *second* of the two bundles `web/` produces, and they are not
interchangeable: the operator dashboard (`internal/dashboard/dist/build/`,
`web/vite.config.ts`) is served at `/` by a real server and is built with
`base: '/'`; this one is read straight off disk — a static host, a `file://`
URL, a downloaded zip — and is built with `base: './'`, so every asset
reference is relative.

Run `make web` (or `cd web && pnpm install && pnpm build:stakeholder`) to
populate `stakeholder/`. Until then, `publish.Bundle()` returns a filesystem
with nothing useful in it — see `TestBundleRequiresABuild` in
`../bundle_test.go`.
