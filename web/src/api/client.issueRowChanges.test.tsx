// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { focusManager, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { issueRowKey, type Issue, type IssueRowChange } from '@skyhook-io/k8s-ui'
import { setApiBase } from './config'
import { ISSUE_ROW_CHANGES_MAX, issueRowChangeParam, useIssueRowChanges } from './client'
import { timelineEventSearch } from '../context/TimelineNavigation'

const issue = (id: string, extra: Partial<Issue> = {}): Issue => ({
  id, severity: 'critical', source: 'problem', category: 'crashloop', category_group: 'runtime', grouping_scope: 'workload',
  kind: 'Deployment', group: 'apps', namespace: 'shop', name: id, reason: 'CrashLoopBackOff', ...extra,
})
const change: IssueRowChange = {
  first_seen: '2026-11-18T12:00:00Z',
  change: { kind: 'Deployment', apiVersion: 'apps/v1', namespace: 'shop', name: 'api', changeType: 'update', timestamp: '2026-11-18T11:56:00Z', event_id: 'ev-1' },
}

describe('issueRowChangeParam', () => {
  it("names the issue and its subject in the per-resource route's terms", () => {
    expect(issueRowChangeParam(issue('api'))).toBe('api/Deployment/shop/api/apps')
    expect(issueRowChangeParam(issue('svc', { kind: 'Service', group: '' }))).toBe('svc/Service/shop/svc')
    expect(issueRowChangeParam(issue('node', { kind: 'Node', group: '', namespace: undefined }))).toBe('node/Node/_/node')
  })
})

describe('timelineEventSearch', () => {
  it('opens the event in a frozen range around it, so an event older than the default hour is in view', () => {
    const now = Date.parse('2026-11-18T15:00:00Z')
    const at = Date.parse('2026-11-18T12:00:00Z')
    const search = timelineEventSearch({ eventId: 'ev-1', namespace: 'shop', timestamp: '2026-11-18T12:00:00Z' }, now)
    expect(search.get('event')).toBe('ev-1')
    expect(Number(search.get('from'))).toBeLessThan(at)
    expect(Number(search.get('to'))).toBeGreaterThan(at)
  })
  it("widens a list that keeps its own time range far enough to hold the event", () => {
    const now = Date.parse('2026-11-18T15:00:00Z')
    const range = (at: string) => timelineEventSearch({ eventId: 'ev-1', namespace: 'shop', timestamp: at }, now).get('time')
    expect(range('2026-11-18T14:50:00Z')).toBe('1h')
    expect(range('2026-11-18T12:00:00Z')).toBe('6h')
    expect(range('2026-11-17T12:00:00Z')).toBe('all')
    expect(range('2026-09-01T12:00:00Z')).toBe('all')
  })

  it('never ends the range in the future', () => {
    const now = Date.parse('2026-11-18T12:05:00Z')
    const search = timelineEventSearch({ eventId: 'ev-1', namespace: '', timestamp: '2026-11-18T12:00:00Z' }, now)
    expect(Number(search.get('to'))).toBe(now)
    expect(Number(search.get('from'))).toBeLessThan(Date.parse('2026-11-18T12:00:00Z'))
  })
})

let captured: ReadonlyMap<string, IssueRowChange> | undefined
function Probe({ issues, onRender }: { issues: Issue[]; onRender: (changes: ReadonlyMap<string, IssueRowChange> | undefined) => void }) {
  onRender(useIssueRowChanges(issues))
  return null
}

const roots: ReturnType<typeof createRoot>[] = []
const clients: QueryClient[] = []
beforeEach(() => {
  vi.useFakeTimers()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  focusManager.setFocused(true)
  setApiBase('/api')
  captured = undefined
})
afterEach(() => {
  roots.splice(0).forEach((root) => act(() => root.unmount()))
  clients.splice(0).forEach((client) => client.clear())
  focusManager.setFocused(undefined)
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

async function setup(flag: boolean, issues: Issue[]) {
  const requests: URL[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = new URL(input, 'http://localhost')
    requests.push(url)
    const body = url.pathname.endsWith('/capabilities')
      ? { version: 'v1.30.0', features: { resourceIssues: true, issueCorrelation: flag } }
      : { changes: { api: change } }
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } })
  clients.push(client)
  const root = createRoot(document.createElement('div'))
  roots.push(root)
  const render = (next: Issue[]) => act(async () => { root.render(<QueryClientProvider client={client}><Probe issues={next} onRender={(changes) => { captured = changes }} /></QueryClientProvider>) })
  await render(issues)
  const settle = async (ms = 50) => { await act(async () => { await vi.advanceTimersByTimeAsync(ms) }) }
  await settle()
  return { settle, render, rowRequests: () => requests.filter((url) => url.pathname.endsWith('/issues/recent-changes')) }
}

describe('useIssueRowChanges', () => {
  it('asks once for the rows on screen and keys the answer by row', async () => {
    const h = await setup(true, [issue('api'), issue('web')])
    expect(h.rowRequests()).toHaveLength(1)
    expect(h.rowRequests()[0].searchParams.getAll('issue')).toEqual(['api/Deployment/shop/api/apps', 'web/Deployment/shop/web/apps'])
    expect(captured?.get(issueRowKey(issue('api')))).toEqual(change)
    expect(captured?.has(issueRowKey(issue('web')))).toBe(false)
  })

  it('asks again when the rows on screen change, and polls every minute', async () => {
    const h = await setup(true, [issue('api')])
    await h.render([issue('api'), issue('web')])
    await h.settle()
    expect(h.rowRequests()).toHaveLength(2)
    await h.settle(60_000)
    expect(h.rowRequests()).toHaveLength(3)
  })

  it(`sends at most ${ISSUE_ROW_CHANGES_MAX} rows`, async () => {
    const many = Array.from({ length: ISSUE_ROW_CHANGES_MAX + 5 }, (_, i) => issue(`i${i}`))
    const h = await setup(true, many)
    expect(h.rowRequests()[0].searchParams.getAll('issue')).toHaveLength(ISSUE_ROW_CHANGES_MAX)
  })

  it('asks nothing of a Radar without the capability, or with no rows on screen', async () => {
    const old = await setup(false, [issue('api')])
    expect(old.rowRequests()).toHaveLength(0)
    expect(captured).toBeUndefined()
    const none = await setup(true, [])
    expect(none.rowRequests()).toHaveLength(0)
  })
})
