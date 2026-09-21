import { describe, expect, it, vi } from 'vitest'
import type { WorkloadMetrics } from './workloadMetrics'

vi.mock('@tanstack/react-query', () => ({ useQuery: (options: unknown) => options }))
vi.mock('./client', () => ({
  useClusterInfo: () => ({ data: { context: 'cluster-a' } }),
  fetchJSON: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock('./config', () => ({ getApiBase: () => '/api' }))
const { useWorkloadMetrics: workloadQueryOptions } = await import('./workloadMetrics')

describe('workload source-switch placeholders', () => {
  const options = () => workloadQueryOptions('Deployment', 'shop', 'api', '1h', 'istio', true) as unknown as {
    queryKey: unknown[]
    placeholderData: (data: WorkloadMetrics, query: { queryKey: unknown[] }) => WorkloadMetrics | undefined
    refetchInterval: (query: { state: { data: WorkloadMetrics } }) => number
  }
  const data: WorkloadMetrics = {
    state: 'available', sources: [], pods: 1, podsTotal: 1,
    history: {}, comparison: {},
    start: 0, end: 60, stepSeconds: 60, rateWindowSeconds: 300, panels: {},
  }
  it('keeps the previous payload when only the observer changes', () => {
    const query = options()
    const key = [...query.queryKey]
    key[key.length - 1] = 'beyla'
    expect(query.placeholderData(data, { queryKey: key })).toBe(data)
  })
  it('polls pending current attribution even while history is available', () => {
    expect(options().refetchInterval({ state: { data } })).toBe(30_000)
    expect(options().refetchInterval({ state: { data: { ...data, comparison: { cpu: { state: 'detecting', unit: 'cores', series: [] } } } } })).toBe(3_000)
  })
  it.each([1, 2, 3, 4, 5, 6])('drops the previous payload when identity field %s changes', (index) => {
    const query = options()
    const key = [...query.queryKey]
    key[index] = 'different'
    expect(query.placeholderData(data, { queryKey: key })).toBeUndefined()
  })
})
