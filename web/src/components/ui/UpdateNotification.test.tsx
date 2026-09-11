import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderToString } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'

let deploymentMode = 'local'

vi.mock('../../api/client', () => ({
  useCapabilities: () => ({ data: { deployment: { mode: deploymentMode } } }),
  useVersionCheck: () => ({
    data: {
      currentVersion: '1.2.3',
      latestVersion: '1.3.0',
      updateAvailable: true,
      installMethod: 'direct',
      releaseUrl: 'https://github.com/skyhook-io/radar/releases/tag/v1.3.0',
    },
  }),
  useStartDesktopUpdate: () => ({ mutate: vi.fn(), isPending: false }),
  useDesktopUpdateStatus: () => ({ data: undefined }),
  useApplyDesktopUpdate: () => ({ mutate: vi.fn() }),
}))

import { UpdateNotification } from './UpdateNotification'

function renderNotification() {
  const client = new QueryClient()
  return renderToString(
    <QueryClientProvider client={client}>
      <UpdateNotification />
    </QueryClientProvider>,
  )
}

describe('UpdateNotification', () => {
  beforeEach(() => {
    deploymentMode = 'local'
  })

  it('keeps the update popup for local installations', () => {
    const html = renderNotification()
    expect(html).toContain('Update Available')
    expect(html).toContain('1.3.0')
  })

  it('suppresses the update popup for shared in-cluster viewers', () => {
    deploymentMode = 'in-cluster'
    expect(renderNotification()).toBe('')
  })
})
