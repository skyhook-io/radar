import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { RayClusterRenderer, rayClusterConditionTone } from './RayClusterRenderer'
const root = { apiVersion: 'ray.io/v1', kind: 'RayCluster', metadata: { name: 'cluster', namespace: 'ml', uid: 'uid', generation: 2 }, spec: { workerGroupSpecs: [{ groupName: 'zero', replicas: 0, minReplicas: 0, maxReplicas: 3, numOfHosts: 2 }] }, status: { observedGeneration: 2, conditions: [{ type: 'HeadPodReady', status: 'True' }, { type: 'RayClusterProvisioned', status: 'True' }] } }
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
