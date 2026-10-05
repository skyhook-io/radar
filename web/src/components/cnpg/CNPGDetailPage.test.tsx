import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { CNPGDetailPage } from './CNPGDetailPage'
const state = vi.hoisted(() => ({ refresh: undefined as undefined | (() => Promise<unknown>), refetch: vi.fn(async () => {}) }))
vi.mock('./refresh', () => ({ refetchCNPGDetail: state.refetch }))
vi.mock('../workload/WorkloadView', () => ({ WorkloadView: ({ onRefresh }: { onRefresh?: () => Promise<unknown> }) => { state.refresh = onRefresh; return <div>detail</div> } }))
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
