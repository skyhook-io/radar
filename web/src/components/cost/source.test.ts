import { describe, expect, it } from 'vitest'
import { costFreshnessLabel, costIntegrationUnavailableMessage, costRateLabels, costSourceLabel, isCostDiscoveryPending } from './source'

describe('cost source presentation', () => {
  it('distinguishes Prometheus windows from Kubecost ETL freshness', () => {
    expect(costSourceLabel('prometheus')).toBe('OpenCost via Prometheus')
    expect(costSourceLabel('kubecost')).toBe('Kubecost Aggregator')
    expect(costFreshnessLabel('prometheus', '1h')).toBe('last 1h average')
    expect(costFreshnessLabel('kubecost', '1h', '2026-08-26T13:58:00Z')).toContain('1-hour allocation average')
    expect(costFreshnessLabel('kubecost', '1d', '2026-08-26T13:58:00Z')).toContain('1-day allocation average')
  })

  it('labels fallback allocation windows without calling a daily average current', () => {
    expect(costRateLabels('1h').hourly).toBe('Hourly (1-hour average)')
    expect(costRateLabels('1d').rate).toBe('1-day average hourly rate')
    expect(costRateLabels().allocationTitle).toBe('Current allocation and use')
  })

  it('keeps both absent-source reasons inside the discovery grace period', () => {
    expect(isCostDiscoveryPending('no_prometheus')).toBe(true)
    expect(isCostDiscoveryPending('no_cost_source')).toBe(true)
    expect(isCostDiscoveryPending('source_unavailable')).toBe(false)
  })

  it('does not point embedded users at standalone Settings', () => {
    expect(costIntegrationUnavailableMessage('no_cost_source', true)).toContain('Settings → Metrics')
    expect(costIntegrationUnavailableMessage('no_cost_source', false)).toContain('host application')
    expect(costIntegrationUnavailableMessage('source_unavailable', false)).not.toContain('Settings')
    expect(costIntegrationUnavailableMessage('authentication_error', false)).not.toContain('Settings')
  })
})
