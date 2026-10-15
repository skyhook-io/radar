import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { CNPGClusterActivity } from './CNPGClusterActivity'
vi.mock('../../api/cnpg', () => ({ useCNPGClusterActivity: () => ({ data: { events: [], oldest: null, attributionSince: null }, isRefetchError: true, error: new Error('Activity timeout'), dataUpdatedAt: Date.now() - 180_000 }) }))
vi.mock('../../api/cnpg-history', () => ({ useCNPGClusterActivityWindow: () => ({}) }))
vi.mock('./CNPGTrends', () => ({ useCNPGIntervalParams: () => null }))
it('labels retained activity after a failed refresh', () => {
  const html = renderToStaticMarkup(<CNPGClusterActivity namespace="db" name="pg" />)
  expect(html).toContain('Last refresh failed: Activity timeout')
  expect(html).toContain('3m ago')
})
