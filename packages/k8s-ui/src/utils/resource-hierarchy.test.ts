import { describe, it, expect } from 'vitest'

import type { TimelineEvent, Topology } from '../types/core'

import { buildResourceHierarchy, extractPinnedLanes, removePinnedLanes, isPinnedLaneRef, laneTrackEvents, laneHasEventInWindow, isChildVisibleInWindow, subtreeEvents, getAllEventsFromHierarchy, collidingLaneKeys, laneCollisionKey, type ResourceLane } from './resource-hierarchy'
import type { AppMembershipIndex, AppMembership } from './applications'

function svcEvent(namespace: string, name: string): TimelineEvent {
  return {
    id: `${namespace}/${name}`,
    timestamp: '2024-01-01T00:00:00.000Z',
    source: 'informer',
    kind: 'Service',
    namespace,
    name,
    eventType: 'update',
  }
}

function svcNode(namespace: string, name: string, app: string) {
  return {
    id: `service/${namespace}/${name}`,
    data: { apiVersion: 'v1', labels: { 'app.kubernetes.io/name': app } },
  }
}

function topo(nodes: ReturnType<typeof svcNode>[]): Topology {
  return { nodes, edges: [] } as unknown as Topology
}

describe('buildResourceHierarchy app-label grouping', () => {
  it('does not merge the same app label across namespaces', () => {
    const events = [svcEvent('team-a', 'web'), svcEvent('team-b', 'web')]
    const topology = topo([svcNode('team-a', 'web', 'web'), svcNode('team-b', 'web', 'web')])

    const lanes = buildResourceHierarchy({ events, topology, groupByApp: true })

    expect(lanes).toHaveLength(2)
    expect(lanes.every((l) => (l.children ?? []).length === 0)).toBe(true)
    expect(new Set(lanes.map((l) => l.namespace))).toEqual(new Set(['team-a', 'team-b']))
  })

  it('groups distinct resources sharing an app label within one namespace', () => {
    const events = [svcEvent('team-a', 'web'), svcEvent('team-a', 'web-edge')]
    const topology = topo([svcNode('team-a', 'web', 'web'), svcNode('team-a', 'web-edge', 'web')])

    const lanes = buildResourceHierarchy({ events, topology, groupByApp: true })

    expect(lanes).toHaveLength(1)
    expect(lanes[0].children).toHaveLength(1)
  })
})

// --- app-membership grouping cascade ----------------------------------------

function changeEvent(kind: string, namespace: string, name: string, over: Partial<TimelineEvent> = {}): TimelineEvent {
  return { id: `${kind}/${namespace}/${name}`, timestamp: '2024-01-01T00:00:00.000Z', source: 'informer', kind, namespace, name, eventType: 'update', ...over }
}

function index(byResource: Record<string, AppMembership>, byEvidence: Record<string, AppMembership> = {}): AppMembershipIndex {
  return { byResource: new Map(Object.entries(byResource)), byEvidence: new Map(Object.entries(byEvidence)) }
}

describe('buildResourceHierarchy app-membership cascade (grouping=app)', () => {
  const billing: AppMembership = { appKey: 'team-a/app/billing', appName: 'billing', env: 'prod', evidence: 'app.kubernetes.io/instance' }

  it('tier 1: wraps resources that ARE known workloads under an app header', () => {
    const events = [changeEvent('Deployment', 'team-a', 'billing-api'), changeEvent('Deployment', 'team-a', 'billing-worker')]
    const appIndex = index({ 'Deployment/team-a/billing-api': billing, 'Deployment/team-a/billing-worker': billing })
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex })
    expect(lanes).toHaveLength(1)
    expect(lanes[0].isAppGroup).toBe(true)
    expect(lanes[0].title).toBe('billing')
    expect(lanes[0].env).toBe('prod')
    expect(lanes[0].children).toHaveLength(2)
  })

  it('tier 2: a DELETED member joins its app via event-label evidence (matchKeys)', () => {
    // billing-api is a live known workload; billing-worker was deleted before the
    // snapshot and is unknown by resource, but its event carries the instance label.
    const events = [
      changeEvent('Deployment', 'team-a', 'billing-api'),
      changeEvent('Deployment', 'team-a', 'billing-worker', { labels: { 'app.kubernetes.io/instance': 'billing-prod' } }),
    ]
    // Evidence keys are namespace-scoped: kind:namespace:value.
    const appIndex = index({ 'Deployment/team-a/billing-api': billing }, { 'instance:team-a:billing-prod': billing })
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex })
    expect(lanes).toHaveLength(1)
    expect(lanes[0].isAppGroup).toBe(true)
    expect(lanes[0].children?.map((c) => c.name).sort()).toEqual(['billing-api', 'billing-worker'])
  })

  it('tier 2: GitOps identity labels join argo:/helm: matchKeys (Argo-/Flux-primary apps)', () => {
    // An Argo-primary app whose members lack the standard k8s labels: the
    // deleted member's event carries only the Argo instance label, and a
    // Flux-HelmRelease member carries only the Flux name label. Both must
    // re-join through the server's argo:/helm: evidence keys.
    const argoApp: AppMembership = { appKey: 'team-a/argo/shop', appName: 'shop', evidence: 'argocd.argoproj.io/instance' }
    const events = [
      changeEvent('Deployment', 'team-a', 'shop-api'),
      changeEvent('Deployment', 'team-a', 'shop-worker', { labels: { 'argocd.argoproj.io/instance': 'shop' } }),
      changeEvent('Deployment', 'team-a', 'shop-cache', { labels: { 'helm.toolkit.fluxcd.io/name': 'shop' } }),
    ]
    const appIndex = index(
      { 'Deployment/team-a/shop-api': argoApp },
      { 'argo:team-a:shop': argoApp, 'helm:team-a:shop': argoApp },
    )
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex })
    expect(lanes).toHaveLength(1)
    expect(lanes[0].isAppGroup).toBe(true)
    expect(lanes[0].children?.map((c) => c.name).sort()).toEqual(['shop-api', 'shop-cache', 'shop-worker'])
  })

  it('namespace isolation: the same label value in another namespace does NOT cross-join', () => {
    // Two apps in two namespaces share the instance label value "shared". The
    // evidence index is scoped per namespace, so a deleted member's event in
    // team-b must NOT join team-a's app (and vice-versa).
    const appA: AppMembership = { appKey: 'team-a/app/svc', appName: 'svc-a', env: 'prod', evidence: 'app.kubernetes.io/instance' }
    const events = [
      changeEvent('Deployment', 'team-a', 'svc-a-api'),
      // Deleted member in team-b carrying the SAME label value as team-a's app.
      changeEvent('Deployment', 'team-b', 'svc-b-worker', { labels: { 'app.kubernetes.io/instance': 'shared' } }),
    ]
    // Index knows svc-a's live workload + only the team-a-scoped evidence key.
    const appIndex = index({ 'Deployment/team-a/svc-a-api': appA }, { 'instance:team-a:shared': appA })
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex })

    // team-a's app is a single-member group (only svc-a-api) → renders as a plain
    // lane; the team-b event must NOT have joined it.
    const svcAApi = lanes.find((l) => l.name === 'svc-a-api' || l.children?.some((c) => c.name === 'svc-a-api'))
    expect(svcAApi).toBeTruthy()
    const joinedTeamB = (l: typeof lanes[number]) =>
      l.name === 'svc-b-worker' || (l.children ?? []).some((c) => c.name === 'svc-b-worker')
    const teamBUnderA = lanes.some((l) => (l.appKey === appA.appKey || l.name === 'svc-a-api') && joinedTeamB(l))
    expect(teamBUnderA).toBe(false)
    // svc-b-worker exists as its own lane, not folded into svc-a.
    expect(lanes.some((l) => l.name === 'svc-b-worker' || joinedTeamB(l))).toBe(true)
  })

  it('label evidence but no AppRow → no synthetic group; resources stay ungrouped', () => {
    // The server is the only grouping authority: raw label evidence with no
    // server-declared app behind it must NOT invent a client-side group. These
    // two label-bearing resources render as their own plain lanes.
    const events = [
      changeEvent('Deployment', 'team-a', 'ghost-api', { labels: { 'app.kubernetes.io/name': 'ghost' } }),
      changeEvent('Deployment', 'team-a', 'ghost-worker', { labels: { 'app.kubernetes.io/name': 'ghost' } }),
    ]
    const appIndex = index({}, {}) // no matching AppRow
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex })
    expect(lanes).toHaveLength(2)
    expect(lanes.every((l) => !l.isAppGroup)).toBe(true)
    expect(lanes.map((l) => l.name).sort()).toEqual(['ghost-api', 'ghost-worker'])
  })

  it('fallback: ungrouped resources render as their own lane', () => {
    const events = [changeEvent('ConfigMap', 'team-a', 'orphan-cm')]
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex: index({}) })
    expect(lanes).toHaveLength(1)
    expect(lanes[0].isAppGroup).toBeFalsy()
    expect(lanes[0].name).toBe('orphan-cm')
  })

  it('a single-member app does not earn a header (renders as a plain lane)', () => {
    const events = [changeEvent('Deployment', 'team-a', 'billing-api')]
    const appIndex = index({ 'Deployment/team-a/billing-api': billing })
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex })
    expect(lanes).toHaveLength(1)
    expect(lanes[0].isAppGroup).toBeFalsy()
    expect(lanes[0].name).toBe('billing-api')
  })

  // --- tier 2.5: member name-prefix fallback -------------------------------
  const koala: AppMembership = { appKey: 'autopush/app/koala-backend', appName: 'koala-backend', env: 'prod', evidence: 'app.kubernetes.io/instance' }

  it('tier 2.5: a Pod with owner:null + labels:null joins its app by name-prefix', () => {
    // The Deployment member is present; a deletion-time Pod event shipped with no
    // owner and no labels (connector cache miss), so it fails tiers 1-2 but its
    // generated name still encodes the Deployment.
    const events = [
      changeEvent('Deployment', 'autopush', 'koala-backend'),
      changeEvent('Pod', 'autopush', 'koala-backend-6dc59db657-6jm4n'),
    ]
    const appIndex = index({ 'Deployment/autopush/koala-backend': koala })
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex })
    expect(lanes).toHaveLength(1)
    expect(lanes[0].isAppGroup).toBe(true)
    expect(lanes[0].title).toBe('koala-backend')
    const pod = lanes[0].children?.find((c) => c.kind === 'Pod')
    expect(pod?.name).toBe('koala-backend-6dc59db657-6jm4n')
    expect(pod?.matchedByName).toBe(true)
    // The Deployment member joined by resource, not by name.
    expect(lanes[0].children?.find((c) => c.kind === 'Deployment')?.matchedByName).toBeFalsy()
  })

  it('contract nesting supersedes tier 2.5 when the parent lanes are present (RS+Pod nest, not flat)', () => {
    // With the ReplicaSet present, naming contracts nest the Pod under the RS and
    // the RS under the Deployment — the correct hierarchy — instead of tier-2.5
    // flattening all three as app-group siblings. The Deployment is then the sole
    // member, so it renders as a plain lane (single-member app earns no header).
    const events = [
      changeEvent('Deployment', 'autopush', 'koala-backend'),
      changeEvent('ReplicaSet', 'autopush', 'koala-backend-6dc59db657'),
      changeEvent('Pod', 'autopush', 'koala-backend-6dc59db657-6jm4n'),
    ]
    const appIndex = index({ 'Deployment/autopush/koala-backend': koala })
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex })
    expect(lanes).toHaveLength(1)
    expect(lanes[0].isAppGroup).toBeFalsy()
    expect(lanes[0].id).toBe('Deployment/autopush/koala-backend')
    const rs = lanes[0].children!.find((c) => c.kind === 'ReplicaSet')!
    expect(rs.nestedByContract).toBe(true)
    const pod = rs.children!.find((c) => c.kind === 'Pod')!
    expect(pod.nestedByContract).toBe(true)
  })

  it('tier 2.5 namespace isolation: same member name in another namespace does NOT join', () => {
    const events = [
      changeEvent('Deployment', 'autopush', 'koala-backend'),
      // Pod in a DIFFERENT namespace whose name would prefix-match koala-backend.
      changeEvent('Pod', 'other-ns', 'koala-backend-6dc59db657-6jm4n'),
    ]
    const appIndex = index({ 'Deployment/autopush/koala-backend': koala })
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex })
    // koala-backend is a single-member group → plain lane; the other-ns Pod stands alone.
    expect(lanes.map((l) => l.name).sort()).toEqual(['koala-backend', 'koala-backend-6dc59db657-6jm4n'])
    expect(lanes.every((l) => !l.isAppGroup)).toBe(true)
  })

  it('tier 2.5 longest-wins: web-admin claims web-admin-* over web', () => {
    const web: AppMembership = { appKey: 'team-a/app/web', appName: 'web', evidence: 'name' }
    const webAdmin: AppMembership = { appKey: 'team-a/app/web-admin', appName: 'web-admin', evidence: 'name' }
    const events = [
      changeEvent('Deployment', 'team-a', 'web'),
      changeEvent('Deployment', 'team-a', 'web-admin'),
      // Single-segment suffix so BOTH "web" (suffix admin-abc12) and "web-admin"
      // (suffix abc12) are candidates — longest member name must win.
      changeEvent('Pod', 'team-a', 'web-admin-abc12'),
    ]
    const appIndex = index({
      'Deployment/team-a/web': web,
      'Deployment/team-a/web-admin': webAdmin,
    })
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex })
    const webAdminGroup = lanes.find((l) => l.isAppGroup && l.title === 'web-admin')
    expect(webAdminGroup).toBeTruthy()
    expect(webAdminGroup?.children?.some((c) => c.name === 'web-admin-abc12')).toBe(true)
    // web stayed a single-member plain lane (the pod did not join it).
    const webLane = lanes.find((l) => l.name === 'web' && !l.isAppGroup)
    expect(webLane).toBeTruthy()
  })

  it('tier 2.5 suffix strictness: a word suffix (canary) does NOT match', () => {
    const events = [
      changeEvent('Deployment', 'autopush', 'koala-backend'),
      changeEvent('Pod', 'autopush', 'koala-backend-canary'),
    ]
    const appIndex = index({ 'Deployment/autopush/koala-backend': koala })
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex })
    // No join: koala-backend renders as a plain lane, canary Pod stands alone.
    expect(lanes.every((l) => !l.isAppGroup)).toBe(true)
    expect(lanes.map((l) => l.name).sort()).toEqual(['koala-backend', 'koala-backend-canary'])
  })

  it('tier 2.5 kind gate: a Service named like a member + suffix does NOT match', () => {
    const events = [
      changeEvent('Deployment', 'autopush', 'koala-backend'),
      // A Service, not a generated-name kind — a real name collision, not a child.
      changeEvent('Service', 'autopush', 'koala-backend-6dc59db657'),
    ]
    const appIndex = index({ 'Deployment/autopush/koala-backend': koala })
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex })
    expect(lanes.every((l) => !l.isAppGroup)).toBe(true)
    expect(lanes.map((l) => l.name).sort()).toEqual(['koala-backend', 'koala-backend-6dc59db657'])
  })
})

// --- kind-contract nesting (parent-driven naming) ---------------------------
//
// A resource whose only in-window events are ownerless (deletion-time cache miss)
// can still nest under a PRESENT parent lane when its generated name encodes the
// parent by a Kubernetes naming contract. Confidence: ownerRef > kind-contract >
// name-stem. Nested lanes ride their chain root into app membership rather than
// joining an app flat as siblings via the weaker tier-2.5 name-prefix fallback.

describe('buildResourceHierarchy kind-contract nesting', () => {
  it('nests an ownerless Job under its CronJob (numeric schedule stamp)', () => {
    const events = [
      changeEvent('CronJob', 'ops', 'backup'),
      changeEvent('Job', 'ops', 'backup-27700001'),
    ]
    const lanes = buildResourceHierarchy({ events, grouping: 'owner' })
    expect(lanes.map((l) => l.id)).toEqual(['CronJob/ops/backup'])
    const job = lanes[0].children!.find((c) => c.kind === 'Job')
    expect(job?.id).toBe('Job/ops/backup-27700001')
    expect(job?.nestedByContract).toBe(true)
  })

  it('nests an ownerless Pod under its Job (5-char generateName tail)', () => {
    const events = [
      changeEvent('Job', 'ops', 'backup-27700001'),
      changeEvent('Pod', 'ops', 'backup-27700001-abc12'),
    ]
    const lanes = buildResourceHierarchy({ events, grouping: 'owner' })
    expect(lanes.map((l) => l.id)).toEqual(['Job/ops/backup-27700001'])
    const pod = lanes[0].children!.find((c) => c.kind === 'Pod')
    expect(pod?.nestedByContract).toBe(true)
  })

  it('nests an ownerless ReplicaSet under its Deployment (pod-template-hash)', () => {
    const events = [
      changeEvent('Deployment', 'ns', 'web'),
      changeEvent('ReplicaSet', 'ns', 'web-6dc59db657'),
    ]
    const lanes = buildResourceHierarchy({ events, grouping: 'owner' })
    expect(lanes.map((l) => l.id)).toEqual(['Deployment/ns/web'])
    const rs = lanes[0].children!.find((c) => c.kind === 'ReplicaSet')
    expect(rs?.nestedByContract).toBe(true)
  })

  it('negative: a word suffix (canary) is not a schedule stamp — no nesting', () => {
    const events = [
      changeEvent('CronJob', 'ops', 'backup'),
      changeEvent('Job', 'ops', 'backup-canary'),
    ]
    const lanes = buildResourceHierarchy({ events, grouping: 'owner' })
    expect(lanes.map((l) => l.id).sort()).toEqual(['CronJob/ops/backup', 'Job/ops/backup-canary'])
  })

  it('negative: a parent in another namespace does not adopt the child', () => {
    const events = [
      changeEvent('CronJob', 'ops', 'backup'),
      changeEvent('Job', 'other', 'backup-27700001'),
    ]
    const lanes = buildResourceHierarchy({ events, grouping: 'owner' })
    expect(lanes.map((l) => l.id).sort()).toEqual(['CronJob/ops/backup', 'Job/other/backup-27700001'])
  })

  it('negative: wrong kind pairing (a Pod does not adopt onto a CronJob)', () => {
    const events = [
      changeEvent('CronJob', 'ops', 'backup'),
      changeEvent('Pod', 'ops', 'backup-abc12'),
    ]
    const lanes = buildResourceHierarchy({ events, grouping: 'owner' })
    expect(lanes.map((l) => l.id).sort()).toEqual(['CronJob/ops/backup', 'Pod/ops/backup-abc12'])
  })

  it('two-hyphen ambiguity: the exact present parent wins (a-b, not a)', () => {
    const events = [
      changeEvent('CronJob', 'ops', 'a'),
      changeEvent('CronJob', 'ops', 'a-b'),
      changeEvent('Job', 'ops', 'a-b-123'),
    ]
    const lanes = buildResourceHierarchy({ events, grouping: 'owner' })
    const ab = lanes.find((l) => l.id === 'CronJob/ops/a-b')
    expect(ab?.children?.some((c) => c.id === 'Job/ops/a-b-123')).toBe(true)
    const a = lanes.find((l) => l.id === 'CronJob/ops/a')
    expect(a?.children ?? []).toHaveLength(0)
  })

  it('cycle guard: never reparent a lane under its own descendant', () => {
    // An owner ref already nests the Job under the Pod; the naming contract must
    // NOT then nest the Pod under that Job (the candidate parent is in the Pod's
    // own subtree).
    const events = [
      changeEvent('Pod', 'ops', 'parent-abc12', { id: 'pod' }),
      changeEvent('Job', 'ops', 'parent', { id: 'job', owner: { kind: 'Pod', name: 'parent-abc12' } }),
    ]
    const lanes = buildResourceHierarchy({ events, grouping: 'owner' })
    expect(lanes.map((l) => l.id)).toEqual(['Pod/ops/parent-abc12'])
    expect(lanes[0].nestedByContract).toBeFalsy()
    expect(lanes[0].children!.map((c) => c.id)).toEqual(['Job/ops/parent'])
  })

  it('app membership via chain root: a contract-nested orphan is not a flat member', () => {
    const batch: AppMembership = { appKey: 'ops/app/batch', appName: 'batch' }
    const events = [
      changeEvent('CronJob', 'ops', 'backup'),
      changeEvent('Deployment', 'ops', 'web'),
      changeEvent('Job', 'ops', 'backup-27700001'),
    ]
    const appIndex = index({
      'CronJob/ops/backup': batch,
      'Deployment/ops/web': batch,
    })
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex })
    const app = lanes.find((l) => l.isAppGroup)!
    // The Job did NOT join the app flat as a sibling of the CronJob.
    expect(app.children!.some((c) => c.id === 'Job/ops/backup-27700001')).toBe(false)
    // It rides the CronJob member instead, marked contract-nested (not name-matched).
    const cron = app.children!.find((c) => c.id === 'CronJob/ops/backup')!
    const job = cron.children!.find((c) => c.id === 'Job/ops/backup-27700001')!
    expect(job.nestedByContract).toBe(true)
    expect(job.matchedByName).toBeFalsy()
  })
})

// --- window-filtered group members ------------------------------------------

describe('laneHasEventInWindow / isChildVisibleInWindow', () => {
  const START = Date.parse('2024-06-01T00:00:00.000Z')
  const END = Date.parse('2024-06-01T01:00:00.000Z')
  const IN = '2024-06-01T00:30:00.000Z'
  const OUT = '2024-05-01T00:00:00.000Z'
  const leaf = (id: string, iso: string, over: Partial<ResourceLane> = {}): ResourceLane => ({
    id, kind: 'Pod', namespace: 'ns', name: id, isWorkload: true,
    events: [changeEvent('Pod', 'ns', id, { id: `${id}-ev`, timestamp: iso })], children: [], ...over,
  })

  it('true when the lane owns an in-window event', () => {
    expect(laneHasEventInWindow(leaf('a', IN), START, END)).toBe(true)
  })

  it('true when a descendant owns an in-window event', () => {
    const parent: ResourceLane = { id: 'Job/ns/j', kind: 'Job', namespace: 'ns', name: 'j', isWorkload: true, events: [], children: [leaf('a', IN)] }
    expect(laneHasEventInWindow(parent, START, END)).toBe(true)
  })

  it('false when every subtree event is outside the window', () => {
    const parent: ResourceLane = { id: 'Job/ns/j', kind: 'Job', namespace: 'ns', name: 'j', isWorkload: true, events: [], children: [leaf('a', OUT)] }
    expect(laneHasEventInWindow(parent, START, END)).toBe(false)
  })

  it('prefers the precomputed allEventsSorted roll-up when present', () => {
    const lane = leaf('a', OUT, { events: [], allEventsSorted: [changeEvent('Pod', 'ns', 'a', { id: 'roll', timestamp: IN })] })
    expect(laneHasEventInWindow(lane, START, END)).toBe(true)
  })

  it('hides an out-of-window child; shows an in-window one', () => {
    expect(isChildVisibleInWindow(leaf('a', OUT), START, END, { pinned: false, userExpanded: false })).toBe(false)
    expect(isChildVisibleInWindow(leaf('b', IN), START, END, { pinned: false, userExpanded: false })).toBe(true)
  })

  it('pinned exemption keeps an out-of-window child', () => {
    expect(isChildVisibleInWindow(leaf('a', OUT), START, END, { pinned: true, userExpanded: false })).toBe(true)
  })

  it('user-expanded exemption keeps an out-of-window child (auto-expand does not)', () => {
    expect(isChildVisibleInWindow(leaf('a', OUT), START, END, { pinned: false, userExpanded: true })).toBe(true)
  })
})

describe('buildResourceHierarchy flat mode', () => {
  it('skips ALL parenting — every resource is its own lane', () => {
    const events = [
      changeEvent('Deployment', 'team-a', 'api'),
      changeEvent('Pod', 'team-a', 'api-7d4f-x2p', { owner: { kind: 'ReplicaSet', name: 'api-7d4f' } }),
    ]
    const lanes = buildResourceHierarchy({ events, grouping: 'flat' })
    expect(lanes.every((l) => (l.children ?? []).length === 0)).toBe(true)
    // The Pod owner-ref did NOT create/attach a ReplicaSet parent.
    expect(lanes.some((l) => l.kind === 'ReplicaSet')).toBe(false)
  })

  it('still attaches K8s events to their owner lane (identity, not grouping)', () => {
    const events = [
      changeEvent('Deployment', 'team-a', 'api'),
      { id: 'ev', timestamp: '2024-01-01T00:00:00.000Z', source: 'k8s_event', kind: 'Event', namespace: 'team-a', name: 'evt', eventType: 'Warning', owner: { kind: 'Deployment', name: 'api' } } as TimelineEvent,
    ]
    const lanes = buildResourceHierarchy({ events, grouping: 'flat' })
    // One lane (the Deployment) carrying both its own event and the attached K8s event.
    const dep = lanes.find((l) => l.id === 'Deployment/team-a/api')
    expect(dep).toBeTruthy()
    expect(dep!.events.length).toBe(2)
  })
})

// --- CronJob → Job → Pod attribution (the swimlane empty-child bug) ----------
//
// Each level owns events. The chain must nest (Pod under Job under CronJob) with
// every event landing on ITS OWN lane — the k8s-event→owner attachment rule must
// NOT strip a Pod's lifecycle events onto an ancestor. Then `laneTrackEvents`
// pins the render slice: a collapsed parent paints its subtree roll-up, an
// expanded parent (or a leaf) paints only its own events.

function cronChainEvents(): TimelineEvent[] {
  return [
    // CronJob owns 2
    changeEvent('CronJob', 'ops', 'backup', { eventType: 'add' }),
    changeEvent('CronJob', 'ops', 'backup', { id: 'cj-k8s', source: 'k8s_event', eventType: 'Normal', reason: 'SuccessfulCreate' }),
    // Job owns 2 (owner = CronJob)
    changeEvent('Job', 'ops', 'backup-1700', { id: 'job-add', eventType: 'add', owner: { kind: 'CronJob', name: 'backup' } }),
    changeEvent('Job', 'ops', 'backup-1700', { id: 'job-comp', source: 'k8s_event', eventType: 'Normal', reason: 'Completed', owner: { kind: 'CronJob', name: 'backup' } }),
    // Pod owns 3 — informer add (owner = Job) + two k8s lifecycle events. The
    // backend enriches a Pod k8s event's owner to the Pod's controller (Job), so
    // these carry owner:Job while their kind stays 'Pod' (involvedObject).
    changeEvent('Pod', 'ops', 'backup-1700-abc12', { id: 'pod-add', eventType: 'add', owner: { kind: 'Job', name: 'backup-1700' } }),
    changeEvent('Pod', 'ops', 'backup-1700-abc12', { id: 'pod-sched', source: 'k8s_event', eventType: 'Normal', reason: 'Scheduled', owner: { kind: 'Job', name: 'backup-1700' } }),
    changeEvent('Pod', 'ops', 'backup-1700-abc12', { id: 'pod-start', source: 'k8s_event', eventType: 'Normal', reason: 'Started', owner: { kind: 'Job', name: 'backup-1700' } }),
  ]
}

describe('buildResourceHierarchy CronJob → Job → Pod attribution', () => {
  it('nests the chain and keeps every event on its own lane (owner mode)', () => {
    const lanes = buildResourceHierarchy({ events: cronChainEvents(), grouping: 'owner' })
    expect(lanes).toHaveLength(1)
    const cron = lanes[0]
    expect(cron.id).toBe('CronJob/ops/backup')
    // Two-level chain: CronJob's only direct child is the Job; the Pod hangs
    // under the Job (a grandchild), not as a sibling of the Job.
    expect(cron.children!.map((c) => c.kind)).toEqual(['Job'])
    const job = cron.children!.find((c) => c.kind === 'Job')!
    const pod = job.children!.find((c) => c.kind === 'Pod')!
    // Each resource owns exactly its own events — nothing stripped upward.
    expect(cron.events).toHaveLength(2)
    expect(job.events).toHaveLength(2)
    expect(pod.events).toHaveLength(3)
    // The Pod's lifecycle k8s events stay on the Pod's own lane.
    expect(pod.events.map((e) => e.id).sort()).toEqual(['pod-add', 'pod-sched', 'pod-start'])
    // No event was double-attached to an ancestor.
    expect(cron.events.some((e) => e.id.startsWith('pod-') || e.id.startsWith('job-'))).toBe(false)
  })

  it('getAllEventsFromHierarchy flattens the FULL depth — grandchild Pod events included', () => {
    const lanes = buildResourceHierarchy({ events: cronChainEvents(), grouping: 'owner' })
    const all = getAllEventsFromHierarchy(lanes)
    // CronJob (2) + Job (2) + grandchild Pod (3) = 7. A one-level walk would drop
    // the Pod's 3, undercounting the events the swimlane (built from the full
    // tree) shows — this pins the recursion that fixes that.
    expect(all).toHaveLength(7)
    expect(all.filter((e) => e.id.startsWith('pod-'))).toHaveLength(3)
    expect(all.map((e) => e.id)).toContain('pod-start')
  })

  it('laneTrackEvents: collapsed parent = subtree aggregate, expanded parent = own only', () => {
    const cron = buildResourceHierarchy({ events: cronChainEvents(), grouping: 'owner' })[0]
    // Collapsed CronJob paints the whole subtree roll-up (2 + 2 + 3 = 7).
    expect(laneTrackEvents(cron, false)).toHaveLength(7)
    // Expanded CronJob paints ONLY its own 2 events (children render their own).
    expect(laneTrackEvents(cron, true)).toHaveLength(2)
    // The intermediate Job, collapsed, rolls up its own + Pod events (2 + 3).
    const job = cron.children!.find((c) => c.kind === 'Job')!
    expect(laneTrackEvents(job, false)).toHaveLength(5)
    expect(laneTrackEvents(job, true)).toHaveLength(2)
    // A leaf child paints its own events whether "expanded" or not.
    const pod = job.children!.find((c) => c.kind === 'Pod')!
    expect(laneTrackEvents(pod, false)).toHaveLength(3)
    expect(laneTrackEvents(pod, true)).toHaveLength(3)
  })

  it('no event appears on two visible rows once the parent is expanded', () => {
    const cron = buildResourceHierarchy({ events: cronChainEvents(), grouping: 'owner' })[0]
    const parentSlice = laneTrackEvents(cron, true) // expanded → own
    const childSlices = cron.children!.flatMap((c) => laneTrackEvents(c, false))
    const ids = [...parentSlice, ...childSlices].map((e) => e.id)
    expect(new Set(ids).size).toBe(ids.length) // no duplicate id across visible rows
    expect(ids).toHaveLength(7) // and every event is shown exactly once
  })

  it('subtreeEvents rolls up a nested parent that carries no precomputed aggregate', () => {
    // A hand-built Job with a Pod child (no allEventsSorted) still rolls up.
    const pod: ResourceLane = { id: 'Pod/ops/p', kind: 'Pod', namespace: 'ops', name: 'p', isWorkload: true, events: [changeEvent('Pod', 'ops', 'p', { id: 'p1' })], children: [] }
    const job: ResourceLane = { id: 'Job/ops/j', kind: 'Job', namespace: 'ops', name: 'j', isWorkload: true, events: [changeEvent('Job', 'ops', 'j', { id: 'j1' })], children: [pod] }
    expect(subtreeEvents(job).map((e) => e.id).sort()).toEqual(['j1', 'p1'])
  })

  it('app-group mode: a CronJob MEMBER keeps its Job/Pod as drill-down rows (grandchildren, own slices)', () => {
    const events = [...cronChainEvents(), changeEvent('Deployment', 'ops', 'web', { id: 'dep-add', eventType: 'add' })]
    const appIndex = index({
      'CronJob/ops/backup': { appKey: 'batch', appName: 'batch' } as AppMembership,
      'Deployment/ops/web': { appKey: 'batch', appName: 'batch' } as AppMembership,
    })
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex })
    const app = lanes.find((l) => l.isAppGroup)!
    const cron = app.children!.find((c) => c.kind === 'CronJob')!
    // The member is NOT flattened to an aggregate leaf — its Job (direct child)
    // and Pod (grandchild under the Job) survive so they can be drilled into
    // with their own event slices.
    expect(cron.children!.map((c) => c.kind)).toEqual(['Job'])
    const job = cron.children!.find((c) => c.kind === 'Job')!
    expect(job.children!.map((c) => c.kind)).toEqual(['Pod'])
    // Collapsed member = aggregate (all pills); expanded member = own only.
    expect(laneTrackEvents(cron, false)).toHaveLength(7)
    expect(laneTrackEvents(cron, true)).toHaveLength(2)
    const pod = job.children!.find((c) => c.kind === 'Pod')!
    expect(laneTrackEvents(pod, false)).toHaveLength(3)
  })
})

describe('extractPinnedLanes', () => {
  it('matches a top-level root and clones it as a pinned lane (subtree events preserved)', () => {
    const events = [
      changeEvent('Deployment', 'team-a', 'web'),
      changeEvent('Pod', 'team-a', 'web-7d4f-x2p', { owner: { kind: 'Deployment', name: 'web' } }),
    ]
    const lanes = buildResourceHierarchy({ events, grouping: 'owner' })
    const pinned = extractPinnedLanes(lanes, [
      { id: 'Deployment/team-a/web', kind: 'Deployment', namespace: 'team-a', name: 'web' },
    ])
    expect(pinned).toHaveLength(1)
    expect(pinned[0].id).toBe('Deployment/team-a/web')
    // Root's merged track carries its own + the child Pod's events.
    const ids = (pinned[0].allEventsSorted ?? []).map((e) => e.id).sort()
    expect(ids).toEqual(['Deployment/team-a/web', 'Pod/team-a/web-7d4f-x2p'])
  })

  it('matches a nested child lane and clones it as its own top-level pinned lane', () => {
    const events = [
      changeEvent('Deployment', 'team-a', 'web'),
      changeEvent('Pod', 'team-a', 'web-7d4f-x2p', { owner: { kind: 'Deployment', name: 'web' } }),
    ]
    const lanes = buildResourceHierarchy({ events, grouping: 'owner' })
    // The Pod is a child of the Deployment in the built hierarchy.
    expect(lanes.find((l) => l.id === 'Pod/team-a/web-7d4f-x2p')).toBeFalsy()
    const pinned = extractPinnedLanes(lanes, [
      { id: 'Pod/team-a/web-7d4f-x2p', kind: 'Pod', namespace: 'team-a', name: 'web-7d4f-x2p' },
    ])
    expect(pinned).toHaveLength(1)
    expect(pinned[0].id).toBe('Pod/team-a/web-7d4f-x2p')
    expect((pinned[0].allEventsSorted ?? []).map((e) => e.id)).toEqual(['Pod/team-a/web-7d4f-x2p'])
  })

  it('synthesizes an empty lane from the record when the resource is absent', () => {
    const lanes = buildResourceHierarchy({ events: [changeEvent('Deployment', 'team-a', 'web')], grouping: 'owner' })
    const pinned = extractPinnedLanes(lanes, [
      { id: 'Pod/team-a/ghost', kind: 'Pod', namespace: 'team-a', name: 'ghost' },
    ])
    expect(pinned).toHaveLength(1)
    expect(pinned[0]).toMatchObject({ id: 'Pod/team-a/ghost', kind: 'Pod', namespace: 'team-a', name: 'ghost' })
    expect(pinned[0].allEventsSorted).toEqual([])
    expect(pinned[0].events).toEqual([])
  })

  it('returns pinned lanes in pin order, not hierarchy order', () => {
    const lanes = buildResourceHierarchy({
      events: [changeEvent('Deployment', 'team-a', 'aaa'), changeEvent('Deployment', 'team-a', 'zzz')],
      grouping: 'owner',
    })
    const pinned = extractPinnedLanes(lanes, [
      { id: 'Deployment/team-a/zzz', kind: 'Deployment', namespace: 'team-a', name: 'zzz' },
      { id: 'Deployment/team-a/aaa', kind: 'Deployment', namespace: 'team-a', name: 'aaa' },
    ])
    expect(pinned.map((l) => l.name)).toEqual(['zzz', 'aaa'])
  })

  it('returns an empty array for no pins', () => {
    const lanes = buildResourceHierarchy({ events: [changeEvent('Deployment', 'team-a', 'web')], grouping: 'owner' })
    expect(extractPinnedLanes(lanes, [])).toEqual([])
  })

  // --- app-group pins ------------------------------------------------------
  const billingApp: AppMembership = { appKey: 'team-a/app/billing', appName: 'billing', env: 'prod', evidence: 'app.kubernetes.io/instance' }
  const billingIndex = index({
    'Deployment/team-a/billing-api': billingApp,
    'Deployment/team-a/billing-worker': billingApp,
  })

  it('resolves an app-group ref to the live group lane (members re-resolve)', () => {
    const events = [changeEvent('Deployment', 'team-a', 'billing-api'), changeEvent('Deployment', 'team-a', 'billing-worker')]
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex: billingIndex })
    const pinned = extractPinnedLanes(lanes, [
      { type: 'appGroup', id: 'app:team-a/app/billing', appKey: 'team-a/app/billing', appName: 'billing' },
    ])
    expect(pinned).toHaveLength(1)
    expect(pinned[0].isAppGroup).toBe(true)
    expect(pinned[0].appKey).toBe('team-a/app/billing')
    expect(pinned[0].absentPinnedApp).toBeFalsy()
    // Live members re-resolved from the current lanes, not the stored record.
    expect(pinned[0].children?.map((c) => c.name).sort()).toEqual(['billing-api', 'billing-worker'])
  })

  it('synthesizes a quiet header for an app-group ref with no matching group lane (owner mode)', () => {
    // Owner grouping never builds the group lane by construction — the pin still
    // renders as a dimmed "not present" header from the record.
    const events = [changeEvent('Deployment', 'team-a', 'billing-api'), changeEvent('Deployment', 'team-a', 'billing-worker')]
    const lanes = buildResourceHierarchy({ events, grouping: 'owner' })
    const pinned = extractPinnedLanes(lanes, [
      { type: 'appGroup', id: 'app:team-a/app/billing', appKey: 'team-a/app/billing', appName: 'billing' },
    ])
    expect(pinned).toHaveLength(1)
    expect(pinned[0]).toMatchObject({ id: 'app:team-a/app/billing', isAppGroup: true, appKey: 'team-a/app/billing', name: 'billing', absentPinnedApp: true })
    expect(pinned[0].children).toEqual([])
    expect(pinned[0].allEventsSorted).toEqual([])
  })

  it('synthesizes a quiet header when the app has vanished entirely', () => {
    const lanes = buildResourceHierarchy({ events: [changeEvent('Deployment', 'team-a', 'other')], grouping: 'app', appIndex: index({}) })
    const pinned = extractPinnedLanes(lanes, [
      { type: 'appGroup', id: 'app:team-a/app/gone', appKey: 'team-a/app/gone', appName: 'gone' },
    ])
    expect(pinned[0].absentPinnedApp).toBe(true)
    expect(pinned[0].title).toBe('gone')
  })

  it('preserves pin order across mixed resource + app-group refs', () => {
    const events = [
      changeEvent('Deployment', 'team-a', 'billing-api'),
      changeEvent('Deployment', 'team-a', 'billing-worker'),
      changeEvent('Deployment', 'team-a', 'standalone'),
    ]
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex: billingIndex })
    const pinned = extractPinnedLanes(lanes, [
      { id: 'Deployment/team-a/standalone', kind: 'Deployment', namespace: 'team-a', name: 'standalone' },
      { type: 'appGroup', id: 'app:team-a/app/billing', appKey: 'team-a/app/billing', appName: 'billing' },
    ])
    expect(pinned.map((l) => l.id)).toEqual(['Deployment/team-a/standalone', 'app:team-a/app/billing'])
  })

  it('resolves a legacy resource ref that carries no `type` (back-compat)', () => {
    const lanes = buildResourceHierarchy({ events: [changeEvent('Deployment', 'team-a', 'web')], grouping: 'owner' })
    const pinned = extractPinnedLanes(lanes, [
      { id: 'Deployment/team-a/web', kind: 'Deployment', namespace: 'team-a', name: 'web' },
    ])
    expect(pinned).toHaveLength(1)
    expect(pinned[0].id).toBe('Deployment/team-a/web')
    expect(pinned[0].isAppGroup).toBeFalsy()
  })

  // Dedup: a pinned app group subsumes a separately-pinned member of that group,
  // regardless of pin order — the app-group wins so the member renders once
  // (inside the group's roll-up), never a second standalone row that would
  // double-count its events.
  it.each([
    {
      name: 'app pinned FIRST, then member → member folded into group only',
      refs: [
        { type: 'appGroup' as const, id: 'app:team-a/app/billing', appKey: 'team-a/app/billing', appName: 'billing' },
        { id: 'Deployment/team-a/billing-api', kind: 'Deployment', namespace: 'team-a', name: 'billing-api' },
      ],
      expectedIds: ['app:team-a/app/billing'],
    },
    {
      name: 'member pinned FIRST, then app → same outcome (app-group wins)',
      refs: [
        { id: 'Deployment/team-a/billing-api', kind: 'Deployment', namespace: 'team-a', name: 'billing-api' },
        { type: 'appGroup' as const, id: 'app:team-a/app/billing', appKey: 'team-a/app/billing', appName: 'billing' },
      ],
      expectedIds: ['app:team-a/app/billing'],
    },
  ])('$name', ({ refs, expectedIds }) => {
    const events = [changeEvent('Deployment', 'team-a', 'billing-api'), changeEvent('Deployment', 'team-a', 'billing-worker')]
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex: billingIndex })
    const pinned = extractPinnedLanes(lanes, refs)
    // The member appears exactly once — inside the group roll-up, not as its own row.
    expect(pinned.map((l) => l.id)).toEqual(expectedIds)
    const group = pinned.find((l) => l.isAppGroup)
    expect(group?.children?.map((c) => c.name).sort()).toEqual(['billing-api', 'billing-worker'])
    // Counts don't double: the member's event id appears once across all pinned lanes.
    const allEventIds = pinned.flatMap((l) => (l.allEventsSorted ?? []).map((e) => e.id))
    expect(allEventIds.filter((id) => id === 'Deployment/team-a/billing-api')).toHaveLength(1)
  })

  it('keeps a standalone member pin that is NOT part of any pinned app group', () => {
    const events = [
      changeEvent('Deployment', 'team-a', 'billing-api'),
      changeEvent('Deployment', 'team-a', 'billing-worker'),
      changeEvent('Deployment', 'team-a', 'standalone'),
    ]
    const lanes = buildResourceHierarchy({ events, grouping: 'app', appIndex: billingIndex })
    const pinned = extractPinnedLanes(lanes, [
      { type: 'appGroup', id: 'app:team-a/app/billing', appKey: 'team-a/app/billing', appName: 'billing' },
      { id: 'Deployment/team-a/standalone', kind: 'Deployment', namespace: 'team-a', name: 'standalone' },
    ])
    expect(pinned.map((l) => l.id)).toEqual(['app:team-a/app/billing', 'Deployment/team-a/standalone'])
  })
})

describe('isPinnedLaneRef (localStorage validation)', () => {
  it('accepts a legacy resource ref with no type', () => {
    expect(isPinnedLaneRef({ id: 'Deployment/team-a/web', kind: 'Deployment', namespace: 'team-a', name: 'web' })).toBe(true)
  })
  it('accepts an explicit resource ref', () => {
    expect(isPinnedLaneRef({ type: 'resource', id: 'Deployment/team-a/web', kind: 'Deployment', namespace: 'team-a', name: 'web' })).toBe(true)
  })
  it('accepts an app-group ref', () => {
    expect(isPinnedLaneRef({ type: 'appGroup', id: 'app:team-a/app/billing', appKey: 'team-a/app/billing', appName: 'billing' })).toBe(true)
  })
  it('rejects an app-group ref missing appKey/appName', () => {
    expect(isPinnedLaneRef({ type: 'appGroup', id: 'app:x' })).toBe(false)
  })
  it('rejects a resource ref missing required fields', () => {
    expect(isPinnedLaneRef({ id: 'Deployment/team-a/web', kind: 'Deployment' })).toBe(false)
  })
  it('rejects non-objects and null', () => {
    expect(isPinnedLaneRef(null)).toBe(false)
    expect(isPinnedLaneRef('nope')).toBe(false)
  })
})

describe('removePinnedLanes (pin = move, not copy)', () => {
  const lane = (id: string, children?: any[], events: any[] = []) => {
    const [kind, namespace, name] = id.split('/')
    return { id, kind, namespace, name, events, children, isWorkload: false } as any
  }
  it('removes a pinned root lane', () => {
    const out = removePinnedLanes([lane('Deployment/team-a/billing-api'), lane('Service/team-a/web')], new Set(['Deployment/team-a/billing-api']))
    expect(out.map((l: any) => l.id)).toEqual(['Service/team-a/web'])
  })
  it('prunes a pinned child and keeps the parent when it still has content', () => {
    const parent = lane('Service/team-a/web', [lane('Deployment/team-a/web'), lane('Pod/team-a/web-1')], [{ id: 'e1' }])
    const out = removePinnedLanes([parent], new Set(['Pod/team-a/web-1']))
    expect(out[0].children!.map((c: any) => c.id)).toEqual(['Deployment/team-a/web'])
  })
  it('drops a parent left with no children and no own events', () => {
    const parent = lane('Service/team-a/web', [lane('Deployment/team-a/web')], [])
    const out = removePinnedLanes([parent], new Set(['Deployment/team-a/web']))
    expect(out).toEqual([])
  })
  it('no-ops when nothing is pinned', () => {
    const input = [lane('Service/team-a/web')]
    expect(removePinnedLanes(input, new Set())).toBe(input)
  })

  // --- two-level owner-chain nesting (CronJob → Job → Pod) -------------------
  // Real shape: Pod events carry owner {kind:Job}, Job events carry owner
  // {kind:CronJob}. The tree must nest each lane under its DIRECT owner, not
  // flatten every descendant onto the chain root.
  it('nests Pods under their Job, and the Job under the CronJob (not siblings)', () => {
    const events: TimelineEvent[] = [
      changeEvent('CronJob', 'team-a', 'aliaser', { id: 'cj' }),
      changeEvent('Job', 'team-a', 'aliaser-27700001', { id: 'j1', owner: { kind: 'CronJob', name: 'aliaser' } }),
      changeEvent('Job', 'team-a', 'aliaser-27700002', { id: 'j2', owner: { kind: 'CronJob', name: 'aliaser' } }),
      changeEvent('Pod', 'team-a', 'aliaser-27700001-abc12', { id: 'p1', owner: { kind: 'Job', name: 'aliaser-27700001' } }),
      changeEvent('Pod', 'team-a', 'aliaser-27700001-def34', { id: 'p2', owner: { kind: 'Job', name: 'aliaser-27700001' } }),
      changeEvent('Pod', 'team-a', 'aliaser-27700002-ghi56', { id: 'p3', owner: { kind: 'Job', name: 'aliaser-27700002' } }),
    ]
    const lanes = buildResourceHierarchy({ events, grouping: 'owner' })

    // One root: the CronJob.
    expect(lanes.map((l) => l.id)).toEqual(['CronJob/team-a/aliaser'])
    const cron = lanes[0]
    // CronJob's direct children are the two Jobs — NO Pods flattened here.
    expect((cron.children ?? []).map((c) => c.id).sort()).toEqual([
      'Job/team-a/aliaser-27700001',
      'Job/team-a/aliaser-27700002',
    ])
    const job1 = cron.children!.find((c) => c.id === 'Job/team-a/aliaser-27700001')!
    const job2 = cron.children!.find((c) => c.id === 'Job/team-a/aliaser-27700002')!
    // Pods hang under their own Job.
    expect((job1.children ?? []).map((c) => c.id).sort()).toEqual([
      'Pod/team-a/aliaser-27700001-abc12',
      'Pod/team-a/aliaser-27700001-def34',
    ])
    expect((job2.children ?? []).map((c) => c.id)).toEqual(['Pod/team-a/aliaser-27700002-ghi56'])
  })

  it('collapsed root roll-up still spans every descendant depth (Pods included)', () => {
    const events: TimelineEvent[] = [
      changeEvent('CronJob', 'team-a', 'aliaser', { id: 'cj' }),
      changeEvent('Job', 'team-a', 'aliaser-1', { id: 'j1', owner: { kind: 'CronJob', name: 'aliaser' } }),
      changeEvent('Pod', 'team-a', 'aliaser-1-abc12', { id: 'p1', owner: { kind: 'Job', name: 'aliaser-1' } }),
    ]
    const [cron] = buildResourceHierarchy({ events, grouping: 'owner' })
    // Collapsed parent paints its whole-subtree aggregate — the Pod (grandchild)
    // must be in it, or a two-level chain's roll-up would drop a level.
    const ids = laneTrackEvents(cron, false).map((e) => e.id)
    expect(ids).toEqual(expect.arrayContaining(['cj', 'j1', 'p1']))
  })
  it('removes an app-group root matched by appKey', () => {
    const group = { id: 'app:team-a/app/billing', kind: 'AppGroup', namespace: '', name: 'billing', events: [], isAppGroup: true, appKey: 'team-a/app/billing', children: [lane('Deployment/team-a/billing-api')] } as any
    const out = removePinnedLanes([group, lane('Service/team-a/web')], new Set(), new Set(['team-a/app/billing']))
    expect(out.map((l: any) => l.id)).toEqual(['Service/team-a/web'])
  })
  it('rebuilds the parent roll-up after pruning a pinned child (collapsed parent excludes the moved events)', () => {
    const parentOwn = changeEvent('Service', 'team-a', 'web', { id: 'svc-own' })
    const keepChildEv = changeEvent('Deployment', 'team-a', 'web', { id: 'dep-ev' })
    const pinnedChildEv = changeEvent('Pod', 'team-a', 'web-1', { id: 'pod-ev' })
    const parent: ResourceLane = {
      id: 'Service/team-a/web', kind: 'Service', namespace: 'team-a', name: 'web', isWorkload: false,
      events: [parentOwn],
      children: [
        { id: 'Deployment/team-a/web', kind: 'Deployment', namespace: 'team-a', name: 'web', isWorkload: true, events: [keepChildEv], children: [] },
        { id: 'Pod/team-a/web-1', kind: 'Pod', namespace: 'team-a', name: 'web-1', isWorkload: true, events: [pinnedChildEv], children: [] },
      ],
      // Stale aggregate built WITH the pinned child included — the bug this guards.
      allEventsSorted: [parentOwn, keepChildEv, pinnedChildEv],
      childEventCount: 2,
    }
    const [pruned] = removePinnedLanes([parent], new Set(['Pod/team-a/web-1']))
    const ids = laneTrackEvents(pruned, false).map((e) => e.id) // collapsed → roll-up
    expect(ids).toContain('svc-own')
    expect(ids).toContain('dep-ev')
    expect(ids).not.toContain('pod-ev') // the pinned child's events must not double-paint
    expect(pruned.childEventCount).toBe(1)
  })
})

// --- group-qualified lane identity ------------------------------------------

describe('group-qualified lane identity', () => {
  const capiCluster = (over: Partial<TimelineEvent> = {}) =>
    changeEvent('Cluster', 'prod', 'main', { id: 'capi', apiVersion: 'cluster.x-k8s.io/v1beta1', ...over })
  const cnpgCluster = (over: Partial<TimelineEvent> = {}) =>
    changeEvent('Cluster', 'prod', 'main', { id: 'cnpg', apiVersion: 'postgresql.cnpg.io/v1', ...over })

  it('keeps two same-kind CRDs from different groups as SEPARATE lanes', () => {
    const lanes = buildResourceHierarchy({ events: [capiCluster(), cnpgCluster()], grouping: 'flat' })
    expect(lanes).toHaveLength(2)
    const ids = lanes.map((l) => l.id).sort()
    expect(ids).toEqual([
      'Cluster.cluster.x-k8s.io/prod/main',
      'Cluster.postgresql.cnpg.io/prod/main',
    ])
    // Each lane carries its own group.
    expect(new Set(lanes.map((l) => l.group))).toEqual(new Set(['cluster.x-k8s.io', 'postgresql.cnpg.io']))
  })

  it('keeps core/built-in resource ids bare (byte-stable)', () => {
    const lanes = buildResourceHierarchy({
      events: [
        changeEvent('Pod', 'team-a', 'x', { apiVersion: 'v1' }),
        changeEvent('Deployment', 'ns', 'web', { apiVersion: 'apps/v1' }),
      ],
      grouping: 'flat',
    })
    expect(lanes.map((l) => l.id).sort()).toEqual(['Deployment/ns/web', 'Pod/team-a/x'])
  })

  it('collidingLaneKeys flags a same-kind cross-group collision, nothing when unique', () => {
    const colliding = buildResourceHierarchy({ events: [capiCluster(), cnpgCluster()], grouping: 'flat' })
    const keys = collidingLaneKeys(colliding)
    expect(keys.has(laneCollisionKey({ kind: 'Cluster', namespace: 'prod', name: 'main' }))).toBe(true)

    const unique = buildResourceHierarchy({ events: [cnpgCluster()], grouping: 'flat' })
    expect(collidingLaneKeys(unique).size).toBe(0)
  })

  it('a CRD lane still joins its app via the group-less byResource key', () => {
    const db: AppMembership = { appKey: 'prod/app/shop', appName: 'shop', env: 'prod' }
    const appIndex = index({
      'Cluster/prod/main': db,          // group-less key, as AppRow workloads ship
      'Deployment/prod/web': db,
    })
    const lanes = buildResourceHierarchy({
      events: [cnpgCluster(), changeEvent('Deployment', 'prod', 'web', { apiVersion: 'apps/v1' })],
      grouping: 'app',
      appIndex,
    })
    // Both roots fold under one app-group header.
    expect(lanes).toHaveLength(1)
    expect(lanes[0].isAppGroup).toBe(true)
    const childIds = (lanes[0].children ?? []).map((c) => c.id).sort()
    expect(childIds).toEqual(['Cluster.postgresql.cnpg.io/prod/main', 'Deployment/prod/web'])
  })

  it('owner parenting reconciles a CRD owner across the qualified/bare boundary', () => {
    // A Pod owned by a CNPG Cluster: the owner ref carries no group, but it must
    // nest under the Cluster's group-qualified lane, not fork a bare duplicate.
    const lanes = buildResourceHierarchy({
      events: [
        cnpgCluster(),
        changeEvent('Pod', 'prod', 'main-1', { apiVersion: 'v1', owner: { kind: 'Cluster', name: 'main' } }),
      ],
      grouping: 'owner',
    })
    expect(lanes).toHaveLength(1)
    expect(lanes[0].id).toBe('Cluster.postgresql.cnpg.io/prod/main')
    expect((lanes[0].children ?? []).map((c) => c.id)).toEqual(['Pod/prod/main-1'])
  })

  it('ignores an old-format (group-less) CRD pin gracefully — no crash, empty ghost row', () => {
    const lanes = buildResourceHierarchy({ events: [cnpgCluster()], grouping: 'flat' })
    // Stale pin written before group-qualified ids existed: bare id, no match.
    const stale = { type: 'resource' as const, id: 'Cluster/prod/main', kind: 'Cluster', namespace: 'prod', name: 'main' }
    expect(isPinnedLaneRef(stale)).toBe(true) // still a well-formed record
    const pinned = extractPinnedLanes(lanes, [stale])
    expect(pinned).toHaveLength(1)
    expect(pinned[0].id).toBe('Cluster/prod/main')
    expect(pinned[0].events).toHaveLength(0) // synthesized empty — did not hijack the live lane
    // The live qualified lane is untouched by the stale pin.
    expect(lanes[0].id).toBe('Cluster.postgresql.cnpg.io/prod/main')
  })
})

describe('app membership outranks topology attachment (grouping=app)', () => {
  const crashloop: AppMembership = { appKey: 'radar-netdiag/Deployment/crashloop', appName: 'crashloop' }

  const exposesTopo = {
    nodes: [
      { id: 'service/radar-netdiag/crashloop', kind: 'Service', name: 'crashloop', data: { namespace: 'radar-netdiag' } },
      { id: 'deployment/radar-netdiag/crashloop', kind: 'Deployment', name: 'crashloop', data: { namespace: 'radar-netdiag' } },
    ],
    edges: [{ source: 'service/radar-netdiag/crashloop', target: 'deployment/radar-netdiag/crashloop', type: 'exposes' }],
  } as unknown as Topology

  it('keeps two same-app members SIBLINGS under the app header despite an exposes edge', () => {
    const events = [
      changeEvent('Service', 'radar-netdiag', 'crashloop'),
      changeEvent('Deployment', 'radar-netdiag', 'crashloop'),
    ]
    const appIndex = index({
      'Service/radar-netdiag/crashloop': crashloop,
      'Deployment/radar-netdiag/crashloop': crashloop,
    })
    const lanes = buildResourceHierarchy({ events, topology: exposesTopo, grouping: 'app', appIndex })

    expect(lanes).toHaveLength(1)
    expect(lanes[0].isAppGroup).toBe(true)
    expect(lanes[0].name).toBe('crashloop')
    const memberKinds = (lanes[0].children ?? []).map((c) => c.kind).sort()
    expect(memberKinds).toEqual(['Deployment', 'Service'])
    // Neither member nests under the other.
    for (const m of lanes[0].children ?? []) {
      expect((m.children ?? []).map((c) => c.kind)).not.toContain('Deployment')
    }
  })

  it('still parents the Deployment under the Service when they belong to DIFFERENT apps', () => {
    const other: AppMembership = { appKey: 'radar-netdiag/Service/other', appName: 'other' }
    const events = [
      changeEvent('Service', 'radar-netdiag', 'crashloop'),
      changeEvent('Deployment', 'radar-netdiag', 'crashloop'),
    ]
    const appIndex = index({
      'Service/radar-netdiag/crashloop': other,
      'Deployment/radar-netdiag/crashloop': crashloop,
    })
    const lanes = buildResourceHierarchy({ events, topology: exposesTopo, grouping: 'app', appIndex })

    const service = lanes.flatMap((l) => [l, ...(l.children ?? [])]).find((l) => l.kind === 'Service')
    expect((service?.children ?? []).some((c) => c.kind === 'Deployment')).toBe(true)
  })

  it('keeps exposes parenting when no app index is supplied (owner grouping)', () => {
    const events = [
      changeEvent('Service', 'radar-netdiag', 'crashloop'),
      changeEvent('Deployment', 'radar-netdiag', 'crashloop'),
    ]
    const lanes = buildResourceHierarchy({ events, topology: exposesTopo, grouping: 'owner' })
    const service = lanes.find((l) => l.kind === 'Service')
    expect((service?.children ?? []).some((c) => c.kind === 'Deployment')).toBe(true)
  })
})

describe('structural app members survive the window filter', () => {
  it('keeps a server-declared member visible with no events in the window', () => {
    const member: ResourceLane = {
      id: 'Service/radar-netdiag/crashloop', kind: 'Service', namespace: 'radar-netdiag',
      name: 'crashloop', events: [], isWorkload: false, structuralMember: true,
    }
    expect(isChildVisibleInWindow(member, 0, 1000, { pinned: false, userExpanded: false })).toBe(true)
  })

  it('still filters an event-less lane that is NOT a structural member', () => {
    const stray: ResourceLane = {
      id: 'Pod/radar-netdiag/old-pod', kind: 'Pod', namespace: 'radar-netdiag',
      name: 'old-pod', events: [], isWorkload: false,
    }
    expect(isChildVisibleInWindow(stray, 0, 1000, { pinned: false, userExpanded: false })).toBe(false)
  })
})
