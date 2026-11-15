// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'

import { installDesktopZoomShortcuts, zoomStep } from './desktop-zoom'

const key = (k: string, mods: Partial<KeyboardEventInit> = {}) =>
  ({ key: k, ctrlKey: true, altKey: false, metaKey: false, ...mods }) as KeyboardEvent

describe('zoomStep', () => {
  it('maps Ctrl+= / Ctrl++ to zoom in and Ctrl+- / Ctrl+_ to zoom out', () => {
    expect(zoomStep(key('='))).toBeCloseTo(0.1)
    expect(zoomStep(key('+'))).toBeCloseTo(0.1)
    expect(zoomStep(key('-'))).toBeCloseTo(-0.1)
    expect(zoomStep(key('_'))).toBeCloseTo(-0.1)
  })

  it('ignores other keys and modifier combinations', () => {
    expect(zoomStep(key('0'))).toBe(0)
    expect(zoomStep(key('=', { ctrlKey: false }))).toBe(0)
    expect(zoomStep(key('=', { altKey: true }))).toBe(0)
    expect(zoomStep(key('=', { metaKey: true }))).toBe(0)
  })
})

describe('installDesktopZoomShortcuts', () => {
  afterEach(() => {
    document.body.style.zoom = ''
    document.body.innerHTML = ''
  })

  const press = (target: EventTarget, k: string) =>
    target.dispatchEvent(new KeyboardEvent('keydown', { key: k, ctrlKey: true, bubbles: true, cancelable: true }))

  it('zooms the page inside WebView2, clamped to the menu range, but not from a terminal', () => {
    ;(window as unknown as { chrome: unknown }).chrome = { webview: {} }
    installDesktopZoomShortcuts()

    press(document.body, '=')
    expect(document.body.style.zoom).toBe('1.1')
    for (let i = 0; i < 20; i++) press(document.body, '=')
    expect(document.body.style.zoom).toBe('2')
    for (let i = 0; i < 30; i++) press(document.body, '-')
    expect(document.body.style.zoom).toBe('0.5')

    const term = document.createElement('div')
    term.className = 'xterm'
    const input = document.createElement('textarea')
    term.appendChild(input)
    document.body.appendChild(term)
    press(input, '=')
    expect(document.body.style.zoom).toBe('0.5')
  })
})
