import { describe, expect, it } from 'vitest'
import { costConfigurationAction, isCostConfigurable, costDataThroughLabel, costFreshnessLabel, costIntegrationUnavailableMessage, costRateLabels, costSourceLabel, isCostDiscoveryPending } from './source'

describe('cost source presentation', () => {
  it('routes missing sources to one applicable previous connection', () => {
    const both = { metrics: 'previous', cost: 'previous', explicitCostBackend: true } as const
    expect(costConfigurationAction('no_prometheus', both).section).toBe('prometheus')
    expect(costConfigurationAction('no_cost_source', both).section).toBe('cost')
    expect(costConfigurationAction('no_prometheus', { ...both, metrics: undefined }).section).toBe('cost')
    expect(costConfigurationAction('no_cost_source', { ...both, explicitCostBackend: false }).section).toBe('prometheus')
    expect(costConfigurationAction('no_prometheus', { ...both, metrics: undefined, explicitCostBackend: false }).label).toBe('Configure metrics')
    expect(costConfigurationAction('authentication_error', { ...both, cost: undefined }).label).toBe('Configure cost source')
    expect(costConfigurationAction('configuration_mismatch', both).section).toBe('cost')
  })
  it('labels a copy offer as copying from another cluster', () => {
    const copy = { metrics: 'copy', explicitCostBackend: false } as const
    expect(costConfigurationAction('no_prometheus', copy)).toMatchObject({ section: 'prometheus', label: 'Copy from another cluster' })
    expect(costConfigurationAction('no_prometheus', { ...copy, metrics: 'previous' }).label).toBe('Review previous settings')
  })
  it.each(['access_denied', 'not_found', 'history_unsupported', 'no_workload_data', 'no_metrics', 'query_error', 'load_error', undefined])('does not offer connection edits for %s', reason => {
    expect(isCostConfigurable(reason)).toBe(false)
  })
  it('distinguishes Prometheus windows from Kubecost ETL freshness', () => {
    expect(costSourceLabel('prometheus')).toBe('OpenCost via Prometheus')
    expect(costSourceLabel('kubecost')).toBe('Kubecost Aggregator')
    expect(costFreshnessLabel('prometheus', '1h')).toBe('last 1h average')
    expect(costFreshnessLabel('kubecost', '1h', '2026-08-26T13:58:00Z')).toContain('1-hour allocation average')
    expect(costFreshnessLabel('kubecost', '1d', '2026-08-26T13:58:00Z')).toContain('1-day allocation average')
    expect(costDataThroughLabel('2026-08-26T13:58:00Z')).toContain('2026')
    expect(costDataThroughLabel('invalid')).toBe('')
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
