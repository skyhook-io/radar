// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, expect, it, vi } from 'vitest'
import { CNPGProtectionSetup } from './CNPGProtectionSetup'

const state = vi.hoisted(() => ({
  support: 'supported',
  context: 'ctx',
  cluster: {} as any,
  schedules: [] as any[],
  coverage: 'full',
  wal: 'Not reported',
  walState: 'unknown',
  backup: 'No successful backup yet',
}))
vi.mock('../../../api/client', () => ({ useRadarFeature: () => ({ support: state.support }) }))
vi.mock('../../../context/ConnectionContext', () => ({
  useConnection: () => ({ connection: { context: state.context } }),
}))
vi.mock('../useCNPGSidebarWorkspace', () => ({
  useCNPGFleet: () => ({
    query: {
      data: {
        objects: { objectStores: [], scheduledBackups: state.schedules },
        coverage: { scheduledBackups: { state: state.coverage } },
      },
      refetch: vi.fn(),
    },
    fleet: {
      rows: [
        {
          namespace: 'db',
          name: 'pg',
          cluster: state.cluster,
          protection: { walArchiving: { text: state.wal, state: state.walState }, lastSuccessfulBackup: { text: state.backup } },
        },
      ],
    },
  }),
}))
vi.mock('../../../api/cnpg', () => ({
  useCNPGOperator: () => ({
    data: { components: [{ pluginName: 'barman-cloud.cloudnative-pg.io' }], coverage: { services: { state: 'full' } } },
  }),
  useCNPGClusterCapabilities: () => ({
    data: { actions: { backup: { allowed: false, reason: 'You need create backups in namespace db' } } },
  }),
}))
vi.mock('@skyhook-io/k8s-ui', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@skyhook-io/k8s-ui')>()),
  DialogPortal: ({ children }: any) => children,
}))

beforeEach(() => {
  state.support = 'supported'
  state.cluster = { metadata: { name: 'pg', namespace: 'db', uid: 'uid' }, spec: {} }
  state.schedules = []
  state.coverage = 'full'
  state.walState = 'unknown'
  state.wal = 'Not reported'
  state.backup = 'No successful backup yet'
})
const text = () =>
  renderToStaticMarkup(
    <MemoryRouter initialEntries={['/?protectionSetup=1']}>
      <CNPGProtectionSetup namespace="db" name="pg" onInspect={vi.fn()} />
    </MemoryRouter>,
  ).replace(/<[^>]*>/g, '')

it('presents the whole task without treating a declaration or an unread fact as protection', () => {
  const html = text()
  expect(html).toContain('1. Archive storage')
  expect(html).toContain('2. Backup schedule')
  expect(html).toContain('3. Verify protection')
  expect(html).toContain('Attach the Barman archive destination first')
  expect(html).toContain('You need create backups in namespace db')
  expect(html).toContain('Not reported')
  expect(html).not.toContain('Protection complete')
})

it('makes a suspended matching schedule reachable rather than presenting active protection', () => {
  state.cluster.spec.plugins = [
    { name: 'barman-cloud.cloudnative-pg.io', isWALArchiver: true, parameters: { barmanObjectName: 'store' } },
  ]
  state.schedules = [
    {
      metadata: { name: 'nightly', namespace: 'db' },
      spec: {
        cluster: { name: 'pg' },
        method: 'plugin',
        pluginConfiguration: { name: 'barman-cloud.cloudnative-pg.io' },
        schedule: '@daily',
        suspend: true,
      },
    },
  ]
  expect(text()).toContain('suspended: open the schedule to resume it')
  expect(text()).not.toContain('Create matching schedule…')
})

it('does not interpret denied schedule inventory as an absent schedule', () => {
  state.coverage = 'denied'
  expect(text()).toContain('Inventory incomplete')
  expect(text()).toContain('Schedules may exist outside the readable inventory')
})

it('shows the upgrade path before offering setup writes against an older Radar', () => {
  state.support = 'unsupported'
  expect(text()).toContain('need a newer Radar')
  expect(text()).not.toContain('Choose existing ObjectStore')
})

it('puts failed uploads ahead of the setup milestones and links to the existing repair flow', () => {
 state.wal = 'Failing'
 state.walState = 'failing'
 const html = renderToStaticMarkup(<MemoryRouter initialEntries={['/?protectionSetup=1']}><CNPGProtectionSetup namespace="db" name="pg" onInspect={vi.fn()} onOpenArchivingRepair={vi.fn()} /></MemoryRouter>).replace(/<[^>]*>/g, '')
 expect(html.indexOf('Repair WAL archiving first')).toBeLessThan(html.indexOf('1. Archive storage'))
 expect(html).toContain('Open archiving repair')
 expect(html).toContain('Uploads failing')
})
