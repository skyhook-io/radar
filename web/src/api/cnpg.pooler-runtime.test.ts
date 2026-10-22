import { expect, it, vi } from 'vitest'
import { useQuery } from '@tanstack/react-query'
import { useCNPGPoolerRuntime } from './cnpg'

vi.mock('@tanstack/react-query', () => ({ useQuery: vi.fn(), useMutation: vi.fn(), useQueryClient: vi.fn() }))
vi.mock('./client', () => ({ fetchJSON: vi.fn(), useRadarFeature: () => ({ guard: (f: () => unknown) => f(), gatedKey: [] }) }))

it('keeps the scheduling inventory fresh even when live proxy measurements are denied', () => {
  useCNPGPoolerRuntime('prod', 'pooler')
  const options = vi.mocked(useQuery).mock.calls[0][0]
  const interval = options.refetchInterval
  const deniedQuery = { state: { data: { permission: { proxy: 'denied' }, pods: [{ pod: 'pending', schedulingReason: 'Unschedulable' }] } } }
  expect(typeof interval === 'function' ? interval(deniedQuery as any) : interval).toBe(30000)
  expect(options.refetchIntervalInBackground).toBe(false)
})
