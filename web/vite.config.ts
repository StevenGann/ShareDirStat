/// <reference types="vitest/config" />
import { writeFileSync } from 'node:fs';
import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';

/**
 * Keeps web/dist/.gitkeep in place after a build. The Go binary embeds the
 * directory with `go:embed all:dist`, which refuses to compile when the
 * directory is empty; the tracked .gitkeep is what makes a checkout without
 * a UI build still compile. emptyOutDir would otherwise delete it.
 */
function keepGitkeep(): Plugin {
  return {
    name: 'keep-gitkeep',
    closeBundle() {
      writeFileSync('dist/.gitkeep', '');
    },
  };
}

// base './' makes asset URLs relative so the same build works under any
// server.base_path; the app uses hash routing (spec §10.1 FR-UI-03).
export default defineConfig({
  plugins: [react(), keepGitkeep()],
  base: './',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    target: 'es2022',
  },
  server: {
    port: 5173,
    // During `npm run dev` the Go server runs on :8080 and serves the API.
    proxy: { '/api': 'http://127.0.0.1:8080' },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/setupTests.ts'],
    globals: true,
  },
});
