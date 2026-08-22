import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

// https://vite.dev/config/
//
// `base` is conditional so dev + prod each Just Work:
//   - `pnpm dev`   → base = "/"     (Vite dev server serves at :5173/)
//   - `pnpm build` → base = "/spa/" (compiled bundles are served by
//                                    FastAPI at http://127.0.0.1:7420/spa/)
//
// The router's basename is derived from `import.meta.env.BASE_URL` in
// src/App.tsx so BrowserRouter picks up whatever Vite decided — no second
// place to keep in sync.
export default defineConfig(({ command }) => ({
  base: command === 'build' ? '/spa/' : '/',
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    port: 5173,
  },
}))
