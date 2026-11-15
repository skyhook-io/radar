/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

export default defineConfig({
  base: './',
  plugins: [tailwindcss(), react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
      '@skyhook-io/k8s-ui': path.resolve(__dirname, '../packages/k8s-ui/src'),
      // monaco-yaml's worker (via monaco-worker-manager) imports Monaco's worker entry
      // at esm/vs/editor/editor.worker.js, a path monaco-editor's exports map does not serve.
      'monaco-editor/esm/vs/editor/editor.worker.js': 'monaco-editor/editor/editor.worker.js',
    },
  },
  server: {
    port: 9273,
    proxy: {
      '/api': {
        target: `http://localhost:${process.env.RADAR_PORT || '9280'}`,
        changeOrigin: true,
        ws: true, // WebSocket/SSE support
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // Split large vendor chunk to avoid Vite build-import-analysis parse failures
    rolldownOptions: {
      output: {
        // Groups claim modules in priority order, each taking its modules'
        // dependencies along. Vendor goes first so it claims Vite's preload
        // helper: every dynamic import calls it, and inside the monaco group
        // it would make the entry load the whole editor at startup.
        codeSplitting: {
          groups: [
            {
              name: 'vendor',
              // Trailing slashes keep react/ and react-dom/ from matching
              // react-resizable and the like; react-router covers -dom too.
              test: /node_modules\/(react\/|react-dom\/|react-router)|vite\/preload-helper/,
              priority: 30,
            },
            { name: 'ui', test: /node_modules\/(@xyflow|@xterm)\//, priority: 20 },
            // Only the editor itself. The React wrapper is imported statically
            // and must stay out, or the editor becomes a startup dependency.
            { name: 'monaco', test: /node_modules\/monaco-editor\//, priority: 10 },
          ],
        },
      },
    },
  },
  // Handle client-side routing - serve index.html for all routes
  appType: 'spa',
  test: {
    fsModuleCache: true,
    // Worktrees symlink node_modules to one checkout, so the default cache
    // location would be shared by every worktree.
    fsModuleCachePath: path.resolve(__dirname, '.vitest-cache'),
  },
})
