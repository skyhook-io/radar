import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const state = vi.hoisted(() => ({ support: 'supported' as string, query: {} as any, calls: [] as any[], tab: null as any }))
vi.mock('../../api/client', () => {
  class ApiError extends Error { constructor(message: string, public status: number) { super(message) } }
  return {
    ApiError,
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
const { ApiError } = await import('../../api/client') as any
const { RayClusterLogsTab, isKubeRayCluster } = await import('./RayClusterLogsTab')

const history = [{ kind: 'Pod', namespace: 'ml', name: 'deleted-worker' }, { kind: 'Pod', namespace: 'ml', name: 'cluster-head' }]
const started = (name: string, init = false, ready = true) => ({ name, init, ready, restartCount: 0 })
const owned = { pods: [
  { name: 'cluster-worker-b', ready: false, containers: ['wait-gcs-ready', 'ray-worker'], containerStatuses: [started('wait-gcs-ready', true, false), started('ray-worker', false, false)] },
  { name: 'cluster-head', ready: true, containers: ['ray-head'], containerStatuses: [started('ray-head')] },
], total: 2, truncated: false }
const render = (uid: string | undefined = 'uid-1', selectedPod: string | null = null) => renderUID(uid, selectedPod)
const renderUID = (uid: string | undefined, selectedPod: string | null = null) => renderToStaticMarkup(
  <RayClusterLogsTab namespace="ml" name="cluster" uid={uid} historyPods={history} selectedPod={selectedPod} onSelectPod={() => {}} initialContainer={null} />,
)

describe('RayClusterLogsTab', () => {
  beforeEach(() => { state.support = 'supported'; state.query = {}; state.calls = []; state.tab = null })

  it('lists the Pods the current incarnation owns, by name, not the ones history recalls', () => {
    state.query = { data: owned }
    const html = render()
    expect(state.calls[0]).toEqual(['rayclusters', 'ml', 'cluster', { ownerUID: 'uid-1', limit: 200, refetchInterval: 5000 }])
    expect(html).toContain('pods:cluster-head,cluster-worker-b')
    expect(html).not.toContain('deleted-worker')
    expect(state.tab.emptyMessage).toBe('This RayCluster has no Pods right now.')
  })

  it('opens a selected worker that waits on the head in its init container', () => {
    state.query = { data: owned }
    render('uid-1', 'cluster-worker-b')
    expect(state.tab.initialContainer).toBe('wait-gcs-ready')
  })

  it('says when the list is capped and how to reach the rest', () => {
    state.query = { data: { ...owned, total: 250, truncated: true } }
    const html = render()
    expect(html).toContain('Showing 2 of 250 Pods, failures first')
    expect(html).toContain('choose its worker group under Runtime Pods on Overview')
  })

  it('falls back to the recorded Pods with the reason when the current list is denied', () => {
    state.query = { error: new ApiError('Reading RayCluster Pods requires get rayclusters and list pods in this namespace', 403), data: owned }
    const html = render()
    expect(html).toContain('pods:deleted-worker,cluster-head')
    expect(html).toContain('can include deleted ones')
    expect(html).toContain('requires get rayclusters and list pods')
    expect(state.tab.emptyMessage).toBeUndefined()
  })

  it('keeps the last list through a passing refresh failure', () => {
    state.query = { error: new ApiError('Cluster connection changed; retry the Pod request', 503), data: owned }
    const html = render()
    expect(html).toContain('pods:cluster-head,cluster-worker-b')
    expect(html).not.toContain('Current Pods unavailable')
  })

  it('asks for a newer Radar instead of requesting a list it cannot serve', () => {
    state.support = 'unsupported'
    const html = render()
    expect(state.calls[0][2]).toBe('')
    expect(html).toContain('upgrade:RayCluster runtime Pods v1.15.0')
    expect(html).toContain('pods:deleted-worker,cluster-head')
  })

  it.each([[{ isLoading: true }, 'uid-1'], [{}, undefined]])('waits rather than flashing recorded Pods (%#)', (query, uid) => {
    state.query = query
    expect(renderUID(uid as string | undefined)).toContain('Reading runtime Pods')
    expect(state.tab).toBeNull()
  })

  it('applies only to the KubeRay ray.io/v1 RayCluster', () => {
    expect(isKubeRayCluster('RayCluster', 'ray.io', 'ray.io/v1')).toBe(true)
    expect(isKubeRayCluster('RayCluster', 'ray.io', undefined)).toBe(true)
    expect(isKubeRayCluster('RayCluster', 'ray.io', 'ray.io/v1alpha1')).toBe(false)
    expect(isKubeRayCluster('RayCluster', 'example.io', 'example.io/v1')).toBe(false)
    expect(isKubeRayCluster('RayJob', 'ray.io', 'ray.io/v1')).toBe(false)
  })
})
