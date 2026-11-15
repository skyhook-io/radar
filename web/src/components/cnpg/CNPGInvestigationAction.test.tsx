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

const draw = (render: any) => renderToStaticMarkup(<CNPGInvestigationAction namespace="db" name="payments" render={render} context={{ kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'db', name: 'payments', health: 'healthy' }} />)
const POSTURE_CHECKS = ['cnpgNoDeclarativeBackup', 'cnpgSingleInstance', 'cnpgNoBackupDestination', 'cnpgNoWALArchiving', 'cnpgInTreeBackupDeprecated']

it('leaves every posture check without a problem mark, as the rest of the workspace does', () => {
  const render = vi.fn(() => null)
  state.dimensions = [{ tone: 'healthy' }]
  for (const id of POSTURE_CHECKS) {
    state.row = { attention: false, problems: [{ id: `audit:${id}:db/payments`, severity: 'posture' }] }
    draw(render)
    expect(render).toHaveBeenLastCalledWith(expect.objectContaining({ health: 'healthy' }))
  }
  state.row = { attention: false, problems: POSTURE_CHECKS.map((id) => ({ id: `audit:${id}:db/payments`, severity: 'posture' })) }
  draw(render)
  expect(render).toHaveBeenLastCalledWith(expect.objectContaining({ health: 'healthy' }))
})

it('still investigates posture beside a degraded dimension or a real problem, and stays unknown when unassessed', () => {
  const render = vi.fn(() => null)
  const posture = { id: 'audit:cnpgNoBackupDestination:db/payments', severity: 'posture' }
  state.row = { attention: false, problems: [posture] }
  state.dimensions = [{ tone: 'healthy' }, { tone: 'degraded' }]
  draw(render)
  expect(render).toHaveBeenLastCalledWith(expect.objectContaining({ health: 'problem' }))
  state.row = { attention: true, problems: [posture, { id: 'issue:x', severity: 'warning' }] }
  state.dimensions = [{ tone: 'healthy' }]
  draw(render)
  expect(render).toHaveBeenLastCalledWith(expect.objectContaining({ health: 'problem' }))
  state.row = { attention: false, problems: [posture] }
  state.dimensions = [{ tone: 'unknown' }]
  draw(render)
  expect(render).toHaveBeenLastCalledWith(expect.objectContaining({ health: 'unknown' }))
})
