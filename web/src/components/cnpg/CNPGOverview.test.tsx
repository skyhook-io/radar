import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { buildCNPGFleet, type CNPGKindCoverage, type CNPGWorkspaceResponse } from '@skyhook-io/k8s-ui'

vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'kind-test' } }) }))
vi.mock('./CNPGOperatorBanner', () => ({ CNPGOperatorBanner: () => null }))

const { CNPGOverview } = await import('./CNPGOverview')

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

  it('offers Create when the host can create', () => {
    expect(render(two, '', { onCreate: () => {} })).toContain('Create')
    expect(render(two)).not.toMatch(/>Create</)
  })
})
