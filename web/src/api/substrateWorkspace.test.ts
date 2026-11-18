import { beforeEach, expect, it, vi } from 'vitest'
import { useQuery } from '@tanstack/react-query'
import { useSubstrateActor, useSubstrateActors, useSubstrateStatus } from './substrateWorkspace'

vi.mock('@tanstack/react-query', () => ({ useQuery: vi.fn(() => ({})), useMutation: vi.fn(), useQueryClient: vi.fn() }))
const feature = vi.hoisted(() => ({ support: 'unsupported', gatedKey: ['radar-feature-unsupported'] }))
vi.mock('./config', () => ({ getApiBase: () => '/api' }))
vi.mock('./client', () => ({
  ApiError: class extends Error {},
  apiFetch: vi.fn(),
  fetchJSON: vi.fn(),
  useClusterInfo: () => ({ data: { context: 'cluster-a' } }),
  useRadarFeature: () => ({
    support: feature.support,
    gatedKey: feature.gatedKey,
    upgrade: undefined,
    blockedReason: undefined,
    guard: (request: () => Promise<unknown>) => request(),
  }),
}))
beforeEach(() => vi.clearAllMocks())

type Placeholder = (previous: unknown, query?: { queryKey: readonly unknown[] }) => unknown
function lastQuery() { return vi.mocked(useQuery).mock.calls.at(-1)![0] }

it('lets the feature guard decide, so an unsupported Radar reaches the upgrade path', () => {
  useSubstrateStatus()
  const options = lastQuery()
  expect(options.enabled).toBe(true)
  expect(options.queryKey).toEqual(['substrate', '/api', 'cluster-a', 'status', 'radar-feature-unsupported'])
})

it('keeps no previous answer for a detail, only for another page of the same actor list', () => {
  feature.gatedKey = []
  useSubstrateActor({ atespace: 'research', name: 'a' })
  const a = lastQuery()
  expect(a.placeholderData).toBeUndefined()

  useSubstrateActors({ state: 'crashed', offset: 0, limit: 50 })
  const page1 = lastQuery()
  useSubstrateActors({ state: 'crashed', offset: 50, limit: 50 })
  const page2 = lastQuery()
  useSubstrateActors({ state: 'running', offset: 50, limit: 50 })
  const otherFilter = lastQuery()
  const previous = { rows: [] }
  const keep = page2.placeholderData as Placeholder
  expect(keep(previous, { queryKey: page1.queryKey })).toBe(previous)
  expect((otherFilter.placeholderData as Placeholder)(previous, { queryKey: page2.queryKey })).toBeUndefined()
  expect(keep(previous, { queryKey: ['substrate', '/api', 'cluster-b', ...page1.queryKey.slice(3)] })).toBeUndefined()
  expect(keep(previous, { queryKey: ['substrate', '/other', 'cluster-a', ...page1.queryKey.slice(3)] })).toBeUndefined()
  expect(keep(previous, { queryKey: a.queryKey })).toBeUndefined()
  expect(keep(previous, undefined)).toBeUndefined()
})
