import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  testMatch: 'settings-connections.spec.ts',
  timeout: 30000,
  workers: 1,
  use: { baseURL: 'http://127.0.0.1:19381', headless: true },
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
  webServer: {
    command: 'go run ../cmd/testserver -port 19381',
    url: 'http://127.0.0.1:19381',
    reuseExistingServer: false,
    timeout: 120000,
  },
})
