import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { CNPGStorage } from './CNPGStorage'
const state = vi.hoisted(() => ({ permission: 'denied', error: null as Error | null, terminating: false, webhookRejects: false, noData: false, instances: [] as any[] }))
vi.mock('../../api/cnpg', () => ({ useCNPGClusterCapabilities: () => ({ data: state.noData ? undefined : { facts: { terminating: state.terminating }, operator: { webhookRejects: state.webhookRejects, webhookReason: 'no ready webhook endpoint' }, actions: { reload: { permission: state.permission, allowed: false, reason: 'Hibernated', grant: { verb: 'patch', group: 'postgresql.cnpg.io', resource: 'clusters', namespace: 'db' } } } }, error: state.error, isRefetchError: !!state.error && !state.noData, dataUpdatedAt: Date.now() - 120_000 }) }))
vi.mock('../../api/cnpg-storage', () => ({ useCNPGClusterStorage: () => ({ data: { volumes: { state: 'ok' }, usage: { state: 'notRead' }, wal: { state: 'ok' }, expansion: { targets: [{ role: 'PG_DATA', field: 'spec.storage.size', declared: '1Gi' }] }, instances: state.instances, findings: [] } }) }))
const render = () => renderToStaticMarkup(<CNPGStorage namespace="db" name="pg" clusterObject={{ status: { currentPrimary: 'pg-1' } }} runtime={{ data: { permission: { proxy: 'denied', grant: { verb: 'get', resource: 'pods', subresource: 'proxy', namespace: 'db' } } } } as any} />)
it('names the missing proxy grant and retains the primary identity', () => {
  const html = render()
  expect(html).toContain('Database sizes unavailable: needs')
  expect(html).toContain('get pods/proxy')
  expect(html).toContain('in namespace db')
  expect(html).not.toContain('No primary reported')
})
it('refuses a known failed admission webhook or terminating Cluster even with patch access', () => {
  state.permission = 'allowed'; state.error = null; state.webhookRejects = true
  expect(render()).toContain('no ready webhook endpoint')
  expect(render()).toMatch(/<button[^>]*disabled=""[^>]*>Edit size…/)
  state.webhookRejects = false; state.terminating = true
  expect(render()).toContain('Cluster is being deleted')
  expect(render()).toMatch(/<button[^>]*disabled=""[^>]*>Edit size…/)
  state.terminating = false
})
it('disables size editing for denied patch access and names the grant', () => {
  state.permission = 'denied'; state.error = null
  const html = render()
  expect(html).toMatch(/<button[^>]*disabled=""[^>]*>Edit size…/)
  expect(html).toContain('patch clusters (postgresql.cnpg.io)')
  state.permission = 'allowed'
  expect(render()).not.toMatch(/<button[^>]*disabled=""[^>]*>Edit size…/)
  state.error = new Error('Permission timeout')
  expect(render()).toContain('Last refresh failed: Permission timeout')
  expect(render()).toContain('2m ago')
  expect(render()).not.toMatch(/<button[^>]*disabled=""[^>]*>Edit size…/)
  state.noData = true
  expect(render()).toContain('Permissions could not be checked: Permission timeout')
  expect(render()).toMatch(/<button[^>]*disabled=""[^>]*>Edit size…/)
  state.noData = false; state.error = null
})

it('offers declared-size editing without promising existing claims will expand', () => {
  state.permission = 'allowed'
  state.instances = [{ name: 'pg-1', role: 'primary', volumes: [{ claim: 'pg-1', role: 'PG_DATA', requested: '1Gi', capacity: '1Gi', storageClass: { name: 'local-path', allowVolumeExpansion: false }, resize: {}, usage: { state: 'notRead' } }] }]
  const html = render()
  expect(html).toContain('Declared volume sizes')
  expect(html).toContain('Edit declared size — existing volumes unchanged')
  expect(html).toContain('larger size will not resize the existing claims')
  expect(html).not.toContain('operator applies them to every instance')
  state.instances = []
})

it('shows slot existence independently of retention and unrelated partial reads', () => {
  const wal: any = { status: { state: 'ok' }, metrics: { state: 'partial' }, slotInventory: [{ name: '_cnpg_pg_2', active: false }], slots: [] }
  state.instances = [{ name: 'pg-1', role: 'primary', volumes: [], wal }]
  expect(render()).toContain('1 inactive slot; retained WAL not reported')
  wal.status = { state: 'partial', reason: 'Replication connections were capped' }
  wal.slotInventory = []
  expect(render()).toContain('No slots')
  wal.status = { state: 'unreachable' }
  wal.metrics = { state: 'ok' }
  wal.slotInventory = null
  wal.slots = [{ slot: 'subscriber', bytes: 2048 }]
  const html = render()
  expect(html).toContain('≥2.0 KiB retained WAL reported; slot inventory not read')
  expect(html).not.toContain('No slots')
  state.instances = []
})
