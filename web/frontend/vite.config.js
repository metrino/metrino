import { resolve } from 'node:path'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// Two entry pages rather than a single-page app: the Go server decides who may see /admin/ by path, so the
// admin page has to be its own file at admin/index.html. Built assets land in /assets/ and are readable by
// every group member; only /api/admin/ is admin-only, and the server enforces that.
export default defineConfig({
  plugins: [vue()],
  build: {
    rollupOptions: {
      input: {
        docs: resolve(import.meta.dirname, 'index.html'),
        admin: resolve(import.meta.dirname, 'admin/index.html'),
      },
    },
  },
  server: {
    // `npm run dev` against the compose stack (`docker compose up web`). In Kubernetes oauth2-proxy sets
    // X-Forwarded-Email; here the proxy does, as whoever DEV_EMAIL names (any user in test-users.json, e.g.
    // `DEV_EMAIL=analyst@example.com npm run dev`). Only the dev server does this; it is not in the build.
    proxy: {
      '/api': {
        target: 'http://localhost:8000',
        headers: { 'X-Forwarded-Email': process.env.DEV_EMAIL || 'admin@example.com' },
      },
    },
  },
})
