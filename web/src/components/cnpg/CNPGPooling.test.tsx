import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { buildCNPGFleet, CNPG_WORKSPACE_KEYS, type CNPGWorkspaceResponse, type CNPGPoolerPressureLive } from '@skyhook-io/k8s-ui'
import { CNPGPooling } from './CNPGPooling'

const live = vi.hoisted(() => ({ pods: [] as CNPGPoolerPressureLive['pods'] }))
vi.mock('../../api/cnpg', () => ({ useCNPGPoolerRuntime: () => ({ data: { permission: { proxy: 'allowed' }, pods: live.pods } }) }))
vi.mock('../../api/cnpg-sessions', () => ({ useCNPGPoolerCapabilities: () => ({ data: { facts: { deployment: { name: 'p', state: 'ok', replicas: 2, readyReplicas: 0 } } } }) }))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
function render() {
  const data: CNPGWorkspaceResponse = { installed: true, context: 'test', namespaces: null, coverage: Object.fromEntries(CNPG_WORKSPACE_KEYS.map((k) => [k, { state: 'full' }])), objects: { poolers: [{ apiVersion: 'postgresql.cnpg.io/v1', metadata: { name: 'p', namespace: 'pg' }, spec: { cluster: { name: 'pg' }, pgbouncer: { paused: true } } }] }, issues: [], audit: [], backupsOmitted: 0 }
  return renderToStaticMarkup(<CNPGPooling data={data} fleet={buildCNPGFleet(data)} namespaces={[]} searchParams={new URLSearchParams()} onSetParams={() => {}} onInspect={() => {}} inspected={null} onClearNamespaces={() => {}} />)
}
it('does not call an empty partial read idle and names the unread Pod', () => {
  live.pods = [{ pod: 'a', state: 'ok', pools: [] }, { pod: 'b', state: 'unreachable', reason: 'timeout' }]
  const html = render()
  expect(html).toContain('No pools seen in what was read')
  expect(html).toContain('b: not read (timeout)')
  expect(html).not.toContain('Idle')
})
it('qualifies partial totals and retains readiness beside the request', () => {
  live.pods = [{ pod: 'a', state: 'partial', reason: 'pools capped', pools: [{ database: 'app', user: 'app', clWaiting: 0, svActive: 3 }] }]
  const html = render()
  expect(html).toContain('Unknown')
  expect(html).toContain('≥3')
  expect(html).toContain('pools capped')
  expect(html).toContain('Not ready')
  expect(html).toContain('Pause requested')
})
it('says idle for complete empty reads', () => {
  live.pods = [{ pod: 'a', state: 'ok', pools: [] }]
  expect(render()).toContain('Idle: no client pools open')
})
