import { describe, it, expect } from 'vitest'
import { computeTooltipPosition } from './tooltip-position'

const VIEWPORT = { width: 1280, height: 800 }

describe('computeTooltipPosition', () => {
  it('returns null when the trigger has no layout (prevents 0,0 flash)', () => {
    expect(
      computeTooltipPosition({
        triggerRect: { top: 0, left: 0, width: 0, height: 0 },
        tooltipSize: { width: 80, height: 24 },
        position: 'top',
        viewport: VIEWPORT,
      })
    ).toBeNull()
  })

  it('returns a valid position even when the tooltip itself is unmeasured (size 0,0)', () => {
    // Real situation on the very first show: tooltip portal exists but
    // hasn't laid out yet, so its rect is (0,0,0,0). We still need a
    // sane anchor so the next frame's update has something to refine —
    // but it must NOT be (0,0).
    const result = computeTooltipPosition({
      triggerRect: { top: 12, left: 200, width: 80, height: 28 },
      tooltipSize: { width: 0, height: 0 },
      position: 'top',
      viewport: VIEWPORT,
    })
    expect(result).not.toBeNull()
    // Top placement above a near-the-edge trigger flips to bottom; either
    // way the tooltip must not land at the viewport origin.
    expect(result!.top).toBeGreaterThanOrEqual(8)
    expect(result!.left).toBeGreaterThanOrEqual(8)
  })

  it('centers a top-placed tooltip horizontally above the trigger', () => {
    const result = computeTooltipPosition({
      triggerRect: { top: 200, left: 400, width: 100, height: 40 },
      tooltipSize: { width: 80, height: 24 },
      position: 'top',
      viewport: VIEWPORT,
    })
    expect(result).toEqual({
      // 200 - 24 - 6
      top: 170,
      // 400 + 50 - 40
      left: 410,
    })
  })

  it('flips a top-placed tooltip to bottom when the trigger is too close to the top edge (nav button case)', () => {
    // Trigger near the top edge: "top" placement computes a negative top,
    // so the helper must flip below.
    const result = computeTooltipPosition({
      triggerRect: { top: 12, left: 200, width: 80, height: 28 },
      tooltipSize: { width: 80, height: 24 },
      position: 'top',
      viewport: VIEWPORT,
    })
    // 12 - 24 - 6 = -18 → flips to 12 + 28 + 6 = 46
    expect(result!.top).toBe(46)
    // Centered: 200 + 40 - 40 = 200
    expect(result!.left).toBe(200)
  })

  it('clamps a tooltip that would overflow the right edge', () => {
    const result = computeTooltipPosition({
      triggerRect: { top: 100, left: 1240, width: 30, height: 28 },
      tooltipSize: { width: 200, height: 24 },
      position: 'top',
      viewport: VIEWPORT,
    })
    // viewport.width - tooltipWidth - padding = 1280 - 200 - 8
    expect(result!.left).toBe(1072)
  })

  it('clamps a tooltip that would overflow the left edge', () => {
    const result = computeTooltipPosition({
      triggerRect: { top: 100, left: 4, width: 30, height: 28 },
      tooltipSize: { width: 200, height: 24 },
      position: 'top',
      viewport: VIEWPORT,
    })
    expect(result!.left).toBe(8)
  })

  it('places a bottom-anchored tooltip below the trigger', () => {
    const result = computeTooltipPosition({
      triggerRect: { top: 200, left: 400, width: 100, height: 40 },
      tooltipSize: { width: 80, height: 24 },
      position: 'bottom',
      viewport: VIEWPORT,
    })
    expect(result!.top).toBe(246)
  })

  it('places a left-anchored tooltip to the left of the trigger and centers vertically', () => {
    const result = computeTooltipPosition({
      triggerRect: { top: 200, left: 400, width: 100, height: 40 },
      tooltipSize: { width: 80, height: 24 },
      position: 'left',
      viewport: VIEWPORT,
    })
    expect(result).toEqual({
      // 200 + 20 - 12
      top: 208,
      // 400 - 80 - 6
      left: 314,
    })
  })

  it('places a right-anchored tooltip to the right of the trigger', () => {
    const result = computeTooltipPosition({
      triggerRect: { top: 200, left: 400, width: 100, height: 40 },
      tooltipSize: { width: 80, height: 24 },
      position: 'right',
      viewport: VIEWPORT,
    })
    expect(result!.left).toBe(506)
  })

  it('respects a custom padding', () => {
    const result = computeTooltipPosition({
      triggerRect: { top: 100, left: 4, width: 30, height: 28 },
      tooltipSize: { width: 200, height: 24 },
      position: 'top',
      viewport: VIEWPORT,
      padding: 20,
    })
    expect(result!.left).toBe(20)
  })
})
