import { describe, it, expect, beforeEach } from 'vitest'
import { getEdgeColor, getEdgeStyle } from './TopologyGraph'

// SKY-832 bug 62: the "Policies" button toggled state and shipped a
// `?policyEffect=true` to the backend, but the resulting per-edge
// `policyEffect` field on the topology response was never consulted
// when computing edge color / style. Toggling the button therefore
// had zero visible effect on the canvas.
//
// These tests pin the priority rule (policy effect overrides view-mode
// and per-type colors) and the "blocked" presentation (red + dashed +
// thicker stroke).

describe('getEdgeColor', () => {
  it('uses the per-type palette in resource view when no policy effect', () => {
    expect(getEdgeColor('routes-to', false)).toBe('#22c55e')
    expect(getEdgeColor('exposes', false)).toBe('#3b82f6')
    expect(getEdgeColor('manages', false)).toBe('#64748b')
    expect(getEdgeColor('configures', false)).toBe('#f59e0b')
    expect(getEdgeColor('uses', false)).toBe('#ec4899')
  })

  it('uses traffic-green for every edge in traffic view', () => {
    expect(getEdgeColor('routes-to', true)).toBe('#22c55e')
    expect(getEdgeColor('manages', true)).toBe('#22c55e')
  })

  it('falls back to gray for unknown edge types', () => {
    expect(getEdgeColor('unknown-type', false)).toBe('#64748b')
  })

  it('policy-effect overrides per-type color in resource view', () => {
    expect(getEdgeColor('routes-to', false, 'allowed')).toBe('#10b981')
    expect(getEdgeColor('routes-to', false, 'blocked')).toBe('#ef4444')
    expect(getEdgeColor('routes-to', false, 'unprotected')).toBe('#f59e0b')
  })

  it('policy-effect overrides traffic-view color too', () => {
    expect(getEdgeColor('routes-to', true, 'blocked')).toBe('#ef4444')
    expect(getEdgeColor('exposes', true, 'allowed')).toBe('#10b981')
  })

  it('falls through to base behaviour for an unrecognised effect string', () => {
    // Defensive: an unexpected string from the backend should not
    // poison the renderer.
    expect(
      getEdgeColor('routes-to', false, 'mystery' as 'allowed'),
    ).toBe('#22c55e')
  })
})

describe('getEdgeStyle', () => {
  beforeEach(() => {
    // The implementation memoizes by composite key; the cache lives
    // for the lifetime of the module under test. That's fine for
    // production but means tests for different inputs need distinct
    // keys, which they naturally do.
  })

  it('blocked edges render red, dashed, and thicker than the base width', () => {
    const style = getEdgeStyle('routes-to', false, true, true, 'blocked')
    expect(style.stroke).toBe('#ef4444')
    expect(style.strokeDasharray).toBe('6 4')
    // Base width in resource view is 1.5; blocked adds 0.5.
    expect(style.strokeWidth).toBe(2)
  })

  it('blocked in traffic view also gets the +0.5 width bump', () => {
    const style = getEdgeStyle('routes-to', true, true, true, 'blocked')
    expect(style.strokeWidth).toBe(2.5) // traffic base 2 + 0.5
    expect(style.strokeDasharray).toBe('6 4')
  })

  it('allowed/unprotected use the policy color but keep the base presentation', () => {
    const allowed = getEdgeStyle('routes-to', false, true, false, 'allowed')
    expect(allowed.stroke).toBe('#10b981')
    expect(allowed.strokeWidth).toBe(1.5)
    // Not animated, not blocked → no dash.
    expect(allowed.strokeDasharray).toBeUndefined()

    const unprotected = getEdgeStyle('routes-to', false, true, false, 'unprotected')
    expect(unprotected.stroke).toBe('#f59e0b')
    expect(unprotected.strokeWidth).toBe(1.5)
  })

  it('without a policy effect, traffic-edge animations still produce the dotted dash', () => {
    const animated = getEdgeStyle('routes-to', true, true, true)
    expect(animated.strokeDasharray).toBe('5 5')
    expect(animated.stroke).toBe('#22c55e')
  })

  it('static traffic edges (animated=false) have no dash', () => {
    const style = getEdgeStyle('routes-to', true, true, false)
    expect(style.strokeDasharray).toBeUndefined()
  })
})
