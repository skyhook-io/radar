import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import { CNPGBackupsTab, CNPGStorageTab } from './CNPGClusterTabs'
const state = vi.hoisted(() => ({ coverage: 'full', primary: {} as any, storageProps: {} as any, schedules: [] as any[] }))
const cluster = { apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', metadata: { name: 'pg', namespace: 'db' }, status: { currentPrimary: 'pg-1' }, spec: {} as any }
vi.mock('../../api/cnpg', () => ({ useCNPGRuntime: () => ({ data: { permission: { proxy: 'denied' }, instances: [state.primary] } }) }))
vi.mock('./useCNPGClusterAssessment', () => ({ useCNPGClusterAssessment: () => ({ dimensions: [], row: { cluster, problems: [] }, runtime: {} }) }))
vi.mock('./useCNPGSidebarWorkspace', () => ({ useCNPGFleet: () => ({ query: { data: { installed: true, coverage: { backups: { state: state.coverage } }, objects: { backups: [], scheduledBackups: state.schedules } } }, fleet: { rows: [{ name: 'pg', namespace: 'db', cluster }] } }) }))
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

it('offers the destination setup path and Cluster YAML without claiming it is configured', () => {
  state.schedules = [{ metadata: { name: 'pg-nightly', namespace: 'db' }, spec: { cluster: { name: 'pg' } } }]
  const html = renderToStaticMarkup(<MemoryRouter><CNPGBackupsTab namespace="db" name="pg" onInspect={() => {}} onOpenYaml={() => {}} /></MemoryRouter>)
  expect(html).toContain('How to set up backups:')
  expect(html).toContain('barman-cloud plugin and an ObjectStore')
  expect(html).toContain('volume snapshots')
  expect(html).toContain('CloudNativePG backup docs')
  expect(html).toContain('Cluster YAML →')
  expect(html).toContain('pg-nightly (barmanObjectStore) cannot back up pg until its method has a destination')
  state.schedules = []
})

it('explains a schedule method mismatch without asking to configure an existing plugin destination', () => {
  cluster.spec.plugins = [{ name: 'barman-cloud.cloudnative-pg.io', parameters: { barmanObjectName: 'store' } }]
  state.schedules = [{ metadata: { name: 'pg-nightly', namespace: 'db' }, spec: { cluster: { name: 'pg' } } }]
  const html = renderToStaticMarkup(<MemoryRouter><CNPGBackupsTab namespace="db" name="pg" onInspect={() => {}} /></MemoryRouter>)
  expect(html).toContain('This Cluster uses ObjectStore store')
  expect(html).toContain('spec.method to plugin')
  expect(html).toContain('spec.pluginConfiguration.name to barman-cloud.cloudnative-pg.io')
  expect(html).not.toContain('configure a destination with the barman-cloud plugin')
  state.schedules = []
  delete cluster.spec.plugins
})

it('does not invent a missing destination for an enabled third-party plugin', () => {
  cluster.spec.plugins = [{ name: 'third-party-backup' }]
  const html = renderToStaticMarkup(<MemoryRouter><CNPGBackupsTab namespace="db" name="pg" onInspect={() => {}} /></MemoryRouter>)
  expect(html).not.toContain('How to set up backups')
  delete cluster.spec.plugins
})
