import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { CNPGReplicationTab, OtherSlots } from './CNPGReplicationTab'
const state = vi.hoisted(() => ({ haProps: {} as any, navigate: vi.fn(), runtime: undefined as any }))
vi.mock('../../api/cnpg', () => ({ useCNPGRuntime: () => ({ data: state.runtime }), useCNPGClusterCapabilities: () => ({}) }))
vi.mock('../../api/cnpg-ha', () => ({ useCNPGClusterHA: () => ({}), cnpgInstanceLive: () => undefined, cnpgInstanceLiveUnavailable: () => 'not read' }))
vi.mock('./useCNPGSidebarWorkspace', () => ({ useCNPGFleet: () => ({ fleet: { rows: [{ name: 'pg', namespace: 'db', cluster: { status: { currentPrimary: 'pg-1' } } }] } }) }))
vi.mock('./actions/CNPGInstanceActions', () => ({ CNPGInstanceActions: () => null }))
vi.mock('./useCNPGNavigate', () => ({ useCNPGNavigate: () => state.navigate }))
vi.mock('./CNPGClusterTabs', () => ({ CNPGTabVerdict: ({ id, alwaysShow }: { id: string; alwaysShow?: boolean }) => <span>verdict: {id}{alwaysShow ? " always" : ""}</span> }))
vi.mock('@skyhook-io/k8s-ui', async (original) => ({ ...(await original<typeof import('@skyhook-io/k8s-ui')>()), CNPGClusterHASection: (props: any) => { state.haProps = props; return null } }))
it('explains Serving on Replication and connects endpoint evidence to Reachability', () => {
  const html = renderToStaticMarkup(<CNPGReplicationTab namespace="db" name="pg" />)
  expect(html).toContain('verdict: serving always')
  expect(state.haProps.showReadiness).toBe(true)
  expect(html).toContain('verdict: replication')
  expect(state.haProps.currentPrimary).toBe('pg-1')
  state.haProps.onOpenReachability({ namespace: 'db', name: 'pg-rw' })
  expect(state.navigate).toHaveBeenCalledWith('/workload/services/db/pg-rw?tab=reachability')
})

it('associates a slot with a recorded expected standby while keeping unknown associations unverified', () => {
  const primary = { pod: 'orders-1', status: { state: 'ok', slots: [{ name: '_cnpg_orders_2', type: 'physical', active: false }] } } as any
  const html = renderToStaticMarkup(<OtherSlots primary={primary} instances={['orders-1']} expectedInstances={['orders-1', 'orders-2']} joiningInstances={['orders-2']} clusterObject={{}} />)
  expect(html).toContain('orders-2</span> — waiting to join')
  expect(html).not.toContain('unmanaged')
  expect(html).toContain('retained WAL not reported')
  const unverified = renderToStaticMarkup(<OtherSlots primary={primary} instances={['orders-1']} clusterObject={{}} />)
  expect(unverified).toContain('association unverified')
  expect(unverified).not.toContain('waiting to join')
  const expected = renderToStaticMarkup(<OtherSlots primary={primary} instances={['orders-1']} expectedInstances={['orders-2']} />)
  expect(expected).toContain('orders-2</span> — no instance Pod observed')
  expect(expected).not.toContain('waiting to join')
})
it('distinguishes an empty slot inventory from slots associated with standbys', () => {
  const primary = { pod: 'payments-1', status: { state: 'ok', slots: [] } } as any
  const html = renderToStaticMarkup(<OtherSlots primary={primary} instances={['payments-1']} />)
  expect(html).toContain('No replication slots on payments-1')
  expect(html).not.toContain('every slot')
  primary.status.slots = [{ name: '_cnpg_payments_2', type: 'physical' }]
  expect(renderToStaticMarkup(<OtherSlots primary={primary} instances={['payments-1', 'payments-2']} />)).toContain('The only slot belongs to a standby')
  primary.status.slotsTruncated = true
  expect(renderToStaticMarkup(<OtherSlots primary={primary} instances={['payments-1', 'payments-2']} />)).toContain('The one reported slot belongs to a standby')
})
it('says checked instead of sampled when no instance answered', () => {
  state.runtime = { permission: { proxy: 'allowed' }, sampledAt: '2026-10-05T12:00:00Z', instances: [] }
  // The composed view has its own capabilities hook; isolate its source label here.
  const html = renderToStaticMarkup(<CNPGReplicationTab namespace="db" name="pg" />)
  expect(html).toContain('No instance data yet; checked')
  expect(html).not.toContain('sampled')
  expect(html).not.toContain('catch-up measure')
  expect(html).toContain('Source: instance manager status')
  state.runtime = undefined
})

it('keeps the replication measurement explanation absent for a lone primary, and present for measured standby rows', () => {
  state.runtime = { permission: { proxy: 'allowed' }, sampledAt: '2026-10-05T12:00:00Z', instances: [{ pod: 'pg-1', role: 'primary', status: { state: 'ok', currentLsn: '0/4000' }, metrics: { state: 'ok' } }] }
  expect(renderToStaticMarkup(<CNPGReplicationTab namespace="db" name="pg" />)).not.toContain('catch-up measure')
  state.runtime.instances.push({ pod: 'pg-2', role: 'replica', status: { state: 'ok', replayLsn: '0/3000' }, metrics: { state: 'ok' } })
  expect(renderToStaticMarkup(<CNPGReplicationTab namespace="db" name="pg" />)).toContain('catch-up measure')
  state.runtime = undefined
})
