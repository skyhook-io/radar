import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import type { AppRow, AppWorkload } from '@skyhook-io/k8s-ui'

const sharedStore = { state: 'several_clusters' as const, note: 'This metrics store holds several clusters.' }
let workloadTrend: Record<string, unknown> | undefined
let applicationTrend: Record<string, unknown> | undefined
let namespaceTrend: Record<string, unknown> | undefined

const query = (data: unknown) => ({ data, isLoading: false, isFetching: false, error: null, refetch: vi.fn() })

vi.mock('../../api/client', async (importActual) => ({
  ...(await importActual<typeof import('../../api/client')>()),
  useOpenCostWorkload: () => query({
    available: true,
    currency: 'USD',
    namespace: 'shop',
    kind: 'Deployment',
    name: 'api',
    current: {
      name: 'api', kind: 'Deployment', hourlyCost: 0.2, cpuCost: 0.12, memoryCost: 0.08, replicas: 2,
      cpuUsageAvailable: true, memoryUsageAvailable: true, cpuAllocationUse: 25, memoryAllocationUse: 25,
    },
  }),
  useOpenCostWorkloadTrend: () => query(workloadTrend),
  useOpenCostApplicationCost: () => query({
    available: true,
    currency: 'USD',
    totals: { hourlyCost: 0.2, cpuCost: 0.12, memoryCost: 0.08, replicas: 2, cpuUsageAvailable: true, memoryUsageAvailable: true },
    coverage: { total: 1, included: 1 },
    workloads: [],
  }),
  useOpenCostApplicationCostTrend: () => query(applicationTrend),
  useOpenCostTrend: () => query(namespaceTrend),
}))

const { WorkloadCostTab } = await import('./WorkloadCostTab')
const { ApplicationCostTab } = await import('./ApplicationCostTab')
const { CostTrendChart } = await import('./CostTrendChart')
const { COST_TREND_PARTIAL_NOTE, COST_TREND_WITHHELD_TITLE } = await import('./source')

const NO_HISTORY = 'No historical workload owner cost points for this range.'
const withRouter = (node: React.ReactNode) => renderToStaticMarkup(<MemoryRouter>{node}</MemoryRouter>)
const renderWorkload = () => withRouter(<WorkloadCostTab kind="Deployment" namespace="shop" name="api" />)
const app = { name: 'shop' } as AppRow
const workloads = [{ kind: 'Deployment', namespace: 'shop', name: 'api' }] as AppWorkload[]
const renderApplication = () => withRouter(<ApplicationCostTab app={app} workloads={workloads} />)

function trend(reason: string, extra: Record<string, unknown> = {}) {
  return { available: false, reason, currency: 'USD', namespace: 'shop', kind: 'Deployment', name: 'api', range: '24h', ...extra }
}

beforeEach(() => {
  workloadTrend = undefined
  applicationTrend = undefined
  namespaceTrend = undefined
})

describe('a withheld cost trend beside current costs', () => {
  it.each([
    ['workload', (t: Record<string, unknown>) => { workloadTrend = t }, renderWorkload],
    ['application', (t: Record<string, unknown>) => { applicationTrend = t }, renderApplication],
  ] as const)('%s tab says why the trend was withheld', (_name, setTrend, render) => {
    setTrend(trend('cluster_scope', { clusterScope: sharedStore }))
    let html = render()
    expect(html).toContain(COST_TREND_WITHHELD_TITLE)
    expect(html).toContain(sharedStore.note)
    expect(html).not.toContain(NO_HISTORY)

    setTrend(trend('partial_data'))
    html = render()
    expect(html).toContain(COST_TREND_PARTIAL_NOTE)
    expect(html).not.toContain(NO_HISTORY)

    setTrend(trend('query_error'))
    html = render()
    expect(html).toContain(NO_HISTORY)
    expect(html).not.toContain(COST_TREND_PARTIAL_NOTE)
    expect(html).not.toContain(COST_TREND_WITHHELD_TITLE)
  })
})

describe('CostTrendChart', () => {
  it('shows a cluster-scope withheld Prometheus trend instead of hiding it', () => {
    namespaceTrend = { available: false, reason: 'cluster_scope', source: 'prometheus', range: '24h', clusterScope: sharedStore }
    const html = renderToStaticMarkup(<CostTrendChart />)
    expect(html).toContain(COST_TREND_WITHHELD_TITLE)
    expect(html).toContain(sharedStore.note)
  })

  it('says a Prometheus trend was withheld for a partial answer instead of hiding it', () => {
    namespaceTrend = { available: false, reason: 'partial_data', source: 'prometheus', range: '24h' }
    const html = renderToStaticMarkup(<CostTrendChart />)
    expect(html).toContain(COST_TREND_PARTIAL_NOTE)
    expect(html).not.toContain(COST_TREND_WITHHELD_TITLE)
  })

  it('keeps hiding other unavailable Prometheus trends', () => {
    for (const reason of ['query_error', 'no_metrics']) {
      namespaceTrend = { available: false, reason, source: 'prometheus', range: '24h' }
      expect(renderToStaticMarkup(<CostTrendChart />)).toBe('')
    }
  })
})
