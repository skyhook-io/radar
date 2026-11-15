// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { setupTerminalClipboard, copyTerminalSelection, type TerminalCopyOnSelectState } from './terminalClipboard'

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
    modes: { bracketedPasteMode: opts.bracketed ?? false, mouseTrackingMode: 'none' },
    options: { macOptionClickForcesSelection: false },
  }
}

function makeElement() {
  const handlers: Record<string, EventListener> = {}
  const doc = document.implementation.createHTMLDocument()
  const element = doc.createElement('div')
  const terminal = doc.createElement('div')
  terminal.className = 'xterm'
  const screen = doc.createElement('div')
  screen.className = 'xterm-screen'
  terminal.append(screen)
  element.append(terminal)
  doc.body.append(element)
  for (const target of [element, doc]) {
    const add = target.addEventListener.bind(target)
    const remove = target.removeEventListener.bind(target)
    vi.spyOn(target, 'addEventListener').mockImplementation((type, fn, options) => {
      if (typeof fn === 'function') handlers[type] = fn
      add(type, fn, options)
    })
    vi.spyOn(target, 'removeEventListener').mockImplementation((type, fn, options) => {
      delete handlers[type]
      remove(type, fn, options)
    })
  }
  return Object.assign(element, { handlers, screen, terminal })
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

describe('optional copy on select', () => {
  it.each(['new account on', 'new account off', 'still pending'])('checks the current choice at release when a selection began pending: %s', (scenario) => {
    const xterm = makeXterm({ selection: 'selected output' })
    const el = makeElement()
    let currentChoice: TerminalCopyOnSelectState = 'pending'
    const onCopyPending = vi.fn()
    setupTerminalClipboard(xterm as never, el, { copyOnSelect: () => currentChoice, onCopyPending })
    el.screen.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0, clientX: 1 }))
    el.screen.dispatchEvent(new MouseEvent('mousemove', { bubbles: true, buttons: 1, clientX: 30 }))
    expect(xterm.getSelection).not.toHaveBeenCalled()
    currentChoice = scenario === 'new account on' ? 'on' : scenario === 'new account off' ? 'off' : 'pending'
    el.screen.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, button: 0 }))
    if (scenario === 'new account on') {
      expect(navigator.clipboard.writeText).toHaveBeenCalledExactlyOnceWith('selected output')
    } else {
      expect(xterm.getSelection).not.toHaveBeenCalled()
      expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
    }
    expect(onCopyPending).toHaveBeenCalledTimes(scenario === 'still pending' ? 1 : 0)
  })

  it('does not report pending copy when there is no selected text', () => {
    const xterm = makeXterm()
    const el = makeElement()
    const onCopyPending = vi.fn()
    setupTerminalClipboard(xterm as never, el, { copyOnSelect: () => 'pending', onCopyPending })
    el.screen.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0, detail: 2 }))
    el.screen.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, button: 0, detail: 2 }))
    expect(onCopyPending).not.toHaveBeenCalled()
    expect(xterm.getSelection).not.toHaveBeenCalled()
  })

  it.each(['plain click', 'mouse tracking', 'scrollbar', 'abandoned gesture'])('does not copy an old Find selection on %s', (scenario) => {
    const xterm = makeXterm({ selection: 'Find match' })
    const el = makeElement()
    const outside = el.ownerDocument.createElement('button')
    el.ownerDocument.body.append(outside)
    const scrollbar = el.ownerDocument.createElement('div')
    scrollbar.className = 'scrollbar'
    el.terminal.append(scrollbar)
    if (scenario === 'mouse tracking') xterm.modes.mouseTrackingMode = 'vt200'
    setupTerminalClipboard(xterm as never, el, { copyOnSelect: () => 'on' })
    const target = scenario === 'scrollbar' ? scrollbar : el.screen
    target.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0, clientX: 1 }))
    if (scenario !== 'plain click') {
      target.dispatchEvent(new MouseEvent('mousemove', { bubbles: true, buttons: 1, clientX: 30 }))
    }
    if (scenario === 'abandoned gesture') {
      outside.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0 }))
    }
    outside.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, button: 0 }))
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
  })

  it('captures a selection release even when its target stops propagation', () => {
    const xterm = makeXterm({ selection: 'selected output' })
    const el = makeElement()
    const outside = el.ownerDocument.createElement('button')
    el.ownerDocument.body.append(outside)
    outside.addEventListener('mouseup', event => event.stopPropagation())
    const dispose = setupTerminalClipboard(xterm as never, el, { copyOnSelect: () => 'on' })
    el.screen.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0, clientX: 1 }))
    el.screen.dispatchEvent(new MouseEvent('mousemove', { bubbles: true, buttons: 1, clientX: 30 }))
    outside.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, button: 0 }))
    expect(navigator.clipboard.writeText).toHaveBeenCalledExactlyOnceWith('selected output')
    outside.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, button: 0 }))
    expect(navigator.clipboard.writeText).toHaveBeenCalledTimes(1)
    dispose()
    expect(el.ownerDocument.removeEventListener).toHaveBeenCalledWith('mouseup', expect.any(Function), true)
  })

  it.each([2, 3])('copies a %s-click selection without mouse movement', (detail) => {
    const xterm = makeXterm({ selection: 'selected output' })
    const el = makeElement()
    setupTerminalClipboard(xterm as never, el, { copyOnSelect: () => 'on' })
    el.screen.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0, detail }))
    el.screen.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, button: 0, detail }))
    expect(navigator.clipboard.writeText).toHaveBeenCalledExactlyOnceWith('selected output')
  })

  it('allows shift selection while a terminal app handles ordinary mouse events', () => {
    const xterm = makeXterm({ selection: 'selected output' })
    xterm.modes.mouseTrackingMode = 'vt200'
    const el = makeElement()
    setupTerminalClipboard(xterm as never, el, { copyOnSelect: () => 'on' })
    el.screen.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0, shiftKey: true }))
    el.screen.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, button: 0, shiftKey: true }))
    expect(navigator.clipboard.writeText).toHaveBeenCalledExactlyOnceWith('selected output')
  })

  it('copies a drag starting in xterm padding', () => {
    const xterm = makeXterm({ selection: 'selected output' })
    const el = makeElement()
    setupTerminalClipboard(xterm as never, el, { copyOnSelect: () => 'on' })
    el.terminal.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0, clientX: 1 }))
    el.screen.dispatchEvent(new MouseEvent('mousemove', { bubbles: true, buttons: 1, clientX: 30 }))
    el.screen.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, button: 0 }))
    expect(navigator.clipboard.writeText).toHaveBeenCalledExactlyOnceWith('selected output')
  })

  it('reports automatic copy rejection once per terminal and ignores a late rejection after disposal', async () => {
    vi.mocked(navigator.clipboard.writeText).mockRejectedValue(new Error('denied'))
    const xterm = makeXterm({ selection: 'selected output' })
    const el = makeElement()
    const onCopyError = vi.fn()
    setupTerminalClipboard(xterm as never, el, { copyOnSelect: () => 'on', onCopyError })
    for (let i = 0; i < 2; i++) {
      el.screen.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0, detail: 2 }))
      el.screen.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, button: 0, detail: 2 }))
      await flush()
    }
    expect(onCopyError).toHaveBeenCalledOnce()
    const late = makeElement()
    const onLateError = vi.fn()
    const dispose = setupTerminalClipboard(xterm as never, late, { copyOnSelect: () => 'on', onCopyError: onLateError })
    late.screen.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0, detail: 2 }))
    late.screen.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, button: 0, detail: 2 }))
    dispose()
    await flush()
    expect(onLateError).not.toHaveBeenCalled()
  })

  it('allows a new failure notice after an automatic copy succeeds', async () => {
    vi.mocked(navigator.clipboard.writeText)
      .mockRejectedValueOnce(new Error('denied'))
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new Error('denied again'))
    const xterm = makeXterm({ selection: 'selected output' })
    const el = makeElement()
    const onCopyError = vi.fn()
    setupTerminalClipboard(xterm as never, el, { copyOnSelect: () => 'on', onCopyError })
    for (let i = 0; i < 3; i++) {
      el.screen.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0, detail: 2 }))
      el.screen.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, button: 0, detail: 2 }))
      await flush()
    }
    expect(onCopyError).toHaveBeenCalledTimes(2)
  })

  it('copies only at the end of a deliberate selection and reads the live preference', async () => {
    const xterm = makeXterm({ selection: 'selected output' })
    const el = makeElement()
    let enabled = false
    setupTerminalClipboard(xterm as never, el as never, { copyOnSelect: () => enabled ? 'on' : 'off' })
    const select = () => {
      el.handlers.mousedown({ button: 0, target: el.screen } as never)
      xterm._fireSelection()
      expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
      el.handlers.mouseup({ button: 0, detail: 2 } as never)
    }
    select()
    await flush()
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
    enabled = true
    select()
    await flush()
    expect(navigator.clipboard.writeText).toHaveBeenCalledExactlyOnceWith('selected output')
  })

  it.each(['MacIntel', 'Linux x86_64'])('never copies programmatic Find selection or right-click selection on %s', async (platform) => {
    setPlatform(platform)
    const xterm = makeXterm({ selection: 'output' })
    const el = makeElement()
    setupTerminalClipboard(xterm as never, el as never, { copyOnSelect: () => 'on' })
    xterm._fireSelection()
    el.handlers.mousedown({ button: 2 } as never)
    xterm._fireSelection()
    el.handlers.mouseup({ button: 2 } as never)
    if (el.handlers.contextmenu) el.handlers.contextmenu({ preventDefault: vi.fn() } as never)
    await flush()
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
    expect(xterm.paste).toHaveBeenCalledTimes(platform === 'MacIntel' ? 0 : 1)
  })

  it('treats macOS ctrl-click as a context-menu gesture', async () => {
    setPlatform('MacIntel')
    const xterm = makeXterm({ selection: 'output' })
    const el = makeElement()
    setupTerminalClipboard(xterm as never, el as never, { copyOnSelect: () => 'on' })
    el.handlers.mousedown({ button: 0, ctrlKey: true } as never)
    xterm._fireSelection()
    el.handlers.mouseup({ button: 0, ctrlKey: true } as never)
    await flush()
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
  })

  it.each(['empty', 'disposed', 'disabled before release'])('does not copy a %s selection', async (scenario) => {
    const xterm = makeXterm({ selection: scenario === 'empty' ? '' : 'output' })
    const el = makeElement()
    let enabled = true
    const dispose = setupTerminalClipboard(xterm as never, el as never, { copyOnSelect: () => enabled ? 'on' : 'off' })
    el.handlers.mousedown({ button: 0, target: el.screen } as never)
    xterm._fireSelection()
    const mouseup = el.handlers.mouseup
    if (scenario === 'disposed') dispose()
    if (scenario === 'disabled before release') enabled = false
    mouseup({ button: 0, detail: 2 } as never)
    await flush()
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
  })

  it('copies repeated selection even when xterm reports selection change after release', () => {
    const xterm = makeXterm({ selection: 'same output' })
    const el = makeElement()
    setupTerminalClipboard(xterm as never, el as never, { copyOnSelect: () => 'on' })
    for (let i = 0; i < 2; i++) {
      el.handlers.mousedown({ button: 0, target: el.screen } as never)
      el.handlers.mouseup({ button: 0, detail: 2 } as never)
      xterm._fireSelection()
    }
    expect(navigator.clipboard.writeText).toHaveBeenCalledTimes(2)
  })

  it('handles clipboard denial and browsers without a clipboard API', async () => {
    vi.mocked(navigator.clipboard.writeText).mockRejectedValue(new Error('permission denied'))
    const xterm = makeXterm({ selection: 'output' })
    copyTerminalSelection(xterm as never)
    await flush()
    Object.defineProperty(navigator, 'clipboard', { value: undefined })
    expect(() => copyTerminalSelection(xterm as never)).not.toThrow()
  })
})

describe('setupTerminalClipboard - key handler', () => {
  // xterm keeps one custom key handler; attachTerminalKeys owns it, so a second
  // registration here would silently replace the find and ⌘C keys.
  it.each(['MacIntel', 'Linux x86_64'])('never attaches a key handler (%s)', (platform) => {
    setPlatform(platform)
    const xterm = makeXterm({ selection: 'x' })
    setupTerminalClipboard(xterm as never, makeElement() as never)
    expect(xterm.attachCustomKeyEventHandler).not.toHaveBeenCalled()
  })
})

describe('setupTerminalClipboard — right-click paste (non-mac)', () => {
  it('keeps the native context menu available when the clipboard API is missing', () => {
    Object.defineProperty(navigator, 'clipboard', { value: undefined })
    const el = makeElement()
    setupTerminalClipboard(makeXterm() as never, el)
    const event = { preventDefault: vi.fn() }
    el.handlers.contextmenu(event as never)
    expect(event.preventDefault).not.toHaveBeenCalled()
  })
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
