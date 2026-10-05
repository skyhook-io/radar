import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { CNPGReplicationTab } from './CNPGReplicationTab'
const state = vi.hoisted(() => ({ haProps: {} as any, navigate: vi.fn() }))
vi.mock('../../api/cnpg', () => ({ useCNPGRuntime: () => ({}) }))
vi.mock('../../api/cnpg-ha', () => ({ useCNPGClusterHA: () => ({}), cnpgInstanceLive: () => undefined, cnpgInstanceLiveUnavailable: () => 'not read' }))
vi.mock('./useCNPGSidebarWorkspace', () => ({ useCNPGFleet: () => ({ fleet: { rows: [{ name: 'pg', namespace: 'db', cluster: { status: { currentPrimary: 'pg-1' } } }] } }) }))
vi.mock('./useCNPGNavigate', () => ({ useCNPGNavigate: () => state.navigate }))
vi.mock('./CNPGClusterTabs', () => ({ CNPGTabVerdict: ({ id }: { id: string }) => <span>verdict: {id}</span> }))
vi.mock('@skyhook-io/k8s-ui', async (original) => ({ ...(await original<typeof import('@skyhook-io/k8s-ui')>()), CNPGClusterHASection: (props: any) => { state.haProps = props; return null } }))
it('explains Serving on Replication and connects endpoint evidence to Reachability', () => {
  const html = renderToStaticMarkup(<CNPGReplicationTab namespace="db" name="pg" />)
  expect(html).toContain('verdict: serving')
  expect(html).toContain('verdict: replication')
  expect(state.haProps.currentPrimary).toBe('pg-1')
  state.haProps.onOpenReachability({ namespace: 'db', name: 'pg-rw' })
  expect(state.navigate).toHaveBeenCalledWith('/workload/services/db/pg-rw?tab=reachability')
})
