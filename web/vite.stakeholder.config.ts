import { defineConfig, type Plugin } from 'vite'
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
//
// The bundle is ONE classic script, not an ES module (#293). `orch publish
// --to dir` promises a page that renders when a client double-clicks
// index.html, and Chromium refuses `<script type="module">` and any
// `crossorigin` request from a file:// page (its origin is null), leaving it
// blank. So rollup emits an IIFE with no code splitting, and the plugin below
// takes the module/crossorigin attributes vite always writes off the tags.
// `defer` keeps what module gave for free: the script runs after #root
// exists, and after the data.js tag orch publish puts in front of it.
const classicScript: Plugin = {
  name: 'orch-classic-script',
  transformIndexHtml: {
    order: 'post',
    handler: (html) =>
      html
        .replace(/<script type="module" crossorigin/g, '<script defer')
        .replace(/ crossorigin(?=[\s>])/g, ''),
  },
}

export default defineConfig(() => ({
  base: './',
  plugins: [react(), tailwindcss(), classicScript],
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
    // No modulepreload polyfill: there is nothing to preload in one file.
    modulePreload: false,
    // Without this an IIFE build folds the CSS into the JS, injected at run
    // time: a flash of unstyled page, and font URLs no longer beside the CSS.
    cssCodeSplit: false,
    rollupOptions: {
      input: path.resolve(__dirname, './stakeholder.html'),
      output: { format: 'iife' as const, inlineDynamicImports: true },
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
