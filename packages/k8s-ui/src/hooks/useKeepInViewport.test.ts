import { describe, expect, it } from 'vitest'
import { keepInViewportShift } from './useKeepInViewport'

describe('keepInViewportShift', () => {
  it('leaves a menu that fits alone', () => {
    expect(keepInViewportShift(56, 336, 8, 1272)).toBe(0)
  })

  it('pulls a menu past the right edge back inside', () => {
    // A 280px menu opened at x=56 on a 320px phone.
    expect(keepInViewportShift(56, 336, 8, 312)).toBe(-24)
  })

  it('stops at a clipping ancestor, not the viewport edge', () => {
    // Content column starts after a 56px rail: the left bound is 64, not 8,
    // so a menu sitting under the rail moves out from behind it.
    expect(keepInViewportShift(40, 240, 64, 312)).toBe(24)
  })

  it('keeps the left edge when the menu is wider than the bounds', () => {
    expect(keepInViewportShift(20, 420, 8, 312)).toBe(-12)
  })

  it('pushes a menu past the left edge back in', () => {
    expect(keepInViewportShift(-30, 200, 8, 382)).toBe(38)
  })
})
