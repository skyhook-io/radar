// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { isTouchIntent } from './touch-intent'

function press(pointerType: string) {
  const e = new Event('pointerdown', { bubbles: true }) as Event & { pointerType: string }
  Object.defineProperty(e, 'pointerType', { value: pointerType })
  document.body.dispatchEvent(e)
}

// Order matters: the module keeps the last touch time, so the mouse case
// runs before any touch has been recorded.
describe('isTouchIntent', () => {
  it('ignores mouse presses', () => {
    press('mouse')
    expect(isTouchIntent()).toBe(false)
  })

  it('is set right after a touch press and expires', () => {
    press('touch')
    const now = performance.now()
    expect(isTouchIntent(now)).toBe(true)
    expect(isTouchIntent(now + 2000)).toBe(false)
  })
})

describe('isTouchIntent and the keyboard', () => {
  it('ends the tap window on a key press', () => {
    press('touch')
    expect(isTouchIntent()).toBe(true)
    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true }))
    expect(isTouchIntent()).toBe(false)
  })
})
