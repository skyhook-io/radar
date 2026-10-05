// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { CNPGConfiguration, CNPGDeclaredSettings } from './CNPGConfiguration'

const state = vi.hoisted(() => ({ cluster: {} as any, params: {} as any, ha: {} as any, row: undefined as any, workspace: {} as any, navigate: vi.fn() }))
vi.mock('../../api/client', () => ({ useResource: (kind: string, ns: string, name: string, group: string) => {
  expect([kind, ns, name, group]).toEqual(['clusters', 'db', 'pg', 'postgresql.cnpg.io'])
  return { data: state.cluster }
} }))
vi.mock('../../api/cnpg-inspect', () => ({ useCNPGParameters: () => state.params }))
vi.mock('./useCNPGClusterAssessment', () => ({ useCNPGClusterAssessment: () => ({ query: state.workspace, row: state.row, ha: state.ha }) }))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
vi.mock('./useCNPGNavigate', () => ({ useCNPGNavigate: () => state.navigate }))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const render = () => renderToStaticMarkup(<MemoryRouter><CNPGConfiguration namespace="db" name="pg" onNavigate={() => {}} onSelectTab={() => {}} /></MemoryRouter>)
beforeEach(() => {
  state.cluster = { apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', metadata: { namespace: 'db', name: 'pg' }, spec: { postgresql: { parameters: { shared_buffers: '256MB', work_mem: '8MB' } } }, status: { conditions: [{ type: 'Ready', status: 'False' }] } }
  state.params = { data: { state: 'denied', declared: [], permission: { exec: 'denied', grant: { verb: 'create', resource: 'pods', subresource: 'exec', namespace: 'db' } }, instances: [] } }
  state.ha = { error: new Error('HA read denied') }
  state.row = undefined
  state.workspace = {}
  state.navigate.mockClear()
})

describe('Configuration composition', () => {
  it('shows declarations when exec is denied, without duplicating status, events or certificates', () => {
    const html = render()
    for (const text of ['shared_buffers', '256MB', 'work_mem', '8MB', 'Reported by instance', 'create pods/exec', 'Certificates could not be read: HA read denied']) expect(html).toContain(text)
    for (const text of ['Conditions', 'Recent Events', 'Audit Findings', 'Cluster Overview', 'on this tab below']) expect(html).not.toContain(text)
    expect(html.match(/>Certificates</g)).toHaveLength(1)
    expect(html.indexOf('Connect')).toBeLessThan(html.indexOf('PostgreSQL parameters'))
    expect(html.indexOf('PostgreSQL parameters')).toBeLessThan(html.indexOf('Certificates'))
    expect(html.indexOf('Certificates')).toBeLessThan(html.indexOf('Labels and annotations'))
    expect(html).toContain('aria-expanded="false"')
  })
  it.each([{ isLoading: true }, { error: new Error('runtime failed') }])('keeps Cluster declarations when observations are unavailable: %j', (query) => {
    state.params = query
    const html = render()
    expect(html).toContain('shared_buffers')
    expect(html).toContain('256MB')
    expect(html).toContain('Not read:')
    expect(html).not.toContain('>In effect<')
  })
  it('names which instances answered each parameter and which restart is pending', () => {
    state.params = { data: { state: 'partial', declared: [{ name: 'shared_buffers', value: '256MB' }, { name: 'work_mem', value: '8MB' }], permission: { exec: 'allowed' }, instances: [
      { pod: 'pg-1', state: 'ok', settings: [{ name: 'shared_buffers', value: '256MB', source: 'configuration file', context: 'postmaster' }, { name: 'work_mem', value: '8MB', context: 'user' }] },
      { pod: 'pg-2', state: 'ok', settings: [{ name: 'shared_buffers', value: '128MB', source: 'configuration file', context: 'postmaster', pendingRestart: true }] },
      { pod: 'pg-3', state: 'unreachable', error: 'timeout' },
    ] } }
    const html = render()
    for (const text of ['pg-1: 256MB', 'pg-2: 128MB', 'Restart pending on pg-2', 'Not reported by pg-2', 'pg-3 (timeout)', '>restart<', 'reload']) expect(html).toContain(text)
  })
  it('shows parameters beyond the sampled set as declared and unreported', () => {
    state.cluster.spec.postgresql.parameters.extra = 'x'
    state.params = { data: { state: 'ok', declared: [{ name: 'shared_buffers', value: 'old declaration' }], permission: { exec: 'allowed' }, instances: [], omitted: 1 } }
    const html = render()
    expect(html).toContain('extra')
    expect(html).toContain('256MB')
    expect(html).not.toContain('old declaration')
    expect(html).toContain('Not sampled')
  })
  it('renders the shared certificate section once when HA answers, including the renewal owner', () => {
    state.ha = { data: { cluster: { namespace: 'db', name: 'pg' }, certificates: [{ secret: 'pg-server', raw: '2100-01-01', expiresAt: '2100-01-01T00:00:00Z', renewal: 'operator' }] } }
    const html = render()
    expect(html.match(/>Certificates</g)).toHaveLength(1)
    expect(html).toContain('pg-server')
    expect(html).toContain('CloudNativePG renews it')
    expect(html).not.toContain('Certificates could not be read')
  })
  it('keeps cached parameter observations with a refresh-failed notice', () => {
    state.params = { isRefetchError: true, dataUpdatedAt: Date.now() - 60_000, error: new Error('refresh timed out'), data: { state: 'ok', sampledAt: '2026-10-04T00:00:00Z', declared: [{ name: 'work_mem', value: '8MB' }], permission: { exec: 'allowed' }, instances: [{ pod: 'pg-1', state: 'ok', settings: [{ name: 'work_mem', value: '8MB', context: 'user' }] }] } }
    const html = render()
    expect(html).toContain('Last refresh failed: refresh timed out')
    expect(html).toContain('showing data from')
    expect(html).toContain('>8MB</span>')
    expect(html).toContain('on pg-1')
    expect(html).not.toContain('Instance values could not be read')
  })
  it('distinguishes skipped names, no readable instances and a parameter missing from a successful read', () => {
    state.cluster.spec.postgresql.parameters['invalid name'] = 'x'
    state.params = { data: { state: 'ok', declared: Object.entries(state.cluster.spec.postgresql.parameters).map(([name, value]) => ({ name, value })), skipped: ['invalid name'], permission: { exec: 'allowed' }, instances: [{ pod: 'pg-1', state: 'unreachable', error: 'timeout' }] } }
    let html = render()
    expect(html).toContain('Not read: not a parameter name')
    expect(html).toContain('Not read: no instance answered')
    state.params.data.instances = [{ pod: 'pg-1', state: 'ok', settings: [] }]
    html = render()
    expect(html).toContain('Not reported by pg-1')
  })
  it('opens Declarations scoped to this Cluster with return context', () => {
    state.cluster.spec.managed = { roles: [{ name: 'reader' }] }
    const host = document.createElement('div')
    const root = createRoot(host)
    act(() => root.render(<MemoryRouter><CNPGConfiguration namespace="db" name="pg" onNavigate={() => {}} onSelectTab={() => {}} /></MemoryRouter>))
    act(() => [...host.querySelectorAll('button')].find((b) => b.textContent === 'Declarations →')!.click())
    expect(state.navigate).toHaveBeenCalledWith('/cnpg/declarations?cluster=db%2Fpg', { state: { returnLabel: expect.any(String), returnCtx: 'test' } })
    act(() => root.unmount())
  })
  it('labels a retained workspace row when its Pooler refresh fails', () => {
    state.row = { poolersKnown: true, poolerObjects: [{ metadata: { namespace: 'db', name: 'pg-pool' }, spec: { cluster: { name: 'pg' } } }] }
    state.workspace = { isRefetchError: true, dataUpdatedAt: Date.now() - 60_000, error: new Error('workspace timed out') }
    const html = render()
    expect(html).toContain('pg-pool')
    expect(html).toContain('Last refresh failed: workspace timed out')
    expect(html).toContain('showing data from')
  })
  it('labels newly declared parameters as unsampled rather than inferring absent Pods', () => {
    state.params = { data: { state: 'ok', declared: [], instances: [], permission: { exec: 'allowed' } } }
    const html = render()
    expect(html).toContain('2 declared · no parameters sampled')
    expect(html).not.toContain('no instance Pod')
    expect(html).toContain('Not sampled')
  })
})

describe('Declared settings', () => {
  const settings = () => renderToStaticMarkup(<CNPGDeclaredSettings cluster={state.cluster} onSelectTab={() => {}} onOpenDeclarations={() => {}} />)
  it('omits empty cards and does not invent defaults from the restore preflight', () => {
    state.cluster.spec = {}
    state.cluster.status.image = 'running:17'
    expect(settings()).toBe('')
    state.cluster.spec = { instances: 2, enableSuperuserAccess: false }
    const html = settings()
    expect(html).toContain('Instances and image')
    expect(html).toContain('Disabled')
    for (const title of ['Placement and resources', 'Storage', 'Bootstrap and source', 'Backups configuration', 'Roles, services and monitoring']) expect(html).not.toContain(title)
  })
  it('omits empty storage stanzas and names PVC template size and class paths precisely', () => {
    state.cluster.spec = { storage: {}, walStorage: {}, tablespaces: [], resources: { requests: {}, limits: {} }, affinity: { tolerations: [], nodeSelector: {} }, replica: {}, monitoring: {}, managed: { roles: [], services: { additional: [], disabledDefaultServices: [] } }, plugins: [] }
    expect(settings()).toBe('')
    state.cluster.spec.storage = { pvcTemplate: { resources: { requests: { storage: '30Gi' } }, storageClassName: 'template-class' } }
    const html = settings()
    for (const text of ['30Gi', 'template-class', 'spec.storage.pvcTemplate.resources.requests.storage', 'spec.storage.pvcTemplate.storageClassName']) expect(html).toContain(text)
    expect(html).not.toContain('size from pvcTemplate')
  })
  it('shows each populated card with spec paths and links to operational evidence', () => {
    state.cluster.spec = {
      instances: 3, imageName: 'desired:17', primaryUpdateStrategy: 'supervised', primaryUpdateMethod: 'switchover', minSyncReplicas: 1, maxSyncReplicas: 2, postgresql: { synchronous: { method: 'any', number: 1, dataDurability: 'required' } },
      affinity: { enablePodAntiAffinity: true, topologyKey: 'zone', nodeSelector: { disk: 'ssd' } },
      topologySpreadConstraints: [{ topologyKey: 'zone', maxSkew: 1, whenUnsatisfiable: 'DoNotSchedule' }], resources: { requests: { cpu: '500m' }, limits: { memory: '1Gi' } }, priorityClassName: 'database',
      storage: { size: '20Gi', storageClass: 'fast' }, walStorage: { size: '5Gi', storageClass: 'wal' }, tablespaces: [{ name: 'analytics', storage: { size: '10Gi', storageClass: 'slow' } }],
      bootstrap: { initdb: { database: 'appdb', owner: 'owner' } }, replica: { enabled: false, source: 'origin' }, externalClusters: [{ name: 'origin', connectionParameters: { password: 'DO-NOT-DISPLAY' } }],
      backup: { barmanObjectStore: { destinationPath: 's3://bucket' }, retentionPolicy: '7d', volumeSnapshot: { className: 'csi' }, target: 'primary' },
      managed: { roles: [{ name: 'reader' }], services: { disabledDefaultServices: ['ro'], additional: [{ serviceTemplate: { metadata: { name: 'custom' } } }] } },
      monitoring: { enablePodMonitor: false, customQueriesConfigMap: [{ name: 'queries', key: 'sql' }], customQueriesSecret: [{ name: 'secret-queries', key: 'sql' }] },
      plugins: [{ name: 'barman-cloud.cloudnative-pg.io', isWALArchiver: true, parameters: { barmanObjectName: 'archive', serverName: 'origin-server' } }],
    }
    state.cluster.status.image = 'running:16'
    const html = settings()
    for (const title of ['Instances and image', 'Placement and resources', 'Storage', 'Bootstrap and source', 'Backups configuration', 'Roles, services and monitoring']) expect(html).toContain(title)
    for (const value of ['desired:17', 'running:16', 'status.image', 'spec.instances', 'spec.primaryUpdateMethod', 'disk=ssd', 'max skew 1', '500m', '20Gi', '5Gi', 'analytics', 'appdb', 'owner', 'origin', 's3://bucket', 'volumeSnapshot', '7d', 'archive', 'custom', 'queries', 'secret-queries', 'Declarations', 'Usage → Storage tab', 'Runs and recovery evidence → Backups tab']) expect(html).toContain(value)
    expect(html).not.toContain('DO-NOT-DISPLAY')
    for (const path of ['spec.minSyncReplicas', 'spec.maxSyncReplicas', 'spec.postgresql.synchronous.method', 'spec.backup.target', '.isWALArchiver', '.parameters.serverName']) expect(html).toContain(path)
  })
  it('links image catalogs and preserves their declared major alongside the resolved image', () => {
    state.cluster.spec = { imageCatalogRef: { kind: 'ClusterImageCatalog', name: 'catalog', major: 17 } }
    state.cluster.status.image = 'resolved:17'
    const html = settings()
    expect(html).toContain('catalog')
    expect(html).toContain('major 17')
    expect(html).toContain('resolved:17')
    expect(html).toContain('spec.imageCatalogRef')
  })
})
