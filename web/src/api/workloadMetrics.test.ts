import { describe, expect, it, vi } from 'vitest'
import { ApiError } from './client'
import type { WorkloadMetrics } from './workloadMetrics'

vi.mock('@tanstack/react-query', () => ({ useQuery: (options: unknown) => options }))
vi.mock('./client', () => ({
  useClusterInfo: () => ({ data: { context: 'cluster-a' } }),
  fetchJSON: vi.fn(),
  ApiError: class extends Error {
    constructor(message: string, readonly status: number) { super(message) }
  },
}))
vi.mock('./config', () => ({ getApiBase: () => '/api' }))
const { useWorkloadMetrics: workloadQueryOptions } = await import('./workloadMetrics')

describe('workload source-switch placeholders', () => {
  const options = () => workloadQueryOptions('Deployment', 'shop', 'api', '1h', 'istio', true) as unknown as {
    queryKey: unknown[]
    placeholderData: (data: WorkloadMetrics, query: { queryKey: unknown[] }) => WorkloadMetrics | undefined
    refetchInterval: (query: { state: { data: WorkloadMetrics } }) => number
    retry: (failureCount: number, error: Error) => boolean
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
  it.each([409, 503])('retries an HTTP %s once on connection replacement', (status) => {
    const { retry } = options()
    const error = new ApiError('Metrics connection closed. Retry the request', status)
    expect(retry(0, error)).toBe(true)
    expect(retry(1, error)).toBe(false)
    expect(retry(0, new ApiError('Forbidden', 403))).toBe(false)
  })
  it.each([1, 2, 3, 4, 5, 6])('drops the previous payload when identity field %s changes', (index) => {
    const query = options()
    const key = [...query.queryKey]
    key[index] = 'different'
    expect(query.placeholderData(data, { queryKey: key })).toBeUndefined()
  })
})
