// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { focusManager, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { IssueRow, issueTimingForDisplay, type Issue, type IssueExpansion } from '@skyhook-io/k8s-ui'
import { renderToString } from 'react-dom/server'
import { setApiBase } from './config'
import { RADAR_FEATURES, RadarFeatureUnsupportedError } from './radarFeatures'
import { issueExpansionFromQuery, useIssueExpansion } from '../components/issues/useIssueExpansion'

const issue: Issue = {
  id: 'crash-api', severity: 'critical', source: 'problem', category: 'crashloop', category_group: 'runtime', grouping_scope: 'workload',
  kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'api', reason: 'CrashLoopBackOff', first_seen: '2026-11-18T12:00:00Z',
}
const fresh: Issue = { ...issue, correlated_changes: [], no_recent_changes: { window_seconds: 3600 }, correlation: { window_from: '2026-11-18T11:00:00Z', window_to: '2026-11-18T12:10:00Z' } }

describe('issueExpansionFromQuery', () => {
  const base = { data: undefined, error: null, isFetching: false, dataUpdatedAt: 0, refetch: () => {} }
  it('uses the fresh issue with the same id', () => {
    const e = issueExpansionFromQuery(issue, { ...base, data: { issues: [{ ...fresh, id: 'other' }, fresh], coverage: 'ok' } })
    expect(e.recentChanges.status).toBe('loaded')
    expect(e.issue).toBe(fresh)
  })
  it('keeps the last answer when a refresh fails and says when it arrived', () => {
    const e = issueExpansionFromQuery(issue, { ...base, data: { issues: [fresh], coverage: 'ok' }, error: new Error('boom'), dataUpdatedAt: 1234 })
    expect(e.recentChanges.status).toBe('loaded')
    expect(e.issue).toBe(fresh)
    expect(e.refreshFailed).toEqual({ fetchedAt: 1234 })
    expect(e.onRetry).toEqual(expect.any(Function))
  })
  it('marks no refresh failure on a clean answer', () => {
    expect(issueExpansionFromQuery(issue, { ...base, data: { issues: [fresh], coverage: 'ok' }, dataUpdatedAt: 1234 }).refreshFailed).toBeUndefined()
  })
  it.each([
    ['the server reports not_found', { issues: [fresh], coverage: 'ok' as const, correlation_status: 'not_found' as const }],
    ['the id is missing from the answer', { issues: [{ ...fresh, id: 'other' }], coverage: 'ok' as const }],
  ])('is not_found when %s', (_name, data) => {
    const e = issueExpansionFromQuery(issue, { ...base, data })
    expect(e.recentChanges.status).toBe('not_found')
    expect(e.issue).toBeUndefined()
  })
  it.each(['user_forbidden', 'read_error'] as const)('maps a native Helm %s answer to that reason, not to not_found', (status) => {
    const e = issueExpansionFromQuery(issue, { ...base, data: { issues: [], coverage: 'ok', correlation_status: status } })
    expect(e.recentChanges).toEqual({ status: 'unavailable', reason: status })
    expect(e.issue).toBeUndefined()
    expect(e.onRetry).toEqual(expect.any(Function))
  })
  it.each([false, true])('does not override the row with an uncorrelated (ineligible) issue (refresh failed: %s)', (failed) => {
    const unchanged = { ...issue, cause: 'from the correlation request' }
    const e = issueExpansionFromQuery(issue, { ...base, data: { issues: [unchanged], coverage: 'ok' }, ...(failed ? { error: new Error('boom'), dataUpdatedAt: 5 } : {}) })
    expect(e.recentChanges.status).toBe('loaded')
    expect(e.issue).toBeUndefined()
  })
  it('maps an older Radar to the upgrade note', () => {
    const e = issueExpansionFromQuery(issue, { ...base, error: new RadarFeatureUnsupportedError('issueCorrelation', {}) })
    expect(e.recentChanges).toMatchObject({ status: 'unsupported', upgrade: { feature: RADAR_FEATURES.issueCorrelation.label } })
  })
  it('maps any other failure to a retryable error, and no answer yet to loading', () => {
    expect(issueExpansionFromQuery(issue, { ...base, error: new Error('boom') })).toMatchObject({ recentChanges: { status: 'failed' }, onRetry: expect.any(Function) })
    expect(issueExpansionFromQuery(issue, base).recentChanges.status).toBe('loading')
  })
})

interface Captured { open?: (issue: Issue | null) => void; expansion?: IssueExpansion; other?: IssueExpansion }
function Probe({ onRender, onNotFound }: { onRender: (captured: Captured) => void; onNotFound: () => void }) {
  const { onOpenIssueChange, expandedIssue } = useIssueExpansion(onNotFound)
  onRender({ open: onOpenIssueChange, expansion: expandedIssue(issue), other: expandedIssue({ ...issue, id: 'other' }) })
  return null
}

const roots: ReturnType<typeof createRoot>[] = []
const clients: QueryClient[] = []

beforeEach(() => {
  vi.useFakeTimers()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  focusManager.setFocused(true)
  setApiBase('/api')
})

afterEach(() => {
  roots.splice(0).forEach((root) => act(() => root.unmount()))
  clients.splice(0).forEach((client) => client.clear())
  focusManager.setFocused(undefined)
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

async function setup(flag: boolean, reply: object | ((issueRequest: number) => object) = { issues: [fresh], coverage: 'ok' }) {
  let issueRequestCount = 0
  const requests: URL[] = []
  let failIssues = false
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = new URL(input, 'http://localhost')
    requests.push(url)
    if (failIssues && url.pathname.includes('/issues/resource/')) return new Response(JSON.stringify({ error: 'store unavailable' }), { status: 500, headers: { 'Content-Type': 'application/json' } })
    const isCapabilities = url.pathname.endsWith('/capabilities')
    if (!isCapabilities && url.pathname.includes('/issues/resource/')) issueRequestCount++
    const body = isCapabilities
      ? { version: 'v1.30.0', features: { resourceIssues: true, issueCorrelation: flag } }
      : typeof reply === 'function' ? reply(issueRequestCount) : reply
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } })
  clients.push(client)
  const root = createRoot(document.createElement('div'))
  roots.push(root)
  const captured: Captured = {}
  const onNotFound = vi.fn()
  await act(async () => { root.render(<QueryClientProvider client={client}><Probe onRender={(next) => Object.assign(captured, next)} onNotFound={onNotFound} /></QueryClientProvider>) })
  const settle = async (ms = 50) => { await act(async () => { await vi.advanceTimersByTimeAsync(ms) }) }
  await settle()
  const issueRequests = () => requests.filter((url) => url.pathname.includes('/issues/resource/'))
  return { captured, onNotFound, settle, issueRequests, setFailIssues: (value: boolean) => { failIssues = value } }
}

describe('useIssueExpansion', () => {
  it('requests nothing until a row opens', async () => {
    const h = await setup(true)
    expect(h.issueRequests()).toHaveLength(0)
    expect(h.captured.expansion).toBeUndefined()
  })

  it('sends correlate=<id> for the open issue only, and polls every 60s while open', async () => {
    const h = await setup(true)
    await act(async () => { h.captured.open!(issue) })
    await h.settle()
    expect(h.issueRequests()).toHaveLength(1)
    const url = h.issueRequests()[0]
    expect(url.pathname).toBe('/api/issues/resource/Deployment/shop/api')
    expect(url.searchParams.get('correlate')).toBe('crash-api')
    expect(url.searchParams.get('coverage')).toBe('1')
    expect(url.searchParams.get('group')).toBe('apps')
    expect(h.captured.expansion?.issue).toMatchObject({ id: 'crash-api', no_recent_changes: { window_seconds: 3600 } })
    expect(h.captured.other).toBeUndefined()

    await h.settle(60_000)
    expect(h.issueRequests()).toHaveLength(2)

    await act(async () => { h.captured.open!(null) })
    await h.settle(120_000)
    expect(h.issueRequests()).toHaveLength(2)
    expect(h.captured.expansion).toBeUndefined()
  })

  it('keeps the fresh issue when a later poll fails, and says the refresh failed', async () => {
    const h = await setup(true)
    await act(async () => { h.captured.open!(issue) })
    await h.settle()
    expect(h.captured.expansion?.refreshFailed).toBeUndefined()
    const firstAnswerAt = Date.now()
    h.setFailIssues(true)
    await h.settle(60_000)
    await h.settle(5_000)
    expect(h.captured.expansion?.issue).toMatchObject({ id: 'crash-api', no_recent_changes: { window_seconds: 3600 } })
    expect(h.captured.expansion?.recentChanges.status).toBe('loaded')
    expect(h.captured.expansion?.refreshFailed?.fetchedAt).toBeLessThanOrEqual(firstAnswerAt)
    expect(h.captured.expansion?.refreshFailed?.fetchedAt).toBeGreaterThan(0)

    h.setFailIssues(false)
    await act(async () => { h.captured.expansion!.onRetry!() })
    await h.settle()
    expect(h.captured.expansion?.refreshFailed).toBeUndefined()
  })

  it('shows a new occurrence its own answer, even within the stale time', async () => {
    const oldAnswer: Issue = {
      ...fresh, first_seen: '2026-11-18T10:00:00Z', cause: 'Old occurrence: probe failed after the 10:00 rollout.',
      issue_timing: 'started_at_resource_creation', issue_timing_basis: 'pod_creation',
      correlation: { window_from: '2026-11-18T09:00:00Z', window_to: '2026-11-18T10:30:00Z' },
    }
    const newAnswer: Issue = {
      ...fresh, first_seen: '2026-11-18T14:00:00Z', cause: 'New occurrence: image tag missing.',
      issue_timing: 'started_after_resource_was_healthy', issue_timing_basis: 'condition',
      correlation: { window_from: '2026-11-18T13:00:00Z', window_to: '2026-11-18T14:10:00Z' },
    }
    const h = await setup(true, (n) => ({ issues: [n === 1 ? oldAnswer : newAnswer], coverage: 'ok' }))
    await act(async () => { h.captured.open!({ ...issue, first_seen: oldAnswer.first_seen }) })
    await h.settle()
    expect(h.issueRequests()).toHaveLength(1)
    expect(h.captured.expansion?.issue?.cause).toBe(oldAnswer.cause)

    await act(async () => { h.captured.open!(null) })
    const recurred = { ...issue, first_seen: newAnswer.first_seen }
    await act(async () => { h.captured.open!(recurred) })
    await h.settle()
    expect(h.issueRequests()).toHaveLength(2)
    const shown = h.captured.expansion?.issue
    expect(shown).toMatchObject({
      first_seen: newAnswer.first_seen, issue_timing: newAnswer.issue_timing, correlation: { window_from: newAnswer.correlation!.window_from },
    })

    const html = renderToString(<IssueRow issue={recurred} open onToggle={() => {}} expandedIssue={h.captured.expansion} />)
    expect(html).toContain(newAnswer.cause)
    expect(html).not.toContain(oldAnswer.cause)
    expect(html).toContain(issueTimingForDisplay(newAnswer)!.chip)
    expect(html).not.toContain(issueTimingForDisplay(oldAnswer)!.chip)
    await act(async () => { h.captured.open!({ ...issue, first_seen: newAnswer.first_seen }) })
    await h.settle()
    expect(h.issueRequests()).toHaveLength(2)
  })

  it('tells occurrences apart by the subject creation time when first_seen is missing', async () => {
    const h = await setup(true)
    await act(async () => { h.captured.open!({ ...issue, first_seen: undefined, resource_created_at: '2026-11-18T10:00:00Z' }) })
    await h.settle()
    await act(async () => { h.captured.open!({ ...issue, first_seen: undefined, resource_created_at: '2026-11-18T13:00:00Z' }) })
    await h.settle()
    expect(h.issueRequests()).toHaveLength(2)
  })

  it('uses _ for a cluster-scoped subject', async () => {
    const h = await setup(true, { issues: [{ ...fresh, kind: 'Node', group: undefined, namespace: undefined, name: 'n1' }], coverage: 'ok' })
    await act(async () => { h.captured.open!({ ...issue, kind: 'Node', group: undefined, namespace: undefined, name: 'n1' }) })
    await h.settle()
    expect(h.issueRequests()[0].pathname).toBe('/api/issues/resource/Node/_/n1')
    expect(h.issueRequests()[0].searchParams.has('group')).toBe(false)
  })

  it('sends no request to a Radar without the flag and shows the upgrade note', async () => {
    const h = await setup(false)
    await act(async () => { h.captured.open!(issue) })
    await h.settle()
    expect(h.issueRequests()).toHaveLength(0)
    expect(h.captured.expansion?.recentChanges.status).toBe('unsupported')
  })

  it.each(['user_forbidden', 'read_error'] as const)('does not refresh the list for a native Helm %s answer', async (status) => {
    const h = await setup(true, { issues: [], coverage: 'ok', correlation_status: status, unchecked_sources: [{ source: 'helm', reason: status }] })
    await act(async () => { h.captured.open!(issue) })
    await h.settle()
    expect(h.captured.expansion?.recentChanges).toEqual({ status: 'unavailable', reason: status })
    expect(h.onNotFound).not.toHaveBeenCalled()
  })

  it('asks the host to refresh its list when the open issue is not found', async () => {
    const h = await setup(true, { issues: [], coverage: 'ok', correlation_status: 'not_found' })
    await act(async () => { h.captured.open!(issue) })
    await h.settle()
    expect(h.captured.expansion?.recentChanges.status).toBe('not_found')
    expect(h.onNotFound).toHaveBeenCalledTimes(1)
  })
})
