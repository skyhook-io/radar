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
    history: { cpu: { mode: 'current-pods', reason: 'Missing retained ownership.' }, requests: { mode: 'current-pods' } },
    comparison: { cpu: panel(0, 'cores') },
    panels: { requests: panel(0), observedPods: panel(1, 'pods'), cpu: panel(0, 'cores') },
  } }
})

describe('Workload metrics interpretation', () => {
  it('shows matching evidence instead of claiming no observations when no observer is usable', () => {
    result.data!.source = undefined
    result.data!.sources = [{ id: 'beyla', label: 'Beyla', state: 'unavailable' }]
    result.data!.attribution = { beyla: 'Metrics exist, but their cluster identity could not be established automatically.' }
    result.data!.panels.requests = { state: 'unavailable', unit: 'requests/s', series: [], reason: 'Generic prerequisites' }
    const html = render()
    expect(html).toContain('No usable HTTP metrics in this window')
    expect(html).toContain('Metrics exist, but their cluster identity')
    expect(html).not.toContain('Generic prerequisites')
    expect(html).not.toContain('No HTTP observations')
  })
  it('preserves a selected observer’s query reason rather than replacing it with matching evidence', () => {
    result.data!.attribution = { beyla: 'Matched to current Pod UIDs' }
    result.data!.panels.requests = { state: 'unavailable', unit: 'requests/s', series: [], reason: 'No samples from the selected observer' }
    const html = render()
    expect(html).toContain('No samples from the selected observer')
    expect(html).not.toContain('Matched to current Pod UIDs')
  })
  it('separates historical aggregates from bounded current comparison', () => {
    result.data!.podsTotal = 128
    result.data!.pods = 100
    result.data!.history = { cpu: { mode: 'workload-history' }, requests: { mode: 'workload-history' } }
    result.data!.panels.cpu = { ...panel(128, 'cores'), series: [
      { labels: { aggregation: 'Workload' }, dataPoints: [{ timestamp: 160, value: 128 }] },
      { labels: { aggregation: 'Maximum Pod' }, dataPoints: [{ timestamp: 160, value: 1 }] },
    ] }
    const html = render().replaceAll('<!-- -->', '')
    expect(html).toContain('CPU usage · workload')
    expect(html).toContain('Maximum Pod')
    expect(html).toContain('1 Pods reporting')
    expect(html).not.toContain('of 128 current Pods reporting')
    expect(html).toContain('checkout-0')
    expect(html).toContain('Comparing 100 of 128 current Pods')
    expect(html).toContain('when ownership disappears after Pod deletion')
  })
  it('names the current-only fallback rather than implying full history', () => {
    const html = render()
    expect(html).toContain('Current Pods only')
    expect(html).toContain('Missing retained ownership')
  })
  it('keeps name-matched fallback charts separate from identity-checked metrics', () => {
    result.data!.panels.cpu = { state: 'error', unit: 'cores', reason: 'History lookup failed', series: [] }
    result.data!.panels.memory = panel(100, 'bytes')
    const html = renderToString(<MemoryRouter><WorkloadMetricsSection kind="Deployment" namespace="shop" name="checkout" range="1h" nameMatchedCharts={{ cpu: <span>Existing CPU chart</span>, memory: <span>Existing memory chart</span> }} /></MemoryRouter>)
    expect(html.replaceAll('<!-- -->', '')).toContain('Basic CPU · identity unverified')
    expect(html).toContain('<details aria-label="Name-matched resource metrics">')
    expect(html).toContain('matching names in a shared backend may include another cluster')
    expect(html).toContain('Existing CPU chart')
    expect(html).toContain('History lookup failed')
    expect(html).not.toContain('Existing memory chart')
  })
  it('does not flash name-matched charts during the initial request', () => {
    result = { isLoading: true }
    const html = renderToString(<MemoryRouter><WorkloadMetricsSection kind="Deployment" namespace="shop" name="checkout" range="1h" nameMatchedCharts={{ cpu: <span>Existing CPU chart</span> }} /></MemoryRouter>)
    expect(html).not.toContain('Existing CPU chart')
    expect(html).toContain('Checking request metrics')
  })
  it('shows only supplied template values and qualifies actual Pod differences', () => {
    const html = renderToString(<MemoryRouter><WorkloadMetricsSection kind="Deployment" namespace="shop" name="checkout" range="1h" cpuReferenceLines={[{ value: 0.1, label: 'request 100m', kind: 'request' }]} memoryReferenceLines={[]} /></MemoryRouter>).replaceAll('<!-- -->', '')
    expect(html).toContain('Template per Pod · actual Pods may differ')
    expect(html).toContain('Template request 100m')
    expect(html).toContain('Actual Pods can differ after injection or rollout.')
    expect(html).not.toContain(' · Memory:')
    expect(html).not.toContain('unlimited')
    expect(render()).not.toContain('Template per Pod')
  })
  it.each([true, false])('keeps absent HTTP compact without narrating resource availability: %s', (usable) => {
    result.data!.panels.requests = { state: 'unavailable', unit: 'requests/s', series: [], reason: 'No observations' }
    result.data!.panels.cpu = usable ? panel(0, 'cores') : { state: 'error', unit: 'cores', series: [], reason: 'Query failed' }
    const html = render()
    expect(html).toContain('No usable HTTP metrics in this window')
    expect(html).not.toContain('Resource charts below remain available')
    expect(html).not.toContain('--beyla-job-selector')
    if (!usable) expect(html).not.toContain('CPU usage · per Pod')
  })
  it('does not call pending attribution a lack of HTTP traffic', () => {
    result.data!.panels.requests = { state: 'detecting', unit: 'requests/s', series: [], reason: 'Matching identity' }
    const html = render()
    expect(html).toContain('Checking request metrics…')
    expect(html).toContain('Matching identity')
    expect(html).not.toContain('No usable HTTP metrics')
  })
  it('keeps empty resource titles neutral even with an all-null series', () => {
    result.data!.panels.cpu!.series[0].dataPoints = [{ timestamp: 160, value: null }]
    const html = render()
    expect(html).toContain('CPU usage')
    expect(html).not.toContain('CPU usage · per Pod')
  })
  it('discloses combined request ports and reporting sidecars', () => {
    const html = render()
    expect(html).toContain('Ports combined · includes health checks and admin traffic.')
    expect(html).toContain('Resource totals include reporting containers and sidecars.')
  })
  it('describes throttling scope independently of a failed CPU history query', () => {
    result.data!.history.cpu = { mode: 'unavailable' }
    result.data!.history.throttling = { mode: 'workload-history' }
    result.data!.panels.throttling = panel(5, 'percent')
    const html = render().replaceAll('<!-- -->', '')
    expect(html).toContain('Throttling is weighted by total periods')
    expect(html).not.toContain('Examines 2 of 2 current Pods')
  })
  it('does not describe aggregate series in current-Pod help', () => {
    result.data!.history.throttling = { mode: 'current-pods' }
    result.data!.panels.throttling = panel(5, 'percent')
    const html = render()
    expect(html).not.toContain('Workload is the total across reporting Pods')
    expect(html).not.toContain('Throttling is weighted by total periods')
  })
  it('explains a historical memory aggregate without implying historical throttling', () => {
    result.data!.history.memory = { mode: 'workload-history' }
    result.data!.panels.memory = panel(100, 'bytes')
    result.data!.history.throttling = { mode: 'current-pods' }
    result.data!.panels.throttling = panel(5, 'percent')
    const html = render()
    expect(html).toContain('In workload-history CPU and memory charts, Workload is the total')
    expect(html).not.toContain('Throttling is weighted by total periods')
  })
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
    expect(html).toContain('operator-asserted scope')
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
  it('keeps operator overrides out of the chart surface', () => {
    result.data!.attribution = { cpu: 'Metrics exist, but their cluster identity could not be established automatically.' }
    const html = render()
    expect(html).toContain('aria-haspopup="dialog"')
    expect(html).not.toContain('--prometheus-single-cluster')
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
  it('retains response-level population limitations outside help', () => {
    result.data!.state = 'partial'
    result.data!.reason = 'Current pod population was capped; values cover only the included pods.'
    expect(render()).toContain('Current pod population was capped; values cover only the included pods.')
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
    expect(html.match(/Istio · destination sidecars/g)).toHaveLength(1)
    expect(html).toContain('5-minute rates')
    expect(html).not.toContain('HTTP server ·')
  })
  it('does not describe history for an empty Requests section', () => {
    result.data!.panels.requests = { state: 'unavailable', unit: 'requests/s', series: [] }
    result.data!.history.requests = { mode: 'workload-history', reason: 'Request-only history marker' }
    const html = render()
    expect(html).not.toContain('Request-only history marker')
    expect(html).toContain('No usable HTTP metrics in this window')
  })
  it('keeps successful attribution evidence and definitions collapsed', () => {
    result.data!.attribution = { cpu: 'Matched CPU identity' }
    const html = render()
    expect(html).toContain('About these metrics')
    expect(html).not.toContain('Matched CPU identity')
    expect(html).toContain('Resource totals include reporting containers and sidecars.')
    expect(html).not.toMatch(/<details[^>]*\bopen[=> ]/)
  })
  it('deduplicates stale RED notices without losing individual reasons', () => {
    for (const key of ['requests', 'errors', 'p50', 'p95'] as const) {
      result.data!.panels[key] = { ...panel(1), state: 'stale', reason: `${key} history only` }
    }
    const html = render()
    expect(html.match(/no recent samples/g)).toHaveLength(1)
    for (const key of ['requests', 'errors', 'p50', 'p95']) expect(html).toContain(`${key} history only`)
  })
  it('shares identical partial request coverage while keeping the warning visible', () => {
    for (const key of ['requests', 'errors', 'p50', 'p95'] as const) {
      result.data!.panels[key] = { ...panel(1), state: 'partial', reason: 'Only one reporting Pod was identified.' }
    }
    const html = render().replaceAll('<!-- -->', '')
    expect(html.match(/Only one reporting Pod was identified/g)).toHaveLength(1)
    expect(html).toContain('Requests, HTTP 5xx and latency: Only one reporting Pod was identified.')
  })
  it.each([true, false])('shares partial resource coverage only with matching scopes: %s', (sameScope) => {
    for (const key of ['cpu', 'memory', 'throttling'] as const) {
      result.data!.history[key] = { mode: 'current-pods' }
      result.data!.panels[key] = { ...panel(1), state: 'partial', reason: 'Attributed to 1 of 2 Pods.' }
    }
    if (!sameScope) result.data!.history.memory = { mode: 'workload-history' }
    expect(render().match(/Attributed to 1 of 2 Pods/g)).toHaveLength(sameScope ? 1 : 3)
  })
  it('does not hide a withheld chart behind a shared partial warning', () => {
    for (const key of ['requests', 'errors', 'p50', 'p95'] as const) {
      result.data!.panels[key] = { ...panel(1), state: 'partial', reason: 'Incomplete observation population.' }
    }
    result.data!.panels.p95!.series = []
    result.data!.panels.p50!.series = []
    const html = render()
    expect(html).not.toContain('Requests, HTTP 5xx and latency:')
    expect(html.match(/Incomplete observation population/g)).toHaveLength(3)
  })
  it('keeps stale samples withheld by coverage checks local to their chart', () => {
    for (const key of ['requests', 'errors', 'p50', 'p95'] as const) {
      result.data!.panels[key] = { ...panel(1), state: 'stale', reason: 'Historical samples only' }
    }
    result.data!.panels.errors!.series[0].dataPoints = [{ timestamp: 100, value: null }]
    result.data!.panels.errors!.reason = 'Historical samples only. HTTP status coverage is incomplete.'
    const html = render()
    expect(html).not.toContain('Requests, HTTP 5xx and latency:')
    expect(html).toContain('HTTP status coverage is incomplete')
    expect(html).toMatch(/<p role="status"[^>]*>Historical samples only\. HTTP status coverage is incomplete\.<\/p>/)
  })
  it('keeps stale and failed quantiles local when RED states differ', () => {
    result.data!.panels.p95 = { ...panel(1, 'seconds'), state: 'stale', reason: 'Old latency' }
    result.data!.panels.p50 = { state: 'error', unit: 'seconds', series: [], reason: 'Median query failed' }
    const html = render()
    expect(html).toContain('Historical samples only')
    expect(html).toContain('Median query failed')
    expect(html).not.toContain('Requests, HTTP 5xx and latency:')
  })
  it('shows an identical empty-quantile explanation once', () => {
    result.data!.panels.p50 = { state: 'unavailable', unit: 'seconds', series: [], reason: 'No histogram samples' }
    result.data!.panels.p95 = { state: 'unavailable', unit: 'seconds', series: [], reason: 'No histogram samples' }
    expect(render().match(/No histogram samples/g)).toHaveLength(1)
  })
  it('does not repeat a chart error in its history details', () => {
    result.data!.history.cpu = { mode: 'unavailable', reason: 'CPU history query failed' }
    result.data!.panels.cpu = { state: 'error', unit: 'cores', series: [], reason: 'CPU history query failed' }
    expect(render().match(/CPU history query failed/g)).toHaveLength(1)
  })
  it('shows comparison matching once, not as missing Pod samples', () => {
    result.data!.comparison = {
      cpu: { state: 'detecting', unit: 'cores', series: [], reason: 'Matching metrics' },
      memory: { state: 'detecting', unit: 'bytes', series: [], reason: 'Matching metrics' },
    }
    const html = render().replaceAll('<!-- -->', '')
    expect(html.match(/Matching CPU \/ Memory metrics to current Pods/g)).toHaveLength(1)
    expect(html).not.toContain('No Pod samples available')
  })
  it('deduplicates identical comparison failures and retains affected metrics', () => {
    result.data!.comparison = {
      cpu: { state: 'error', unit: 'cores', series: [], reason: 'Comparison query failed' },
      memory: { state: 'error', unit: 'bytes', series: [], reason: 'Comparison query failed' },
    }
    const html = render().replaceAll('<!-- -->', '')
    expect(html.match(/Comparison query failed/g)).toHaveLength(1)
    expect(html).toContain('CPU / Memory: Comparison query failed')
    expect(html).not.toContain('No Pod samples available')
  })
  it('does not explain an empty resource chart in terms of HTTP traffic', () => {
    result.data!.panels.cpu = { state: 'unavailable', unit: 'cores', series: [] }
    const html = render()
    expect(html).toContain('No usable samples in this window')
    expect(html).not.toContain('Idle traffic has no defined error percentage or latency')
  })
})
