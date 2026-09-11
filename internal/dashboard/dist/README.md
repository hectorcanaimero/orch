# Generated — do not edit

This directory exists so `go:embed` in `../spa.go` always has at least one
real file to embed, even on a fresh checkout before the SPA has ever been
built (a `go:embed` pattern that matches zero files is a compile error, not
a runtime one).

- **This file** is the only thing tracked directly under `dist/`.
- **`build/`** is `web/`'s vite `build.outDir` (see `../../../web/vite.config.ts`)
  — the real compiled SPA lands there. It's gitignored and rebuilt from
  scratch (`emptyOutDir: true`) every `pnpm build`, which never touches this
  file because it lives one level up, outside `build/`.

Run `make build` (or `cd web && pnpm install && pnpm build`) to populate
`build/`. Until then, `dashboard.SPA()` returns a filesystem with nothing
useful in it — see `TestSPARequiresABuild` in `../spa_test.go`.
