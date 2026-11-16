// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ClusterConnectedContext } from '../context/clusterConnected'
import { projectAuditBadges, useAudit, useAuditBadges } from './client'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let useProbe: () => void = () => useAudit([])

function AuditProbe() {
  useProbe()
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
    useProbe = () => useAudit([])
    expect(await renderWith(false)).toHaveLength(0)
  })

  it('fetches checks while connected', async () => {
    useProbe = () => useAudit([])
    expect(await renderWith(true)).toHaveLength(1)
  })
})

describe('useAuditBadges', () => {
  it('asks only for badge findings, in the picked namespaces', async () => {
    useProbe = () => useAuditBadges(['shop', 'web'])
    const [url, ...rest] = await renderWith(true)
    expect(rest).toHaveLength(0)
    const params = new URL(url, 'http://radar').searchParams
    expect(params.get('include')).toBe('badges')
    expect(params.get('namespaces')).toBe('shop,web')
  })

  it('does not ask a disconnected cluster', async () => {
    useProbe = () => useAuditBadges([])
    expect(await renderWith(false)).toHaveLength(0)
  })

  it('stays idle while disabled', async () => {
    useProbe = () => useAuditBadges([], { enabled: false })
    expect(await renderWith(true)).toHaveLength(0)
  })
})

describe('projectAuditBadges', () => {
  const check = (id: string, badgeWorthy: boolean) => ({ id, title: id, description: '', remediation: '', category: 'Reliability', severity: 'warning', badgeWorthy }) as never
  const finding = (checkID: string, name: string) => ({ kind: 'Service', group: '', namespace: 'shop', name, checkID, category: 'Reliability', severity: 'warning', message: name }) as never

  it('cuts an older server\'s full response down to badge-worthy findings and their checks', () => {
    const full = {
      findings: [finding('noEndpoints', 'a'), finding('runAsRoot', 'b'), finding('noEndpoints', 'c')],
      checks: { noEndpoints: check('noEndpoints', true), runAsRoot: check('runAsRoot', false), unused: check('unused', true) },
      groups: [{ findings: [] }],
      groupedChecks: [],
    }
    const out = projectAuditBadges(full)
    expect(Object.keys(out).sort()).toEqual(['checks', 'findings'])
    expect(out.findings.map((f) => f.name)).toEqual(['a', 'c'])
    expect(Object.keys(out.checks)).toEqual(['noEndpoints'])
  })

  it('tolerates an empty audit', () => {
    expect(projectAuditBadges({ findings: null as never, checks: {} })).toEqual({ findings: [], checks: {} })
  })
})

describe('useAudit', () => {
  it('marks the badges query stale with every full scan, so Checks and badges agree', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ findings: [], checks: {}, groups: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    client.setQueryData(['audit', ['shop'], 'badges'], { findings: [], checks: {} })
    expect(client.getQueryState(['audit', ['shop'], 'badges'])?.isInvalidated).toBe(false)
    function FullAudit() {
      useAudit(['shop'])
      return null
    }
    const root = createRoot(document.createElement('div'))
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <ClusterConnectedContext.Provider value={true}>
            <FullAudit />
          </ClusterConnectedContext.Provider>
        </QueryClientProvider>,
      )
    })
    await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
    act(() => root.unmount())
    expect(client.getQueryState(['audit', ['shop'], 'badges'])?.isInvalidated).toBe(true)
  })
})
