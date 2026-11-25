import { describe, expect, it } from 'vitest'
import type { PrometheusStatus } from './client'
import { hasPrometheusCharts, metricsConnectionLabel, metricsSourceUnavailable } from './prometheusCharts'

const down = { kind: 'openobserve', via: 'local', unavailable: true, error: 'OpenObserve unavailable: timeout' } as PrometheusStatus['source']

describe('metrics connection state', () => {
  // Older Radar reports a down OpenObserve relay as connected; newer as not.
  it.each([true, false])('never says Connected while OpenObserve is down (connected=%s)', (connected) => {
    const status = { connected, available: connected, address: 'OpenObserve https://oo.example', source: down } as PrometheusStatus
    expect(metricsSourceUnavailable(status)).toBe(true)
    expect(hasPrometheusCharts(status)).toBe(false)
    expect(metricsConnectionLabel(status)).toBe('OpenObserve unavailable')
  })

  it('keeps the usual labels otherwise', () => {
    expect(metricsConnectionLabel(undefined)).toBe('Checking…')
    expect(metricsConnectionLabel({ connected: true } as PrometheusStatus)).toBe('Connected')
    expect(metricsConnectionLabel({ connected: false, discovering: true } as PrometheusStatus)).toBe('Discovering…')
    expect(metricsConnectionLabel({ connected: false } as PrometheusStatus)).toBe('Not connected')
    expect(hasPrometheusCharts({ connected: true, source: { ...down, unavailable: false } } as PrometheusStatus)).toBe(true)
  })
})
