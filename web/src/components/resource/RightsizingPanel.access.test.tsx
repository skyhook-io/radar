import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import { RightsizingPanel } from './RightsizingStrip'

vi.mock('../../api/client', () => ({
  useAutoPromConnect: () => {},
  usePrometheusStatus: () => ({ data: { connected: true } }),
  usePrometheusRightsizing: () => ({
    error: new Error('Access denied'),
    data: { window: '7d', ownerCoverage: 'ksm_history', rows: [], reason: 'Previous scan evidence' },
  }),
}))

it('hides previous evidence after the workload read is denied', () => {
  const html = renderToStaticMarkup(
    <MemoryRouter>
      <RightsizingPanel kind="Deployment" namespace="shop" name="api" />
    </MemoryRouter>,
  )
  expect(html).toContain('Access denied')
  expect(html).not.toContain('Previous scan evidence')
  expect(html).not.toContain('retained ownership')
})
