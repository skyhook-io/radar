import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { RayServiceRenderer } from './RayServiceRenderer'
import { ApiError } from '../../../api/client'

const mock = vi.hoisted(() => ({ queries: {} as Record<string, any>, reads: [] as any[] }))
vi.mock('../../../api/client', () => ({
  ApiError: class extends Error { constructor(message: string, public status: number) { super(message) } },
  useResource: (...args: any[]) => { mock.reads.push(args); return mock.queries[args[2]] ?? {} },
}))
const root = { apiVersion: 'ray.io/v1', kind: 'RayService', metadata: { namespace: 'ml', name: 'serve', uid: 'root' }, status: { activeServiceStatus: { rayClusterName: 'active' }, pendingServiceStatus: { rayClusterName: 'pending' } } }
const cluster = (name: string, extra = {}) => ({ apiVersion: 'ray.io/v1', kind: 'RayCluster', metadata: { namespace: 'ml', name, generation: 1, ownerReferences: [{ controller: true, apiVersion: 'ray.io/v1', kind: 'RayService', name: 'serve', uid: 'root' }], ...extra }, status: { conditions: [{ type: 'HeadPodReady', status: 'False', reason: 'CrashLoopBackOff', message: 'Head container failed' }] } })
const render = (data = root) => renderToStaticMarkup(<RayServiceRenderer data={data} />)

describe('RayService runtime observation', () => {
  beforeEach(() => { mock.queries = {}; mock.reads = [] })
  it('makes only two exact named reads and reports direct pending failures', () => {
    mock.queries.pending = { data: cluster('pending') }
    const html = render()
    expect(mock.reads).toEqual([['rayclusters', 'ml', 'active', 'ray.io'], ['rayclusters', 'ml', 'pending', 'ray.io']])
    expect(html).toContain('Reading RayCluster status')
    expect(html).toContain('Head container failed')
  })
  it.each([403, 500, 503])('shows %s read failure instead of empty/healthy runtime', status => {
    mock.queries.pending = { error: new ApiError('Access unavailable', status), data: cluster('pending') }
    const html = render()
    expect(html).toContain('Could not read RayCluster: Access unavailable')
    expect(html).not.toContain('Head container failed')
  })
  it('uses root freshness rather than unstamped RayCluster conditions', () => {
    const data = cluster('pending')
    mock.queries.pending = { data: { ...data, status: { observedGeneration: 1, conditions: [{ type: 'HeadPodReady', status: 'True', observedGeneration: 0 }] } } }
    expect(render()).not.toContain('RayCluster status describes an earlier generation')
    mock.queries.pending.data.metadata.generation = 2
    expect(render()).toContain('RayCluster status describes an earlier generation')
  })
  it('treats missing and conditions-unreported as distinct observations', () => {
    mock.queries.active = { error: new ApiError('Missing', 404) }
    mock.queries.pending = { data: { ...cluster('pending'), status: {} } }
    const html = render()
    expect(html).toContain('may have been cleaned up')
    expect(html).toContain('Runtime conditions are not reported')
  })
  it('does not attribute foreign, recreated or incorrectly owned children', () => {
    for (const data of [
      cluster('wrong-name'), cluster('pending', { namespace: 'other' }),
      { ...cluster('pending'), apiVersion: 'foreign.io/v1' },
      cluster('pending', { ownerReferences: [{ controller: true, apiVersion: 'ray.io/v1', kind: 'RayService', name: 'serve', uid: 'old-root' }] }),
      cluster('pending', { ownerReferences: [{ controller: false, apiVersion: 'ray.io/v1', kind: 'RayService', name: 'serve', uid: 'root' }] }),
    ]) {
      mock.queries.pending = { data }
      expect(render()).toContain('Runtime status is not attributed')
      expect(render()).not.toContain('Head container failed')
    }
  })
  it('accepts the same controller identity across served owner versions', () => {
    const data = cluster('pending')
    data.metadata.ownerReferences[0].apiVersion = 'ray.io/v1alpha1'
    mock.queries.pending = { data }
    expect(render()).toContain('Head container failed')
  })
  it('follows promotion and root recreation using current slot and owner identity', () => {
    mock.queries.pending = { data: cluster('pending') }
    const promoted = { ...root, status: { activeServiceStatus: { rayClusterName: 'pending' }, pendingServiceStatus: { rayClusterName: '' } } }
    expect(render(promoted)).not.toContain('Pending Revision')
    expect(mock.reads).toEqual([['rayclusters', 'ml', 'pending', 'ray.io']])
    expect(render({ ...promoted, metadata: { ...root.metadata, uid: 'new-root' } })).toContain('Runtime status is not attributed')
  })
})
