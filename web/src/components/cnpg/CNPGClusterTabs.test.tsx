import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, expect, it, vi } from 'vitest'
import type { CNPGDimension } from '@skyhook-io/k8s-ui'
import { CNPGBackupsTab, CNPGStorageTab } from './CNPGClusterTabs'
const state = vi.hoisted(() => ({
  coverage: 'full',
  primary: {} as any,
  storageProps: {} as any,
  schedules: [] as any[],
  dimensions: [] as CNPGDimension[],
}))
const cluster = {
  apiVersion: 'postgresql.cnpg.io/v1',
  kind: 'Cluster',
  metadata: { name: 'pg', namespace: 'db' },
  status: { currentPrimary: 'pg-1' } as any,
  spec: {} as any,
}
vi.mock('../../api/client', () => ({ useRadarFeature: () => ({ support: 'supported' }) }))
vi.mock('../../api/cnpg', () => ({
  useCNPGRuntime: () => ({ data: { permission: { proxy: 'denied' }, instances: [state.primary] } }),
}))
vi.mock('./useCNPGClusterAssessment', () => ({
  useCNPGClusterAssessment: () => ({
    dimensions: state.dimensions,
    row: {
      cluster,
      problems: [],
      protection: { walArchiving: { text: 'Not archived: no destination configured', tone: 'neutral' } },
    },
    query: {
      data: { coverage: { backups: { state: state.coverage } }, objects: { backups: [], clusters: [cluster] } },
    },
    runtime: {},
  }),
}))
vi.mock('./useCNPGSidebarWorkspace', () => ({
  useCNPGFleet: () => ({
    query: {
      data: {
        installed: true,
        coverage: { backups: { state: state.coverage } },
        objects: { backups: [], scheduledBackups: state.schedules },
      },
    },
    fleet: { rows: [{ name: 'pg', namespace: 'db', cluster }] },
  }),
}))
vi.mock('../../api/cnpg-recovery', () => ({ useCNPGRestoreCapability: () => ({ data: { allowed: true } }) }))
vi.mock('./CNPGProtection', () => ({ CNPGProtection: () => null }))
vi.mock('./CNPGArchivingRepair', () => ({ CNPGArchivingRepair: () => null }))
vi.mock('./CNPGStorage', () => ({
  CNPGStorage: (props: any) => {
    state.storageProps = props
    return null
  },
}))
vi.mock('./recovery/CNPGRestoreButton', () => ({
  CNPGRestoreButton: ({ disabledReason }: { disabledReason?: string }) => (
    <button disabled={!!disabledReason}>Restore</button>
  ),
}))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
beforeEach(() => {
  state.coverage = 'full'
  state.primary = {}
  state.schedules = []
  state.dimensions = []
  cluster.spec = {}
  delete cluster.status.lastSuccessfulBackup
})
it('passes primary identity through to Storage when database reads are denied', () => {
  state.primary = { pod: 'pg-1', role: 'primary', metrics: { state: 'denied' } }
  renderToStaticMarkup(<CNPGStorageTab namespace="db" name="pg" />)
  expect(state.storageProps.primary).toBe(state.primary)
  expect(state.storageProps.runtime.data.permission.proxy).toBe('denied')
})
it('uses the shared restore assessment for the Backups button', () => {
  const render = () =>
    renderToStaticMarkup(
      <MemoryRouter>
        <CNPGBackupsTab namespace="db" name="pg" onInspect={() => {}} />
      </MemoryRouter>,
    )
  state.coverage = 'full'
  expect(render()).toContain('disabled=""')
  expect(render()).toContain('Nothing to restore from yet')
  state.coverage = 'denied'
  expect(render()).not.toContain('disabled=""')
  expect(render()).not.toContain('Nothing to restore from yet')
})

it('starts a single guided setup path beside restore, without treating a schedule as protection', () => {
  state.schedules = [{ metadata: { name: 'pg-nightly', namespace: 'db' }, spec: { cluster: { name: 'pg' } } }]
  const html = renderToStaticMarkup(
    <MemoryRouter>
      <CNPGBackupsTab namespace="db" name="pg" onInspect={() => {}} onOpenYaml={() => {}} />
    </MemoryRouter>,
  )
  expect(html).toContain('Set up backups…')
  expect(html).toContain('Nothing to restore from yet')
  expect(html).not.toContain('Configure spec.backup.barmanObjectStore')
})

it('keeps the protection verdict ahead of setup without repeating the blocker', () => {
  state.schedules = [{ metadata: { name: 'pg-nightly', namespace: 'db' }, spec: { cluster: { name: 'pg' } } }]
  state.dimensions = [
    {
      id: 'protection',
      label: 'Backups',
      tone: 'degraded',
      text: 'Backup schedule pg-nightly cannot run: no backup destination',
      source: 'ScheduledBackup method against its target Cluster spec',
    },
  ]
  const html = renderToStaticMarkup(
    <MemoryRouter>
      <CNPGBackupsTab namespace="db" name="pg" onInspect={() => {}} onOpenYaml={() => {}} />
    </MemoryRouter>,
  )
  expect(html.match(/cannot run:/g)).toHaveLength(1)
  expect(html.indexOf(state.dimensions[0].text)).toBeLessThan(html.indexOf('Set up backups…'))
})

it('does not invent a missing destination for an enabled third-party plugin', () => {
  cluster.spec.plugins = [{ name: 'third-party-backup' }]
  const html = renderToStaticMarkup(
    <MemoryRouter>
      <CNPGBackupsTab namespace="db" name="pg" onInspect={() => {}} />
    </MemoryRouter>,
  )
  expect(html).not.toContain('Configure spec.backup')
  delete cluster.spec.plugins
})

it('passes recovery certainty instead of equating a destination with a usable backup', () => {
  const renderStorage = () => renderToStaticMarkup(<CNPGStorageTab namespace="db" name="pg" />)
  state.coverage = 'full'
  cluster.spec.backup = { barmanObjectStore: { destinationPath: 's3://backups' } }
  renderStorage()
  expect(state.storageProps.restoreState).toBe('none')
  cluster.status.lastSuccessfulBackup = '2026-10-01T00:00:00Z'
  renderStorage()
  expect(state.storageProps.restoreState).toBe('available')
  delete cluster.status.lastSuccessfulBackup
  state.coverage = 'denied'
  renderStorage()
  expect(state.storageProps.restoreState).toBe('unknown')
  state.coverage = 'full'
  delete cluster.spec.backup
})
