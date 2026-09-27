import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { RayJobRenderer } from './RayJobRenderer'
import { ApiError } from '../../../api/client'
const mock = vi.hoisted(() => ({ query: {} as any, reads: [] as any[] }))
vi.mock('../../../api/client', () => ({ ApiError: class extends Error { constructor(message: string, public status: number) { super(message) } }, useResource: (...args: any[]) => { mock.reads.push(args); return mock.query } }))
vi.mock('../../execution/JobSetAdmission', () => ({ KueueAdmission: () => null }))
const root = { apiVersion: 'ray.io/v1', kind: 'RayJob', metadata: { name: 'train', namespace: 'ml', uid: 'root' }, spec: {} }
const child = { apiVersion: 'batch/v1', kind: 'Job', metadata: { name: 'train', namespace: 'ml', ownerReferences: [{ apiVersion: 'ray.io/v1', kind: 'RayJob', name: 'train', uid: 'root', controller: true }] }, spec: {}, status: {} }
const render = (data: any = root) => renderToStaticMarkup(<RayJobRenderer data={data} onNavigate={() => {}} />)
describe('RayJob child reads', () => {
  beforeEach(() => { mock.query = {}; mock.reads = [] })
  it('reads the exact submitter candidate and accepts only the controller incarnation', () => {
    mock.query = { data: child }
    expect(render()).toContain('Open this Job for submitter Pods')
    expect(mock.reads).toEqual([['jobs', 'ml', 'train', 'batch']])
    expect(render({ ...root, metadata: { ...root.metadata, uid: 'recreated' } })).toContain('ownership mismatch')
  })
  it.each([
    { ...child, apiVersion: 'batch.volcano.sh/v1alpha1' },
    { ...child, metadata: { ...child.metadata, namespace: 'other' } },
    { ...child, metadata: { ...child.metadata, ownerReferences: [{ ...child.metadata.ownerReferences[0], controller: false }] } },
  ])('rejects wrong identity', data => { mock.query = { data }; expect(render()).toContain('ownership mismatch') })
  it.each(['HTTPMode', 'SidecarMode', 'InteractiveMode', 'FutureMode'])('does not read a submitter in %s', submissionMode => { render({ ...root, spec: { submissionMode } }); expect(mock.reads).toEqual([]) })
  it('does not read remote children, including selected clusters', () => { render({ ...root, spec: { managedBy: 'remote', clusterSelector: { 'ray.io/cluster': 'shared' } } }); expect(mock.reads).toEqual([]) })
  it('keeps missing and denied observations distinct, even with cached data', () => {
    mock.query = { data: child, error: new ApiError('not found', 404) }
    expect(render()).toContain('No submitter Job observed')
    mock.query.error = new ApiError('denied', 403)
    expect(render()).toContain('Submitter evidence unavailable')
    expect(render()).not.toContain('Open this Job')
  })
  it('keeps deleting submitter logs reachable without declaring a current attempt', () => {
    mock.query = { data: { ...child, metadata: { ...child.metadata, deletionTimestamp: '2026-09-27T00:00:00Z' } } }
    const html = render({ ...root, status: { jobDeploymentStatus: 'Retrying' } })
    expect(html).toContain('Deleting')
    expect(html).toContain('attempt being cleaned up')
    expect(html).toContain('Open this Job')
  })
})
