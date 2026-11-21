// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it, vi } from 'vitest'

const state = vi.hoisted(() => ({ status: { connected: true } as { connected: boolean; historyReason?: string } }))
vi.mock('./client', () => ({
  fetchJSON: vi.fn(),
  useRadarFeature: () => ({ support: 'supported', guard: (fn: () => unknown) => fn(), gatedKey: [] }),
}))
vi.mock('@tanstack/react-query', () => ({ useQuery: () => ({ data: state.status }) }))

const { usePodHistory } = await import('./loki')

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

describe('usePodHistory', () => {
  // A status poll that catches Loki reconnecting must not flip an open History tab to Live.
  it('keeps History once it was ready', () => {
    const seen: boolean[] = []
    function Probe() {
      seen.push(!!usePodHistory('team', 'app-1'))
      return null
    }
    const root = createRoot(document.createElement('div'))
    act(() => root.render(<Probe />))
    state.status = { connected: false }
    act(() => root.render(<Probe />))
    act(() => root.unmount())
    expect(seen.at(-1)).toBe(true)
  })
})
