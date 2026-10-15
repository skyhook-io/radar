import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { CNPGDetailPage } from './CNPGDetailPage'
const state = vi.hoisted(() => ({ extraTabs: undefined as any, support: 'supported' as 'supported' | 'unsupported' | 'unknown', refresh: undefined as undefined | (() => Promise<unknown>), refetch: vi.fn(async () => {}), specTab: undefined as any, renderStatusBadge: undefined as undefined | ((resource: any) => any) }))
vi.mock('./refresh', () => ({ refetchCNPGDetail: state.refetch }))
vi.mock('../workload/WorkloadView', () => ({ WorkloadView: ({ onRefresh, specTab, renderStatusBadge, extraTabs }: { onRefresh?: () => Promise<unknown>; extraTabs?: any; specTab?: any; renderStatusBadge?: (resource: any) => any }) => { state.extraTabs = extraTabs; state.refresh = onRefresh; state.specTab = specTab; state.renderStatusBadge = renderStatusBadge; return <div>detail</div> } }))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
vi.mock('../../api/client', () => ({ useRadarFeature: () => ({ support: state.support }) }))
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
      expect(state.renderStatusBadge).toBeTypeOf('function')
      const badge = state.renderStatusBadge!({ spec: { instances: 2 }, status: { readyInstances: 1, phase: 'Cluster in healthy state' } })
      const html = renderToStaticMarkup(badge)
      expect(html).toContain('>Phase</span>Cluster in healthy state')
      expect(html).not.toContain('Degraded')
      expect(badge.props.content).toContain('status.phase, reported by CloudNativePG: Cluster in healthy state')
      const creating = renderToStaticMarkup(state.renderStatusBadge!({ status: { phase: 'Creating a new replica' } }))
      expect(creating).toContain('Creating a new replica')
      expect(creating).not.toMatch(/amber|emerald/)
      expect(renderToStaticMarkup(state.renderStatusBadge!({ status: {} }))).toContain('Not reported')
      expect(state.specTab.render).toBeTypeOf('function')
      expect(state.specTab.lead).toBeUndefined()
    } else { expect(state.specTab).toBeUndefined(); expect(state.renderStatusBadge).toBeUndefined() }
  }
  client.clear()
})

it('keeps the ordinary detail and omits advanced tabs/configuration until advertised', () => {
  const client = new QueryClient()
  for (const support of ['unsupported', 'unknown'] as const) {
    state.support = support
    const html = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/cnpg/clusters/db/pg?ctx=test&tab=activity']}><CNPGDetailPage target={{ plural: 'clusters', group: 'postgresql.cnpg.io', namespace: 'db', name: 'pg' }} namespaces={['db']} onOpenResource={() => {}} /></MemoryRouter></QueryClientProvider>)
    expect(html).toContain('detail')
    expect(state.specTab).toBeUndefined()
    expect(state.renderStatusBadge).toBeUndefined()
    expect(state.refresh).toBeUndefined()
    expect(state.extraTabs.map((tab: any) => tab.id)).toEqual(['activity'])
    expect(state.extraTabs[0].render().props).toMatchObject({ namespace: 'db', name: 'pg' })
  }
  state.support = 'supported'
  client.clear()
})
