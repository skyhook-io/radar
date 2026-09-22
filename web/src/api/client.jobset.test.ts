import { describe, expect, it, vi } from 'vitest'
import { useQuery } from '@tanstack/react-query'
import { useWorkloadRuns, type WorkloadRunsResponse } from './client'

vi.mock('@tanstack/react-query', async (original) => ({ ...await original<typeof import('@tanstack/react-query')>(), useQuery: vi.fn(() => ({})) }))

describe('JobSet member polling', () => {
  it('refreshes an active selected member even when the displayed window is idle', () => {
    useWorkloadRuns('jobsets', 'training', 'distributed', true, { refetchActive: true, role: 'prepare', selected: 'jobs/training/worker' })
    const options = vi.mocked(useQuery).mock.calls.at(-1)![0]
    const interval = options.refetchInterval as (query: { state: { data: WorkloadRunsResponse } }) => number
    const run = { group: 'batch', kind: 'jobs', namespace: 'training', name: 'prepare', phase: 'Succeeded', active: false }
    const response: WorkloadRunsResponse = { collection: 'members', runs: [run], selected: { ...run, name: 'worker', phase: 'Running', active: true }, total: 2, filteredTotal: 1, truncated: false }
    expect(interval({ state: { data: response } })).toBe(5000)
    expect(interval({ state: { data: { ...response, selected: run } } })).toBe(30000)
    expect(interval({ state: { data: { ...response, runs: [{ ...run, active: true }], selected: undefined } } })).toBe(5000)
  })
})
