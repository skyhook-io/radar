import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { KueueQueueWorkloads, emptyMessage, listedFact, membershipText, uniqueRows } from './KueueQueueWorkloads'
import type { KueueQueueMembers, KueueQueueWorkload, KueueQueueWorkloadsResponse } from '../../../types/kueue-queues'
import type { ResourceReadCoverage } from '../../../types/resource-coverage'

const complete: ResourceReadCoverage = { status: 'complete', scope: { type: 'all_namespaces' }, read: { type: 'all_namespaces' }, gaps: [] }
const yours: ResourceReadCoverage = {
  status: 'complete', scope: { type: 'namespaces', namespaces: { count: 2, names: ['team-a', 'team-c'] } }, narrowedBy: ['user_access'],
  read: { type: 'namespaces', namespaces: { count: 2, names: ['team-a', 'team-c'] } }, gaps: [],
}
const partial: ResourceReadCoverage = {
  status: 'partial', scope: { type: 'namespaces', namespaces: { count: 3, names: ['team-a', 'team-b', 'team-c'] } }, narrowedBy: ['user_access'],
  read: { type: 'namespaces', namespaces: { count: 2, names: ['team-a', 'team-c'] } },
  gaps: [{ code: 'user_forbidden', namespaces: { count: 1, names: ['team-b'] } }],
}

const row = (name: string, extra: Partial<KueueQueueWorkload> = {}): KueueQueueWorkload => ({
  namespace: 'team-a', name, uid: `uid-${name}`, apiVersion: 'kueue.x-k8s.io/v1beta2', createdAt: null, deleting: false,
  membership: ['submission'], queueName: 'lq', submission: { localQueue: 'lq', clusterQueue: 'cq' }, ...extra,
})

const members = (items: KueueQueueWorkload[], coverage: ResourceReadCoverage, extra: Partial<KueueQueueMembers> = {}): KueueQueueMembers => ({
  items, matching: items.length, counts: { members: items.length, submission: items.length, reservation: 0 }, coverage, ...extra,
})

const response = (workloads: KueueQueueMembers | undefined, extra: Partial<KueueQueueWorkloadsResponse> = {}): KueueQueueWorkloadsResponse => ({
  queue: { kind: 'ClusterQueue', name: 'cq', uid: 'cq-uid' }, workloads, ...extra,
})

const render = (props: Partial<Parameters<typeof KueueQueueWorkloads>[0]>) =>
  renderToStaticMarkup(<KueueQueueWorkloads kind="ClusterQueue" queue={{ metadata: { name: 'cq' } }} {...props} />)

describe('the count Radar lists', () => {
  it('is exact when the read was complete and says whose namespaces it covers', () => {
    expect(listedFact(members([row('a')], complete), false)).toMatchObject({ text: '1' })
    expect(listedFact(members([row('a')], yours), false).source).toContain('namespaces you can see')
    expect(listedFact(members([row('a')], complete), false).source).toContain("Not the controller's count")
  })

  it('is a lower bound when the read was partial, and an unread zero is not a count', () => {
    expect(listedFact(members([row('a')], partial), false).text).toBe('≥1')
    expect(listedFact(members([], partial), false).text).toBe('None in what Radar read')
  })
})

describe('empty answers', () => {
  it('keeps confirmed empty apart from empty where Radar or you could read', () => {
    expect(emptyMessage(members([], complete), false, false)).toBe('No Workloads belong to this queue.')
    expect(emptyMessage(members([], complete), true, false)).toBe('No Workloads are submitted to this queue.')
    expect(emptyMessage(members([], yours), false, false)).toBe('No Workloads in the namespaces you can see.')
    expect(emptyMessage(members([], partial), false, false)).toBe('No Workloads found in the namespaces Radar could read.')
    expect(emptyMessage(members([], complete, { counts: { members: 3, submission: 3, reservation: 0 } }), false, true)).toBe('No Workloads match this filter.')
  })

  it('renders scenario 7 as confirmed empty only when nothing narrowed the read', () => {
    expect(render({ pages: [response(members([], complete))] })).toContain('Radar read Workloads in every namespace.')
    expect(render({ pages: [response(members([], yours))] })).not.toContain('every namespace')
  })
})

describe('membership in words', () => {
  it('names both queues when the reservation and the LocalQueue disagree', () => {
    const stale = row('w', { membership: ['reservation'], reservation: { clusterQueue: 'cq' }, submission: { localQueue: 'lq', clusterQueue: 'new-cq' } })
    expect(membershipText(stale, false, undefined, false)).toBe('Reserved here · its LocalQueue lq now points to new-cq')
    expect(membershipText(row('w', { reservation: { clusterQueue: 'old-cq' } }), true, 'cq', false)).toBe('Reserved in old-cq · this LocalQueue now points to cq')
  })

  it('says when a LocalQueue was not read rather than calling it missing', () => {
    const reserved = row('w', { membership: ['reservation'], reservation: { clusterQueue: 'cq' }, submission: undefined })
    expect(membershipText(reserved, false, undefined, true)).toBe('Reserved here · LocalQueue lq not read')
    expect(membershipText(reserved, false, undefined, false)).toBe('Reserved here · LocalQueue lq not found')
    expect(membershipText(row('w', { membership: ['submission', 'reservation'], reservation: { clusterQueue: 'cq' } }), false, undefined, false)).toBe('Submitted through lq · reserved here')
  })
})

describe('scenarios', () => {
  it('scenario 2: names the namespace you cannot list and shows its LocalQueue counts as the controller reports them', () => {
    const html = render({ pages: [response(members([row('a')], partial), {
      localQueues: { total: 2, coverage: complete, items: [
        { namespace: 'team-a', name: 'lq', reported: { pending: 1, reserving: null, admitted: 0 }, workloadsRead: true },
        { namespace: 'team-b', name: 'lq', reported: { pending: 3, reserving: 1, admitted: 1 }, workloadsRead: false, workloadsGap: 'user_forbidden' },
      ] },
    })] })
    expect(html).toContain('your permissions exclude Workloads in team-b')
    expect(html).toContain('≥1')
    expect(html).toContain('Controller reports 3 pending · 1 reserving · 1 admitted')
    expect(html).toContain("You can&#x27;t list Workloads in team-b, so its Workloads aren&#x27;t listed above.")
    expect(html).toContain('reserving not reported')
    expect(html.replace(/<[^>]+>/g, ' ')).not.toMatch(/hidden/i)
  })

  it('scenario 6: a LocalQueue whose Workloads you cannot list shows its own reported counts, not a list', () => {
    const html = renderToStaticMarkup(<KueueQueueWorkloads kind="LocalQueue" queue={{ metadata: { namespace: 'team-b', name: 'lq' }, status: { pendingWorkloads: 3, admittedWorkloads: 1 } }} pages={[{
      queue: { kind: 'LocalQueue', namespace: 'team-b', name: 'lq', uid: 'lq-uid', clusterQueue: 'cq' },
      workloadsError: { error: 'insufficient permissions to list workloads', error_code: 'user_forbidden' },
    }]} />)
    expect(html).toContain('You can&#x27;t list Workloads in team-b.')
    expect(html).toContain('The controller reports 3 pending · reserving not reported · 1 admitted for this LocalQueue.')
  })

  it('never implies admission order and says how many of the matching rows are shown', () => {
    const html = render({ pages: [response(members([row('a'), row('b')], complete, { matching: 5, next: 'x' }))], hasMore: true, onLoadMore: () => {} })
    expect(html).toContain('Showing 2 of 5 · sorted by namespace and name, not admission order')
    expect(html).toContain('Load more')
  })

  it('shows an older Radar the upgrade note instead of an error', () => {
    const html = render({ upgrade: { feature: 'Workload lists on Kueue queues' } })
    expect(html).toContain('Workload lists on Kueue queues')
  })
})

it('lists a row once when it moved between pages', () => {
  const first = response(members([row('a'), row('b')], complete))
  const second = response(members([row('b'), row('c')], complete))
  expect(uniqueRows([first, second]).map(r => r.name)).toEqual(['a', 'b', 'c'])
})
