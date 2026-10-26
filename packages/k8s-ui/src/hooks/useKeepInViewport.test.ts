import { describe, expect, it } from 'vitest'
import { keepInViewportShift } from './useKeepInViewport'

describe('keepInViewportShift', () => {
  it('leaves a menu that fits alone', () => {
    expect(keepInViewportShift(56, 336, 1280, 8)).toBe(0)
  })

  it('pulls a menu past the right edge back inside the margin', () => {
    // A 280px menu opened at x=56 on a 320px phone.
    expect(keepInViewportShift(56, 336, 320, 8)).toBe(-24)
  })

  it('keeps the left edge when the menu is wider than the viewport', () => {
    expect(keepInViewportShift(20, 420, 320, 8)).toBe(-12)
  })

  it('pushes a menu past the left edge back in', () => {
    expect(keepInViewportShift(-30, 200, 390, 8)).toBe(38)
  })
})
