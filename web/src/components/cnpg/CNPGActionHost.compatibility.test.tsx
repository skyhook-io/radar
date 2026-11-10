import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, expect, it, vi } from 'vitest'
import { cnpgHost } from './host'
import { renderCNPGHeaderActions } from './actions/renderCNPGHeaderActions'
import { radarFeatureSupport } from '../../api/radarFeatures'
const state = vi.hoisted(() => ({ capabilities: { features: { resourceIssues: true } } as any }))
vi.mock('../../api/client', async original => ({ ...await original<typeof import('../../api/client')>(), useRadarFeature: () => ({ support: radarFeatureSupport('cnpgWorkspace', state.capabilities, 'v1.16.0') }) }))
vi.mock('./CNPGClusterTabs', () => ({ CNPGInvestigationAction: () => <span>CNPG investigation</span> }))
vi.mock('./actions/CNPGClusterActions', () => ({ CNPGClusterActions: () => <span>CNPG write actions</span> }))
vi.mock('./actions/CNPGConnectButton', () => ({ CNPGConnectButton: () => null }))
vi.mock('./actions/CNPGScheduleActions', () => ({ CNPGScheduleActions: () => null }))
vi.mock('./actions/CNPGPoolerActions', () => ({ CNPGPoolerActions: () => null }))
vi.mock('./operations/CNPGOperationTracker', () => ({ CNPGOperationTracker: () => null }))
vi.mock('./recovery/CNPGRestoreButton', () => ({ CNPGRestoreButton: () => null }))
beforeEach(() => { state.capabilities = { features: { resourceIssues: true } } })
const context = { kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'db', name: 'pg', compact: false }
const resource = { apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', metadata: { name: 'pg', namespace: 'db', uid: 'uid-pg' } }
it('preserves ordinary Diagnose and omits new action affordances on the shipped missing-flag state', () => {
  const original = vi.fn(() => <span>ordinary investigation</span>)
  expect(renderToStaticMarkup(cnpgHost.diagnoseAction(original)!(context))).toContain('ordinary investigation')
  expect(original).toHaveBeenCalledWith(context)
  expect(renderToStaticMarkup(renderCNPGHeaderActions({ resource, namespace: 'db', name: 'pg', compact: false }))).toBe('')
})
it('mounts the CNPG decorations once their contracts are advertised', () => {
  state.capabilities = { features: { cnpgWorkspace: true } }
  const original = vi.fn(() => <span>ordinary investigation</span>)
  expect(renderToStaticMarkup(cnpgHost.diagnoseAction(original)!(context))).toContain('CNPG investigation')
  expect(original).not.toHaveBeenCalled()
  expect(renderToStaticMarkup(renderCNPGHeaderActions({ resource, namespace: 'db', name: 'pg', compact: false }))).toContain('CNPG write actions')
})
