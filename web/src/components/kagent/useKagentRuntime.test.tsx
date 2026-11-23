import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, expect, it, vi } from 'vitest'
import type { KagentSessionSummary } from '../../api/kagent'
import { connectedTier, counts } from '../substrate/substrate.fixtures'
import { useKagentRuntime, type KagentRuntime } from './shared'

const state = vi.hoisted(() => ({
  pages: undefined as KagentSessionSummary[] | undefined,
  hasNextPage: false,
  isFetchNextPageError: false,
}))
vi.mock('../../api/client', async (original) => ({
  ...(await original<typeof import('../../api/client')>()),
  useRadarFeature: () => ({ support: 'supported' }),
}))
vi.mock('../../api/substrateWorkspace', async (original) => ({
  ...(await original<typeof import('../../api/substrateWorkspace')>()),
  useSubstrateStatus: () => ({ data: { apiTier: connectedTier } }),
}))
vi.mock('../../api/kagent', async (original) => ({
  ...(await original<typeof import('../../api/kagent')>()),
  useKagentSessionSummary: () => ({
    data: state.pages ? { pages: state.pages } : undefined,
    hasNextPage: state.hasNextPage,
    isFetchNextPageError: state.isFetchNextPageError,
  }),
}))

function readRuntime() {
  let result: KagentRuntime | undefined
  function Probe() {
    result = useKagentRuntime([])
    return null
  }
  renderToStaticMarkup(<Probe />)
  return result!
}

beforeEach(() => {
  state.hasNextPage = false
  state.isFetchNextPageError = false
  const rows: KagentSessionSummary['rows'] = Array.from({ length: 1001 }, (_, i) => ({
    agent: { namespace: 'agents', name: `agent-${i}` },
    revisions: [], sessions: counts({ suspended: 1 }), complete: true,
    onEarlierRevisions: 0, needAttention: 0, attention: [], hintedEarlierRevisions: 0,
    crashed: 0, onNewSessionsRevision: 1, onOtherRevisions: 0,
    placement: { state: 'unknown', reading: 'Not read', pools: [], complete: false },
  }))
  const page: KagentSessionSummary = {
    apiTier: connectedTier,
    coverage: { actors: { state: 'full' }, templates: { state: 'full' }, workers: { state: 'full' }, atespaces: { state: 'full' }, kagentAgents: { state: 'full' } },
    rows: rows.slice(0, 1000), total: 1001, limit: 1000, offset: 0, lowerBound: false,
  }
  state.pages = [page, { ...page, rows: rows.slice(1000), offset: 1000 }]
})

it('counts a completely read fleet across multiple pages without a lower-bound marker', () => {
  const runtime = readRuntime()
  expect(runtime.rows).toHaveLength(1001)
  expect(runtime.rows.reduce((n, row) => n + row.sessions.total, 0)).toBe(1001)
  expect(runtime.completePage).toBe(true)
  expect(runtime.lowerBound).toBe(false)
})

it('keeps server-reported partial coverage after every page loads', () => {
  state.pages![1].lowerBound = true
  expect(readRuntime().lowerBound).toBe(true)
})

it.each(['loading', 'failed', 'missing'])('keeps incomplete pagination a lower bound (%s)', (kind) => {
  state.pages = kind === 'missing' ? undefined : state.pages!.slice(0, 1)
  state.hasNextPage = kind === 'loading'
  state.isFetchNextPageError = kind === 'failed'
  const runtime = readRuntime()
  expect(runtime.completePage).toBe(false)
  expect(runtime.lowerBound).toBe(true)
})
