import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, expect, it, vi } from 'vitest'
import { CNPG_WORKSPACE_KEYS, ResourcesSidebar } from '@skyhook-io/k8s-ui'
import { useCNPGFleet, useCNPGSidebarWorkspace } from './useCNPGSidebarWorkspace'
import { radarFeatureSupport } from '../../api/radarFeatures'

const state = vi.hoisted(() => ({ capabilities: { features: { resourceIssues: true } } as any, diskEnabled: [] as boolean[], metricsEnabled: [] as boolean[], result: undefined as any, workspace: undefined as any }))
const oldKinds = ['clusters', 'backups', 'scheduledBackups', 'poolers', 'databases', 'publications', 'subscriptions', 'imageCatalogs', 'clusterImageCatalogs', 'objectStores', 'pods']
const cluster = { apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', metadata: { namespace: 'db', name: 'pg' }, spec: { instances: 1 }, status: { phase: 'Cluster in healthy state', readyInstances: 1, currentPrimary: 'pg-1' } }
const workspace = { installed: true, context: 'test', namespaces: ['db'], coverage: Object.fromEntries(oldKinds.map(k => [k, { state: 'full' }])), objects: { ...Object.fromEntries(oldKinds.map(k => [k, []])), clusters: [cluster], pods: [{ apiVersion: 'v1', kind: 'Pod', metadata: { namespace: 'db', name: 'pg-1', labels: { 'cnpg.io/cluster': 'pg', 'cnpg.io/instanceRole': 'primary' } }, status: { conditions: [{ type: 'Ready', status: 'True' }] } }] }, issues: [], audit: [], backupsOmitted: 0 }
vi.mock('../../api/cnpg', () => ({ useCNPGWorkspace: () => ({ data: state.workspace ?? workspace }) }))
vi.mock('../../api/cnpg-storage', () => ({ useCNPGFleetDisk: (_ns: string[], enabled: boolean) => { state.diskEnabled.push(enabled); return { error: new Error('Live disk needs a newer Radar') } } }))
vi.mock('../../api/cnpg-history', () => ({ useCNPGFleetMetrics: (_ns: string[], enabled: boolean) => { state.metricsEnabled.push(enabled); return {} } }))
vi.mock('../../api/client', () => ({ useRadarFeature: () => ({ support: radarFeatureSupport('cnpgWorkspace', state.capabilities, 'v1.16.0') }) }))
vi.mock('./useCNPGNavigate', () => ({ useCNPGNavigate: () => vi.fn() }))
beforeEach(() => { state.capabilities = { features: { resourceIssues: true } }; state.diskEnabled = []; state.metricsEnabled = []; state.workspace = undefined })
function Host() {
  const { fleet } = useCNPGFleet(['db'])
  const sidebar = useCNPGSidebarWorkspace({ apiResources: [{ name: 'clusters', kind: 'Cluster', group: 'postgresql.cnpg.io', namespaced: true }] as any, namespaces: ['db'] })
  state.result = { fleet, sidebar }
  return <div>{fleet?.rows[0].cluster.status.phase}{sidebar?.CloudNativePG.destinations.map(d => <span key={d.id}>{d.label}</span>)}</div>
}
it('keeps the ordinary workspace destinations and CRD facts without new measurement failures', () => {
  const html = renderToStaticMarkup(<Host />)
  expect(html).toContain('Cluster in healthy state')
  expect(html).toContain('Clusters')
  expect(html).toContain('Backups')
  expect(state.diskEnabled).toEqual([false, false])
  expect(state.metricsEnabled).toEqual([false, false])
  expect(state.result.fleet.rows[0].disk).toBeUndefined()
  expect(state.result.fleet.incompleteKinds).toContain('databaseRoles')
  expect(state.result.fleet.rows[0].declarations.summary.text).toContain('DatabaseRoles not read by this Radar')
  expect(state.result.sidebar.CloudNativePG.destinations[0]).toMatchObject({ count: null, countLowerBound: true })
})
it('enables new measurements only once the server advertises their contracts', () => {
  state.capabilities = { features: { cnpgWorkspace: true } }
  renderToStaticMarkup(<Host />)
  expect(state.diskEnabled).toEqual([true, false])
  expect(state.metricsEnabled).toEqual([true, false])
  expect(state.result.fleet.rows[0].disk).toMatchObject({ text: 'Unavailable', tone: 'unknown', source: 'Disk usage could not be read: Live disk needs a newer Radar' })
})
it('the Resources sidebar reads disk and lag only on the CloudNativePG views', () => {
  state.capabilities = { features: { cnpgWorkspace: true } }
  const apiResources = [{ name: 'clusters', kind: 'Cluster', group: 'postgresql.cnpg.io', namespaced: true }] as any
  function Sidebar({ active }: { active: boolean }) {
    state.result = useCNPGSidebarWorkspace({ apiResources, namespaces: ['db'], active: active ? { screen: 'overview' } : undefined })
    return null
  }
  renderToStaticMarkup(<Sidebar active={false} />)
  expect(state.diskEnabled).toEqual([false])
  renderToStaticMarkup(<Sidebar active />)
  expect(state.diskEnabled).toEqual([false, true])
  expect(state.metricsEnabled).toEqual([false, true])
})

it('outside the CloudNativePG views the Clusters badge is a lower bound, and its zero is not none', () => {
  state.capabilities = { features: { cnpgWorkspace: true } }
  const apiResources = [{ name: 'clusters', kind: 'Cluster', group: 'postgresql.cnpg.io', namespaced: true }] as any
  const fullCoverage = Object.fromEntries(CNPG_WORKSPACE_KEYS.map((k) => [k, { state: 'full' }]))
  const clusterIssue = { id: 'i1', severity: 'warning', kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'db', name: 'pg', reason: 'CNPGNotReady', message: 'not ready' }
  function Sidebar({ active }: { active: boolean }) {
    state.result = useCNPGSidebarWorkspace({ apiResources, namespaces: ['db'], active: active ? { screen: 'overview' } : undefined })
    return null
  }
  const overview = (active: boolean) => {
    renderToStaticMarkup(<Sidebar active={active} />)
    return state.result.CloudNativePG.destinations.find((d: any) => d.id === 'overview')
  }
  const crd = [{ group: 'postgresql.cnpg.io', version: 'v1', kind: 'Cluster', name: 'clusters', namespaced: true, isCrd: true, verbs: ['list'] }] as any
  const badge = (destination: any) => {
    const html = renderToStaticMarkup(
      <ResourcesSidebar selectedKind={null} onSelectedKindChange={() => {}} apiResources={crd} resourceCounts={{ 'postgresql.cnpg.io/Cluster': 1 }} categoryWorkspaces={{ CloudNativePG: { destinations: [destination] } }} />,
    )
    return html.match(new RegExp(`>${destination.label}</span>(.*?)</button>`))?.[1] ?? ''
  }

  state.workspace = { ...workspace, coverage: fullCoverage }
  const calm = overview(false)
  expect(calm).toMatchObject({ count: null, countLowerBound: true })
  expect(calm.countTitle).toBe('None found in the workspace read. Disk use and replication lag are counted on the CloudNativePG views.')
  expect(badge(calm)).toMatch(/>–</)
  expect(overview(true)).toMatchObject({ count: 0, countLowerBound: false })

  state.workspace = { ...workspace, coverage: fullCoverage, issues: [clusterIssue] }
  const flagged = overview(false)
  expect(flagged).toMatchObject({ count: 1, countLowerBound: true })
  expect(flagged.countTitle).toBe('At least 1 clusters need attention. Disk use and replication lag are counted on the CloudNativePG views.')
  expect(badge(flagged)).toMatch(/≥(<!-- -->)?1/)
  expect(overview(true)).toMatchObject({ count: 1, countLowerBound: false })
})
