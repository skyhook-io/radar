import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, expect, it, vi } from 'vitest'
import { RightsizingPanel } from './RightsizingStrip'

let source: Record<string, unknown> | undefined

vi.mock('../../api/client', () => ({
  useAutoPromConnect: () => {},
  usePrometheusStatus: () => ({ data: { connected: true, source } }),
  usePrometheusRightsizing: () => ({ error: null, data: { window: '7d', ownerCoverage: 'ksm_history', rows: [] } }),
}))

beforeEach(() => { source = undefined })

const render = () => renderToStaticMarkup(
  <MemoryRouter>
    <RightsizingPanel kind="Deployment" namespace="shop" name="api" />
  </MemoryRouter>,
)

it('names Prometheus without an OpenObserve source', () => {
  expect(render()).toContain('Rightsizing · Prometheus')
})

it('names OpenObserve when it serves this cluster', () => {
  source = { kind: 'openobserve', via: 'hub', matchedBy: 'id' }
  expect(render()).toContain('Rightsizing · OpenObserve')
})

it('keeps Prometheus when OpenObserve only explains why it is not used', () => {
  source = { kind: 'openobserve', via: 'local', warning: 'Not matched for this cluster.' }
  expect(render()).toContain('Rightsizing · Prometheus')
})
