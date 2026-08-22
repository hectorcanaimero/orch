import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

// https://vite.dev/config/
//
// `base = "/"` for both dev and prod — the SPA is served at the root of
// the FastAPI dashboard (http://127.0.0.1:7420/) since we removed the
// legacy Jinja UI. Dev server on :5173/ mirrors the same base.
export default defineConfig(() => ({
  base: '/',
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
