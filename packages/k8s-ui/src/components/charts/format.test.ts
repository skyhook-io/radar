import { describe, expect, it } from 'vitest'
import { formatMetricValue } from './format'

describe('formatMetricValue', () => {
  it('scales unitless values with SI prefixes instead of a runaway k', () => {
    expect(formatMetricValue(19_027_100, '')).toBe('19.0M')
    expect(formatMetricValue(38_054_300, '')).toBe('38.1M')
    expect(formatMetricValue(57_081, '')).toBe('57.1k')
    expect(formatMetricValue(4_200_000_000, '')).toBe('4.20G')
    expect(formatMetricValue(812, '')).toBe('812')
  })

  it('formats seconds and negative values', () => {
    expect(formatMetricValue(0.25, 'seconds')).toBe('250ms')
    expect(formatMetricValue(90, 'seconds')).toBe('1.5m')
    expect(formatMetricValue(-3.5, '')).toBe('-3.50')
    expect(formatMetricValue(-2048, 'bytes')).toBe('-2.0 KiB')
  })
})
