// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { TimelineEvent } from '../../types'

const event = (id: string, minutesAgo: number): TimelineEvent => ({
  id, timestamp: new Date(Date.now() - minutesAgo * 60_000).toISOString(), source: 'informer', kind: 'ConfigMap', apiVersion: 'v1',
  namespace: 'shop', name: `cfg-${id}`, eventType: 'update', reason: 'update', message: `${id} changed`,
} as TimelineEvent)
const events = [event('ev-a', 5), event('ev-b', 9), event('ev-c', 14)]

vi.mock('../../context/TimelineSource', () => ({
  useTimelineSource: () => ({ useEvents: () => ({ data: events, isLoading: false, isFetching: false, isError: false, error: null, refetch: vi.fn() }) }),
}))
vi.mock('../../contexts/CapabilitiesContext', () => ({ useHasLimitedAccess: () => false }))
vi.mock('./TimelineRetentionNote', () => ({ TimelineRetentionBanner: () => null }))

const { TimelineList } = await import('./TimelineList')

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
const roots: ReturnType<typeof createRoot>[] = []
afterEach(() => { roots.splice(0).forEach((root) => act(() => root.unmount())) })

describe('the Timeline list with an event selected', () => {
  it('marks the selected event, so a link to it lands on it in list view too', async () => {
    const host = document.createElement('div')
    document.body.append(host)
    const root = createRoot(host)
    roots.push(root)
    await act(async () => {
      root.render(<TimelineList namespaces={['shop']} showDeleted onShowDeletedChange={() => {}} showRoutine={false} onShowRoutineChange={() => {}}
        search="" onSearchChange={() => {}} activityFilter={[]} onActivityFilterChange={() => {}} kindFilter={[]} onKindFilterChange={() => {}}
        selectedEventId="ev-b" onSelectEvent={() => {}} />)
    })
    const selected = [...host.querySelectorAll('[class*="ring-skyhook-500/60"]')]
    expect(selected).toHaveLength(1)
    expect(selected[0].textContent).toContain('cfg-ev-b')
  })
})
