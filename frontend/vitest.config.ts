import { defineConfig } from 'vitest/config';

// Unit tests here cover the framework-agnostic logic modules
// (collage planning, tus priority queue). DOM/Angular-heavy services are
// exercised by scripts/e2e.sh and manual testing.
export default defineConfig({
  test: {
    environment: 'node',
    include: ['src/**/*.spec.ts'],
    globals: true,
  },
});
