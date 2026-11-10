// @vitest-environment jsdom
import { MemoryRouter } from 'react-router-dom'
import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { CNPGStorage } from './CNPGStorage'
const state = vi.hoisted(() => ({ permission: 'denied', error: null as Error | null, terminating: false, webhookRejects: false, noData: false, instances: [] as any[], usage: { state: 'notRead' } as any }))
vi.mock('../../api/cnpg', () => ({ useCNPGClusterCapabilities: () => ({ data: state.noData ? undefined : { facts: { terminating: state.terminating }, operator: { webhookRejects: state.webhookRejects, webhookReason: 'no ready webhook endpoint' }, actions: { reload: { permission: state.permission, allowed: false, reason: 'Hibernated', grant: { verb: 'patch', group: 'postgresql.cnpg.io', resource: 'clusters', namespace: 'db' } } } }, error: state.error, isRefetchError: !!state.error && !state.noData, dataUpdatedAt: Date.now() - 120_000 }) }))
vi.mock('../../api/cnpg-storage', () => ({ useCNPGClusterStorage: () => ({ data: { volumes: { state: 'ok' }, usage: state.usage, wal: { state: 'ok' }, expansion: { targets: [{ role: 'PG_DATA', field: 'spec.storage.size', declared: '1Gi' }] }, instances: state.instances, findings: [] } }) }))
const render = () => renderToStaticMarkup(<MemoryRouter><CNPGStorage restoreState="none" namespace="db" name="pg" clusterObject={{ status: { currentPrimary: 'pg-1' } }} runtime={{ data: { instances: [{ pod: 'pg-1', role: 'primary' }], permission: { proxy: 'denied', grant: { verb: 'get', resource: 'pods', subresource: 'proxy', namespace: 'db' } } } } as any} /></MemoryRouter>)
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
  expect(render()).toContain('1 inactive slot')
  expect(render().match(/retained WAL not reported/g)).toHaveLength(1)
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

it('labels volumes for an absent instance as an expected standby and associates its slot once', () => {
  const wal: any = { status: { state: 'ok' }, metrics: { state: 'ok' }, slotInventory: [{ name: '_cnpg_pg_2', active: false }], slots: [] }
  state.instances = [{ name: 'pg-1', role: 'primary', volumes: [], wal }, { name: 'pg-2', role: 'replica', volumes: [{ claim: 'pg-2', role: 'PG_DATA', requested: '1Gi', capacity: '1Gi', storageClass: {}, resize: {}, usage: { state: 'notRead' } }] }]
  const html = renderToStaticMarkup(<MemoryRouter><CNPGStorage namespace="db" name="pg" clusterObject={{ status: { currentPrimary: 'pg-1', instanceNames: ['pg-1', 'pg-2'] } }} runtime={{ data: { permission: { proxy: 'allowed' }, instances: [{ pod: 'pg-1', role: 'primary' }] } } as any} /></MemoryRouter>)
  expect(html).toContain('expected standby · not running')
  expect(html).not.toContain('>replica<')
  expect(html.match(/expected instance pg-2/g)).toHaveLength(1)
  expect(html).toContain('<div>retained WAL not reported</div>')
  expect(html.match(/retained WAL not reported/g)).toHaveLength(1)
  const unread = renderToStaticMarkup(<MemoryRouter><CNPGStorage namespace="db" name="pg" /></MemoryRouter>)
  expect(unread).not.toContain('expected standby · not running')
  state.instances = []
})

it('keeps raw last-archived time folded when no recovery destination exists', () => {
  state.instances = [{ name: 'pg-1', role: 'primary', volumes: [], wal: { status: { state: 'ok' }, metrics: { state: 'ok' }, lastArchivedAt: '2026-10-01T12:00:00Z', readyToArchive: 0, slots: [], slotInventory: [] } }]
  const host = document.createElement('div')
  host.innerHTML = renderToStaticMarkup(<MemoryRouter><CNPGStorage namespace="db" name="pg" walArchiving={{ state: 'no_destination' as const, text: 'Not archived: no destination configured', tone: 'neutral' }} /></MemoryRouter>)
  expect(host.textContent).toContain('Not archived: no destination configured')
  expect(host.textContent).toContain('Instance manager record')
  expect(host.querySelector('[inert]')?.textContent).toContain('as recorded by the instance manager')
  expect(host.querySelector('[inert]')?.textContent).toContain('Last archived')
  state.instances = []
})
it('attributes pending volume states and identifies the first instance without an observed role', () => {
  state.instances = [{ name: 'analytics-1', role: 'unknown', volumes: [{ claim: 'analytics-1', role: 'PG_DATA', requested: '1Gi', phase: 'Pending', clusterState: 'initializing', storageClass: { name: 'local', allowVolumeExpansion: false }, resize: {}, usage: { state: 'notRead' } }] }]
  const html = renderToStaticMarkup(<MemoryRouter><CNPGStorage namespace="db" name="analytics" clusterObject={{ status: { phase: 'Setting up primary' } }} runtime={{ data: { permission: { proxy: 'allowed' }, instances: [] } } as any} /></MemoryRouter>)
  for (const text of ['expected first instance · not running', 'WAL cannot be measured until the instance starts', 'Operator: initializing', 'Claim: Pending', 'No capacity provisioned yet', 'Edit the declared size for future claims']) expect(html).toContain(text)
  expect(html).not.toContain('role unknown')
  expect(html).not.toContain('Restore into a new Cluster')
  state.instances = []
})
it('prescribes restore only with a verified source and uses intrinsic sibling volume rows', () => {
  const v = { claim: 'pg-1', role: 'PG_DATA', requested: '1Gi', capacity: '1Gi', storageClass: { name: 'local', allowVolumeExpansion: false }, resize: {}, usage: { state: 'notRead' } }
  state.instances = [{ name: 'pg-1', role: 'primary', volumes: [v] }, { name: 'pg-2', role: 'replica', volumes: [{ ...v, claim: 'pg-2', clusterState: 'initializing' }] }]
  const html = render()
  expect(html).not.toContain('Restore into a new Cluster')
  expect(html).toContain('Set up backups before moving to a new Cluster')
  expect(html).toContain('Backups →')
  expect(html).toContain('xl:grid-rows-subgrid')
  expect(html.match(/--cnpg-instance-rows:5/g)).toHaveLength(2)
  expect(renderToStaticMarkup(<MemoryRouter><CNPGStorage namespace="db" name="pg" restoreState="available" /></MemoryRouter>)).toContain('Restore into a new Cluster')
  state.instances = []
})
it('explains Prometheus discovery once with candidate details folded', () => {
  state.usage = { state: 'noPrometheus', reason: 'Prometheus was not found.\nCandidate monitoring/prometheus: connection refused.' }
  const host = document.createElement('div'); host.innerHTML = render()
  expect(host.textContent!.match(/Prometheus was not found/g)).toHaveLength(1)
  expect(host.querySelector('[inert]')?.textContent).toContain('monitoring/prometheus')
  state.usage = { state: 'notRead' }
})

it('does not prescribe backup setup or restoration when the source is unread', () => {
  state.instances = [{ name: 'pg-1', role: 'primary', volumes: [{ claim: 'pg-1', role: 'PG_DATA', capacity: '1Gi', storageClass: { allowVolumeExpansion: false }, resize: {}, usage: { state: 'notRead' } }] }]
  const html = renderToStaticMarkup(<MemoryRouter><CNPGStorage namespace="db" name="pg" restoreState="unknown" /></MemoryRouter>)
  expect(html).toContain('A restore source has not been verified')
  expect(html).not.toContain('Set up backups')
  expect(html).not.toContain('Restore into a new Cluster')
  state.instances = []
})
it('keeps a configured-destination absence independent of unread instance records', () => {
  state.instances = [{ name: 'pg-1', role: 'primary', volumes: [], wal: { status: { state: 'unreachable', error: 'proxy timeout' }, metrics: { state: 'error' }, slots: [] } }]
  const html = renderToStaticMarkup(<MemoryRouter><CNPGStorage namespace="db" name="pg" walArchiving={{ state: 'no_destination' as const, text: 'Not archived: no destination configured', tone: 'neutral' }} /></MemoryRouter>)
  expect(html).toContain('Not archived: no destination configured')
  expect(html).toContain('Archiving record not read: proxy timeout')
  expect(html).not.toContain('Last archived time not reported')
  expect(html).not.toContain('wait for the archive')
  state.instances = []
})
