import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

// Separate config file, not a second entry in vite.config.ts's build —
// the two bundles need incompatible `base` values that Vite applies
// per-build, not per-entry. The operator dashboard (vite.config.ts) is
// mounted at `/` by a real server and needs `base: '/'`; this bundle is
// `orch publish`'s output — G6.2 — read straight off disk (a static host,
// or literally opened as a file) with data.json sitting next to it, so
// every asset reference has to be relative or it resolves against
// whatever origin/path happens to be hosting it (or nothing, under
// file://).
//
// `publicDir` points at a bundle-local folder (not the operator
// dashboard's `public/`) so its dev-only example data.json never leaks
// into the operator SPA's build, and vice versa.
export default defineConfig(() => ({
  base: './',
  plugins: [react(), tailwindcss()],
  publicDir: path.resolve(__dirname, './src/stakeholder/public'),
  build: {
    // Settled in G6.3, not a placeholder any more: internal/publish/
    // bundle.go embeds `dist` and roots it at dist/stakeholder, the same
    // shape internal/dashboard/spa.go uses for the other bundle. go:embed
    // cannot reach outside its own package directory, which is why this
    // path points here rather than at a web/dist. `make web` runs BOTH
    // builds; dist/README.md (one level up, untouched by emptyOutDir)
    // keeps the package compiling before either has run.
    outDir: '../internal/publish/dist/stakeholder',
    emptyOutDir: true,
    rollupOptions: {
      input: path.resolve(__dirname, './stakeholder.html'),
    },
  },
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    port: 5174,
  },
}))
