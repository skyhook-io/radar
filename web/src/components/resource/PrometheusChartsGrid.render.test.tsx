import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { NavCustomizationProvider } from '../../context/NavCustomization'
import type { WorkloadMetrics } from '../../api/workloadMetrics'

let connected = false
const ranges: string[] = []
const charts: { seriesLabels?: string[]; referenceLines?: unknown[] }[] = []
let workload: WorkloadMetrics

vi.mock('../../api/client', async (importActual) => ({
  ...(await importActual<typeof import('../../api/client')>()),
  useAutoPromConnect: () => undefined,
  usePrometheusStatus: () => ({ data: { connected, error: 'Backend query failed' } }),
  usePrometheusConnect: () => ({ mutate: vi.fn(), isPending: false }),
  usePrometheusResourceMetrics: (_kind: string, _ns: string, _name: string, _category: string, range: string) => {
    ranges.push(range)
    return { data: { unit: 'cores', result: { series: [{ labels: { pod: 'web-0' }, dataPoints: [{ timestamp: 100, value: 1 }, { timestamp: 160, value: 2 }] }] } } }
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
const resource = { spec: { template: { spec: { containers: [{ resources: { requests: { cpu: '100m' }, limits: { cpu: '200m' } } }] } } } }
const render = (url = '/workloads/Deployment/demo/web?tab=metrics', embedded = false) => renderToString(
  <MemoryRouter initialEntries={[url]}><NavCustomizationProvider value={embedded ? { embedded: true } : {}}>
    <PrometheusChartsGrid kind="Deployment" namespace="demo" name="web" resource={resource} />
  </NavCustomizationProvider></MemoryRouter>,
)

beforeEach(() => {
  connected = false
  ranges.length = 0
  charts.length = 0
  workload = { state: 'available', sources: [], pods: 1, podsTotal: 1, start: 100, end: 160, stepSeconds: 60, rateWindowSeconds: 300, history: {}, comparison: {}, panels: {} }
})

describe('Expanded metrics recovery and scope', () => {
  it('offers standalone setup without losing the backend error or retry', () => {
    const html = render()
    expect(html).toContain('Backend query failed')
    expect(html).toContain('Discover Prometheus')
    expect(html).toContain('Configure metrics')
    expect(html).toContain('workload-metrics.md#what-each-chart-needs')
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
  it('retains template references on verified current-Pod charts', () => {
    connected = true
    workload.history.cpu = { mode: 'current-pods' }
    workload.panels.cpu = { state: 'available', unit: 'cores', series: [{ labels: { pod: 'web-0' }, dataPoints: [{ timestamp: 160, value: 0.1 }] }] }
    render()
    expect(charts.some((chart) => chart.referenceLines?.some((line) => (line as { label: string }).label === 'Template request 100m'))).toBe(true)
  })
})
