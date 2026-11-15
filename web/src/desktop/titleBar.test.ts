import { describe, expect, it } from 'vitest'

import { RESIZE_EDGE_PX, isDoublePress, looksMaximised, mnemonicMenuIndex, resizeEdgeAt } from './titleBar'

describe('resizeEdgeAt', () => {
  const W = 1000
  const H = 800
  it('names edges and corners within the resize band', () => {
    expect(resizeEdgeAt(0, 0, W, H, false)).toBe('nw-resize')
    expect(resizeEdgeAt(W - 1, 0, W, H, false)).toBe('ne-resize')
    expect(resizeEdgeAt(0, H - 1, W, H, false)).toBe('sw-resize')
    expect(resizeEdgeAt(W - 1, H - 1, W, H, false)).toBe('se-resize')
    expect(resizeEdgeAt(500, 2, W, H, false)).toBe('n-resize')
    expect(resizeEdgeAt(500, H - 2, W, H, false)).toBe('s-resize')
    expect(resizeEdgeAt(2, 400, W, H, false)).toBe('w-resize')
    expect(resizeEdgeAt(W - 2, 400, W, H, false)).toBe('e-resize')
  })

  it('is null inside the window and whenever the window is maximised', () => {
    expect(resizeEdgeAt(RESIZE_EDGE_PX, 400, W, H, false)).toBeNull()
    expect(resizeEdgeAt(500, 400, W, H, false)).toBeNull()
    expect(resizeEdgeAt(0, 0, W, H, true)).toBeNull()
  })
})

describe('mnemonicMenuIndex', () => {
  const menus = ['File', 'Edit', 'View', 'Help'].map((label) => ({ label, items: [] }))
  const ev = (key: string, mods: Partial<KeyboardEvent> = {}) =>
    ({ key, altKey: true, ctrlKey: false, metaKey: false, shiftKey: false, ...mods }) as KeyboardEvent

  it('maps Alt+first letter to the menu, in either case', () => {
    expect(mnemonicMenuIndex(menus, ev('f'))).toBe(0)
    expect(mnemonicMenuIndex(menus, ev('E'))).toBe(1)
    expect(mnemonicMenuIndex(menus, ev('v'))).toBe(2)
    expect(mnemonicMenuIndex(menus, ev('h'))).toBe(3)
  })

  it('ignores other letters, plain keys and other modifiers', () => {
    expect(mnemonicMenuIndex(menus, ev('x'))).toBe(-1)
    expect(mnemonicMenuIndex(menus, ev('f', { altKey: false }))).toBe(-1)
    expect(mnemonicMenuIndex(menus, ev('f', { ctrlKey: true }))).toBe(-1)
    expect(mnemonicMenuIndex(menus, ev('f', { shiftKey: true }))).toBe(-1)
    expect(mnemonicMenuIndex(menus, ev('ArrowDown'))).toBe(-1)
  })
})

describe('isDoublePress', () => {
  it('needs both a short gap and almost no movement', () => {
    const first = { t: 1000, x: 100, y: 10 }
    expect(isDoublePress(first, { t: 1300, x: 101, y: 11 })).toBe(true)
    expect(isDoublePress(first, { t: 1600, x: 100, y: 10 })).toBe(false)
    expect(isDoublePress(first, { t: 1100, x: 140, y: 10 })).toBe(false)
  })
})

describe('looksMaximised', () => {
  it('compares the window with the work area', () => {
    expect(looksMaximised(1920, 1040, 1920, 1040)).toBe(true)
    expect(looksMaximised(1440, 900, 1920, 1040)).toBe(false)
  })
})
