import { describe, it, expect } from 'vitest'
import { mapHealthToTone, worseTone } from './status-tone'

// Inputs flow in from heterogeneous sources (Problems API, Audit findings,
// multi-cluster aggregation). A regression here is a silent visual
// downgrade — the wrong color renders, no error thrown. Pin the
// canonical mappings so a vocabulary drift on either side fails loudly.

describe('mapHealthToTone', () => {
  it('maps healthy/ok/success/passing → healthy', () => {
    expect(mapHealthToTone('healthy')).toBe('healthy')
    expect(mapHealthToTone('ok')).toBe('healthy')
    expect(mapHealthToTone('success')).toBe('healthy')
    expect(mapHealthToTone('passing')).toBe('healthy')
  })

  it('maps degraded/warning/warn/medium → degraded', () => {
    expect(mapHealthToTone('degraded')).toBe('degraded')
    expect(mapHealthToTone('warning')).toBe('degraded')
    expect(mapHealthToTone('warn')).toBe('degraded')
    expect(mapHealthToTone('medium')).toBe('degraded')
  })

  it('maps high/alert → alert (the orange tier)', () => {
    expect(mapHealthToTone('high')).toBe('alert')
    expect(mapHealthToTone('alert')).toBe('alert')
  })

  it('maps unhealthy/danger/critical/error/failed → unhealthy', () => {
    expect(mapHealthToTone('unhealthy')).toBe('unhealthy')
    expect(mapHealthToTone('danger')).toBe('unhealthy')
    expect(mapHealthToTone('critical')).toBe('unhealthy')
    expect(mapHealthToTone('error')).toBe('unhealthy')
    expect(mapHealthToTone('failed')).toBe('unhealthy')
  })

  it('maps neutral/info → neutral', () => {
    expect(mapHealthToTone('neutral')).toBe('neutral')
    expect(mapHealthToTone('info')).toBe('neutral')
  })

  it('falls back to unknown for unrecognized input', () => {
    expect(mapHealthToTone('frobnicated')).toBe('unknown')
    expect(mapHealthToTone('')).toBe('unknown')
    expect(mapHealthToTone('low')).toBe('unknown')
  })

  it('is case-insensitive', () => {
    expect(mapHealthToTone('CRITICAL')).toBe('unhealthy')
    expect(mapHealthToTone('Healthy')).toBe('healthy')
    expect(mapHealthToTone('HIGH')).toBe('alert')
  })
})

describe('worseTone', () => {
  it('never reads calmer than a part that could not be read', () => {
    expect(worseTone('healthy', 'unknown')).toBe('unknown')
    expect(worseTone('unknown', 'degraded')).toBe('degraded')
    expect(worseTone('alert', 'unhealthy')).toBe('unhealthy')
    expect(worseTone('neutral', 'healthy')).toBe('neutral')
  })
})
