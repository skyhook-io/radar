import { describe, expect, it } from 'vitest'
import type { OpenCostTrendSeries } from '../../api/client'
import { kubecostRetentionNote, kubecostTrendIsStale, kubecostTrendUnavailableMessage } from './CostTrendChart'

const hour = 60 * 60

function series(...timestamps: number[]): OpenCostTrendSeries[] {
  return [{
    namespace: 'default',
    dataPoints: timestamps.map((timestamp) => ({ timestamp, value: 1 })),
  }]
}

describe('kubecostRetentionNote', () => {
  it('describes a material retention gap', () => {
    const start = 1_000
    expect(kubecostRetentionNote(series(start + 4 * hour, start + 5 * hour), start, start + 7 * 24 * hour))
      .toContain('Kubecost history is available from')
  })

  it('stays quiet when the first hourly bucket follows the requested start', () => {
    const start = 1_000
    expect(kubecostRetentionNote(series(start + hour, start + 2 * hour), start, start + 24 * hour)).toBeNull()
  })

  it('stays quiet when there is only one retained sample', () => {
    const start = 1_000
    expect(kubecostRetentionNote(series(start + 4 * hour), start, start + 24 * hour)).toBeNull()
  })

  it('allows the normal first daily bucket without calling retention partial', () => {
    const start = 1_000
    expect(kubecostRetentionNote(series(start + 24 * hour, start + 48 * hour), start, start + 7 * 24 * hour)).toBeNull()
  })
})

describe('kubecostTrendIsStale', () => {
  it('allows normal hourly ingestion lag', () => {
    const now = Date.parse('2026-08-30T12:00:00Z')
    expect(kubecostTrendIsStale('2026-08-30T10:00:00Z', '6h', now)).toBe(false)
  })

  it('flags a source beyond the range lag budget', () => {
    const now = Date.parse('2026-08-30T12:00:00Z')
    expect(kubecostTrendIsStale('2026-08-30T02:00:00Z', '24h', now)).toBe(true)
    expect(kubecostTrendIsStale('2026-08-28T02:00:00Z', '7d', now)).toBe(true)
  })

  it('stays quiet without a valid retained timestamp', () => {
    expect(kubecostTrendIsStale(undefined, '6h')).toBe(false)
    expect(kubecostTrendIsStale('invalid', '6h')).toBe(false)
  })
})

describe('kubecostTrendUnavailableMessage', () => {
  it('distinguishes an empty namespace scope from missing Kubecost history', () => {
    expect(kubecostTrendUnavailableMessage('no_metrics', true)).toContain('current namespace scope')
    expect(kubecostTrendUnavailableMessage('no_metrics', false)).toContain('Kubecost has no retained')
  })

  it('distinguishes insufficient scoped history', () => {
    expect(kubecostTrendUnavailableMessage('insufficient_history', true)).toContain('namespace scope')
    expect(kubecostTrendUnavailableMessage('insufficient_history', false)).toContain('two retained samples')
  })
})
