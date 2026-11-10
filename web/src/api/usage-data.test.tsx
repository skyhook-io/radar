// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { automaticRequestHeaders, setAutomaticRequestMarking } from './config'
import { useUsageRecording, type UsageDataStatus } from './usage-data'
import { exampleStatus } from './usage-data.fixtures'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const TICK_MS = 5 * 60_000
let root: Root
let container: HTMLDivElement
let visibility: DocumentVisibilityState
const fetchMock = vi.fn<(...args: unknown[]) => Promise<Response>>(() => Promise.resolve(new Response(null, { status: 204 })))

function Recorder({ view, status }: { view: string; status: UsageDataStatus | undefined }) {
  useUsageRecording(view, status)
  return null
}

function render(view: string, status: UsageDataStatus | undefined) {
  act(() => root.render(<Recorder view={view} status={status} />))
}

function sentEvents(): Array<Record<string, unknown>> {
  return fetchMock.mock.calls.map((call) => JSON.parse(String((call[1] as RequestInit).body)))
}

function activeEvents() {
  return sentEvents().filter((e) => e.type === 'active')
}

function tick() {
  act(() => {
    vi.advanceTimersByTime(TICK_MS)
  })
}

const on = exampleStatus({ state: 'on', source: 'user' })

beforeEach(() => {
  vi.useFakeTimers()
  vi.stubGlobal('fetch', fetchMock)
  fetchMock.mockClear()
  visibility = 'visible'
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => visibility })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  // Leave recording off for whatever runs next in this worker.
  render('home', undefined)
  act(() => root.unmount())
  container.remove()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('useUsageRecording active time', () => {
  it('counts a tick only when someone used the visible page during it', () => {
    render('topology', on)
    tick()
    expect(activeEvents()).toEqual([])

    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'j' }))
    })
    tick()
    expect(activeEvents()).toEqual([{ type: 'active', minutes: 5 }])

    // Input belongs to the tick it happened in.
    tick()
    expect(activeEvents()).toHaveLength(1)
  })

  it('does not count a hidden page, even with input', () => {
    render('topology', on)
    visibility = 'hidden'
    act(() => {
      window.dispatchEvent(new PointerEvent('pointerdown'))
    })
    tick()
    expect(activeEvents()).toEqual([])
  })

  it('ignores a page that scrolls by itself, such as logs being followed', () => {
    render('resources:pods', on)
    const list = document.createElement('div')
    container.appendChild(list)
    act(() => {
      list.dispatchEvent(new Event('scroll'))
    })
    tick()
    expect(activeEvents()).toEqual([])
  })

  it('sends nothing while usage data is off', () => {
    render('topology', exampleStatus({ state: 'off', source: 'user' }))
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'j' }))
    })
    tick()
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('automatic request marking', () => {
  afterEach(() => setAutomaticRequestMarking(false))

  it('keeps an embedding host unmarked', () => {
    render('home', undefined)
    expect(automaticRequestHeaders()).toEqual({})
  })

  it('stays marked until the status says usage data is not recorded', () => {
    setAutomaticRequestMarking(true)
    render('home', undefined)
    expect(automaticRequestHeaders()).toEqual({ 'X-Radar-Request-Origin': 'auto' })
    render('home', on)
    expect(automaticRequestHeaders()).toEqual({ 'X-Radar-Request-Origin': 'auto' })
    render('home', exampleStatus({ state: 'off', source: 'user' }))
    expect(automaticRequestHeaders()).toEqual({})
  })

  it('marks requests when usage data is turned on later', () => {
    render('home', exampleStatus({ state: 'off', source: 'user' }))
    expect(automaticRequestHeaders()).toEqual({})
    render('home', on)
    expect(automaticRequestHeaders()).toEqual({ 'X-Radar-Request-Origin': 'auto' })
  })
})
