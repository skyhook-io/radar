import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { KueueQueueWorkloads, emptyMessage, listedFact, membershipText, showingText, uniqueRows, unreadWorkloadsText } from './KueueQueueWorkloads'
import { kueueQueueRetryable } from '../../../utils/kueue-queue-workloads'
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
  membership: ['submission'], queueName: 'lq', submission: { localQueue: 'lq', localQueueUID: 'lq-uid', clusterQueue: 'cq' }, ...extra,
})

const members = (items: KueueQueueWorkload[], coverage: ResourceReadCoverage, extra: Partial<KueueQueueMembers> = {}): KueueQueueMembers => ({
  items, matching: items.length, counts: { members: items.length, submission: items.length, reservation: 0, states: {} }, coverage, ...extra,
})

const response = (workloads: KueueQueueMembers | undefined, extra: Partial<KueueQueueWorkloadsResponse> = {}): KueueQueueWorkloadsResponse => ({
  queue: { kind: 'ClusterQueue', name: 'cq', uid: 'cq-uid' }, workloads, ...extra,
})

const render = (props: Partial<Parameters<typeof KueueQueueWorkloads>[0]>) =>
  renderToStaticMarkup(<KueueQueueWorkloads kind="ClusterQueue" queue={{ metadata: { name: 'cq' } }} {...props} />)

describe('the count Radar lists', () => {
  it('is exact when the read was complete and says whose namespaces it covers', () => {
    expect(listedFact(members([row('a')], complete), false)).toMatchObject({ text: '1' })
    expect(listedFact(members([row('a')], yours), false).source).toContain('namespaces Radar shows you')
    expect(listedFact(members([row('a')], complete), false).source).toContain("Not the controller's count")
  })

  it('is a lower bound when the read was partial, and an unread zero is not a count', () => {
    expect(listedFact(members([row('a')], partial), false).text).toBe('≥1')
    expect(listedFact(members([], partial), false).text).toBe('None in what Radar read')
  })

  it('is a lower bound when LocalQueues went unread somewhere, though the Workload read was complete', () => {
    const unresolved = members([row('a')], complete, { submissionUnresolved: { count: 1, names: ['team-b'] } })
    expect(listedFact(unresolved, false).text).toBe('≥1')
    const html = render({ pages: [response(unresolved)], onMembershipChange: () => {} })
    expect(html).toContain('All (≥1)')
    expect(html).toContain('Showing 1 of ≥1')
    expect(html).toContain("Radar couldn&#x27;t read LocalQueues in team-b")
    expect(emptyMessage(members([], complete, { submissionUnresolved: { count: 1, names: ['team-b'] } }), false, false)).toBe('No Workloads found where Radar could read.')
    expect(render({ pages: [response(members([], complete, { submissionUnresolved: { extent: 'unknown' } }))] })).not.toContain('every namespace')
    expect(render({ pages: [response(members([row('a')], complete, { submissionUnresolved: { extent: 'all' } }))] })).toContain('any namespace it read Workloads in')
  })

  it('bounds the state pills too, and never shows an exact count for a lower bound', () => {
    const html = render({ pages: [response(members([row('a')], partial, { counts: { members: 1, submission: 1, reservation: 0, states: { insufficient_quota: 3, admitted: 2 } } }))], onStateChange: () => {} })
    expect(html).toContain('Waiting for quota (≥3)')
    expect(html).toContain('Admitted (≥2)')
  })
})

describe('empty answers', () => {
  it('keeps confirmed empty apart from empty where Radar or you could read', () => {
    expect(emptyMessage(members([], complete), false, false)).toBe('No Workloads belong to this queue.')
    expect(emptyMessage(members([], complete), true, false)).toBe('No Workloads are submitted to this queue.')
    expect(emptyMessage(members([], yours), false, false)).toBe('No Workloads in the namespaces Radar shows you.')
    expect(emptyMessage(members([], partial), false, false)).toBe('No Workloads found where Radar could read.')
    expect(emptyMessage(members([], complete, { counts: { members: 3, submission: 3, reservation: 0, states: {} } }), false, true)).toBe('No Workloads match this filter.')
  })

  it('renders scenario 7 as confirmed empty only when nothing narrowed the read', () => {
    expect(render({ pages: [response(members([], complete))] })).toContain('Radar read Workloads and LocalQueues in every namespace.')
    expect(render({ pages: [response(members([], yours))] })).not.toContain('every namespace')
  })
})

describe('membership in words', () => {
  it('names both queues when the reservation and the LocalQueue disagree', () => {
    const stale = row('w', { membership: ['reservation'], reservation: { clusterQueue: 'cq' }, submission: { localQueue: 'lq', localQueueUID: 'lq-uid', clusterQueue: 'new-cq' } })
    expect(membershipText(stale, false, undefined)).toBe('Reserved here · its LocalQueue lq now points to new-cq')
    expect(membershipText(row('w', { reservation: { clusterQueue: 'old-cq' } }), true, 'cq')).toBe('Reserved in old-cq · this LocalQueue now points to cq')
  })

  it('says when a LocalQueue was not read rather than calling it missing', () => {
    const reserved = row('w', { membership: ['reservation'], reservation: { clusterQueue: 'cq' }, submission: undefined })
    expect(membershipText({ ...reserved, localQueueUnread: true }, false, undefined)).toBe('Reserved here · LocalQueue lq not read')
    expect(membershipText(reserved, false, undefined)).toBe('Reserved here · LocalQueue lq not found')
    expect(membershipText(row('w', { membership: ['submission', 'reservation'], reservation: { clusterQueue: 'cq' } }), false, undefined)).toBe('Submitted through lq · reserved here')
  })

  it('does not call a finished Workload reserved, nor claim it was admitted', () => {
    const done = row('w', { membership: ['reservation'], reservation: { clusterQueue: 'cq' }, admission: { state: 'finished' }, submission: { localQueue: 'lq', localQueueUID: 'u', clusterQueue: 'cq' } })
    expect(membershipText(done, false, undefined)).toBe('Reservation recorded here · its LocalQueue lq now points to cq')
    expect(membershipText(done, true, 'cq')).toBe('Reservation recorded in cq')
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
    expect(html).toContain('Showing 2 of 5 · sorted by namespace and name, finished last; not admission order')
    expect(html).toContain('Load more')
  })

  it('shows an older Radar the upgrade note instead of an error', () => {
    const html = render({ upgrade: { feature: 'Workload lists on Kueue queues' } })
    expect(html).toContain('Workload lists on Kueue queues')
  })
})

describe('paging', () => {
  it('takes counts from the first page and never shows more rows than it says there are', () => {
    const first = response(members([row('a')], complete, { matching: 1, next: 'x', counts: { members: 1, submission: 1, reservation: 0, states: {} } }))
    const second = response(members([row('b')], complete, { matching: 9, counts: { members: 9, submission: 9, reservation: 0, states: {} } }))
    const html = render({ pages: [first, second], onRetry: () => {} })
    expect(html).toContain('Showing 2; the list changed while paging')
    expect(html).toContain('Refresh')
    expect(showingText(2, 5, false)).toBe('Showing 2 of 5')
  })

  it('stops paging when the next-page request itself failed', () => {
    const html = render({ pages: [response(members([row('a')], complete, { next: 'x' }))], hasMore: true, onLoadMore: () => {}, onRetry: () => {}, error: 'the cursor belongs to another list', nextPageError: 'the cursor belongs to another list' })
    expect(html).toContain('Could not read the next page: the cursor belongs to another list')
    expect(html).not.toContain('Load more')
    expect(html).not.toContain('Could not refresh')
    expect(html).toContain('Refresh')
  })

  it('stops paging when a later page could place members differently, though its coverage reads the same', () => {
    const first = response(members([row('a')], complete, { next: 'x' }))
    const lostLocalQueues = response(members([row('b')], complete, { submissionUnresolved: { count: 1, names: ['team-a'] } }))
    expect(render({ pages: [first, lostLocalQueues], onRetry: () => {} })).toContain('what Radar could read changed while paging')
    const otherScope = response(members([row('b')], yours))
    expect(render({ pages: [first, otherScope], onRetry: () => {} })).toContain('what Radar could read changed while paging')
  })

  it('stops paging when access changed between pages', () => {
    const first = response(members([row('a')], complete, { next: 'x' }))
    const narrowed = response(members([row('b')], partial))
    const html = render({ pages: [first, narrowed], hasMore: true, onLoadMore: () => {}, onRetry: () => {} })
    expect(html).toContain('Could not read the next page: what Radar could read changed while paging')
    expect(html).not.toContain('Load more')
  })

  it('stops paging and says so when a later page could not be read', () => {
    const first = response(members([row('a')], complete, { next: 'x' }))
    const failed = response(undefined, { workloadsError: { error: 'still loading', error_code: 'kind_sync_pending' } })
    const html = render({ pages: [first, failed], hasMore: true, onLoadMore: () => {}, onRetry: () => {} })
    expect(html).toContain('Could not read the next page: still loading')
    expect(html).not.toContain('Load more')
  })

  it('polls sooner only while a retry can complete the answer', () => {
    expect(kueueQueueRetryable(response(members([], { ...partial, gaps: [{ code: 'kind_sync_pending', namespaces: { count: 1, names: ['team-b'] } }] })))).toBe(true)
    expect(kueueQueueRetryable(response(members([], partial)))).toBe(false)
    expect(kueueQueueRetryable(response(undefined, { workloadsError: { error: 'x', error_code: 'kind_sync_pending' } }))).toBe(true)
    // Each poll would spend more permission checks: wait for the ordinary refresh.
    expect(kueueQueueRetryable(response(undefined, { workloadsError: { error: 'x', error_code: 'user_permission_check_failed' } }))).toBe(false)
  })
})

it('says why a LocalQueue\'s Workloads are missing by who can fix it', () => {
  const lq = { namespace: 'team-b', name: 'lq', reported: { pending: 1, reserving: null, admitted: null }, workloadsRead: false }
  expect(unreadWorkloadsText({ ...lq, workloadsGap: 'user_forbidden' })).toContain("You can't list Workloads in team-b")
  expect(unreadWorkloadsText({ ...lq, workloadsGap: 'radar_forbidden' })).toContain("Radar's own access")
  expect(unreadWorkloadsText({ ...lq, workloadsGap: 'kind_sync_pending' })).toBe('Workloads in team-b are still loading.')
  expect(unreadWorkloadsText({ ...lq, workloadsGap: 'user_permission_check_failed' })).toContain('refresh to check again')
})

it('lists a row once when it moved between pages', () => {
  const first = response(members([row('a'), row('b')], complete))
  const second = response(members([row('b'), row('c')], complete))
  expect(uniqueRows([first, second]).map(r => r.name)).toEqual(['a', 'b', 'c'])
  const finishedLater = response(members([row('a', { admission: { state: 'finished' } })], complete))
  expect(uniqueRows([first, finishedLater]).map(r => [r.name, r.admission?.state])).toEqual([['a', 'finished'], ['b', undefined]])
})
