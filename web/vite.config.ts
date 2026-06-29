import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

export default defineConfig({
  plugins: [tailwindcss(), react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
      '@skyhook-io/k8s-ui': path.resolve(__dirname, '../packages/k8s-ui/src'),
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
        manualChunks(id: string) {
          if (!id.includes('node_modules/')) return

          // Trailing slashes on react/ and react-dom/ prevent matching react-router, react-resizable, etc.
          const chunks: Record<string, string[]> = {
            vendor: ['react/', 'react-dom/', 'react-router'],
            monaco: ['monaco-editor/', '@monaco-editor/'],
            ui: ['@xyflow/', '@xterm/'],
          }

          for (const [chunk, prefixes] of Object.entries(chunks)) {
            if (prefixes.some((p) => id.includes(`node_modules/${p}`))) {
              return chunk
            }
          }
        },
      },
    },
  },
  // Handle client-side routing - serve index.html for all routes
  appType: 'spa',
})
