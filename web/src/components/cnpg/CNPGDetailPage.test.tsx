import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { CNPGDetailPage } from './CNPGDetailPage'
const state = vi.hoisted(() => ({ refresh: undefined as undefined | (() => Promise<unknown>), refetch: vi.fn(async () => {}), specTab: undefined as any }))
vi.mock('./refresh', () => ({ refetchCNPGDetail: state.refetch }))
vi.mock('../workload/WorkloadView', () => ({ WorkloadView: ({ onRefresh, specTab }: { onRefresh?: () => Promise<unknown>; specTab?: any }) => { state.refresh = onRefresh; state.specTab = specTab; return <div>detail</div> } }))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
vi.mock('../../api/client', () => ({ useRadarFeature: () => ({ support: 'supported' }) }))
vi.mock('./CNPGOperatorBanner', () => ({ CNPGOperatorBanner: () => null }))
it('passes the detail target to the awaited CNPG refresh hook', async () => {
  const client = new QueryClient()
  const target = { plural: 'clusters' as const, namespace: 'db', name: 'pg', group: 'postgresql.cnpg.io' }
  renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/cnpg/clusters/db/pg?ctx=test']}><CNPGDetailPage target={target} namespaces={['db']} onOpenResource={() => {}} /></MemoryRouter></QueryClientProvider>)
  expect(state.refresh).toBeTypeOf('function')
  await state.refresh!()
  expect(state.refetch).toHaveBeenCalledWith(client, target)
  client.clear()
})
it('replaces only the CNPG Cluster Configuration body, retaining the spec tab identity', () => {
  const client = new QueryClient()
  for (const target of [
    { plural: 'clusters' as const, group: 'postgresql.cnpg.io' },
    { plural: 'backups' as const, group: 'postgresql.cnpg.io' },
    { plural: 'clusters' as const, group: 'cluster.x-k8s.io' },
  ]) {
    renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/cnpg/clusters/db/pg?ctx=test&tab=spec']}><CNPGDetailPage target={{ ...target, namespace: 'db', name: 'pg' }} namespaces={['db']} onOpenResource={() => {}} /></MemoryRouter></QueryClientProvider>)
    if (target.plural === 'clusters' && target.group === 'postgresql.cnpg.io') {
      expect(state.specTab.label).toBe('Configuration')
      expect(state.specTab.render).toBeTypeOf('function')
      expect(state.specTab.lead).toBeUndefined()
    } else expect(state.specTab).toBeUndefined()
  }
  client.clear()
})
