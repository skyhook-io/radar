// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ClusterConnectedContext } from '../context/clusterConnected'
import { useAudit, useAuditFindings } from './client'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let probe: () => void = () => useAudit([])

function AuditProbe() {
  probe()
  return null
}

afterEach(() => {
  vi.unstubAllGlobals()
})

async function renderWith(connected: boolean) {
  const fetchMock = vi.fn<(input: RequestInfo | URL) => Promise<Response>>(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchMock)
  const container = document.createElement('div')
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <ClusterConnectedContext.Provider value={connected}>
          <AuditProbe />
        </ClusterConnectedContext.Provider>
      </QueryClientProvider>,
    )
  })
  await act(async () => { await Promise.resolve() })
  const auditCalls = fetchMock.mock.calls.map(([input]) => String(input)).filter((url) => url.includes('/audit'))
  act(() => root.unmount())
  return auditCalls
}

describe('useAudit', () => {
  it('does not ask a disconnected cluster for checks', async () => {
    probe = () => useAudit([])
    expect(await renderWith(false)).toHaveLength(0)
  })

  it('fetches checks while connected', async () => {
    probe = () => useAudit([])
    expect(await renderWith(true)).toHaveLength(1)
  })
})

describe('useAuditFindings', () => {
  it('asks only for findings and the check catalog, in the picked namespaces', async () => {
    probe = () => useAuditFindings(['shop', 'web'])
    const [url, ...rest] = await renderWith(true)
    expect(rest).toHaveLength(0)
    const params = new URL(url, 'http://radar').searchParams
    expect(params.get('include')).toBe('findings')
    expect(params.get('namespaces')).toBe('shop,web')
  })

  it('does not ask a disconnected cluster', async () => {
    probe = () => useAuditFindings([])
    expect(await renderWith(false)).toHaveLength(0)
  })

  it('stays idle while disabled', async () => {
    probe = () => useAuditFindings([], { enabled: false })
    expect(await renderWith(true)).toHaveLength(0)
  })
})
