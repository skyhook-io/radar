import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { attachTerminalKeys } from './terminalKeys'

function makeXterm(selection = '') {
  let keyHandler: ((e: KeyboardEvent) => boolean) | null = null
  return {
    key: (e: Partial<KeyboardEvent>) => keyHandler?.(e as KeyboardEvent),
    attachCustomKeyEventHandler: vi.fn((fn: (e: KeyboardEvent) => boolean) => { keyHandler = fn }),
    getSelection: vi.fn(() => selection),
    hasSelection: vi.fn(() => selection.length > 0),
  }
}

function keydown(init: Partial<KeyboardEvent>) {
  return { type: 'keydown', metaKey: false, ctrlKey: false, altKey: false, shiftKey: false, preventDefault: vi.fn(), stopPropagation: vi.fn(), ...init }
}

function setPlatform(platform: string) {
  Object.defineProperty(globalThis, 'navigator', {
    value: { platform, clipboard: { writeText: vi.fn(() => Promise.resolve()) } },
    configurable: true,
  })
}

const origNavigator = globalThis.navigator
afterEach(() => {
  Object.defineProperty(globalThis, 'navigator', { value: origNavigator, configurable: true })
})

describe('attachTerminalKeys - find', () => {
  describe('macOS', () => {
    beforeEach(() => setPlatform('MacIntel'))

    it('opens search on ⌘F and keeps the key from xterm, the browser and window listeners', () => {
      const xterm = makeXterm()
      const onFind = vi.fn()
      attachTerminalKeys(xterm as never, { onFind })
      const e = keydown({ metaKey: true, key: 'f' })
      expect(xterm.key(e)).toBe(false)
      expect(onFind).toHaveBeenCalledOnce()
      expect(e.preventDefault).toHaveBeenCalled()
      expect(e.stopPropagation).toHaveBeenCalled()
    })

    it('leaves Ctrl+F to the shell', () => {
      const xterm = makeXterm()
      const onFind = vi.fn()
      attachTerminalKeys(xterm as never, { onFind })
      expect(xterm.key(keydown({ ctrlKey: true, key: 'f' }))).toBe(true)
      expect(onFind).not.toHaveBeenCalled()
    })
  })

  describe('Linux and Windows', () => {
    beforeEach(() => setPlatform('Linux x86_64'))

    it('opens search on Ctrl+Shift+F, which arrives as key "F"', () => {
      const xterm = makeXterm()
      const onFind = vi.fn()
      attachTerminalKeys(xterm as never, { onFind })
      const e = keydown({ ctrlKey: true, shiftKey: true, key: 'F' })
      expect(xterm.key(e)).toBe(false)
      expect(onFind).toHaveBeenCalledOnce()
      expect(e.stopPropagation).toHaveBeenCalled()
    })

    it('leaves plain Ctrl+F to the shell (readline forward-char, less/vim page-forward)', () => {
      const xterm = makeXterm()
      const onFind = vi.fn()
      attachTerminalKeys(xterm as never, { onFind })
      expect(xterm.key(keydown({ ctrlKey: true, key: 'f' }))).toBe(true)
      expect(onFind).not.toHaveBeenCalled()
    })

    it('ignores the release of the shortcut', () => {
      const xterm = makeXterm()
      const onFind = vi.fn()
      attachTerminalKeys(xterm as never, { onFind })
      expect(xterm.key(keydown({ type: 'keyup', ctrlKey: true, shiftKey: true, key: 'F' }))).toBe(true)
      expect(onFind).not.toHaveBeenCalled()
    })

    it('does not copy on Ctrl+C, which stays SIGINT', () => {
      const xterm = makeXterm('x')
      attachTerminalKeys(xterm as never, { onFind: vi.fn() })
      expect(xterm.key(keydown({ ctrlKey: true, key: 'c' }))).toBe(true)
      expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
    })
  })
})

describe('attachTerminalKeys - ⌘C copy on macOS', () => {
  beforeEach(() => setPlatform('MacIntel'))

  it('copies the selection on ⌘C and swallows the event', () => {
    const xterm = makeXterm('cmd-c-text')
    attachTerminalKeys(xterm as never, { onFind: vi.fn() })
    const e = keydown({ metaKey: true, key: 'c' })
    expect(xterm.key(e)).toBe(false)
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('cmd-c-text')
    expect(e.preventDefault).toHaveBeenCalled()
  })

  it('leaves Ctrl+C alone so it still sends SIGINT', () => {
    const xterm = makeXterm('x')
    attachTerminalKeys(xterm as never, { onFind: vi.fn() })
    expect(xterm.key(keydown({ ctrlKey: true, key: 'c' }))).toBe(true)
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
  })

  it('does not treat ⌘⇧C as copy (leaves the chord for the browser)', () => {
    const xterm = makeXterm('x')
    attachTerminalKeys(xterm as never, { onFind: vi.fn() })
    expect(xterm.key(keydown({ metaKey: true, shiftKey: true, key: 'C' }))).toBe(true)
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
  })

  it('does nothing on ⌘C with no selection', () => {
    const xterm = makeXterm('')
    attachTerminalKeys(xterm as never, { onFind: vi.fn() })
    expect(xterm.key(keydown({ metaKey: true, key: 'c' }))).toBe(true)
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
  })
})
