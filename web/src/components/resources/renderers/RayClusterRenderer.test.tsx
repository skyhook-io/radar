// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { RayClusterRenderer } from './RayClusterRenderer'
const mock = vi.hoisted(() => ({ result: {} as any, calls: [] as any[], canViewLogs: true, opened: [] as any[], support: 'supported' as string }))
vi.mock('../../../api/client', () => ({
  ApiError: class ApiError extends Error { constructor(message: string, public status: number) { super(message) } },
  useWorkloadPods: (...args: any[]) => { mock.calls.push(args); return mock.result },
  useCapabilities: () => ({ data: { features: { rayClusterPods: true } } }),
  isStableClientError: () => false,
  useRadarFeature: () => ({ support: mock.support, upgrade: mock.support === 'unsupported' ? { feature: 'RayCluster runtime Pods', minimumVersion: 'v1.15.0' } : null }),
}))
vi.mock('../../../contexts/CapabilitiesContext', () => ({ useNamespacedCapabilities: () => ({ canViewLogs: mock.canViewLogs }) }))
vi.mock('../../dock', () => ({ useOpenLogs: () => (opts: any) => mock.opened.push(opts) }))
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const root = { apiVersion: 'ray.io/v1', kind: 'RayCluster', metadata: { name: 'cluster', namespace: 'ml', uid: 'current' } }
const worker = { name: 'cluster-cpu-worker-def', ready: true, containers: ['wait-gcs-ready', 'ray-worker'], containerStatuses: [{ name: 'ray-worker', ready: true, restartCount: 0 }, { name: 'wait-gcs-ready', init: true, ready: true, restartCount: 0 }] }
describe('RayCluster host evidence', () => {
  beforeEach(() => { mock.result = {}; mock.calls = []; mock.canViewLogs = true; mock.opened = []; mock.support = 'supported' })
  it('makes one bounded UID-bound query with polling', () => {
    renderToStaticMarkup(<RayClusterRenderer data={root} />)
    expect(mock.calls).toEqual([['rayclusters', 'ml', 'cluster', { ownerUID: 'current', limit: 200, refetchInterval: 5000 }]])
  })
  it.each(['Forbidden', 'Cache warming', 'RayCluster was recreated'])('surfaces %s without claiming an empty cluster', message => {
    mock.result = { error: new Error(message) }
    const html = renderToStaticMarkup(<RayClusterRenderer data={root} />)
    expect(html).toContain(message); expect(html).not.toContain('No directly owned Pods')
  })
  it('opens a verified Pod in the logs dock on its application container', () => {
    mock.result = { data: { pods: [worker], total: 1, truncated: false } }
    const host = document.createElement('div')
    const view = createRoot(host)
    act(() => view.render(<RayClusterRenderer data={root} />))
    act(() => host.querySelector<HTMLButtonElement>('[aria-label="Open logs for cluster-cpu-worker-def"]')!.click())
    expect(mock.opened).toEqual([{ namespace: 'ml', podName: 'cluster-cpu-worker-def', containers: ['wait-gcs-ready', 'ray-worker'], containerName: 'ray-worker' }])
    act(() => view.unmount())
  })
  it('offers no log links without permission to read Pod logs', () => {
    mock.canViewLogs = false
    mock.result = { data: { pods: [worker], total: 1, truncated: false } }
    expect(renderToStaticMarkup(<RayClusterRenderer data={root} />)).not.toContain('Open logs for')
  })
  it('names an unsupporting Radar rather than echoing its 400 when support is unknown', async () => {
    const { ApiError } = await import('../../../api/client') as any
    mock.support = 'unknown'
    mock.result = { error: new ApiError('only deployments, statefulsets, daemonsets, rollouts, jobs, and workflows are supported', 400) }
    const html = renderToStaticMarkup(<RayClusterRenderer data={root} />)
    expect(html).toContain('Radar v1.15 and later'); expect(html).not.toContain('only deployments')
  })
  it('does not ask an older Radar for Pods it cannot list, and says which Radar can', () => {
    mock.support = 'unsupported'
    const html = renderToStaticMarkup(<RayClusterRenderer data={root} />)
    expect(mock.calls[0][2]).toBe('')
    expect(html).toContain('RayCluster runtime Pods'); expect(html).toContain('v1.15')
  })})
