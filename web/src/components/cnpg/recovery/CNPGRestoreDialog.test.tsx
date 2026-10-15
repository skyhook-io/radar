import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'
import { CNPGRestoreDialog } from './CNPGRestoreDialog'
const state = vi.hoisted(() => ({ coverage: 'denied', destination: false, backups: [] as any[] }))
vi.mock('../../../api/cnpg', () => ({ useCNPGWorkspace: () => ({ isLoading: false, data: { coverage: { backups: { state: state.coverage } }, objects: { clusters: [{ apiVersion: 'postgresql.cnpg.io/v1', metadata: { name: 'pg', namespace: 'db' }, spec: state.destination ? { backup: { barmanObjectStore: { destinationPath: 's3://archive' } } } : {} }], backups: state.backups } } }), useCNPGRuntime: () => ({}) }))
vi.mock('../../../api/client', () => ({ useResources: () => ({ data: [] }) }))
vi.mock('../../../api/cnpg-recovery', () => ({ useCNPGRestoreCapability: () => ({ data: { allowed: true } }) }))
vi.mock('../../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
vi.mock('../../ui/Toast', () => ({ useToast: () => ({ showSuccess: vi.fn() }) }))
vi.mock('../useCNPGNavigate', () => ({ useCNPGNavigate: () => vi.fn() }))
vi.mock('@skyhook-io/k8s-ui', async (original) => ({ ...(await original<typeof import('@skyhook-io/k8s-ui')>()), ActionConfirmDialog: ({ disabledReason, warnings, children }: { disabledReason?: string; warnings?: string[]; children: ReactNode }) => <div>{disabledReason}{warnings?.join(' · ')}{children}</div> }))
it('says sources could not be read instead of claiming no completed Backup', () => {
  const render = () => renderToStaticMarkup(<CNPGRestoreDialog namespace="db" entry={{ kind: 'cluster', name: 'pg' }} onClose={() => {}} />)
  state.coverage = 'denied'
  expect(render()).toContain('Backup sources could not be read in db')
  expect(render()).not.toContain('no completed Backup')
  state.coverage = 'full'
  expect(render()).toContain('Nothing to restore from yet: no backup destination and no completed Backup')
})
it('names unread Backups even when an archive destination can be used', () => {
  state.coverage = 'denied'; state.destination = true
  const html = renderToStaticMarkup(<CNPGRestoreDialog namespace="db" entry={{ kind: 'cluster', name: 'pg' }} onClose={() => {}} />)
  expect(html).toContain('Backup sources could not be read in db')
  expect(html).toContain('Barman object store (in-tree)')
  expect(html).not.toContain('no completed Backup')
  state.destination = false
})
it('offers a volume snapshot only its own end and says why, while an in-tree Backup keeps WAL targets', () => {
  const backup = (name: string, method: string) => ({ apiVersion: 'postgresql.cnpg.io/v1', metadata: { name, namespace: 'db' }, spec: { cluster: { name: 'pg' }, method }, status: { phase: 'completed', method, stoppedAt: '2026-09-30T12:00:00Z' } })
  state.coverage = 'full'
  state.backups = [backup('snap', 'volumeSnapshot'), backup('base', 'barmanObjectStore')]
  const render = (name: string) => renderToStaticMarkup(<CNPGRestoreDialog namespace="db" entry={{ kind: 'backup', name }} onClose={() => {}} />)
  const snapshot = render('snap')
  expect(snapshot).toContain('The end of this snapshot (2026-09-30 12:00:00 UTC)')
  expect(snapshot).toMatch(/checked=""\/>The end of this snapshot/)
  expect(snapshot).toContain('needs a WAL archive source, which this restore does not configure')
  expect(snapshot).not.toContain('The latest archived WAL')
  expect(snapshot).not.toContain('A point in time')
  expect(snapshot).not.toContain('Last archived WAL')
  const inTree = render('base')
  expect(inTree).toContain('The latest archived WAL')
  expect(inTree).toContain('A point in time')
  expect(inTree).not.toContain('The end of this')
  state.backups = []
})
