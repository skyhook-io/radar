import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { CNPGInvestigationAction } from './CNPGClusterTabs'
const state = vi.hoisted(() => ({ row: undefined as any, dimensions: undefined as any }))
vi.mock('./useCNPGClusterAssessment', () => ({ useCNPGClusterAssessment: () => state }))
it('labels investigation for any CNPG problem or degraded dimension including a healthy controller phase', () => {
  const render = vi.fn((ctx) => <button>{ctx.health === 'problem' ? 'Investigate' : 'Sparkle'}</button>)
  const draw = () => renderToStaticMarkup(<CNPGInvestigationAction namespace="db" name="payments" render={render} context={{ kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'db', name: 'payments', health: 'healthy' }} />)
  state.row = { problems: [{ severity: 'warning' }], controllerStatus: { level: 'healthy' } }
  expect(draw()).toContain('Investigate')
  state.row = { problems: [] }
  for (const tone of ['degraded', 'unhealthy', 'alert']) { state.dimensions = [{ tone }]; expect(draw()).toContain('Investigate') }
  state.dimensions = [{ tone: 'healthy' }]; expect(draw()).toContain('Sparkle')
  state.dimensions = [{ tone: 'unknown' }]; draw()
  expect(render).toHaveBeenLastCalledWith(expect.objectContaining({ health: 'unknown' }))
  state.row = undefined; state.dimensions = undefined; draw()
  expect(render).toHaveBeenLastCalledWith(expect.objectContaining({ health: 'unknown', name: 'payments' }))
})

it('honors the requested any-problem rule for posture and degraded protection without altering fleet attention', () => {
  const render = vi.fn(() => null)
  state.row = { attention: false, problems: [{ id: 'audit:cnpgNoDeclarativeBackup', severity: 'posture' }] }
  state.dimensions = [{ tone: 'healthy' }]
  renderToStaticMarkup(<CNPGInvestigationAction namespace="db" name="payments" render={render} context={{ kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'db', name: 'payments', health: 'healthy' }} />)
  expect(render).toHaveBeenLastCalledWith(expect.objectContaining({ health: 'problem' }))
  expect(state.row.attention).toBe(false)
  state.row = { attention: false, problems: [] }; state.dimensions = [{ tone: 'degraded' }]
  renderToStaticMarkup(<CNPGInvestigationAction namespace="db" name="payments" render={render} context={{ kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'db', name: 'payments', health: 'healthy' }} />)
  expect(render).toHaveBeenLastCalledWith(expect.objectContaining({ health: 'problem' }))
})
