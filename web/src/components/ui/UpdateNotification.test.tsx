import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderToString } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'

let deploymentMode = 'local'
let installMethod = 'direct'
let manualUpdate: 'appimage' | 'package' | 'download' | undefined

vi.mock('../../api/client', () => ({
  useCapabilities: () => ({ data: { deployment: { mode: deploymentMode } } }),
  useVersionCheck: () => ({
    data: {
      currentVersion: '1.2.3',
      latestVersion: '1.3.0',
      updateAvailable: true,
      installMethod,
      manualUpdate,
      releaseUrl: 'https://github.com/skyhook-io/radar/releases/tag/v1.3.0',
      // Off, so the Product Hunt launch day doesn't take the corner whatever today is.
      productHuntLaunch: { enabled: false, url: '', start: '0001-01-01T00:00:00Z', end: '0001-01-01T00:00:00Z' },
    },
  }),
  useStartDesktopUpdate: () => ({ mutate: vi.fn(), isPending: false }),
  useDesktopUpdateStatus: () => ({ data: undefined }),
  useApplyDesktopUpdate: () => ({ mutate: vi.fn() }),
}))

import { UpdateNotification } from './UpdateNotification'
import { ToastProvider } from './Toast'

function renderNotification() {
  const client = new QueryClient()
  return renderToString(
    <QueryClientProvider client={client}>
      <ToastProvider><UpdateNotification /></ToastProvider>
    </QueryClientProvider>,
  )
}

describe('UpdateNotification', () => {
  beforeEach(() => {
    deploymentMode = 'local'
    installMethod = 'direct'
    manualUpdate = undefined
  })

  it('keeps the update popup for local installations', () => {
    const html = renderNotification()
    expect(html).toContain('Update Available')
    expect(html).toContain('1.3.0')
  })

  it('offers the in-app update to desktop installs that can replace themselves', () => {
    installMethod = 'desktop'
    const html = renderNotification()
    // The in-app update button plus the dismiss button.
    expect(html.match(/<button/g)).toHaveLength(2)
    expect(html).not.toContain('href="https://github.com/skyhook-io/radar/releases/tag/v1.3.0"')
  })

  it.each(['appimage', 'package', 'download'] as const)('links %s desktop installs to the release instead of updating in place', how => {
    installMethod = 'desktop'
    manualUpdate = how
    const html = renderNotification()
    expect(html).toContain('href="https://github.com/skyhook-io/radar/releases/tag/v1.3.0"')
    // Only the dismiss button: no in-app update.
    expect(html.match(/<button/g)).toHaveLength(1)
  })

  it('suppresses the update popup for shared in-cluster viewers', () => {
    deploymentMode = 'in-cluster'
    expect(renderNotification()).toBe('')
  })
})
