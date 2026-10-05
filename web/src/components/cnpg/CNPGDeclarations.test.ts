import { renderToStaticMarkup } from 'react-dom/server'
import { createElement } from 'react'
import { buildCNPGFleet, CNPG_WORKSPACE_KEYS, type CNPGWorkspaceResponse } from '@skyhook-io/k8s-ui'
import { describe, expect, it, vi } from 'vitest'
import { CNPGDeclarations, cnpgDeclarationBlocks, type DeclItem } from './CNPGDeclarations'

const item = (kind: DeclItem['kind'], pgName: string, database: string | undefined, state: DeclItem['state'] = 'applied'): DeclItem => ({
  key: `${kind}/${pgName}`,
  kind,
  pgName,
  database,
  state,
  resource: { kind, namespace: 'pg', name: pgName } as DeclItem['resource'],
  isField: false,
})

const items = [
  item('Database', 'demo_app', 'demo_app'),
  item('Publication', 'demo_app_pub', 'demo_app'),
  item('Subscription', 'demo_app_sub', 'demo_app', 'failed'),
  item('Database', 'demo_reporting', 'demo_reporting'),
  item('Publication', 'missing_pub', 'no_such_database', 'failed'),
  item('DatabaseRole', 'reader', undefined),
]

describe('cnpgDeclarationBlocks', () => {
  it('nests what lives in each database under it, undeclared databases after the declared ones, roles last', () => {
    const blocks = cnpgDeclarationBlocks(items, null)
    expect(blocks.map((b) => [b.database, b.head?.pgName, b.children.map((c) => c.pgName)])).toEqual([
      ['demo_app', 'demo_app', ['demo_app_pub', 'demo_app_sub']],
      ['demo_reporting', 'demo_reporting', []],
      ['no_such_database', undefined, ['missing_pub']],
      [undefined, undefined, ['reader']],
    ])
  })

  it('keeps a database as context for a matching item in it, and drops what does not match', () => {
    const blocks = cnpgDeclarationBlocks(items, 'failed')
    expect(blocks.map((b) => [b.database, b.head?.pgName, b.children.map((c) => c.pgName)])).toEqual([
      ['demo_app', 'demo_app', ['demo_app_sub']],
      ['no_such_database', undefined, ['missing_pub']],
    ])
  })
})

vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))

const G = 'postgresql.cnpg.io/v1'
const declaration = (cluster: string, name: string, applied?: boolean, stale = false) => ({ apiVersion: G, kind: 'Database', metadata: { name, namespace: 'pg', generation: 2 }, spec: { name, cluster: { name: cluster } }, status: { applied, observedGeneration: stale ? 1 : 2 } })

function renderDeclarations(query: string) {
  const data: CNPGWorkspaceResponse = {
    installed: true, context: 'test', namespaces: null,
    coverage: Object.fromEntries(CNPG_WORKSPACE_KEYS.map((k) => [k, { state: 'full' }])),
    objects: {
      clusters: ['a', 'b'].map((name) => ({ apiVersion: G, kind: 'Cluster', metadata: { name, namespace: 'pg' }, spec: {} })),
      databases: [declaration('a', 'failed-a', false), declaration('a', 'stale-a', true, true), declaration('b', 'failed-b1', false), declaration('b', 'failed-b2', false), declaration('b', 'pending-b')],
    }, issues: [], audit: [], backupsOmitted: 0,
  }
  return renderToStaticMarkup(createElement(CNPGDeclarations, {
    data, fleet: buildCNPGFleet(data), namespaces: [], searchParams: new URLSearchParams(query), onSetParams: () => {}, onInspect: () => {}, inspected: null, onClearNamespaces: () => {},
  }))
}

it('counts the selected cluster’s actual items before applying the state filter', () => {
  const html = renderDeclarations('cluster=pg/a&show=failed')
  expect(html).toMatch(/Not applied[^<]*<[^>]*>1</)
  expect(html).toMatch(/Pending[^<]*<[^>]*>1</)
  expect(html).toContain('failed-a')
  expect(html).not.toContain('failed-b1')
  expect(html).not.toContain('stale-a')
})

it('shows stale success as pending in the declaration list', () => {
  const html = renderDeclarations('cluster=pg/a&show=pending')
  expect(html).toContain('stale-a')
  expect(html).toContain('awaiting the operator for the current spec')
  expect(html).not.toContain('failed-a')
})
