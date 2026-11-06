// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { RayClusterRenderer, rayClusterConditionTone } from './RayClusterRenderer'
const root = { apiVersion: 'ray.io/v1', kind: 'RayCluster', metadata: { name: 'cluster', namespace: 'ml', uid: 'uid', generation: 2 }, spec: { workerGroupSpecs: [{ groupName: 'zero', replicas: 0, minReplicas: 0, maxReplicas: 3, numOfHosts: 2 }] }, status: { observedGeneration: 2, conditions: [{ type: 'HeadPodReady', status: 'True' }, { type: 'RayClusterProvisioned', status: 'True' }] } }
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
describe('RayCluster detail evidence boundaries', () => {
  it('renders reconciled zeros, declared configuration and historical provisioning', () => {
    const html = renderToStaticMarkup(<RayClusterRenderer data={root} />)
    for (const text of ['Runtime Health', 'ready workers', 'Declared sizing', 'zero', 'Hosts per Replica', 'initial provisioning']) expect(html).toContain(text)
    expect(rayClusterConditionTone({ type: 'RayClusterProvisioned', status: 'True' })).toBe('unknown')
  })
  it('does not show cached Pod evidence as current when a refresh failed', () => {
    const html = renderToStaticMarkup(<RayClusterRenderer data={root} onSelectPods={() => {}} podsError="Access denied" podEvidence={{ pods: [{ name: 'STALE-POD', ready: true, containers: [] }], total: 1, truncated: false }} />)
    expect(html).toContain('Access denied'); expect(html).not.toContain('STALE-POD')
  })
  it('states selected-scope count and truncation without inventing group readiness', () => {
    const html = renderToStaticMarkup(<RayClusterRenderer data={root} onSelectPods={() => {}} selection={{ nodeType: 'worker', workerGroup: 'zero' }} podEvidence={{ pods: [{ name: 'example', ready: false, containers: [] }], total: 250, truncated: true }} />)
    expect(html).toContain('250 Pods'); expect(html).toContain('showing 1, failures first')
    expect(html).not.toContain('No directly owned Pods')
  })
})

it('distinguishes unreported counters and humanizes the API default bound', () => {
 const html = renderToStaticMarkup(<RayClusterRenderer data={{ ...root, spec: { workerGroupSpecs: [{ groupName: 'workers', maxReplicas: 2147483647 }] }, status: {} }} />)
 expect(html).toContain('Not reported'); expect(html).toContain('No limit'); expect(html).not.toContain('2147483647')
})

describe('RayCluster Pod log links', () => {
  const started = (name: string) => [{ name, ready: true, restartCount: 0 }]
  const evidence = { pods: [{ name: 'cluster-head-abc', ready: true, containers: ['ray-head'], containerStatuses: started('ray-head') }, { name: 'cluster-cpu-worker-def', ready: false, containers: ['wait-gcs-ready', 'ray-worker'], containerStatuses: started('ray-worker') }, { name: 'cluster-cpu-worker-new', ready: false, containers: ['wait-gcs-ready', 'ray-worker'] }, { name: 'cluster-cpu-worker-bound', ready: false, nodeName: 'node-a', containers: ['ray-worker'] }, { name: 'cluster-cpu-worker-evicted', ready: false, phase: 'Failed', nodeName: 'node-a', containers: ['ray-worker'] }, { name: 'cluster-cpu-worker-sidecar-only', ready: false, phase: 'Pending', nodeName: 'node-a', containers: ['proxy', 'ray-worker'], containerStatuses: [{ name: 'proxy', init: true, sidecar: true, ready: true, restartCount: 0 }] }], total: 6, truncated: false }
  it('offers logs on each verified Pod only when the host can open them', () => {
    const withLogs = renderToStaticMarkup(<RayClusterRenderer data={root} onSelectPods={() => {}} podEvidence={evidence} onOpenPodLogs={() => {}} />)
    expect(withLogs).toContain('Open logs for cluster-head-abc'); expect(withLogs).toContain('Open logs for cluster-cpu-worker-def')
    expect(withLogs).toContain('container output while the Pod exists'); expect(withLogs).toContain('/tmp/ray')
    const without = renderToStaticMarkup(<RayClusterRenderer data={root} onSelectPods={() => {}} podEvidence={evidence} />)
    expect(without).not.toContain('logs for'); expect(without).not.toContain('/tmp/ray')
  })
  it('says why a Pod with no reported container status has no logs instead of offering a button', () => {
    const html = renderToStaticMarkup(<RayClusterRenderer data={root} onSelectPods={() => {}} podEvidence={evidence} onOpenPodLogs={() => {}} />)
    expect(html).toContain('Logs for cluster-cpu-worker-new: </span>Not scheduled'); expect(html).toContain('Logs for cluster-cpu-worker-bound: </span>No status yet'); expect(html).toContain('Logs for cluster-cpu-worker-evicted: </span>Ended before starting'); expect(html).toContain('Logs for cluster-cpu-worker-sidecar-only: </span>Waiting to start')
    expect(html).not.toContain('Open logs for cluster-cpu-worker-new'); expect(html).not.toContain('Open logs for cluster-cpu-worker-bound')
  })
  it('opens the clicked row\'s Pod', () => {
    const opened: string[] = []
    const host = document.createElement('div')
    const view = createRoot(host)
    act(() => view.render(<RayClusterRenderer data={root} onSelectPods={() => {}} podEvidence={evidence} onOpenPodLogs={pod => opened.push(pod.name)} />))
    act(() => host.querySelector<HTMLButtonElement>('[aria-label="Open logs for cluster-cpu-worker-def"]')!.click())
    expect(opened).toEqual(['cluster-cpu-worker-def'])
    act(() => view.unmount())
  })
  it('offers no log links when the Pod list could not be read', () => {
    const html = renderToStaticMarkup(<RayClusterRenderer data={root} onSelectPods={() => {}} podsError="Forbidden" podEvidence={evidence} onOpenPodLogs={() => {}} />)
    expect(html).not.toContain('Open logs for')
  })
  it('asks for a newer Radar instead of showing an unsupported-route error', () => {
    const html = renderToStaticMarkup(<RayClusterRenderer data={root} onSelectPods={() => {}} podsUpgrade={{ feature: 'RayCluster runtime Pods', minimumVersion: 'v1.15.0', currentVersion: 'v1.14.0' }} podsError="only deployments are supported" />)
    expect(html).toContain('RayCluster runtime Pods'); expect(html).toContain('v1.15')
    expect(html).not.toContain('only deployments are supported')
  })
})
