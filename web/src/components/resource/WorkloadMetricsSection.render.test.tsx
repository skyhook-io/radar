import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import type { WorkloadMetrics, WorkloadMetricPanel } from '../../api/workloadMetrics'

let result: { data?: WorkloadMetrics; isLoading?: boolean; error?: Error } = {}
vi.mock('../../api/workloadMetrics', () => ({ useWorkloadMetrics: () => result }))
const { WorkloadMetricsSection } = await import('./WorkloadMetricsSection')
const panel = (value: number, unit = 'requests/s'): WorkloadMetricPanel => ({
  state: 'available', unit,
  series: [{ labels: { pod: 'checkout-0' }, dataPoints: [{ timestamp: 100, value }, { timestamp: 160, value }] }],
})
const render = () => renderToString(<MemoryRouter><WorkloadMetricsSection kind="Deployment" namespace="shop" name="checkout" range="1h" /></MemoryRouter>)

beforeEach(() => {
  result = { data: {
    setupRequired: false, state: 'available', source: 'beyla',
    sources: [{ id: 'beyla', label: 'Beyla', state: 'available' }],
    pods: 2, podsTotal: 2, start: 100, end: 160, stepSeconds: 60, rateWindowSeconds: 300,
    panels: { requests: panel(0), observedPods: panel(1, 'pods'), cpu: panel(0, 'cores') },
  } }
})

describe('Workload metrics interpretation', () => {
  it('distinguishes reporting Pods from selected Pods and preserves measured zero', () => {
    const html = render()
    expect(html).toContain('1 of 2 current Pods reporting')
    expect(html).toContain('tabular-nums text-theme-text-primary">0</span>')
    expect(html).toContain('previous replicas are not reconstructed')
    expect(html).toContain('/resources/pods?resource=shop%2Fcheckout-0')
    expect(html).toContain('checkout-0')
  })
  it('does not substitute selected count when coverage is missing', () => {
    delete result.data!.panels.observedPods
    expect(render()).toContain('Reporting Pod count unavailable')
  })
  it('gives setup guidance without calling missing configuration a query failure', () => {
    result.data!.state = 'unavailable'
    result.data!.setupRequired = true
    result.data!.reason = 'Request and pressure metrics need a confirmed cluster scope.'
    const html = render()
    expect(html).toContain('operator setup required')
    expect(html).toContain('--prometheus-single-cluster')
    expect(html).not.toContain('Workload resource pressure')
  })
  it('does not show setup instructions for a workload with no Pods', () => {
    result.data!.state = 'unavailable'
    result.data!.reason = 'No current Pods.'
    expect(render()).not.toContain('operator setup required')
  })
  it('shows query failure rather than fabricating missing metrics', () => {
    result = { error: new Error('Forbidden') }
    expect(render().replaceAll('<!-- -->', '')).toContain('Workload metrics could not be loaded: Forbidden')
  })
  it('keeps observers explicitly separate when both are available', () => {
    result.data!.sources.push({ id: 'istio', label: 'Istio', state: 'available' })
    expect(render()).toContain('Request metrics source')
    expect(render()).toContain('value="beyla" selected=""')
  })
  it('does not suggest a job matcher for a failed request query', () => {
    result.data!.panels.requests = { state: 'error', unit: 'requests/s', series: [], reason: 'Query failed' }
    const html = render()
    expect(html).toContain('Request metrics query failed')
    expect(html).not.toContain('--beyla-job-selector')
  })
  it('describes Istio at the selected destination-sidecar observer', () => {
    result.data!.source = 'istio'
    result.data!.sources = [{ id: 'istio', label: 'Istio · destination sidecars', state: 'available' }]
    const html = render().replaceAll('<!-- -->', '')
    expect(html).toContain('Istio · destination sidecars · 5-minute rates')
    expect(html).not.toContain('HTTP server ·')
  })
})
