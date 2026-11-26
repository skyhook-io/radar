import { beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { getApiBase } from '../../api/config'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { NavCustomizationProvider } from '../../context/NavCustomization'
vi.mock('../../hooks/usePreviousIntegrationSettings', () => ({ usePreviousIntegrationSettings: () => ({ metrics: false }) }))
import type { WorkloadMetrics } from '../../api/workloadMetrics'

let connected = false
let source: Record<string, unknown> | undefined
let openobserve: 'hub' | 'local' | undefined
let discoverOutcome: 'connected' | 'collector' | 'none' | undefined
let discoverError: Error | null = null
const ranges: string[] = []
const charts: { seriesLabels?: string[]; referenceLines?: unknown[] }[] = []
let workload: WorkloadMetrics
const drawnMetrics = { unit: 'cores', result: { series: [{ labels: { pod: 'web-0' }, dataPoints: [{ timestamp: 100, value: 1 }, { timestamp: 160, value: 2 }] }] } }
let resourceMetrics: Record<string, unknown> = drawnMetrics
let metricsByCategory: Record<string, Record<string, unknown>> = {}

vi.mock('../../api/client', async (importActual) => ({
  ...(await importActual<typeof import('../../api/client')>()),
  useAutoPromConnect: () => undefined,
  usePrometheusStatus: () => ({ data: { connected, error: openobserve ? undefined : 'Backend query failed', source, openobserve } }),
  usePrometheusConnect: () => ({ mutate: vi.fn(), isPending: false }),
  useDiscoverMetrics: () => ({ mutate: vi.fn(), isPending: false, data: discoverOutcome, error: discoverError }),
  usePrometheusResourceMetrics: (_kind: string, _ns: string, _name: string, category: string, range: string, enabled = true) => {
    if (enabled) ranges.push(range)
    return { data: metricsByCategory[category] ?? resourceMetrics }
  },
}))
vi.mock('../../api/workloadMetrics', () => ({ useWorkloadMetrics: () => ({ data: workload }) }))
vi.mock('./RestartChart', () => ({ RestartEventLane: () => null }))
vi.mock('./RightsizingStrip', () => ({ RightsizingStrip: () => null }))
vi.mock('@skyhook-io/k8s-ui/components/charts', async (importActual) => ({
  ...(await importActual<typeof import('@skyhook-io/k8s-ui/components/charts')>()),
  AreaChart: (props: typeof charts[number]) => { charts.push(props); return <span>Chart</span> },
}))

const { PrometheusChartsGrid } = await import('./PrometheusChartsGrid')
const { PARTIAL_DATA_NOTE } = await import('@skyhook-io/k8s-ui/components/charts')
const resource = { spec: { template: { spec: { containers: [{ resources: { requests: { cpu: '100m' }, limits: { cpu: '200m' } } }] } } } }
const render = (url = '/workloads/Deployment/demo/web?tab=metrics', embedded = false) => renderToString(
  <MemoryRouter initialEntries={[url]}><NavCustomizationProvider value={embedded ? { embedded: true } : {}}>
    <PrometheusChartsGrid kind="Deployment" namespace="demo" name="web" resource={resource} />
  </NavCustomizationProvider></MemoryRouter>,
)

beforeEach(() => {
  connected = false
  source = undefined
  openobserve = undefined
  discoverOutcome = undefined
  discoverError = null
  ranges.length = 0
  charts.length = 0
  resourceMetrics = drawnMetrics
  metricsByCategory = {}
  workload = { state: 'available', sources: [], pods: 1, podsTotal: 1, start: 100, end: 160, stepSeconds: 60, rateWindowSeconds: 300, history: {}, comparison: {}, panels: {} }
})

describe('Expanded metrics recovery and scope', () => {
  it('shows one unavailable state instead of querying charts when OpenObserve through Hub is down', () => {
    connected = true
    source = { kind: 'openobserve', via: 'hub', unavailable: true, error: 'Radar Hub could not reach OpenObserve' }
    const html = render()
    expect(html).toContain('OpenObserve unavailable')
    expect(html).toContain('Radar Hub could not reach OpenObserve')
    expect(html).toContain('through Radar Hub')
    expect(html).not.toContain('Discover Prometheus')
    expect(html).not.toContain('Configure metrics')
    expect(ranges).toEqual([])
  })

  it('shows the OpenObserve notice, not Prometheus setup, when newer Radar reports it not connected', () => {
    source = { kind: 'openobserve', via: 'local', unavailable: true, error: 'OpenObserve unavailable: timeout' }
    const html = render()
    expect(html).toContain('OpenObserve unavailable')
    expect(html).not.toContain('Discover Prometheus')
    expect(ranges).toEqual([])
  })

  it('offers metrics settings when the saved local OpenObserve connection is down', () => {
    connected = true
    source = { kind: 'openobserve', via: 'local', unavailable: true }
    const html = render()
    expect(html).toContain('saved OpenObserve connection')
    expect(html).toContain('Configure metrics')
    expect(ranges).toEqual([])
  })

  it('offers standalone setup without losing the backend error or retry', () => {
    const html = render()
    expect(html).toContain('Backend query failed')
    expect(html).toContain('Discover Prometheus')
    expect(html).toContain('Configure metrics')
    expect(html).toContain('workload-metrics.md#what-each-chart-needs')
  })
  it.each(['hub', 'local'] as const)('offers metrics discovery when OpenObserve is available through %s', (path) => {
    openobserve = path
    const html = render()
    expect(html).toContain('No metrics source connected')
    expect(html).toContain('Radar can read metrics from Prometheus or OpenObserve.')
    expect(html).toContain('Discover metrics')
    expect(html).not.toContain('Discover Prometheus')
    expect(html).not.toContain('Prometheus not connected')
    expect(html).toContain('Configure metrics')
  })
  it.each([
    ['local', 'none', 'No metrics source found.'],
    ['hub', 'none', 'No metrics source found.'],
    ['local', 'collector', 'OpenObserve collector found. Connect it in Settings → Metrics.'],
    ['hub', 'collector', 'OpenObserve collector found. Ask your organization owner to connect OpenObserve in Radar Hub.'],
  ] as const)('shows the %s discovery outcome %s without offering discovery again', (path, outcome, message) => {
    openobserve = path
    discoverOutcome = outcome
    const html = render()
    expect(html).toContain(`<p role="status" class="text-sm text-theme-text-secondary mb-4">${message}</p>`)
    expect(html).not.toContain('Discover metrics')
    expect(html).not.toContain('Discover Prometheus')
  })
  it('shows a failed discovery in place and offers it again', () => {
    openobserve = 'local'
    discoverError = new Error('Not connected to a cluster')
    const html = render()
    expect(html).toContain('<p role="status" class="text-sm text-theme-text-secondary mb-4">Not connected to a cluster</p>')
    expect(html).toContain('Discover metrics')
  })
  it('uses operator guidance instead of standalone settings when embedded', () => {
    const html = render(undefined, true)
    expect(html).toContain('Ask your operator')
    expect(html).not.toContain('Configure metrics')
    expect(html).toContain('Discover Prometheus')
  })
  it.each([
    ['/workloads/Deployment/demo/web?tab=metrics&metricsRange=3h', '3h'],
    ['/applications?app=demo&workload=Deployment%2Fdemo%2Fweb&tab=metrics&metricsRange=6h', '6h'],
    ['/applications?tab=metrics&metricsRange=invalid', '1h'],
    ['/applications?tab=metrics', '1h'],
  ])('reads and validates the URL range: %s', (url, expected) => {
    connected = true
    const html = render(url)
    expect(html).toContain(`value="${expected}" selected=""`)
    expect(ranges.length).toBeGreaterThan(0)
    expect(ranges.every((range) => range === expected)).toBe(true)
  })
  it('leaves unverified charts accessible without template overlays or saturation', () => {
    connected = true
    const html = render()
    expect(html).toContain('<details aria-label="Name-matched resource metrics">')
    expect(html).toContain('Network and storage')
    expect(html).not.toContain('% of limit')
    expect(charts.length).toBeGreaterThan(0)
    expect(charts.every((chart) => !chart.referenceLines?.length)).toBe(true)
  })
  it('labels unlabeled request totals without changing aggregate and quantile labels', () => {
    connected = true
    const series = [{ labels: {}, dataPoints: [{ timestamp: 160, value: 1 }] }]
    workload.panels.requests = { state: 'available', unit: 'requests/s', series }
    workload.panels.p50 = { state: 'available', unit: 'seconds', series }
    workload.panels.p95 = { state: 'available', unit: 'seconds', series }
    workload.panels.cpu = { state: 'available', unit: 'cores', series: [{ ...series[0], labels: { aggregation: 'Workload' } }, { ...series[0], labels: { aggregation: 'Maximum Pod' } }] }
    render()
    expect(charts.map((chart) => chart.seriesLabels)).toContainEqual(['Requests / sec'])
    expect(charts.map((chart) => chart.seriesLabels)).toContainEqual(['p50', 'p95'])
    expect(charts.map((chart) => chart.seriesLabels)).toContainEqual(['Workload', 'Maximum Pod'])
  })
  it('notes a partial answer with no samples instead of claiming no data', () => {
    connected = true
    resourceMetrics = { unit: 'cores', result: { series: [] }, partial: true }
    const html = render()
    expect(html).toContain('No samples in a partial answer')
    expect(html).toContain(PARTIAL_DATA_NOTE)
    resourceMetrics = { unit: 'cores', result: { series: [] } }
    expect(render()).not.toContain(PARTIAL_DATA_NOTE)
  })
  it('retains template references on verified current-Pod charts', () => {
    connected = true
    workload.history.cpu = { mode: 'current-pods' }
    workload.panels.cpu = { state: 'available', unit: 'cores', series: [{ labels: { pod: 'web-0' }, dataPoints: [{ timestamp: 160, value: 0.1 }] }] }
    render()
    expect(charts.some((chart) => chart.referenceLines?.some((line) => (line as { label: string }).label === 'Template request 100m'))).toBe(true)
  })
})

describe('Mapped workload Metrics tab', () => {
  async function renderMapped(denied = false) {
    const { MetricsTabContent } = await import('../workload/WorkloadView')
    const client = new QueryClient({ defaultOptions: { queries: { retryOnMount: false } } })
    client.setQueryData(['datadog-metrics-status', getApiBase()], { configured: true })
    const queryKey = ['datadog-workload-metrics', getApiBase(), 'Deployment/demo/web', '3h']
    const panel = { unit: 'cores', series: [] }
    client.setQueryData(queryKey, { configured: true, charts: { cpu: panel, memory: { ...panel, unit: 'bytes' } } })
    if (denied) client.getQueryCache().find({ queryKey })!.setState({ status: 'error', error: new Error('Datadog read denied') })
    const html = renderToString(
      <QueryClientProvider client={client}><MemoryRouter initialEntries={['/?tab=metrics&metricsRange=3h']}>
        <NavCustomizationProvider value={{ embedded: true }}>
          <MetricsTabContent kind="Deployment" namespace="demo" name="web" resource={resource} expanded />
        </NavCustomizationProvider>
      </MemoryRouter></QueryClientProvider>,
    )
    client.clear()
    return html
  }

  it.each([false, true])('retains Prometheus traffic, network and template lines with a Datadog mapping (read denied=%s)', async (denied) => {
    connected = true
    const series = [{ labels: {}, dataPoints: [{ timestamp: 160, value: 1 }] }]
    workload.panels.requests = { state: 'available', unit: 'requests/s', series }
    workload.panels.errors = { state: 'available', unit: 'percent', series }
    workload.panels.p50 = workload.panels.p95 = { state: 'available', unit: 'seconds', series }
    workload.history.cpu = { mode: 'current-pods' }
    workload.panels.cpu = { state: 'available', unit: 'cores', series }
    const html = await renderMapped(denied)
    for (const label of ['Datadog workload totals', 'Requests / sec', 'HTTP 5xx', 'Latency · p50 / p95', 'Network and storage']) expect(html).toContain(label)
    expect(html).toContain(denied ? 'Datadog read denied' : 'No Datadog samples')
    expect(html.match(/aria-label="Metrics time range"/g)).toHaveLength(1)
    expect(ranges.every((range) => range === '3h')).toBe(true)
    expect(charts.flatMap((chart) => chart.referenceLines ?? [])).toEqual(expect.arrayContaining([
      expect.objectContaining({ label: 'Template request 100m' }), expect.objectContaining({ label: 'Template limit 200m' }),
    ]))
  })

  // Newer Radar reports a down OpenObserve as not connected; older Radar as
  // connected with an unavailable source. Either way Datadog stays.
  it.each([false, true])('keeps Datadog totals beside the OpenObserve notice when it is down (connected=%s)', async (wasConnected) => {
    connected = wasConnected
    source = { kind: 'openobserve', via: 'hub', unavailable: true, error: 'Radar Hub could not reach OpenObserve' }
    const html = await renderMapped()
    expect(html).toContain('OpenObserve unavailable')
    expect(html).toContain('Datadog workload totals')
    expect(html).not.toContain('Discover Prometheus')
    expect(html).not.toContain('Network and storage')
    expect(ranges).toHaveLength(0)
  })

  it('renders Datadog independently when Prometheus is disconnected', async () => {
    const html = await renderMapped()
    expect(html).toContain('Datadog workload totals')
    expect(html).toContain('No Datadog samples')
    expect(html).not.toContain('Network and storage')
    expect(ranges).toHaveLength(0)
  })
})

describe('Name-matched chart captions follow the charts\' cluster scope', () => {
  const ANOTHER_CLUSTER_SECTION = 'matching names in a shared backend may include another cluster'
  const ANOTHER_CLUSTER_GRID = 'Matching names in a shared backend may include another cluster.'
  const scoped = (state: string, note = 'note') => ({ ...drawnMetrics, clusterScope: { state, note } })
  const renderText = () => render().replaceAll('<!-- -->', '')
  beforeEach(() => { connected = true })

  it.each(['scoped_verified', 'scoped_configured'])('drops the other-cluster caveat when every chart is %s', (state) => {
    resourceMetrics = scoped(state)
    const html = renderText()
    expect(html).toContain('Basic CPU / memory · matched by cluster label')
    expect(html).toContain('Pod-name matched · matched by cluster label')
    expect(html).toContain('kept to this cluster by its cluster labels')
    expect(html).not.toContain('may include another cluster')
    expect(html).not.toContain('identity unverified')
  })
  it('names an operator-declared single-cluster store', () => {
    resourceMetrics = scoped('single_cluster_asserted')
    const html = renderText()
    expect(html).toContain('Basic CPU / memory · operator declared single-cluster')
    expect(html).toContain('Pod-name matched · operator declared single-cluster')
    expect(html).not.toContain('may include another cluster')
  })
  it('names OpenObserve, not an operator, when Radar reads metrics through it', () => {
    resourceMetrics = scoped('single_cluster_asserted', 'Scoped to this cluster through OpenObserve (matched by confirmed name).')
    const html = renderText()
    expect(html).toContain('Basic CPU / memory · scoped through OpenObserve')
    expect(html).toContain('Pod-name matched · scoped through OpenObserve')
    expect(html).toContain('kept to this cluster through OpenObserve')
    expect(html).not.toContain('operator declared')
    expect(html).not.toContain('may include another cluster')
  })
  it.each([['absent', drawnMetrics], ['unlabeled', scoped('unlabeled')]])('keeps today\'s caveat when clusterScope is %s', (_label, metrics) => {
    resourceMetrics = metrics
    const html = renderText()
    expect(html).toContain('Basic CPU / memory · identity unverified')
    expect(html).toContain('These charts match current Pod names, not Pod UIDs or historical workload ownership; matching names in a shared backend may include another cluster.')
    expect(html).toContain('Pod-name matched · identity unverified')
    expect(html).toContain(`These charts match current Pod names, independently of the identity-checked charts above. ${ANOTHER_CLUSTER_GRID}`)
  })
  it('keeps the caveat header for withheld charts and defers to their own note', () => {
    resourceMetrics = scoped('label_missing', 'The series lack this cluster\'s label.')
    const html = renderText()
    expect(html).toContain('Basic CPU / memory · identity unverified')
    expect(html).toContain('Pod-name matched · identity unverified')
    expect(html).toContain('say why in place of their data')
    expect(html).toContain('The series lack this cluster&#x27;s label.')
    expect(html).not.toContain('may include another cluster')
  })
  it('uses the least certain chart when a section disagrees', () => {
    resourceMetrics = scoped('scoped_verified')
    metricsByCategory = { memory: drawnMetrics, filesystem: scoped('unlabeled') }
    const html = renderText()
    expect(html).toContain('Basic CPU / memory · identity unverified')
    expect(html).toContain(ANOTHER_CLUSTER_SECTION)
    expect(html).toContain('Pod-name matched · identity unverified')
    expect(html).toContain(ANOTHER_CLUSTER_GRID)
  })
  it('lets a verified network section drop the caveat while a withheld chart above keeps its header', () => {
    resourceMetrics = scoped('scoped_verified')
    metricsByCategory = { cpu: scoped('several_clusters') }
    const html = renderText()
    expect(html).toContain('Basic CPU / memory · identity unverified')
    expect(html).toContain('Pod-name matched · matched by cluster label')
    expect(html).not.toContain('may include another cluster')
  })
})
