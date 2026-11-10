import { afterEach, describe, expect, it, vi } from 'vitest'
import { useQuery } from '@tanstack/react-query'
import { useWorkloadRuns, type WorkloadRunsResponse } from './client'

vi.mock('@tanstack/react-query', async (original) => ({ ...await original<typeof import('@tanstack/react-query')>(), useQuery: vi.fn(() => ({})) }))

type RunsOptions = { queryKey: unknown[]; queryFn: () => Promise<unknown>; placeholderData: (previous: WorkloadRunsResponse | undefined, query: { queryKey: unknown[] } | undefined) => unknown }

function useRunsQueryOptions(...args: Parameters<typeof useWorkloadRuns>): RunsOptions {
  useWorkloadRuns(...args)
  return vi.mocked(useQuery).mock.calls.at(-1)![0] as unknown as RunsOptions
}

async function requestedPath(options: RunsOptions): Promise<string> {
  const fetchMock = vi.fn(async () => Response.json({ collection: 'runs', runs: [], total: 0, truncated: false }))
  vi.stubGlobal('fetch', fetchMock)
  await options.queryFn()
  return String((fetchMock.mock.calls[0] as unknown[])[0])
}

afterEach(() => vi.unstubAllGlobals())

describe('cluster-scoped workload runs follow the view selection', () => {
  it('sends the selection and keys the query on it', async () => {
    const options = useRunsQueryOptions('clusterworkflowtemplates', '', 'build', true, { clusterScoped: true, namespaces: ['team-a', 'team-b'] })
    const params = new URL(await requestedPath(options), 'http://radar.invalid').searchParams
    expect(params.get('clusterScoped')).toBe('true')
    expect(params.get('namespaces')).toBe('team-a,team-b')

    const other = useRunsQueryOptions('clusterworkflowtemplates', '', 'build', true, { clusterScoped: true, namespaces: ['team-a'] })
    expect(other.queryKey).not.toEqual(options.queryKey)
  })

  it('sends no filter when the view covers all namespaces', async () => {
    const options = useRunsQueryOptions('clusterworkflowtemplates', '', 'build', true, { clusterScoped: true, namespaces: [] })
    expect(new URL(await requestedPath(options), 'http://radar.invalid').searchParams.has('namespaces')).toBe(false)
  })

  it('leaves a namespaced workload alone: its path already names the namespace', async () => {
    const options = useRunsQueryOptions('cronjobs', 'team-a', 'nightly', true, { namespaces: ['team-a', 'team-b'] })
    expect(new URL(await requestedPath(options), 'http://radar.invalid').searchParams.has('namespaces')).toBe(false)
    expect(options.queryKey).toEqual(useRunsQueryOptions('cronjobs', 'team-a', 'nightly', true, {}).queryKey)
  })

  it('keeps a member list as placeholder only within the same selection', () => {
    const members: WorkloadRunsResponse = { collection: 'members', runs: [], total: 0, truncated: false }
    const before = useRunsQueryOptions('clusterworkflowtemplates', '', 'build', true, { clusterScoped: true, namespaces: ['team-a'], selected: 'x' })
    const after = useRunsQueryOptions('clusterworkflowtemplates', '', 'build', true, { clusterScoped: true, namespaces: ['team-a'], selected: 'y' })
    expect(after.placeholderData(members, { queryKey: before.queryKey })).toBe(members)
    const widened = useRunsQueryOptions('clusterworkflowtemplates', '', 'build', true, { clusterScoped: true, namespaces: ['team-a', 'team-b'], selected: 'y' })
    expect(widened.placeholderData(members, { queryKey: before.queryKey })).toBeUndefined()
  })
})
