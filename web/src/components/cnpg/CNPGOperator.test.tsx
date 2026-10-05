import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { buildCNPGFleet, CNPG_WORKSPACE_KEYS, type CNPGWorkspaceResponse } from '@skyhook-io/k8s-ui'
import { CNPGOperator } from './CNPGOperator'
vi.mock('./useCNPGNavigate', () => ({ useCNPGNavigate: () => vi.fn() }))
vi.mock('../../api/cnpg', () => ({ useCNPGOperator: () => ({ data: { coverage: { deployments: { state: 'full' }, services: { state: 'full' } }, components: [{ role: 'operator', namespace: 'operator', deployment: 'cnpg', readyReplicas: 1, replicas: 1 }], config: [], diagnosis: [] }, isRefetchError: true, error: new Error('Operator timeout'), dataUpdatedAt: Date.now() - 240_000 }) }))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
it('labels retained operator data after a failed refresh', () => {
  const data: CNPGWorkspaceResponse = { installed: true, context: 'test', namespaces: null, coverage: Object.fromEntries(CNPG_WORKSPACE_KEYS.map((k) => [k, { state: 'full' }])), objects: {}, issues: [], audit: [], backupsOmitted: 0 }
  const html = renderToStaticMarkup(<CNPGOperator data={data} fleet={buildCNPGFleet(data)} namespaces={[]} searchParams={new URLSearchParams()} onSetParams={() => {}} onInspect={() => {}} inspected={null} onClearNamespaces={() => {}} />)
  expect(html).toContain('CloudNativePG operator')
  expect(html).toContain('Last refresh failed: Operator timeout')
  expect(html).toContain('4m ago')
})
