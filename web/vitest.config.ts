import { defineConfig } from 'vitest/config'
import path from 'path'

// Separate from vite.config.ts on purpose: that file's `build.outDir`
// points outside this package (see its own comment), and `server.proxy`
// only makes sense for the dev server — neither belongs in a unit-test
// config. The one thing tests actually need from it, the `@/` alias, is
// duplicated here rather than imported, since the two configs' shapes
// (`defineConfig` from `vite` vs from `vitest/config`) aren't the same
// type.
export default defineConfig({
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  test: {
    // jsdom for everything, render tests included — a plain unit test
    // (no DOM touched) runs the same either way, so one environment for
    // the whole suite is simpler than switching per file.
    environment: 'jsdom',
    setupFiles: ['./vitest.setup.ts'],
  },
})
