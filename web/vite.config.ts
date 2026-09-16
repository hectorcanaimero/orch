import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

// https://vite.dev/config/
//
// `base = "/"` for both dev and prod — the SPA is served at the root of
// the FastAPI dashboard (http://127.0.0.1:7420/) since we removed the
// legacy Jinja UI. Dev server on :5173/ mirrors the same base.
//
// `build.outDir` points OUTSIDE this package, straight into
// internal/dashboard/dist/build — `go:embed` (internal/dashboard/spa.go)
// can only reach files inside its own package directory, so this is the
// one config knob that makes `pnpm build` here and `go build`/`go test`
// over there agree on where the compiled SPA lives, with no separate
// `go:generate` copy step to keep in sync. `emptyOutDir: true` is safe to
// set because the nested `dist/build` subdirectory is vite's alone —
// `dist/README.md`, one level up, is the file that keeps the parent
// `internal/dashboard/dist/` non-empty (and go:embed-able) before this
// has ever run; vite never touches it. See
// docs/brainstorm/go-migration-notes/sonnet-2.md for the alternative
// (copying web/dist via go:generate) and why this was preferred.
export default defineConfig(() => ({
  base: '/',
  plugins: [react(), tailwindcss()],
  build: {
    outDir: '../internal/dashboard/dist/build',
    emptyOutDir: true,
  },
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    port: 5173,
    proxy: {
      // Proxy /api requests to the FastAPI backend during dev
      '/api': {
        target: 'http://127.0.0.1:7420',
        changeOrigin: true,
      },
      // Proxy /logs/stream (SSE) to the FastAPI backend
      '/logs/stream': {
        target: 'http://127.0.0.1:7420',
        changeOrigin: true,
      },
    },
  },
}))
