import { describe, expect, it } from 'vitest'
import { clampDrawerWidth, defaultDrawerWidth, drawerWidthBounds, DRAWER_MIN_WIDTH } from './drawer-width'

// Content area = window width less the pinned nav rail (176px).
const content = (windowWidth: number) => windowWidth - 176

describe('defaultDrawerWidth', () => {
  it('keeps 550 / 750 until the list behind has room to spare', () => {
    for (const w of [1280, 1366, 1440, 1680]) {
      expect(defaultDrawerWidth('pods', content(w))).toBe(550)
      expect(defaultDrawerWidth('vulnerabilityreports', content(w))).toBe(750)
    }
  })

  it('grows into the room beyond what the list needs', () => {
    const at1920 = defaultDrawerWidth('pods', content(1920))
    expect(at1920).toBeGreaterThan(550)
    expect(content(1920) - at1920).toBeGreaterThanOrEqual(1000)
  })

  it('stops growing at the cap, leaving the list most of an ultrawide screen', () => {
    const at2560 = defaultDrawerWidth('pods', content(2560))
    const at3840 = defaultDrawerWidth('pods', content(3840))
    expect(at3840).toBe(at2560)
    expect(at2560 / content(2560)).toBeLessThan(0.4)
  })

  it('keeps wide kinds wider than the rest at every width', () => {
    for (const w of [1280, 1680, 1920, 2560]) {
      expect(defaultDrawerWidth('PolicyReports', content(w))).toBeGreaterThan(defaultDrawerWidth('deployments', content(w)))
    }
  })

  it('never exceeds what the drag handle allows', () => {
    for (const available of [400, 700, 900, 1104, 1504, 1744, 2384]) {
      for (const kind of ['pods', 'clustercompliancereports']) {
        const { min, max } = drawerWidthBounds(available)
        const width = defaultDrawerWidth(kind, available)
        expect(width).toBeGreaterThanOrEqual(min)
        expect(width).toBeLessThanOrEqual(max)
      }
    }
  })
})

describe('clampDrawerWidth', () => {
  it('holds a dragged width inside the bounds when the content area shrinks', () => {
    expect(clampDrawerWidth(1400, content(1440))).toBe(drawerWidthBounds(content(1440)).max)
    expect(clampDrawerWidth(300, content(1920))).toBe(DRAWER_MIN_WIDTH)
    expect(clampDrawerWidth(800, content(1920))).toBe(800)
  })
})
