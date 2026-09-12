import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import type { WorkloadMetrics, WorkloadMetricPanel } from '../../api/workloadMetrics'

let result: { data?: WorkloadMetrics; isLoading?: boolean; isPlaceholderData?: boolean; error?: Error } = {}
vi.mock('../../api/workloadMetrics', () => ({ useWorkloadMetrics: () => result }))
const { WorkloadMetricsSection } = await import('./WorkloadMetricsSection')
const panel = (value: number, unit = 'requests/s'): WorkloadMetricPanel => ({
  state: 'available', unit,
  series: [{ labels: { pod: 'checkout-0' }, dataPoints: [{ timestamp: 100, value }, { timestamp: 160, value }] }],
})
const render = () => renderToString(<MemoryRouter><WorkloadMetricsSection kind="Deployment" namespace="shop" name="checkout" range="1h" /></MemoryRouter>)

beforeEach(() => {
  result = { data: {
    state: 'available', source: 'beyla',
    sources: [{ id: 'beyla', label: 'Beyla', state: 'available' }],
    pods: 2, podsTotal: 2, start: 100, end: 160, stepSeconds: 60, rateWindowSeconds: 300,
    panels: { requests: panel(0), observedPods: panel(1, 'pods'), cpu: panel(0, 'cores') },
  } }
})

describe('Workload metrics interpretation', () => {
  it('retains resource charts while loading another request observer', () => {
    result.isPlaceholderData = true
    const html = render()
    expect(html).toContain('Loading selected request source')
    expect(html).toContain('CPU usage · per Pod')
    expect(html).toContain('Compare current Pods')
    expect(html).not.toContain('1 of 2 current Pods reporting')
  })
  it('labels operator scope and discarded assertions visibly', () => {
    result.data!.attribution = { scope: 'Operator-asserted cluster scope' }
    result.data!.scopeNotice = 'Scope assertion discarded after reconnect'
    const html = render()
    expect(html).toContain('Operator-asserted metrics scope')
    expect(html).toContain('Scope assertion discarded after reconnect')
    expect(html).not.toContain('Automatic metrics attribution')
  })
  it('explains withheld populations instead of suggesting instrumentation is absent', () => {
    result.data!.panels.requests = {state:'partial',unit:'requests/s',series:[],reason:'Multiple observation populations'}
    const html = render()
    expect(html).toContain('Request metrics withheld')
    expect(html).not.toContain('No request observations')
    expect(html).not.toContain('--beyla-job-selector')
  })
  it.each(['detecting', 'unavailable', 'error'] as const)('does not substitute unattributed resource charts while metrics are %s', (state) => {
    result.data!.state = state
    result.data!.panels = {}
    const html = render()
    expect(html).not.toContain('CPU usage · per Pod')
    expect(html).not.toContain('Memory working set · per Pod')
  })
  it('groups CPU and memory with throttling before the full-width Pod comparison', () => {
    result.data!.panels.throttling = panel(5, 'percent')
    const html = render()
    expect(html.indexOf('CPU usage · per Pod')).toBeLessThan(html.indexOf('CPU throttled periods'))
    expect(html.indexOf('CPU throttled periods')).toBeLessThan(html.indexOf('Compare current Pods'))
    expect(html).toContain('metrics-chart-wide')
    expect(html).not.toContain('lg:grid-cols-3')
  })
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
  it('offers an optional override inside automatic attribution details', () => {
    result.data!.attribution = { cpu: 'Metrics exist, but their cluster identity could not be established automatically.' }
    const html = render()
    expect(html).toContain('Automatic metrics attribution')
    expect(html).toContain('--prometheus-single-cluster')
    expect(html).toContain('Workload resource pressure')
    expect(html).not.toContain('operator setup required')
  })
  it('shows detecting without claiming missing data or mandatory setup', () => {
    result.data!.state = 'detecting'
    result.data!.reason = 'Matching metrics to current Pods…'
    result.data!.panels = {}
    const html = render()
    expect(html).toContain('Matching metrics to current Pods')
    expect(html).not.toContain('No request observations')
    expect(html).not.toContain('--prometheus-single-cluster')
  })
  it('discloses partial pressure coverage', () => {
    result.data!.panels.cpu = { ...panel(1, 'cores'), state: 'partial', reason: 'Attributed to 1 of 2 current Pods; other Pods are excluded.' }
    expect(render()).toContain('Attributed to 1 of 2 current Pods')
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
