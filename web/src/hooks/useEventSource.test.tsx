// @vitest-environment jsdom
import { act, useEffect } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Topology } from '../types'
import { useEventSource } from './useEventSource'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

class FakeEventSource {
  static all: FakeEventSource[] = []
  listeners = new Map<string, (e: { data: string }) => void>()
  closed = false
  onopen: (() => void) | null = null
  onerror: ((e: unknown) => void) | null = null
  constructor(public url: string) {
    FakeEventSource.all.push(this)
  }
  addEventListener(name: string, fn: (e: { data: string }) => void) {
    this.listeners.set(name, fn)
  }
  close() {
    this.closed = true
  }
  emit(name: string, data: unknown) {
    act(() => this.listeners.get(name)?.({ data: JSON.stringify(data) }))
  }
}

const latest = () => FakeEventSource.all[FakeEventSource.all.length - 1]

// 300 nodes puts the hook's throttle at 2s, wide enough to land events inside it.
function graph(name: string): Topology {
  return { nodes: Array.from({ length: 300 }, (_, i) => ({ id: `${name}-${i}`, kind: 'Pod', name: `${name}-${i}`, status: 'healthy', data: {} })), edges: [] } as unknown as Topology
}
const shown = (t: Topology | null) => t?.nodes[0]?.id.split('-')[0] ?? null

let root: Root
const current = {} as ReturnType<typeof useEventSource>
function Probe({ view }: { view: 'resources' | 'traffic' }) {
  const value = useEventSource([], view)
  useEffect(() => { Object.assign(current, value) })
  return null
}
function render(view: 'resources' | 'traffic') {
  act(() => root.render(<Probe view={view} />))
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.stubGlobal('EventSource', FakeEventSource)
  vi.spyOn(console, 'log').mockImplementation(() => {})
  FakeEventSource.all = []
  root = createRoot(document.createElement('div'))
})

afterEach(() => {
  act(() => root.unmount())
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

// The server sends a topology frame only when the graph changed, so the hook
// can't count on a later identical frame to correct what it shows: the last
// frame it receives for a connection or cluster must be the one displayed.
describe('useEventSource topology frames', () => {
  it('shows the new cluster after a switch, not a frame queued from the old one', () => {
    render('resources')
    latest().emit('topology', graph('old0'))
    vi.advanceTimersByTime(1000)
    latest().emit('topology', graph('old1')) // inside the throttle: queued
    latest().emit('context_changed', { context: 'other' })
    latest().emit('topology', graph('new'))
    expect(shown(current.topology)).toBe('new')
    act(() => { vi.advanceTimersByTime(5000) })
    expect(shown(current.topology)).toBe('new')
  })

  it('shows the first frame of a reconnect that lands inside the throttle', () => {
    render('resources')
    latest().emit('topology', graph('a0'))
    vi.advanceTimersByTime(500)
    latest().emit('topology', graph('a1')) // queued with a timer
    render('traffic') // view change: new connection, topology cleared
    expect(FakeEventSource.all).toHaveLength(2)
    expect(current.topology).toBeNull()
    latest().emit('topology', graph('b'))
    act(() => { vi.advanceTimersByTime(5000) })
    expect(shown(current.topology)).toBe('b')
  })

  it('still applies the latest of a burst once the throttle ends', () => {
    render('resources')
    latest().emit('topology', graph('a'))
    vi.advanceTimersByTime(500)
    latest().emit('topology', graph('b'))
    latest().emit('topology', graph('c'))
    expect(shown(current.topology)).toBe('a')
    act(() => { vi.advanceTimersByTime(2000) })
    expect(shown(current.topology)).toBe('c')
  })
})
