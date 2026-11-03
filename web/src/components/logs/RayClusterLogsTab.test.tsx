import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const state = vi.hoisted(() => ({ support: 'supported' as string, query: {} as any, calls: [] as any[], tab: null as any, flag: true, canViewLogs: true, capsFailed: false, capsData: true }))
vi.mock('../../contexts/CapabilitiesContext', () => ({ useNamespacedCapabilities: () => ({ canViewLogs: state.canViewLogs, logsUnknown: false }) }))
vi.mock('../../api/client', async (original) => {
  const actual = await original<typeof import('../../api/client')>()
  return {
    ...actual,
    useCapabilities: () => ({ data: state.capsData ? { features: { rayClusterPods: true, podSidecarMarks: state.flag } } : undefined, isError: state.capsFailed }),
    useRadarFeature: () => ({ support: state.support, upgrade: state.support === 'unsupported' ? { feature: 'RayCluster runtime Pods', minimumVersion: 'v1.15.0' } : null }),
    useWorkloadPods: (...args: any[]) => { state.calls.push(args); return state.query },
  }
})
vi.mock('./MultiPodLogsTab', () => ({ MultiPodLogsTab: (props: any) => { state.tab = props; return <div>pods:{props.pods.map((p: any) => p.name).join(',')}</div> } }))
vi.mock('@skyhook-io/k8s-ui', () => ({
  AlertBanner: ({ title, message }: any) => <div>{title}: {message}</div>,
  PaneLoader: ({ label }: any) => <div>{label}</div>,
  RadarUpgradeNote: ({ requirement }: any) => <div>upgrade:{requirement.feature} {requirement.minimumVersion}</div>,
}))
const { ApiError } = await import('../../api/client')
const { RayClusterLogsTab } = await import('./RayClusterLogsTab')
const { isKubeRayCluster } = await import('./rayClusterPods')

const history = [{ kind: 'Pod', namespace: 'ml', name: 'deleted-worker' }, { kind: 'Pod', namespace: 'ml', name: 'cluster-head' }]
const started = (name: string, init = false, ready = true) => ({ name, init, ready, restartCount: 0 })
const owned = { pods: [
  { name: 'cluster-worker-b', ready: false, phase: 'Pending', containers: ['wait-gcs-ready', 'ray-worker'], containerStatuses: [started('wait-gcs-ready', true, false), started('ray-worker', false, false)] },
  { name: 'cluster-head', ready: true, containers: ['ray-head'], containerStatuses: [started('ray-head')] },
], total: 2, truncated: false }
const render = (uid: string | undefined = 'uid-1', selectedPod: string | null = null) => renderUID(uid, selectedPod)
const renderUID = (uid: string | undefined, selectedPod: string | null = null, root: { loading?: boolean; error?: Error | null } = {}) => renderToStaticMarkup(
  <RayClusterLogsTab namespace="ml" name="cluster" uid={uid} suspendRequested={false} rootLoading={root.loading ?? false} rootError={root.error ?? null} historyPods={history} selectedPod={selectedPod} onSelectPod={() => {}} initialContainer={null} />,
)

describe('RayClusterLogsTab', () => {
  beforeEach(() => { state.support = 'supported'; state.query = {}; state.calls = []; state.tab = null; state.flag = true; state.canViewLogs = true; state.capsFailed = false; state.capsData = true })

  it('lists the Pods the current incarnation owns, by name, not the ones history recalls', () => {
    state.query = { data: owned }
    const html = render()
    expect(state.calls[0]).toEqual(['rayclusters', 'ml', 'cluster', { ownerUID: 'uid-1', limit: 200, refetchInterval: 5000 }])
    expect(html).toContain('pods:cluster-head,cluster-worker-b')
    expect(html).not.toContain('deleted-worker')
    expect(state.tab.emptyMessage).toBe('This RayCluster has no Pods right now.')
    expect(state.tab.defaultPod).toBe('cluster-worker-b')
  })

  it('opens a selected worker that waits on the head in its init container', () => {
    state.query = { data: owned }
    render('uid-1', 'cluster-worker-b')
    expect(state.tab.initialContainer).toBe('wait-gcs-ready')
  })

  it('says when the list is capped and how to reach the rest', () => {
    state.query = { data: { ...owned, total: 250, truncated: true } }
    const html = render()
    expect(html).toContain('Showing the 2 least healthy of 250 Pods')
    expect(html).toContain('Runtime Pods on Overview can narrow to the head or one worker group (each view is capped at 200)')
  })

  it('falls back to the recorded Pods with the reason when the current list is denied', () => {
    state.query = { error: new ApiError('Reading RayCluster Pods requires get rayclusters and list pods in this namespace', 403), data: owned }
    const html = render()
    expect(html).toContain('pods:deleted-worker,cluster-head')
    expect(html).toContain('can include deleted ones')
    expect(html).toContain('requires get rayclusters and list pods')
    expect(state.tab.emptyMessage).toBe('Radar hasn\'t recorded any Pods for this RayCluster.')
  })

  it('keeps the last list through a passing refresh failure', () => {
    state.query = { error: new ApiError('Cluster connection changed; retry the Pod request', 503), data: owned }
    const html = render()
    expect(html).toContain('pods:cluster-head,cluster-worker-b')
    expect(html).not.toContain('Current Pods unavailable')
    expect(html).toContain('Pod list not refreshed: Showing the last list Radar read. Cluster connection changed')
  })

  it('asks for a newer Radar instead of requesting a list it cannot serve', () => {
    state.support = 'unsupported'
    const html = render()
    expect(state.calls[0][2]).toBe('')
    expect(html).toContain('upgrade:RayCluster runtime Pods v1.15.0')
    expect(html).toContain('Showing recorded Pods: ')
    expect(html).toContain('pods:deleted-worker,cluster-head')
  })

  it.each([[{ isLoading: true }, 'uid-1', false], [{}, undefined, true]])('waits rather than flashing recorded Pods (%#)', (query, uid, rootLoading) => {
    state.query = query
    expect(renderUID(uid as string | undefined, null, { loading: rootLoading as boolean })).toContain(uid ? 'Reading runtime Pods' : 'Reading RayCluster')
    expect(state.tab).toBeNull()
  })

  it('falls back to the recorded Pods when the RayCluster itself could not be read', () => {
    const html = renderUID(undefined, null, { error: new Error('rayclusters.ray.io "cluster" not found') })
    expect(html).toContain('pods:deleted-worker,cluster-head')
    expect(html).toContain('not found')
    expect(html).not.toContain('Reading runtime Pods')
  })

  it('names an unsupporting Radar rather than echoing its 400 when support is unknown', () => {
    state.support = 'unknown'
    state.query = { error: new ApiError('only deployments, statefulsets, daemonsets, rollouts, jobs, and workflows are supported', 400) }
    const html = render()
    expect(html).toContain('Radar v1.15 and later')
    expect(html).not.toContain('only deployments')
  })

  it('passes a current Radar\'s own 400 through even when its version is unknown', () => {
    state.support = 'unknown'
    state.query = { error: new ApiError('Expected ray.io/v1 RayCluster', 400) }
    const html = render()
    expect(html).toContain('Expected ray.io/v1 RayCluster')
    expect(html).not.toContain('Radar v1.15 and later')
  })

  it('still reports a known denial when only a capabilities refetch failed', () => {
    state.canViewLogs = false
    state.capsFailed = true
    state.query = { data: owned }
    expect(render()).toContain('which you don&#x27;t have')
  })

  it('does not claim a missing permission when capabilities could not be read', () => {
    state.canViewLogs = false
    state.capsFailed = true
    state.capsData = false
    state.query = { data: owned }
    const html = render()
    expect(html).toContain('hasn&#x27;t confirmed whether you can read Pod logs')
    expect(html).not.toContain('which you don&#x27;t have')
  })

  it('shows why a selected Pod has no output instead of an empty viewer', () => {
    state.query = { data: { pods: [{ name: 'pending-worker', ready: false, containers: ['ray-worker'] }], total: 1, truncated: false } }
    render('uid-1', 'pending-worker')
    expect(state.tab.selectedPodNote).toBe('Not scheduled')
  })

  it('says once, up front, when Pod logs are not readable here, and fetches none', () => {
    state.canViewLogs = false
    state.query = { data: owned }
    expect(render()).toContain('needs get on pods/log')
    expect(state.tab.selectedPodNote).toBe('You can\'t read Pod logs in this namespace.')
  })

  it('says what happened when an older Radar has no recorded Pods either', () => {
    state.support = 'unsupported'
    const html = renderToStaticMarkup(<RayClusterLogsTab namespace="ml" name="cluster" uid="uid-1" suspendRequested={false} rootLoading={false} rootError={null} historyPods={[]} selectedPod={null} onSelectPod={() => {}} initialContainer={null} />)
    expect(state.tab.emptyMessage).toBe('Radar hasn\'t recorded any Pods for this RayCluster.')
    expect(html).not.toContain('Showing recorded Pods')
  })

  it('names a requested suspension when the cluster has no Pods', () => {
    state.query = { data: { pods: [], total: 0, truncated: false } }
    renderToStaticMarkup(<RayClusterLogsTab namespace="ml" name="cluster" uid="uid-1" suspendRequested rootLoading={false} rootError={null} historyPods={history} selectedPod={null} onSelectPod={() => {}} initialContainer={null} />)
    expect(state.tab.emptyMessage).toBe('This RayCluster has no Pods right now. Its suspension is requested.')
  })

  it('applies only to the KubeRay ray.io/v1 RayCluster', () => {
    expect(isKubeRayCluster('RayCluster', 'ray.io', 'ray.io/v1')).toBe(true)
    expect(isKubeRayCluster('RayCluster', 'ray.io', undefined)).toBe(true)
    expect(isKubeRayCluster('RayCluster', 'ray.io', 'ray.io/v1alpha1')).toBe(false)
    expect(isKubeRayCluster('RayCluster', 'example.io', 'example.io/v1')).toBe(false)
    expect(isKubeRayCluster('RayJob', 'ray.io', 'ray.io/v1')).toBe(false)
  })
})
