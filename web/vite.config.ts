import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { VitePWA } from 'vite-plugin-pwa'
import path from 'path'

export default defineConfig({
  plugins: [
    tailwindcss(),
    react(),
    VitePWA({
      registerType: 'prompt',
      manifest: {
        name: 'Radar',
        short_name: 'Radar',
        description: 'Real-time Kubernetes cluster management dashboard',
        theme_color: '#0f172a',
        background_color: '#0f172a',
        display: 'standalone',
        scope: '/',
        start_url: '/',
        icons: [
          { src: '/icons/icon-192x192.png', sizes: '192x192', type: 'image/png' },
          { src: '/icons/icon-512x512.png', sizes: '512x512', type: 'image/png' },
          { src: '/icons/icon-512x512-maskable.png', sizes: '512x512', type: 'image/png', purpose: 'maskable' },
        ],
      },
      workbox: {
        globPatterns: ['**/*.{js,css,html,ico,png,svg,woff2}'],
        runtimeCaching: [{
          urlPattern: /\/api\/.*/i,
          handler: 'NetworkFirst',
          options: {
            cacheName: 'radar-api-cache',
            networkTimeoutSeconds: 10,
            expiration: { maxEntries: 200, maxAgeSeconds: 5 * 60 },
            cacheableResponse: { statuses: [0, 200] },
          },
        }],
      },
      devOptions: {
        enabled: process.env.SW_DEV === 'true',
      },
    }),
  ],
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
