import { describe, expect, it } from 'vitest'
import { quotaUsageRatio } from './NamespaceRenderer'

describe('quotaUsageRatio', () => {
  // status.used is computed with quantity arithmetic, so a used pod count of
  // 1000 serializes canonically as "1k". A raw-float read sees 1 and reports a
  // saturated quota as barely used.
  it('reads count quantities with SI suffixes on either side of the ratio', () => {
    expect(quotaUsageRatio('pods', '1k', '1k')).toBe(1)
    expect(quotaUsageRatio('pods', '900', '1k')).toBeCloseTo(0.9)
    expect(quotaUsageRatio('count/pods', '1k', '2k')).toBeCloseTo(0.5)
  })

  it('leaves plain counts unchanged', () => {
    expect(quotaUsageRatio('pods', '5', '10')).toBe(0.5)
    expect(quotaUsageRatio('services', '0', '10')).toBe(0)
  })

  it('parses cpu and memory with their own unit parsers', () => {
    expect(quotaUsageRatio('cpu', '500m', '1')).toBeCloseTo(0.5)
    expect(quotaUsageRatio('requests.cpu', '2', '4')).toBeCloseTo(0.5)
    expect(quotaUsageRatio('memory', '512Mi', '1Gi')).toBeCloseTo(0.5)
    expect(quotaUsageRatio('requests.storage', '1Gi', '2Gi')).toBeCloseTo(0.5)
  })

  it('yields null when hard is unset or unparseable so the row falls back to raw strings', () => {
    expect(quotaUsageRatio('pods', '5', '')).toBeNull()
    expect(quotaUsageRatio('pods', '5', 'garbage')).toBeNull()
  })
})
