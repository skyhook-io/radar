import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { buildCNPGFleet, CNPG_WORKSPACE_KEYS, type CNPGKindCoverage, type CNPGWorkspaceResponse } from '@skyhook-io/k8s-ui'

const sidebar = vi.hoisted(() => ({ data: undefined as CNPGWorkspaceResponse | undefined }))
vi.mock('../../api/cnpg', () => ({ useCNPGWorkspace: () => ({ data: sidebar.data }) }))
vi.mock('../../api/cnpg-storage', () => ({ useCNPGFleetDisk: () => ({}) }))
vi.mock('../../api/cnpg-history', () => ({ useCNPGFleetMetrics: () => ({}) }))
vi.mock('../../api/client', () => ({ useRadarFeature: () => ({ support: 'supported' }) }))

vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'kind-test' } }) }))
vi.mock('./CNPGOperatorBanner', () => ({ CNPGOperatorBanner: () => null }))

const { CNPGOverview } = await import('./CNPGOverview')
const { useCNPGSidebarWorkspace } = await import('./useCNPGSidebarWorkspace')

const cluster = (namespace: string, name: string) => ({
  apiVersion: 'postgresql.cnpg.io/v1',
  kind: 'Cluster',
  metadata: { namespace, name },
  spec: { instances: 1 },
  status: { phase: 'Cluster in healthy state', readyInstances: 1 },
})

function response(clusters: ReturnType<typeof cluster>[], coverage: CNPGKindCoverage = { state: 'full' }): CNPGWorkspaceResponse {
  return { installed: true, context: 'kind-test', namespaces: null, coverage: { clusters: coverage }, objects: { clusters }, issues: [], audit: [], backupsOmitted: 0 }
}

function render(resp: CNPGWorkspaceResponse, query = '', opts: { attention?: string[]; onCreate?: () => void } = {}) {
  const fleet = buildCNPGFleet(resp)
  for (const row of fleet.rows) row.attention = !!opts.attention?.includes(row.name)
  fleet.attentionCount = fleet.rows.filter((r) => r.attention).length
  return renderToStaticMarkup(
    <MemoryRouter>
      <CNPGOverview
        data={resp}
        fleet={fleet}
        namespaces={[]}
        searchParams={new URLSearchParams(query)}
        onSetParams={() => {}}
        onInspect={() => {}}
        inspected={null}
        onClearNamespaces={() => {}}
        onCreate={opts.onCreate}
      />
    </MemoryRouter>,
  )
}

describe('CNPGOverview is the list of clusters', () => {
  const two = response([cluster('pg', 'pg-bad'), cluster('pg', 'pg-good')])

  it('lists every cluster unless asked for less, healthy ones included', () => {
    const html = render(two, '', { attention: ['pg-bad'] })
    expect(html).toContain('pg-bad')
    expect(html).toContain('pg-good')
    expect(html).toMatch(/aria-selected="true"[^>]*>All clusters/)
  })

  it('narrows to the clusters that need attention when asked', () => {
    const html = render(two, 'filter=attention', { attention: ['pg-bad'] })
    expect(html).toContain('pg-bad')
    expect(html).not.toContain('pg-good')
  })

  it('counts a partly read list as a floor', () => {
    const html = render(response([cluster('a', 'pg-a')], { state: 'partial', allowedNamespaces: ['a'] }))
    expect(html).toContain('≥1 PostgreSQL cluster')
  })

  it('says what was not read instead of claiming there are no clusters', () => {
    const html = render(response([], { state: 'uncached', uncachedNamespaces: ['c'] }))
    expect(html).toContain('No visible PostgreSQL clusters')
    expect(html).toContain('Radar does not cache PostgreSQL clusters in c')
    expect(html).not.toContain('No PostgreSQL clusters in kind-test')
  })

  it('says once, for the whole list, why no volume usage is measured', () => {
    const resp = response([cluster('pg', 'pg-a'), cluster('pg', 'pg-b')])
    const fleet = buildCNPGFleet(resp)
    for (const row of fleet.rows) row.disk = { text: 'not measured', tone: 'unknown', source: 'no claims owned by this cluster' }
    const html = renderToStaticMarkup(
      <MemoryRouter>
        <CNPGOverview data={resp} fleet={fleet} namespaces={[]} searchParams={new URLSearchParams()} onSetParams={() => {}} onInspect={() => {}} inspected={null} onClearNamespaces={() => {}} />
      </MemoryRouter>,
    )
    expect(html).toContain('not measured for any cluster here (no claims owned by them)')
  })

  it('offers Create when the host can create', () => {
    expect(render(two, '', { onCreate: () => {} })).toContain('Create')
    expect(render(two)).not.toMatch(/>Create</)
  })
})

it('qualifies heading, attention filter and category counts on both fleet routes', () => {
  const resp = response([cluster('prod', 'orders')], { state: 'partial', allowedNamespaces: ['prod'] })
  const fleet = buildCNPGFleet(resp)
  fleet.attentionCount = 1; fleet.categoryCounts.availability = 1
  const html = renderToStaticMarkup(<MemoryRouter><CNPGOverview data={resp} fleet={fleet} namespaces={[]} searchParams={new URLSearchParams()} onSetParams={() => {}} onInspect={() => {}} inspected={null} onClearNamespaces={() => {}} /></MemoryRouter>)
  expect(html).toContain('≥1 need attention')
  expect(html).toMatch(/Needs attention<span[^>]*>≥1</)
  expect(html).toMatch(/Availability<span[^>]*>≥1</)
  expect(render(resp)).toMatch(/Needs attention<span[^>]*>Unknown</)
  expect(render(resp)).toContain('Needs attention: unknown')
  expect(render(resp)).not.toContain('Unknown need attention')
})

it('does not claim an empty partial attention filter proves no clusters need attention', () => {
  const html = render(response([cluster('prod', 'orders')], { state: 'partial', allowedNamespaces: ['prod'] }), 'filter=attention')
  expect(html).toContain('No attention findings in the readable data; other data was not read.')
  expect(html).not.toContain('No clusters need attention.')
})

it('uses an unknown sidebar count and tooltip for zero over partial coverage', () => {
  sidebar.data = { installed: true, context: 'kind-test', namespaces: null, coverage: Object.fromEntries(CNPG_WORKSPACE_KEYS.map((k) => [k, { state: 'full' }])), objects: {}, issues: [], audit: [], backupsOmitted: 0 }
  const result = { value: undefined as ReturnType<typeof useCNPGSidebarWorkspace> }
  function Probe() {
    result.value = useCNPGSidebarWorkspace({ apiResources: [{ group: 'postgresql.cnpg.io' }] as any, namespaces: [] })
    return null
  }
  sidebar.data.coverage.clusters = { state: 'partial' }
  renderToStaticMarkup(<MemoryRouter><Probe /></MemoryRouter>)
  for (const d of result.value!.CloudNativePG.destinations.filter((d) => d.id !== 'operator')) {
    expect(d.count).toBeNull()
    expect(d.countLowerBound).toBe(true)
    expect(d.countTitle).toBe('Unknown: some CloudNativePG data could not be read.')
    expect(d.countTitle).not.toContain('At least 0')
  }
  sidebar.data.coverage.clusters = { state: 'full' }
  renderToStaticMarkup(<MemoryRouter><Probe /></MemoryRouter>)
  expect(result.value!.CloudNativePG.destinations.find((d) => d.id === 'overview')!.count).toBe(0)
  sidebar.data = undefined
})
