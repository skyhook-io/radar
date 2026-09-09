import { describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'

// The access state is what is under test, so the fetch is stubbed rather than
// run. isForbiddenError is deliberately NOT stubbed: a looser stand-in would
// pass while the real predicate sent a denial down the silent-hide path, which
// is the failure this component exists to prevent.
import { ApiError } from '../../api/client'

const forbidden = new ApiError('forbidden', 403)
let metricsResult: { data?: unknown; error?: unknown } = {}
let connected = true

vi.mock('../../api/client', async (importActual) => ({
  ...(await importActual<typeof import('../../api/client')>()),
  usePrometheusHPAMetrics: () => metricsResult,
  usePrometheusStatus: () => ({ data: { connected } }),
  useAutoPromConnect: () => undefined,
}))

const { HPACharts } = await import('./HPACharts')

const hpa = {
  kind: 'HorizontalPodAutoscaler',
  metadata: { namespace: 'shop', name: 'web' },
  spec: { minReplicas: 2, maxReplicas: 10 },
}

const series = (values: number[]) => ({
  resultType: 'matrix',
  series: [{ labels: {}, dataPoints: values.map((value, i) => ({ timestamp: 1700000000 + i * 60, value })) }],
})

/**
 * A denied chart must not look like an HPA that never scaled: an empty
 * "no history" and "you may not see the history" are different facts.
 */
describe('HPACharts — access state', () => {
  it('says the caller has no access when the curated endpoint returns 403', () => {
    connected = true
    metricsResult = { error: forbidden }
    const html = renderToString(<HPACharts data={hpa} />)
    expect(html).toContain('have access to metrics for this resource')
    expect(html).toContain('Activity (last 1h)')
    expect(html).not.toContain('Replicas')
  })

  it('plots the replica history when the endpoint answers', () => {
    connected = true
    metricsResult = { data: { current: series([2, 3, 4]), desired: series([4, 4, 4]) } }
    const html = renderToString(<HPACharts data={hpa} />)
    expect(html).toContain('Replicas')
    expect(html).toContain('min 2')
    expect(html).toContain('max 10')
    expect(html).not.toContain('access to metrics')
  })

  it('stays hidden when KSM reports nothing for the HPA', () => {
    connected = true
    metricsResult = { data: { current: { resultType: 'matrix', series: [] }, desired: { resultType: 'matrix', series: [] } } }
    expect(renderToString(<HPACharts data={hpa} />)).toBe('')
  })

  // A fault is not a denial: the chart keeps its silent-hide behaviour and
  // leaves the breadcrumb to the console.
  it('stays hidden on a non-authorization failure', () => {
    connected = true
    metricsResult = { error: new Error('Prometheus query failed') }
    expect(renderToString(<HPACharts data={hpa} />)).toBe('')
  })

  it('stays hidden when Prometheus is not connected, even on a denial', () => {
    connected = false
    metricsResult = { error: forbidden }
    expect(renderToString(<HPACharts data={hpa} />)).toBe('')
  })
})
