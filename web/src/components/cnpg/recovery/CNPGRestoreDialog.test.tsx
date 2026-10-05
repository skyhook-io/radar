import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'
import { CNPGRestoreDialog } from './CNPGRestoreDialog'
const state = vi.hoisted(() => ({ coverage: 'denied', destination: false }))
vi.mock('../../../api/cnpg', () => ({ useCNPGWorkspace: () => ({ isLoading: false, data: { coverage: { backups: { state: state.coverage } }, objects: { clusters: [{ apiVersion: 'postgresql.cnpg.io/v1', metadata: { name: 'pg', namespace: 'db' }, spec: state.destination ? { backup: { barmanObjectStore: { destinationPath: 's3://archive' } } } : {} }], backups: [] } } }), useCNPGRuntime: () => ({}) }))
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
