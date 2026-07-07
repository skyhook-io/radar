import { describe, expect, it } from 'vitest'
import { DOMAIN_FRAME_FACTOR, frameDomainForSelection } from './RetainedTimelineScrubber'

const HOUR = 60 * 60 * 1000
const DAY = 24 * HOUR
const NOW = 1_700_000_000_000

// A 60-day span — the shape that made a 1h preset selection render sub-pixel.
const FULL = { fromMs: NOW - 60 * DAY, toMs: NOW }

describe('frameDomainForSelection (strip auto-framing)', () => {
  it('returns the full domain when the selection is a large fraction of it', () => {
    const sel = { fromMs: NOW - 30 * DAY, toMs: NOW }
    expect(frameDomainForSelection(sel, FULL)).toEqual(FULL)
  })

  it('frames a narrow selection at the live edge, clamped to the domain end', () => {
    const sel = { fromMs: NOW - HOUR, toMs: NOW }
    const framed = frameDomainForSelection(sel, FULL)
    expect(framed.toMs).toBe(NOW)
    expect(framed.toMs - framed.fromMs).toBe(HOUR * DOMAIN_FRAME_FACTOR)
    // The selection occupies 1/FACTOR of the strip — visible and grabbable.
    expect((sel.toMs - sel.fromMs) / (framed.toMs - framed.fromMs)).toBeCloseTo(1 / DOMAIN_FRAME_FACTOR)
  })

  it('centers a mid-domain selection', () => {
    const sel = { fromMs: NOW - 30 * DAY, toMs: NOW - 30 * DAY + HOUR }
    const framed = frameDomainForSelection(sel, FULL)
    const center = (sel.fromMs + sel.toMs) / 2
    expect((framed.fromMs + framed.toMs) / 2).toBeCloseTo(center)
    expect(framed.toMs - framed.fromMs).toBe(HOUR * DOMAIN_FRAME_FACTOR)
  })

  it('clamps at the domain start without shrinking the frame', () => {
    const sel = { fromMs: FULL.fromMs, toMs: FULL.fromMs + HOUR }
    const framed = frameDomainForSelection(sel, FULL)
    expect(framed.fromMs).toBe(FULL.fromMs)
    expect(framed.toMs - framed.fromMs).toBe(HOUR * DOMAIN_FRAME_FACTOR)
  })

  it('never frames outside the full domain', () => {
    const sel = { fromMs: NOW - HOUR, toMs: NOW }
    const framed = frameDomainForSelection(sel, FULL)
    expect(framed.fromMs).toBeGreaterThanOrEqual(FULL.fromMs)
    expect(framed.toMs).toBeLessThanOrEqual(FULL.toMs)
  })
})
