import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [tailwindcss(), sveltekit()],
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      // Dev mode: proxy /api and /healthz to the Go server. Run canopy
      // serve --db <path> --addr :8765 alongside `pnpm dev`.
      '/api': { target: 'http://localhost:8765', changeOrigin: true },
      '/healthz': { target: 'http://localhost:8765', changeOrigin: true },
    },
  },
});
