// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TrafficDataNote } from './TrafficDataNote'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  vi.useFakeTimers()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.useRealTimers()
})

describe('TrafficDataNote', () => {
  it('shows a quiet chip and its explanation on keyboard focus without a warning banner', () => {
    const dataNote = 'Data on some edges may count the same calls twice.'
    act(() => root.render(<TrafficDataNote dataNote={dataNote} />))
    const chip = container.querySelector<HTMLElement>('[tabindex="0"]')!
    expect(chip.textContent).toBe(' Data may double-count')
    vi.spyOn(chip.parentElement!, 'getBoundingClientRect').mockReturnValue(new DOMRect(100, 100, 160, 24))
    act(() => {
      chip.focus()
      vi.advanceTimersByTime(350)
    })
    act(() => vi.advanceTimersByTime(50))
    const tooltip = document.querySelector('[role="tooltip"]')!
    expect(tooltip.textContent).toBe(dataNote)
    expect(tooltip.getAttribute('aria-hidden')).toBeNull()
    expect(document.body.textContent).not.toContain('Some values on this map are unreliable')
    expect(container.querySelector('[role="alert"], [role="status"]')).toBeNull()
    act(() => chip.blur())
    expect(document.querySelector('[role="tooltip"]')).toBeNull()
  })

  it('renders no chip when the note is absent or removed', () => {
    act(() => root.render(<TrafficDataNote />))
    expect(container.childElementCount).toBe(0)
    act(() => root.render(<TrafficDataNote dataNote="Reported bytes may overlap." />))
    expect(container.textContent).toContain('Data may double-count')
    act(() => root.render(<TrafficDataNote />))
    expect(container.childElementCount).toBe(0)
    expect(document.querySelector('[role="tooltip"]')).toBeNull()
  })
})
