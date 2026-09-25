import { describe, expect, it, vi } from 'vitest'
import { useQuery } from '@tanstack/react-query'
import { ApiError, useKueueAdmission as admissionQuery } from './client'

vi.mock('@tanstack/react-query', async (original) => ({ ...await original<typeof import('@tanstack/react-query')>(), useQuery: vi.fn(() => ({})) }))

describe('Kueue admission polling', () => {
  function options(settings?: Parameters<typeof admissionQuery>[3]) {
    admissionQuery('ml', 'training', 'current', settings)
    return vi.mocked(useQuery).mock.calls.at(-1)![0]
  }
  function interval(settings: Parameters<typeof admissionQuery>[3], state: any = {}) {
    const poll = options(settings).refetchInterval as (query: any) => number | false
    return poll({ state })
  }
  it('separates Job and JobSet identities and recreated roots', () => {
    expect(options({ isJob: true }).queryKey).toEqual(['kueue-admission', 'batch', 'jobs', 'ml', 'training', 'current'])
    expect(options().queryKey).toEqual(['kueue-admission', 'jobset.x-k8s.io', 'jobsets', 'ml', 'training', 'current'])
  })
  it('polls quiet and terminal Jobs slowly, active admission quickly', () => {
    expect(interval({ isJob: true })).toBe(30000)
    expect(interval({ isJob: true, hinted: true })).toBe(5000)
    expect(interval({ isJob: true }, { data: { workloads: [{}] } })).toBe(5000)
    expect(interval({ isJob: true, hinted: true, terminal: true })).toBe(30000)
    expect(interval(undefined)).toBe(5000)
  })
  it('stops on confirmed absence or denied requests, not transient outages or old absence', () => {
    expect(interval({ isJob: true }, { data: { uid: 'current', installed: false } })).toBe(false)
    expect(interval({ isJob: true, hinted: true }, { data: { uid: 'old', installed: false } })).toBe(5000)
    expect(interval({ isJob: true }, { error: new ApiError('denied', 403) })).toBe(false)
    expect(interval({ isJob: true, hinted: true }, { error: new ApiError('warming', 503) })).toBe(5000)
  })
})
