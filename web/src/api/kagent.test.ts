import { beforeEach, expect, it, vi } from 'vitest'
import { QueryClient, useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { fetchJSON } from './client'
import { useKagentSessionSummary, useKagentWorkspace } from './kagent'
const state = vi.hoisted(() => ({ workspace: 'supported', sessions: 'supported', context: 'one', base: '/api' }))
vi.mock('@tanstack/react-query', async (original) => ({ ...(await original<typeof import('@tanstack/react-query')>()), useQuery: vi.fn(() => ({})), useInfiniteQuery: vi.fn(() => ({})) }))
vi.mock('./config', () => ({ getApiBase: () => state.base }))
vi.mock('./client', () => ({
  fetchJSON: vi.fn(),
  useClusterInfo: () => ({ data: { context: state.context } }),
  useRadarFeature: (feature: string) => ({
    support: feature === 'kagentWorkspace' ? state.workspace : state.sessions,
    gatedKey: [],
    guard: (fn: () => unknown) => fn(),
  }),
}))
beforeEach(() => {
  vi.clearAllMocks()
  state.workspace = state.sessions = 'supported'
  state.context = 'one'
  state.base = '/api'
})
it('requires affirmative support for each new endpoint, including during capability loading', () => {
  for (const support of ['unsupported', 'unknown']) {
    state.workspace = support
    useKagentWorkspace([])
    expect(vi.mocked(useQuery).mock.calls.at(-1)![0].enabled).toBe(false)
    state.sessions = support
    useKagentSessionSummary([])
    expect(vi.mocked(useInfiniteQuery).mock.calls.at(-1)![0].enabled).toBe(false)
  }
})
it('separates namespaces, contexts and API hosts and carries cancellation', async () => {
  useKagentWorkspace(['b', 'a'])
  const first = vi.mocked(useQuery).mock.calls.at(-1)![0]
  await (first.queryFn as Function)({ signal: new AbortController().signal })
  expect(fetchJSON).toHaveBeenCalledWith('/kagent/workspace?namespaces=a%2Cb', expect.any(AbortSignal))
  for (const change of [
    () => {
      state.context = 'two'
    },
    () => {
      state.base = '/other'
    },
  ]) {
    change()
    useKagentWorkspace(['a', 'b'])
    expect(vi.mocked(useQuery).mock.calls.at(-1)![0].queryKey).not.toEqual(first.queryKey)
  }
  expect(first.placeholderData).toBeUndefined()
})
it('pages the fleet summary instead of treating its first 1000 Agents as the fleet', () => {
  useKagentSessionSummary([])
  const options = vi.mocked(useInfiniteQuery).mock.calls.at(-1)![0]
  const next = options.getNextPageParam as Function
  expect(next({ offset: 0, rows: Array(1000), total: 1001 })).toBe(1000)
  expect(next({ offset: 1000, rows: [1], total: 1001 })).toBeUndefined()
  expect(next({ offset: 0, rows: [], total: 1001 })).toBeUndefined()
})

it('invalidates session summaries with the saved Substrate connection, only for that API host', async () => {
  const client = new QueryClient()
  useKagentSessionSummary([])
  const first = vi.mocked(useInfiniteQuery).mock.calls.at(-1)![0].queryKey
  state.base = '/other'
  useKagentSessionSummary([])
  const other = vi.mocked(useInfiniteQuery).mock.calls.at(-1)![0].queryKey
  client.setQueryData(first, { rows: [] })
  client.setQueryData(other, { rows: [] })
  await client.invalidateQueries({ queryKey: ['substrate', '/api'] })
  expect(client.getQueryState(first)?.isInvalidated).toBe(true)
  expect(client.getQueryState(other)?.isInvalidated).toBe(false)
  client.clear()
})


it('negotiates the configuration and runtime capabilities independently', () => {
  state.workspace = 'unsupported'
  state.sessions = 'supported'
  useKagentWorkspace([])
  useKagentSessionSummary([])
  expect(vi.mocked(useQuery).mock.calls.at(-1)![0].enabled).toBe(false)
  expect(vi.mocked(useInfiniteQuery).mock.calls.at(-1)![0].enabled).toBe(true)
})
