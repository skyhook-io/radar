import { beforeEach, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import { NavCustomizationProvider } from '../../context/NavCustomization'
vi.mock('../../hooks/usePreviousIntegrationSettings', () => ({ usePreviousIntegrationSettings: () => ({ metrics: false }) }))

let status: Record<string, unknown>

vi.mock('../../api/client', async (importActual) => ({
  ...(await importActual<typeof import('../../api/client')>()),
  useAutoPromConnect: () => undefined,
  usePrometheusStatus: () => ({ data: status, isLoading: false }),
  usePrometheusConnect: () => ({ mutate: vi.fn(), isPending: false }),
  useDiscoverMetrics: () => ({ mutate: vi.fn(), isPending: false }),
  usePrometheusResourceMetrics: () => ({ data: undefined }),
}))

const { PrometheusCharts } = await import('./PrometheusCharts')
const render = () => renderToString(
  <NavCustomizationProvider value={{}}>
    <PrometheusCharts kind="Deployment" namespace="demo" name="web" showEmptyState />
  </NavCustomizationProvider>,
)

beforeEach(() => { status = { available: false, connected: false } })

it('keeps the Prometheus setup when Radar cannot read OpenObserve', () => {
  const html = render()
  expect(html).toContain('Prometheus not connected')
  expect(html).toContain('Discover Prometheus')
  expect(html).not.toContain('Configure metrics')
})

it.each(['hub', 'local'])('offers metrics discovery when OpenObserve is available through %s', (path) => {
  status.openobserve = path
  const html = render()
  expect(html).toContain('No metrics source connected')
  expect(html).toContain('Discover metrics')
  expect(html).not.toContain('Discover Prometheus')
})

it('shows OpenObserve unavailable instead of Prometheus discovery', () => {
  status = { available: true, connected: true, openobserve: 'local', source: { kind: 'openobserve', via: 'local', unavailable: true, error: 'timeout' } }
  const html = render()
  expect(html).toContain('OpenObserve unavailable')
  expect(html).toContain('timeout')
  expect(html).not.toContain('Discover')
})
