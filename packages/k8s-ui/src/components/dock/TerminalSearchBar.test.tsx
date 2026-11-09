// @vitest-environment jsdom
import { act, useLayoutEffect } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Terminal as XTerm } from '@xterm/xterm'
import { TerminalSearchBar } from './TerminalSearchBar'
import { DockProvider, useDock } from './DockContext'
import { useTerminalSearch, type TerminalSearch } from './useTerminalSearch'

type Results = { resultIndex: number; resultCount: number }

const addon = vi.hoisted(() => ({ instances: [] as FakeAddonShape[], onFind: null as null | (() => void) }))
interface FakeAddonShape {
  listener: ((r: Results) => void) | null
  next: Results | null
  calls: string[]
  findPrevious: ReturnType<typeof vi.fn>
  findNext: ReturnType<typeof vi.fn>
  clearDecorations: ReturnType<typeof vi.fn>
  dispose: ReturnType<typeof vi.fn>
  emit: (r: Results) => void
}

vi.mock('@xterm/addon-search', () => ({
  SearchAddon: class {
    listener: ((r: Results) => void) | null = null
    next: Results | null = null
    calls: string[] = []
    // The real addon reports results synchronously from inside the call.
    findPrevious = vi.fn(() => { this.calls.push('findPrevious'); addon.onFind?.(); if (this.next) this.listener?.(this.next); return true })
    findNext = vi.fn(() => { this.calls.push('findNext'); if (this.next) this.listener?.(this.next); return true })
    clearDecorations = vi.fn(() => { this.calls.push('clearDecorations') })
    dispose = vi.fn(() => { this.calls.push('dispose') })
    onDidChangeResults(fn: (r: Results) => void) { this.listener = fn; return { dispose: () => { this.listener = null } } }
    emit(r: Results) { this.listener?.(r) }
    constructor() { addon.instances.push(this as unknown as FakeAddonShape) }
  },
}))

function makeXterm(bufferLength = 100) {
  const writeListeners = new Set<() => void>()
  return {
    rows: 24,
    options: { scrollback: 1000 },
    buffer: { active: { type: 'normal', length: bufferLength, viewportY: 0 } },
    scrollToLine: vi.fn(),
    loadAddon: vi.fn(),
    clearSelection: vi.fn(),
    focus: vi.fn(),
    onWriteParsed: vi.fn((fn: () => void) => { writeListeners.add(fn); return { dispose: () => writeListeners.delete(fn) } }),
    write: () => writeListeners.forEach(fn => fn()),
  }
}

let root: Root
let element: HTMLDivElement
let search: TerminalSearch
let detach: () => void

function Harness({ xterm, isActive }: { xterm: ReturnType<typeof makeXterm>; isActive: boolean }) {
  const s = useTerminalSearch(isActive)
  search = s
  useLayoutEffect(() => {
    detach = s.attach(xterm as unknown as XTerm)
  }, [s.attach, xterm])
  return <TerminalSearchBar search={s} />
}

async function render(xterm: ReturnType<typeof makeXterm>, isActive = true) {
  await act(async () => root.render(<Harness xterm={xterm} isActive={isActive} />))
}

let dock: ReturnType<typeof useDock>
function DockHandle() {
  dock = useDock()
  return null
}

const input = () => element.querySelector<HTMLInputElement>('input')!
const count = () => element.querySelector('[role="search"] span')
const bar = () => element.querySelector('[role="search"]')

async function type(value: string) {
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
    setter.call(input(), value)
    input().dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function press(target: Element, init: KeyboardEventInit) {
  await act(async () => { target.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init })) })
}

beforeEach(() => {
  addon.instances = []
  addon.onFind = null
  vi.stubGlobal('requestAnimationFrame', (cb: () => void) => { cb(); return 0 })
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})
afterEach(async () => {
  await act(async () => root.unmount())
  element.remove()
  vi.unstubAllGlobals()
})

describe('terminal search', () => {
  it('adds nothing to the output path until someone searches, and removes it on close', async () => {
    const xterm = makeXterm()
    await render(xterm)
    expect(addon.instances).toHaveLength(0)
    expect(xterm.onWriteParsed).not.toHaveBeenCalled()
    await act(async () => search.open())
    expect(bar()).not.toBeNull()
    expect(document.activeElement).toBe(input())
    expect(addon.instances).toHaveLength(0)
    await type('HIT')
    expect(addon.instances).toHaveLength(1)
    await act(async () => search.close())
    expect(addon.instances[0].dispose).toHaveBeenCalled()
  })

  it('starts every new query from the newest output', async () => {
    const xterm = makeXterm()
    await render(xterm)
    await act(async () => search.open())
    await type('HI')
    xterm.clearSelection.mockClear()
    await type('HIT')
    const [previous, current] = addon.instances
    expect(xterm.clearSelection).toHaveBeenCalled()
    expect(previous.dispose).toHaveBeenCalled()
    expect(current.calls).toEqual(['findPrevious'])
    expect(current.findPrevious).toHaveBeenLastCalledWith('HIT', expect.objectContaining({ caseSensitive: false }))
  })

  it('re-scans when Match case changes, so the count follows the option', async () => {
    const xterm = makeXterm()
    await render(xterm)
    await act(async () => search.open())
    await type('foo')
    await act(async () => element.querySelector<HTMLButtonElement>('button[aria-label="Match case"]')!.click())
    const [previous, current] = addon.instances
    expect(previous.dispose).toHaveBeenCalled()
    expect(current.findPrevious).toHaveBeenLastCalledWith('foo', expect.objectContaining({ caseSensitive: true }))
  })

  it('steps older on Enter and newer on Shift+Enter, and ignores Enter while composing', async () => {
    const xterm = makeXterm()
    await render(xterm)
    await act(async () => search.open())
    await type('HIT')
    const a = addon.instances[0]
    a.calls = []
    await press(input(), { key: 'Enter' })
    await press(input(), { key: 'Enter', shiftKey: true })
    expect(a.calls).toEqual(['findPrevious', 'findNext'])
    await press(input(), { key: 'Enter', isComposing: true })
    expect(a.calls).toEqual(['findPrevious', 'findNext'])
  })

  it.each(['input', 'Match case button'])('closes on Escape from the %s without the key reaching the page', async (from) => {
    const xterm = makeXterm()
    await render(xterm)
    await act(async () => search.open())
    await type('HIT')
    const a = addon.instances[0]
    xterm.clearSelection.mockClear()
    xterm.focus.mockClear()
    const pageListener = vi.fn()
    window.addEventListener('keydown', pageListener)
    const target = from === 'input' ? input() : element.querySelector('button[aria-label="Match case"]')!
    await press(target, { key: 'Escape' })
    window.removeEventListener('keydown', pageListener)
    expect(bar()).toBeNull()
    expect(a.dispose).toHaveBeenCalled()
    expect(xterm.clearSelection).toHaveBeenCalled()
    expect(xterm.focus).toHaveBeenCalled()
    expect(pageListener).not.toHaveBeenCalled()
  })

  it('keeps the find shortcut inside the bar', async () => {
    Object.defineProperty(navigator, 'platform', { value: 'MacIntel', configurable: true })
    const xterm = makeXterm()
    await render(xterm)
    await act(async () => search.open())
    const pageListener = vi.fn()
    window.addEventListener('keydown', pageListener)
    await press(input(), { key: 'f', metaKey: true })
    window.removeEventListener('keydown', pageListener)
    expect(pageListener).not.toHaveBeenCalled()
    expect(document.activeElement).toBe(input())
  })

  it.each([
    [{ resultIndex: 2, resultCount: 12 }, '3 / 12'],
    [{ resultIndex: -1, resultCount: 5 }, '5'],
    [{ resultIndex: 3, resultCount: 1000 }, '1,000+'],
    [{ resultIndex: -1, resultCount: 0 }, '0 results'],
  ])('shows the count for %o as %s', async (results, label) => {
    const xterm = makeXterm()
    await render(xterm)
    await act(async () => search.open())
    await type('x')
    await act(async () => addon.instances[0].emit(results))
    expect(count()?.textContent).toBe(label)
  })

  it('says older output is gone only when nothing matched and the history is full', async () => {
    const notFull = makeXterm(500)
    await render(notFull)
    await act(async () => search.open())
    await type('gone')
    await act(async () => addon.instances[0].emit({ resultIndex: -1, resultCount: 0 }))
    expect(element.textContent).not.toContain('Older output is no longer kept')

    await act(async () => root.unmount())
    root = createRoot(element)
    const full = makeXterm(1024)
    await render(full)
    await act(async () => search.open())
    await type('gone')
    await act(async () => addon.instances[1].emit({ resultIndex: -1, resultCount: 0 }))
    expect(element.textContent).toContain('Older output is no longer kept')
  })

  it('marks the count stale when output arrives, and only a full re-scan clears it', async () => {
    const xterm = makeXterm()
    await render(xterm)
    await act(async () => search.open())
    await type('HIT')
    const a = addon.instances[0]
    await act(async () => a.emit({ resultIndex: 0, resultCount: 3 }))
    expect(search.isStale).toBe(false)

    await act(async () => xterm.write())
    expect(search.isStale).toBe(true)

    // Stepping reports results without scanning the new output.
    a.next = { resultIndex: 1, resultCount: 3 }
    await press(input(), { key: 'Enter' })
    expect(count()?.textContent).toBe('2 / 3')
    expect(search.isStale).toBe(true)

    // The addon's catch-up pass after output goes quiet is a full scan.
    await act(async () => a.emit({ resultIndex: 1, resultCount: 4 }))
    expect(search.isStale).toBe(false)
  })

  it('keeps an IME Escape from reaching the page while leaving it to the IME', async () => {
    const xterm = makeXterm()
    await render(xterm)
    await act(async () => search.open())
    const pageListener = vi.fn()
    window.addEventListener('keydown', pageListener)
    const e = new KeyboardEvent('keydown', { key: 'Escape', isComposing: true, bubbles: true, cancelable: true })
    await act(async () => { input().dispatchEvent(e) })
    window.removeEventListener('keydown', pageListener)
    expect(pageListener).not.toHaveBeenCalled()
    expect(e.defaultPrevented).toBe(false)
    expect(bar()).not.toBeNull()
  })

  it('re-scans with a fresh addon once output goes quiet, keeping the current match and the view', async () => {
    vi.useFakeTimers()
    try {
      const xterm = makeXterm()
      await render(xterm)
      await act(async () => search.open())
      await type('HIT')
      const first = addon.instances[0]
      // The fresh scan moves the view (it would scroll to a match); catch-up must put it back.
      await act(async () => { xterm.write(); vi.advanceTimersByTime(100); xterm.write() })
      expect(addon.instances).toHaveLength(1)
      xterm.buffer.active.viewportY = 40
      addon.onFind = () => { xterm.buffer.active.viewportY = 10 }
      await act(async () => { vi.advanceTimersByTime(150) })
      const second = addon.instances[1]
      expect(first.dispose).toHaveBeenCalled()
      expect(second.findPrevious).toHaveBeenCalledWith('HIT', expect.objectContaining({ incremental: true }))
      expect(xterm.scrollToLine).toHaveBeenCalledWith(40)
      expect(xterm.clearSelection).toHaveBeenCalledTimes(1) // only the original query reset the selection
    } finally {
      vi.useRealTimers()
    }
  })

  it('drops highlights while the tab is hidden and searches again on return', async () => {
    const xterm = makeXterm()
    await render(xterm)
    await act(async () => search.open())
    await type('HIT')
    await render(xterm, false)
    expect(addon.instances[0].dispose).toHaveBeenCalled()
    await render(xterm, true)
    expect(addon.instances).toHaveLength(2)
    expect(addon.instances[1].findPrevious).toHaveBeenCalledWith('HIT', expect.anything())
  })

  it('drops highlights while the dock is collapsed and searches again when it expands', async () => {
    const xterm = makeXterm()
    await act(async () => root.render(<DockProvider><DockHandle /><Harness xterm={xterm} isActive /></DockProvider>))
    await act(async () => dock.setExpanded(true))
    await act(async () => search.open())
    await type('HIT')
    await act(async () => dock.setExpanded(false))
    expect(addon.instances[0].dispose).toHaveBeenCalled()
    await act(async () => dock.setExpanded(true))
    expect(addon.instances[1].findPrevious).toHaveBeenCalledWith('HIT', expect.anything())
  })

  it('keeps the user\'s selection when the bar closes without a search, and drops the match selection after one', async () => {
    const xterm = makeXterm()
    await render(xterm)
    await act(async () => search.open())
    await act(async () => search.close())
    expect(xterm.clearSelection).not.toHaveBeenCalled()
    await act(async () => search.open())
    await type('HIT')
    await act(async () => search.close())
    expect(xterm.clearSelection).toHaveBeenCalled()
  })

  it('closes when the terminal it searches is replaced', async () => {
    const xterm = makeXterm()
    await render(xterm)
    await act(async () => search.open())
    await act(async () => detach())
    expect(bar()).toBeNull()
  })
})
