import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { setupTerminalClipboard, copyTerminalSelection } from './terminalClipboard'

// Minimal fakes — the helper touches onSelectionChange/getSelection/hasSelection/
// paste/modes/attachCustomKeyEventHandler on the terminal and add/removeEventListener
// on the element.
function makeXterm(opts: { selection?: string; bracketed?: boolean } = {}) {
  let selCb: (() => void) | null = null
  let keyHandler: ((e: KeyboardEvent) => boolean) | null = null
  const selection = opts.selection ?? ''
  return {
    _fireSelection: () => selCb?.(),
    _key: (e: Partial<KeyboardEvent>) => keyHandler?.(e as KeyboardEvent),
    onSelectionChange: vi.fn((fn: () => void) => { selCb = fn; return { dispose: vi.fn() } }),
    attachCustomKeyEventHandler: vi.fn((fn: (e: KeyboardEvent) => boolean) => { keyHandler = fn }),
    getSelection: vi.fn(() => selection),
    hasSelection: vi.fn(() => selection.length > 0),
    paste: vi.fn(),
    modes: { bracketedPasteMode: opts.bracketed ?? false },
  }
}

function makeElement() {
  const handlers: Record<string, EventListener> = {}
  return {
    handlers,
    addEventListener: vi.fn((type: string, fn: EventListener) => { handlers[type] = fn }),
    removeEventListener: vi.fn((type: string) => { delete handlers[type] }),
  }
}

function makePasteEvent(text: string) {
  return { clipboardData: { getData: () => text }, preventDefault: vi.fn(), stopImmediatePropagation: vi.fn() }
}

const flush = () => new Promise((r) => setTimeout(r, 0))

function setPlatform(platform: string) {
  Object.defineProperty(globalThis, 'navigator', {
    value: {
      platform,
      clipboard: {
        writeText: vi.fn(() => Promise.resolve()),
        readText: vi.fn(() => Promise.resolve('line one\nline two')),
      },
    },
    configurable: true,
  })
}

const origNavigator = globalThis.navigator
const allow = () => Promise.resolve(true)
const deny = () => Promise.resolve(false)

beforeEach(() => setPlatform('Linux x86_64'))
afterEach(() => {
  Object.defineProperty(globalThis, 'navigator', { value: origNavigator, configurable: true })
  vi.restoreAllMocks()
})

describe('copyTerminalSelection', () => {
  it('writes the selection to the clipboard', () => {
    copyTerminalSelection(makeXterm({ selection: 'pod-123' }) as never)
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('pod-123')
  })

  it('is a no-op when nothing is selected', () => {
    copyTerminalSelection(makeXterm({ selection: '' }) as never)
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
  })
})

describe('setupTerminalClipboard — selection reporting (no copy-on-select)', () => {
  it('reports selection presence but never copies on selection', () => {
    const xterm = makeXterm({ selection: 'hello' })
    const onSelectionChange = vi.fn()
    setupTerminalClipboard(xterm as never, makeElement() as never, { onSelectionChange })
    xterm._fireSelection()
    expect(onSelectionChange).toHaveBeenCalledWith(true)
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled() // <- key: selecting does NOT copy
  })
})

describe('setupTerminalClipboard — ⌘C copy on macOS', () => {
  beforeEach(() => setPlatform('MacIntel'))

  it('copies the selection on ⌘C and swallows the event', () => {
    const xterm = makeXterm({ selection: 'cmd-c-text' })
    setupTerminalClipboard(xterm as never, makeElement() as never)
    const e = { type: 'keydown', metaKey: true, key: 'c', preventDefault: vi.fn() }
    const result = xterm._key(e)
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('cmd-c-text')
    expect(e.preventDefault).toHaveBeenCalled()
    expect(result).toBe(false)
  })

  it('leaves Ctrl+C alone so it still sends SIGINT', () => {
    const xterm = makeXterm({ selection: 'x' })
    setupTerminalClipboard(xterm as never, makeElement() as never)
    const result = xterm._key({ type: 'keydown', ctrlKey: true, key: 'c', preventDefault: vi.fn() })
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
    expect(result).toBe(true)
  })

  it('does not treat ⌘⇧C as copy (leaves the chord for the browser)', () => {
    const xterm = makeXterm({ selection: 'x' })
    setupTerminalClipboard(xterm as never, makeElement() as never)
    const result = xterm._key({ type: 'keydown', metaKey: true, shiftKey: true, key: 'C', preventDefault: vi.fn() })
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
    expect(result).toBe(true)
  })

  it('does nothing on ⌘C with no selection', () => {
    const xterm = makeXterm({ selection: '' })
    setupTerminalClipboard(xterm as never, makeElement() as never)
    const result = xterm._key({ type: 'keydown', metaKey: true, key: 'c', preventDefault: vi.fn() })
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
    expect(result).toBe(true)
  })
})

describe('setupTerminalClipboard — no copy keybinding off macOS', () => {
  it('does not attach a key handler on Linux/Windows (Copy button only)', () => {
    const xterm = makeXterm({ selection: 'x' })
    setupTerminalClipboard(xterm as never, makeElement() as never)
    expect(xterm.attachCustomKeyEventHandler).not.toHaveBeenCalled()
  })
})

describe('setupTerminalClipboard — right-click paste (non-mac)', () => {
  it('pastes via xterm.paste on right-click', async () => {
    const xterm = makeXterm()
    const el = makeElement()
    setupTerminalClipboard(xterm as never, el as never, { confirmPaste: allow })
    expect(el.addEventListener).toHaveBeenCalledWith('contextmenu', expect.any(Function))
    await el.handlers.contextmenu({ preventDefault: vi.fn() } as never)
    await flush()
    expect(xterm.paste).toHaveBeenCalledWith('line one\nline two')
  })

  it('does not paste when the multi-line confirmation is declined', async () => {
    const xterm = makeXterm()
    const el = makeElement()
    const confirmPaste = vi.fn(deny)
    setupTerminalClipboard(xterm as never, el as never, { confirmPaste })
    await el.handlers.contextmenu({ preventDefault: vi.fn() } as never)
    await flush()
    expect(confirmPaste).toHaveBeenCalled()
    expect(xterm.paste).not.toHaveBeenCalled()
  })
})

describe('setupTerminalClipboard — multi-line paste guard', () => {
  it('blocks a declined multi-line paste when bracketed-paste is off', async () => {
    const xterm = makeXterm({ bracketed: false })
    const el = makeElement()
    const confirmPaste = vi.fn(deny)
    setupTerminalClipboard(xterm as never, el as never, { confirmPaste })
    const ev = makePasteEvent('rm -rf /tmp/a\nrm -rf /tmp/b')
    el.handlers.paste(ev as never)
    expect(ev.preventDefault).toHaveBeenCalled()
    expect(ev.stopImmediatePropagation).toHaveBeenCalled()
    expect(confirmPaste).toHaveBeenCalledWith({ lineCount: 2, text: 'rm -rf /tmp/a\nrm -rf /tmp/b' })
    await flush()
    expect(xterm.paste).not.toHaveBeenCalled()
  })

  it('confirms then pastes a multi-line paste that is accepted', async () => {
    const xterm = makeXterm({ bracketed: false })
    const el = makeElement()
    setupTerminalClipboard(xterm as never, el as never, { confirmPaste: allow })
    const ev = makePasteEvent('one\ntwo\nthree')
    el.handlers.paste(ev as never)
    expect(ev.preventDefault).toHaveBeenCalled()
    await flush()
    expect(xterm.paste).toHaveBeenCalledWith('one\ntwo\nthree')
  })

  it('does not paste once the helper is disposed (reconnect/teardown mid-confirm)', async () => {
    const xterm = makeXterm({ bracketed: false })
    const el = makeElement()
    const dispose = setupTerminalClipboard(xterm as never, el as never, { confirmPaste: allow })
    el.handlers.paste(makePasteEvent('one\ntwo') as never)
    dispose() // terminal torn down before the (already-resolved) confirm runs
    await flush()
    expect(xterm.paste).not.toHaveBeenCalled()
  })

  it('does not warn on a single-line paste', () => {
    const el = makeElement()
    const confirmPaste = vi.fn(allow)
    setupTerminalClipboard(makeXterm({ bracketed: false }) as never, el as never, { confirmPaste })
    const ev = makePasteEvent('just one line\n')
    el.handlers.paste(ev as never)
    expect(confirmPaste).not.toHaveBeenCalled()
    expect(ev.preventDefault).not.toHaveBeenCalled()
  })

  it('does not warn when bracketed-paste mode is on, even for multi-line', () => {
    const el = makeElement()
    const confirmPaste = vi.fn(allow)
    setupTerminalClipboard(makeXterm({ bracketed: true }) as never, el as never, { confirmPaste })
    const ev = makePasteEvent('one\ntwo')
    el.handlers.paste(ev as never)
    expect(confirmPaste).not.toHaveBeenCalled()
    expect(ev.preventDefault).not.toHaveBeenCalled()
  })
})

describe('setupTerminalClipboard — teardown', () => {
  it('disposer removes listeners and disposes the selection listener', () => {
    const xterm = makeXterm()
    const el = makeElement()
    const dispose = setupTerminalClipboard(xterm as never, el as never, { confirmPaste: allow })
    const selectionDisposable = xterm.onSelectionChange.mock.results[0].value
    dispose()
    expect(selectionDisposable.dispose).toHaveBeenCalled()
    expect(el.removeEventListener).toHaveBeenCalledWith('paste', expect.any(Function), true)
    expect(el.removeEventListener).toHaveBeenCalledWith('contextmenu', expect.any(Function))
  })
})
