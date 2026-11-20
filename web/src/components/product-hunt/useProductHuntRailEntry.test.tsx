// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const state = vi.hoisted(() => ({ mode: 'local', pending: false }))
vi.mock('../../api/client', () => ({
  useCapabilities: () => ({ data: { deployment: { mode: state.mode } } }),
  useVersionCheck: () => ({ data: undefined, isPending: state.pending }),
}))

import { useProductHuntRailEntry } from './useProductHuntRailEntry'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root
const seen = vi.fn<(entry: ReturnType<typeof useProductHuntRailEntry>) => void>()
const entry = () => seen.mock.lastCall?.[0]

function Probe() {
  seen(useProductHuntRailEntry())
  return null
}

function mount() {
  root = createRoot(document.createElement('div'))
  act(() => root.render(<Probe />))
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-10-29T17:00:00Z'))
  localStorage.clear()
  state.mode = 'local'
  state.pending = false
  seen.mockClear()
})

afterEach(() => {
  act(() => root.unmount())
  vi.useRealTimers()
})

describe('useProductHuntRailEntry', () => {
  it('is there for the launch window, and opens the card', () => {
    mount()
    expect(entry()?.id).toBe('product-hunt-rail-item')
    expect(entry()?.badge).toBe('Today')
    const reopen = vi.fn()
    window.addEventListener('radar:product-hunt-notice-reopen', reopen)
    entry()?.onOpen()
    expect(reopen).toHaveBeenCalledTimes(1)
  })

  it('stays away in-cluster before the day and after the window', () => {
    state.mode = 'in-cluster'
    vi.setSystemTime(new Date('2026-10-28T17:00:00Z'))
    mount()
    expect(entry()).toBeUndefined()
    act(() => root.unmount())

    state.mode = 'local'
    mount()
    expect(entry()?.badge).toBe('Oct 29')
    act(() => root.unmount())

    vi.setSystemTime(new Date('2026-10-30T17:00:00Z'))
    mount()
    expect(entry()).toBeUndefined()
  })

  it('waits for the version check, which may have moved or cancelled the launch', () => {
    state.pending = true
    mount()
    expect(entry()).toBeUndefined()
  })
})
