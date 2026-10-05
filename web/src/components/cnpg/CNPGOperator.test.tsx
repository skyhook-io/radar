import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { buildCNPGFleet, CNPG_WORKSPACE_KEYS, type CNPGWorkspaceResponse } from '@skyhook-io/k8s-ui'
import { CNPGOperator, ConfigBlock } from './CNPGOperator'
vi.mock('./useCNPGNavigate', () => ({ useCNPGNavigate: () => vi.fn() }))
const operator = vi.hoisted(() => ({ partial: false }))
vi.mock('../../api/cnpg', () => ({ useCNPGOperator: () => ({ data: { coverage: { deployments: { state: operator.partial ? 'partial' : 'full' }, services: { state: operator.partial ? 'partial' : 'full' } }, components: operator.partial ? [] : [{ role: 'operator', namespace: 'operator', deployment: 'cnpg', readyReplicas: 1, replicas: 1 }], config: [], diagnosis: [] }, isRefetchError: true, error: new Error('Operator timeout'), dataUpdatedAt: Date.now() - 240_000 }) }))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
it('labels retained operator data after a failed refresh', () => {
  const data: CNPGWorkspaceResponse = { installed: true, context: 'test', namespaces: null, coverage: Object.fromEntries(CNPG_WORKSPACE_KEYS.map((k) => [k, { state: 'full' }])), objects: {}, issues: [], audit: [], backupsOmitted: 0 }
  const html = renderToStaticMarkup(<CNPGOperator data={data} fleet={buildCNPGFleet(data)} namespaces={[]} searchParams={new URLSearchParams()} onSetParams={() => {}} onInspect={() => {}} inspected={null} onClearNamespaces={() => {}} />)
  expect(html).toContain('CloudNativePG operator')
  expect(html).toContain('Last refresh failed: Operator timeout')
  expect(html).toContain('4m ago')
})

it('renders a known-absent ConfigMap as text and does not claim default settings', () => {
  const html = renderToStaticMarkup(<ConfigBlock config={{ kind: 'ConfigMap', namespace: 'operator', name: 'missing-settings', purpose: 'operator', readable: true, exists: false }} onInspect={vi.fn()} />)
  expect(html).toContain('This referenced ConfigMap does not exist; settings from other sources are not shown here')
  expect(html).not.toContain('<button')
  expect(html).not.toContain('defaults')
})

it('states unread operator health once and keeps a short component empty line', () => {
  operator.partial = true
  const data: CNPGWorkspaceResponse = { installed: true, context: 'test', namespaces: null, coverage: Object.fromEntries(CNPG_WORKSPACE_KEYS.map((k) => [k, { state: 'full' }])), objects: {}, issues: [], audit: [], backupsOmitted: 0 }
  const html = renderToStaticMarkup(<CNPGOperator data={data} fleet={buildCNPGFleet(data)} namespaces={[]} searchParams={new URLSearchParams()} onSetParams={() => {}} onInspect={() => {}} inspected={null} onClearNamespaces={() => {}} />)
  expect(html.match(/Operator health cannot be assessed with your access/g)).toHaveLength(1)
  expect(html).toContain('Deployments not read in some namespaces')
  expect(html).toContain('No operator or plugin Deployments found')
  expect(html).not.toContain('No operator Deployment was found')
  expect(html).not.toContain('Some workloads are not readable')
  operator.partial = false
})
