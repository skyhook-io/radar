import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { CNPGScreenGate, CoverageNotice } from './shared'
import { buildCNPGFleet, CNPG_WORKSPACE_KEYS, type CNPGWorkspaceResponse } from '@skyhook-io/k8s-ui'
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
it('keeps workspace data with its failed-refresh reason and age', () => {
  const query = { data: { installed: true }, isRefetchError: true, error: new Error('Workspace timeout'), dataUpdatedAt: Date.now() - 120_000 } as any
  const html = renderToStaticMarkup(<CNPGScreenGate query={query} fleet={{} as any}>{() => <span>retained Cluster</span>}</CNPGScreenGate>)
  expect(html).toContain('retained Cluster')
  expect(html).toContain('Last refresh failed: Workspace timeout')
  expect(html).toContain('2m ago')
})

it('scopes unread kinds to the screen and its namespace, with details folded', () => {
  const data: CNPGWorkspaceResponse = { installed: true, context: 'test', namespaces: null, objects: {}, coverage: { ...Object.fromEntries(CNPG_WORKSPACE_KEYS.map((k) => [k, { state: 'full' as const }])), backups: { state: 'partial', allowedNamespaces: ['prod'], deniedNamespaces: ['staging'] }, clusterImageCatalogs: { state: 'uncached' } }, issues: [], audit: [], backupsOmitted: 0, jobCoverage: { state: 'denied' } }
  const fleet = buildCNPGFleet(data)
  const html = renderToStaticMarkup(<CoverageNotice data={data} fleet={fleet} kinds={['backups', 'scheduledBackups', 'objectStores']} />)
  expect(html).toContain('Some data used on this screen was not read.')
  expect(html).toContain('Backup (no access in staging)')
  expect(html).toContain('aria-expanded="false"')
  expect(html).not.toContain('ClusterImageCatalog')
  expect(html).not.toContain('Job (')
  expect(renderToStaticMarkup(<CoverageNotice data={data} fleet={fleet} kinds={['backups']} namespace="prod" />)).toBe('')
})
