import { describe, expect, it } from 'vitest'
import { costFreshnessLabel, costSourceLabel } from './source'

describe('cost source presentation', () => {
  it('distinguishes Prometheus windows from Kubecost ETL freshness', () => {
    expect(costSourceLabel('prometheus')).toBe('OpenCost via Prometheus')
    expect(costSourceLabel('kubecost')).toBe('Kubecost Aggregator')
    expect(costFreshnessLabel('prometheus', '1h')).toBe('last 1h average')
    expect(costFreshnessLabel('kubecost', '1h', '2026-08-26T13:58:00Z')).toContain('Kubecost data through')
  })
})
