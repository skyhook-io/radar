import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import { ApiError } from '../../api/client'
import { getApiBase } from '../../api/config'
import { NavCustomizationProvider } from '../../context/NavCustomization'
import { WorkloadCostRightsizing } from '../resource/DatadogWorkloadCharts'
import { MetricsTabContent } from './WorkloadView'

vi.mock('../resource/PrometheusChartsGrid', () => ({
  PrometheusChartsGrid: ({ showRightsizing = true, workloadTotals }: { showRightsizing?: boolean; workloadTotals?: React.ReactNode }) =>
    <div>Prom charts{workloadTotals}{showRightsizing && <p>Prom recommendation</p>}</div>,
}))
vi.mock('../resource/RightsizingStrip', () => ({
  RightsizingPanel: () => <p>Prom recommendation</p>,
  RightsizingStrip: () => <p>Prom recommendation</p>,
}))
vi.mock('../resource/DatadogRightsizingPanel', () => ({
  DatadogRightsizingPanel: () => <p>DD recommendation</p>,
}))
vi.mock('../resource/RestartChart', () => ({ RestartEventLane: () => null }))

function render(status: object | Error, required = true) {
  const client = new QueryClient({ defaultOptions: { queries: { retryOnMount: false } } })
  const queryKey = ['datadog-metrics-status', getApiBase()]
  if (status instanceof Error) {
    const query = client.getQueryCache().build(client, { queryKey })
    query.setState({ status: 'error', error: status })
  } else {
    client.setQueryData(queryKey, status)
  }
  const workload = { kind: 'Deployment', namespace: 'shop', name: 'api' }
  function view(children: React.ReactNode) {
    return renderToStaticMarkup(
      <MemoryRouter><NavCustomizationProvider value={{ embedded: true, rightsizingProviderStatus: required }}>
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      </NavCustomizationProvider></MemoryRouter>,
    )
  }
  const metrics = view(<MetricsTabContent {...workload} resource={{}} expanded />)
  const drawer = view(<MetricsTabContent {...workload} resource={{}} expanded={false} />)
  const cost = view(<WorkloadCostRightsizing {...workload} />)
  client.clear()
  return { cost, metrics, drawer }
}

it.each([
  [{ configured: false, rightsizingProvider: 'datadog' }, 'no usable rightsizing connection'],
  [{ configured: false }, 'could not determine the rightsizing source'],
  [new ApiError('Cluster missing', 404), 'could not determine the rightsizing source'],
  [new ApiError('Gateway down', 503), 'could not determine the rightsizing source'],
])('keeps Prom charts available and withholds sizing when its selection is unresolved', (status, reason) => {
  const { cost, metrics, drawer } = render(status)
  expect(metrics).toContain('Prom charts')
  for (const panel of [metrics, drawer, cost]) {
    expect(panel).toContain(reason)
    expect(panel).not.toContain('Prom recommendation')
    expect(panel).not.toContain('DD recommendation')
  }
})

it.each([
  [{ configured: false, rightsizingProvider: 'prometheus' }, 'Prom charts', 'Prom recommendation'],
  [{ configured: true, rightsizingProvider: 'prometheus' }, 'Datadog', 'Prom recommendation'],
  [{ configured: false, rightsizingProvider: 'datadog', rightsizing: true }, 'Prom charts', 'DD recommendation'],
  [{ configured: true, rightsizingProvider: 'datadog', rightsizing: true }, 'Datadog', 'DD recommendation'],
])('resolves charts independently of the single selected sizing provider', (status, charts, expected) => {
  const { cost, metrics, drawer } = render(status)
  expect(metrics).toContain(charts)
  for (const panel of [metrics, drawer, cost]) {
    expect(panel.match(/(?:DD|Prom) recommendation/g)).toEqual([expected])
  }
})

it('keeps Prometheus sizing for chart-only Hubs and recognizes a missing status route', () => {
  const absentRoute = new ApiError('Not found', 404, {}, { unknownRoute: true })
  expect(render(absentRoute, false).cost).toContain('Prom recommendation')
  expect(render(absentRoute).cost).not.toContain('Prom recommendation')
  expect(render(new ApiError('Cluster unavailable', 404), false).cost).not.toContain('Prom recommendation')
  expect(render({ configured: true }, false).cost).toContain('Prom recommendation')
  expect(render({ configured: false }, false).cost).toContain('Prom recommendation')
})
