import { beforeEach, expect, it, vi } from 'vitest'
import { useQuery } from '@tanstack/react-query'
import { fetchJSON } from './client'
import { useLonghornWorkspace } from './longhorn'

const guard = vi.fn((request: () => Promise<unknown>) => request())
vi.mock('@tanstack/react-query', () => ({ useQuery: vi.fn() }))
vi.mock('./client', () => ({
  fetchJSON: vi.fn(),
  useRadarFeature: (feature: string) => ({ gatedKey: [feature], guard }),
}))
vi.mock('./config', () => ({ getApiBase: () => '/hub/c1/api' }))
beforeEach(() => vi.clearAllMocks())

it('fetches through the longhornWorkspace gate and never shows another cluster’s answer', async () => {
  useLonghornWorkspace()
  const options = vi.mocked(useQuery).mock.calls.at(-1)![0]
  expect(options.queryKey).toEqual(['longhorn', 'workspace', '/hub/c1/api', 'longhornWorkspace'])
  await (options.queryFn as (context: { signal: AbortSignal }) => Promise<unknown>)({ signal: new AbortController().signal })
  expect(guard).toHaveBeenCalledTimes(1)
  expect(fetchJSON).toHaveBeenCalledWith('/longhorn/workspace', expect.any(AbortSignal))
  const placeholder = options.placeholderData as (prev: unknown, query: { queryKey: readonly unknown[] }) => unknown
  const previous = { volumes: [] }
  expect(placeholder(previous, { queryKey: ['longhorn', 'workspace', '/hub/c2/api'] })).toBeUndefined()
  expect(placeholder(previous, { queryKey: options.queryKey as readonly unknown[] })).toBe(previous)
})
