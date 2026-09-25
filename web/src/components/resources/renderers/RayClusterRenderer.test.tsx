import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { RayClusterRenderer } from './RayClusterRenderer'
const mock = vi.hoisted(() => ({ result: {} as any, calls: [] as any[] }))
vi.mock('../../../api/client', () => ({ useWorkloadPods: (...args: any[]) => { mock.calls.push(args); return mock.result } }))
const root = { apiVersion: 'ray.io/v1', kind: 'RayCluster', metadata: { name: 'cluster', namespace: 'ml', uid: 'current' } }
describe('RayCluster host evidence', () => {
  beforeEach(() => { mock.result = {}; mock.calls = [] })
  it('makes one bounded UID-bound query with polling', () => {
    renderToStaticMarkup(<RayClusterRenderer data={root} />)
    expect(mock.calls).toEqual([['rayclusters', 'ml', 'cluster', { ownerUID: 'current', limit: 200, refetchInterval: 5000 }]])
  })
  it.each(['Forbidden', 'Cache warming', 'RayCluster was recreated'])('surfaces %s without claiming an empty cluster', message => {
    mock.result = { error: new Error(message) }
    const html = renderToStaticMarkup(<RayClusterRenderer data={root} />)
    expect(html).toContain(message); expect(html).not.toContain('No directly owned Pods')
  })
})
