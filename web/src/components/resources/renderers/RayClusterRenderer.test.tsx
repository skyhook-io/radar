// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { RayClusterRenderer } from './RayClusterRenderer'
const mock = vi.hoisted(() => ({ result: {} as any, calls: [] as any[], canViewLogs: true, opened: [] as any[], support: 'supported' as string, logsUnknown: false, capsFailed: false }))
vi.mock('../../../api/client', () => ({
  ApiError: class ApiError extends Error { constructor(message: string, public status: number) { super(message) } },
  useWorkloadPods: (...args: any[]) => { mock.calls.push(args); return mock.result },
  useCapabilities: () => ({ data: mock.capsFailed ? undefined : { features: { rayClusterPods: true, podSidecarMarks: true } }, isError: mock.capsFailed }),
  isStableClientError: (e: any) => [400, 403, 404, 409].includes(e?.status),
  useRadarFeature: () => ({ support: mock.support, upgrade: mock.support === 'unsupported' ? { feature: 'RayCluster runtime Pods', minimumVersion: 'v1.15.0' } : null }),
}))
vi.mock('../../../contexts/CapabilitiesContext', () => ({ useNamespacedCapabilities: () => ({ canViewLogs: mock.canViewLogs, logsUnknown: mock.logsUnknown }) }))
vi.mock('../../dock', () => ({ useOpenLogs: () => (opts: any) => mock.opened.push(opts) }))
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const root = { apiVersion: 'ray.io/v1', kind: 'RayCluster', metadata: { name: 'cluster', namespace: 'ml', uid: 'current' } }
const worker = { name: 'cluster-cpu-worker-def', ready: true, containers: ['wait-gcs-ready', 'ray-worker'], containerStatuses: [{ name: 'ray-worker', ready: true, restartCount: 0 }, { name: 'wait-gcs-ready', init: true, ready: true, restartCount: 0 }] }
describe('RayCluster host evidence', () => {
  beforeEach(() => { mock.result = {}; mock.calls = []; mock.canViewLogs = true; mock.opened = []; mock.support = 'supported'; mock.logsUnknown = false; mock.capsFailed = false })
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
  it('opens a Pending worker in the init container it waits on', () => {
    mock.result = { data: { pods: [{ ...worker, phase: 'Pending', containerStatuses: [{ name: 'wait-gcs-ready', init: true, ready: false, restartCount: 0 }, { name: 'ray-worker', ready: false, restartCount: 0 }] }], total: 1, truncated: false } }
    const host = document.createElement('div')
    const view = createRoot(host)
    act(() => view.render(<RayClusterRenderer data={root} />))
    act(() => host.querySelector<HTMLButtonElement>('[aria-label="Open logs for cluster-cpu-worker-def"]')!.click())
    expect(mock.opened.at(-1).containerName).toBe('wait-gcs-ready')
    act(() => view.unmount())
  })
  it('keeps the last Pod list, with its Logs buttons, through a passing refresh failure', async () => {
    const { ApiError } = await import('../../../api/client') as any
    mock.result = { data: { pods: [worker], total: 1, truncated: false }, error: new ApiError('Cluster connection changed; retry the Pod request', 503) }
    const html = renderToStaticMarkup(<RayClusterRenderer data={root} />)
    expect(html).toContain('Open logs for cluster-cpu-worker-def')
    expect(html).toContain('Pod list not refreshed: Cluster connection changed')
    expect(html).not.toContain('Pod evidence unavailable')
  })
  it('offers no log links without permission to read Pod logs, and says so', () => {
    mock.canViewLogs = false
    mock.result = { data: { pods: [worker], total: 1, truncated: false } }
    const html = renderToStaticMarkup(<RayClusterRenderer data={root} />)
    expect(html).not.toContain('Open logs for'); expect(html).toContain('Pod logs need get on pods/log')
  })
  it('keeps log links while the namespace permission is unconfirmed', () => {
    mock.canViewLogs = false
    mock.logsUnknown = true
    mock.result = { data: { pods: [worker], total: 1, truncated: false } }
    expect(renderToStaticMarkup(<RayClusterRenderer data={root} />)).toContain('Open logs for')
  })
  it('keeps log links when the global permission check has failed without an answer', () => {
    mock.canViewLogs = false
    mock.capsFailed = true
    mock.result = { data: { pods: [worker], total: 1, truncated: false } }
    expect(renderToStaticMarkup(<RayClusterRenderer data={root} />)).toContain('Open logs for')
  })
  it('withholds cached Pod evidence after a definitive access denial', async () => {
    const { ApiError } = await import('../../../api/client') as any
    mock.result = { data: { pods: [worker], total: 1, truncated: false }, error: new ApiError('Reading RayCluster Pods requires get rayclusters and list pods', 403) }
    const html = renderToStaticMarkup(<RayClusterRenderer data={root} />)
    expect(html).toContain('Pod evidence unavailable')
    expect(html).not.toContain('Open logs for')
    expect(html).not.toContain('Pod list not refreshed')
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
  })
})
