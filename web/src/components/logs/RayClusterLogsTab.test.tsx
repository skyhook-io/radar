import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const state = vi.hoisted(() => ({ support: 'supported' as string, query: {} as any, calls: [] as any[], tab: null as any }))
vi.mock('../../api/client', () => ({
  useRadarFeature: () => ({ support: state.support }),
  useWorkloadPods: (...args: any[]) => { state.calls.push(args); return state.query },
}))
vi.mock('./MultiPodLogsTab', () => ({ MultiPodLogsTab: (props: any) => { state.tab = props; return <div>pods:{props.pods.map((p: any) => p.name).join(',')}</div> } }))
vi.mock('@skyhook-io/k8s-ui', () => ({
  AlertBanner: ({ title, message }: any) => <div>{title}: {message}</div>,
  PaneLoader: ({ label }: any) => <div>{label}</div>,
}))
const { RayClusterLogsTab, isKubeRayCluster } = await import('./RayClusterLogsTab')

const history = [{ kind: 'Pod', namespace: 'ml', name: 'deleted-worker' }, { kind: 'Pod', namespace: 'ml', name: 'cluster-head' }]
const render = (uid: string | undefined = 'uid-1') => renderToStaticMarkup(
  <RayClusterLogsTab namespace="ml" name="cluster" uid={uid} historyPods={history} selectedPod={null} onSelectPod={() => {}} initialContainer={null} />,
)

describe('RayClusterLogsTab', () => {
  beforeEach(() => { state.support = 'supported'; state.query = {}; state.calls = []; state.tab = null })

  it('lists the Pods the current incarnation owns, not the ones history recalls', () => {
    state.query = { data: { pods: [{ name: 'cluster-head', ready: true, containers: [] }, { name: 'cluster-worker-new', ready: true, containers: [] }], total: 2, truncated: false } }
    const html = render()
    expect(state.calls[0]).toEqual(['rayclusters', 'ml', 'cluster', { ownerUID: 'uid-1', limit: 200, refetchInterval: 5000 }])
    expect(html).toContain('pods:cluster-head,cluster-worker-new')
    expect(html).not.toContain('deleted-worker')
    expect(state.tab.emptyMessage).toBe('This RayCluster has no Pods right now.')
  })

  it('says when the list is capped', () => {
    state.query = { data: { pods: [{ name: 'a', ready: true, containers: [] }], total: 250, truncated: true } }
    expect(render()).toContain('Showing 1 of 250 Pods, failures first')
  })

  it('falls back to the recorded Pods with the reason when the current list is denied', () => {
    state.query = { error: new Error('Reading RayCluster Pods requires get rayclusters and list pods in this namespace'), data: { pods: [{ name: 'stale', ready: true, containers: [] }], total: 1, truncated: false } }
    const html = render()
    expect(html).toContain('pods:deleted-worker,cluster-head')
    expect(html).toContain('can include deleted ones')
    expect(html).toContain('requires get rayclusters and list pods')
    expect(state.tab.emptyMessage).toBeUndefined()
  })

  it('does not ask an older Radar for a list it cannot serve', () => {
    state.support = 'unsupported'
    const html = render()
    expect(state.calls[0][2]).toBe('')
    expect(html).toContain('pods:deleted-worker,cluster-head')
    expect(html).toContain('list the Pods it owns now')
  })

  it('waits for the current list rather than flashing recorded Pods', () => {
    state.query = { isLoading: true }
    const html = render()
    expect(html).toContain('Reading runtime Pods')
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
