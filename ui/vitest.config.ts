import { defineConfig } from 'vitest/config';
import { fileURLToPath } from 'node:url';

// Resolve `$lib` to ui/src/lib so unit tests can import via the same
// alias the SUT uses; SvelteKit owns the alias in dev/build but
// vitest needs its own entry.
const lib = fileURLToPath(new URL('./src/lib', import.meta.url));

export default defineConfig({
  resolve: {
    alias: {
      $lib: lib,
    },
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.{test,spec}.ts'],
  },
});
