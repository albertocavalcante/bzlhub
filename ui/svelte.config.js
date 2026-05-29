import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
const config = {
  preprocess: vitePreprocess(),
  kit: {
    // SPA mode: every request to the Go server that isn't /api/* or BCR
    // falls through to index.html, which boots the client router.
    adapter: adapter({
      fallback: 'index.html',
      pages: 'build',
      assets: 'build',
      precompress: false,
      strict: false,
    }),
    // Match the API host when the UI runs in `pnpm dev`. The Go server
    // serves the UI in production; in dev we proxy /api to it.
    alias: {
      $api: 'src/lib/api',
      $components: 'src/lib/components',
    },
  },
};

export default config;
