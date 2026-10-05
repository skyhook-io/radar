import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import { CNPGBackupsTab, CNPGStorageTab } from './CNPGClusterTabs'
const state = vi.hoisted(() => ({ coverage: 'full', primary: {} as any, storageProps: {} as any }))
const cluster = { metadata: { name: 'pg', namespace: 'db' }, status: { currentPrimary: 'pg-1' }, spec: {} }
vi.mock('../../api/cnpg', () => ({ useCNPGRuntime: () => ({ data: { permission: { proxy: 'denied' }, instances: [state.primary] } }) }))
vi.mock('./useCNPGClusterAssessment', () => ({ useCNPGClusterAssessment: () => ({ dimensions: [], row: { cluster, problems: [] }, runtime: {} }) }))
vi.mock('./useCNPGSidebarWorkspace', () => ({ useCNPGFleet: () => ({ query: { data: { installed: true, coverage: { backups: { state: state.coverage } }, objects: { backups: [] } } }, fleet: { rows: [{ name: 'pg', namespace: 'db', cluster }] } }) }))
vi.mock('../../api/cnpg-recovery', () => ({ useCNPGRestoreCapability: () => ({ data: { allowed: true } }) }))
vi.mock('./CNPGProtection', () => ({ CNPGProtection: () => null }))
vi.mock('./CNPGArchivingRepair', () => ({ CNPGArchivingRepair: () => null }))
vi.mock('./CNPGStorage', () => ({ CNPGStorage: (props: any) => { state.storageProps = props; return null } }))
vi.mock('./recovery/CNPGRestoreButton', () => ({ CNPGRestoreButton: ({ disabledReason }: { disabledReason?: string }) => <button disabled={!!disabledReason}>Restore</button> }))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
it('passes primary identity through to Storage when database reads are denied', () => {
  state.primary = { pod: 'pg-1', role: 'primary', metrics: { state: 'denied' } }
  renderToStaticMarkup(<CNPGStorageTab namespace="db" name="pg" />)
  expect(state.storageProps.primary).toBe(state.primary)
  expect(state.storageProps.runtime.data.permission.proxy).toBe('denied')
})
it('uses the shared restore assessment for the Backups button', () => {
  const render = () => renderToStaticMarkup(<MemoryRouter><CNPGBackupsTab namespace="db" name="pg" onInspect={() => {}} /></MemoryRouter>)
  state.coverage = 'full'
  expect(render()).toContain('disabled=""')
  expect(render()).toContain('Nothing to restore from yet')
  state.coverage = 'denied'
  expect(render()).not.toContain('disabled=""')
  expect(render()).not.toContain('Nothing to restore from yet')
})
