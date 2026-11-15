// @vitest-environment jsdom
import { act, useCallback } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { TimelineEventsResult, TimelineSource } from '../../api/timelineSource'

const state = vi.hoisted(() => ({ result: undefined as unknown as TimelineEventsResult }))
const fakeSource: TimelineSource = {
  capabilities: { mode: 'local', ringLimit: 10_000 },
  useEvents: () => state.result,
}
vi.mock('../../context/TimelineSource', () => ({ useTimelineSource: () => fakeSource }))
// The real hook calls hooks of its own; this stand-in keeps that property.
vi.mock('./LokiLogsAction', () => ({
  useLokiTimelineActions: () => {
    useCallback(() => null, [])
    return undefined
  },
}))

const { TimelineList } = await import('./TimelineList')

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root | undefined
afterEach(() => {
  act(() => root?.unmount())
  root = undefined
})

const props = {
  namespaces: [],
  showDeleted: false,
  onShowDeletedChange: () => {},
  showRoutine: false,
  onShowRoutineChange: () => {},
  search: '',
  onSearchChange: () => {},
  activityFilter: [],
  onActivityFilterChange: () => {},
  kindFilter: [],
  onKindFilterChange: () => {},
}

describe('TimelineList', () => {
  it('recovers from a load error to rows without changing its hook order', () => {
    state.result = { data: undefined, isLoading: false, isFetching: false, isError: true, error: new Error('boom'), refetch: () => {} }
    root = createRoot(document.createElement('div'))
    act(() => root!.render(<TimelineList {...props} />))

    state.result = { data: [], isLoading: false, isFetching: false, isError: false, refetch: () => {} }
    expect(() => act(() => root!.render(<TimelineList {...props} />))).not.toThrow()
  })
})
