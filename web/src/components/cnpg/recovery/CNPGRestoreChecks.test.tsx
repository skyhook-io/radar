import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { CNPGRestoreChecks } from './CNPGRestoreChecks'

const state = vi.hoisted(() => ({ estimatedRows: 0, noEstimate: 1 }))
vi.mock('../../../api/cnpg-inspect', () => ({
  useCNPGRestoreChecks: () => ({
    data: { state: 'ok', database: 'app', timeline: 2, contents: { tables: 1, ...state, largest: [] } },
    refetch: vi.fn(),
  }),
}))

it('distinguishes unavailable, partial and known-zero row estimates', () => {
  const render = () => renderToStaticMarkup(<CNPGRestoreChecks namespace="db" name="restored" />)
  expect(render()).toContain('Row estimate unavailable')
  expect(render()).not.toContain('about 0 rows')
  state.estimatedRows = 12000
  expect(render()).toContain('about 12 k rows (partial estimate)')
  state.estimatedRows = 0
  state.noEstimate = 0
  expect(render()).toContain('about 0 rows')
  expect(render()).not.toContain('Row estimate unavailable')
})
