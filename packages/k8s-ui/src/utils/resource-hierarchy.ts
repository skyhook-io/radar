/**
 * Shared resource hierarchy building logic.
 *
 * This module provides utilities for building hierarchical resource lanes from timeline events.
 * It's used by both TimelineSwimlanes (for the main timeline view) and WorkloadView
 * (for showing related events in the detail view).
 *
 * The hierarchy is built from:
 * 1. Owner references (event.owner) - most reliable for Deployment→RS→Pod chains
 * 2. Topology edges (Service→Deployment via 'exposes', Ingress→Service and Gateway→Route→Service via 'routes-to', etc.)
 * 3. App label grouping (app.kubernetes.io/name or app label)
 */

import type { TimelineEvent, Topology } from '../types/core'
import { isWorkloadKind } from '../types/core'
import { apiVersionToGroup, laneId, laneResourceKey, groupQualifiesLaneId, parseLaneId } from './navigation'
import type { AppMembership, AppMembershipIndex } from './applications'

/** Timeline grouping mode (replaces the legacy groupByApp boolean).
 *  - app   = owner/topology parenting + the app-membership cascade (default)
 *  - owner = owner/topology parenting only (honest name for groupByApp=false)
 *  - flat  = no parenting at all; every resource its own lane. K8s-event→owner
 *            attachment still applies (that's identity, not grouping). */
export type TimelineGrouping = 'app' | 'owner' | 'flat'

/**
 * Resource lane representing a single resource and its timeline events.
 * Can have child lanes for related resources (e.g., Deployment has ReplicaSet and Pod children).
 */
export interface ResourceLane {
  id: string
  kind: string
  /**
   * API group for the resource (e.g. "cluster.x-k8s.io"). Empty for core
   * resources. Needed to disambiguate CRDs whose kind collides with another
   * (e.g. CAPI Cluster vs CNPG Cluster) when the lane is clicked.
   */
  group?: string
  namespace: string
  name: string
  events: TimelineEvent[]
  isWorkload: boolean
  children?: ResourceLane[]
  childEventCount?: number
  allEventsSorted?: TimelineEvent[]
  /** App-group header lane (grouping='app'): a synthetic parent whose children
   *  are the member root lanes of one server-defined application. */
  isAppGroup?: boolean
  /** Stable per-AppRow key the members share (the server row key). */
  appKey?: string
  /** App display name shown on the header (== name, kept explicit for clarity). */
  title?: string
  /** This app instance's env token (dev|staging|prod|…), for the header chip. */
  env?: string
  /** Human-readable grouping evidence, for the header title tooltip. */
  evidence?: string
  /** A pinned app-group whose live group lane is absent from the current view
   *  (owner/flat grouping, or the app vanished): a header-only quiet row
   *  synthesized from the pin record, dimmed with a "not present" tooltip. */
  absentPinnedApp?: boolean
  /** Joined its app-group via the name-prefix fallback (cascade tier 2.5) rather
   *  than owner/topology/label evidence — the lane's generated name matched a
   *  member workload's name. The UI annotates this weaker join. */
  matchedByName?: boolean
  /** Tier-1 app member: the server's app identity DECLARES this resource (a
   *  workload or a related Service/Ingress/Route). Structural members stay
   *  visible even with no events in the window — hiding the app's own Service
   *  makes a matched app read as incomplete. Evidence/name-matched lanes
   *  (deleted pods, generated children) keep the window filter. */
  structuralMember?: boolean
  /** Reparented under a PRESENT parent lane via a Kubernetes naming contract
   *  (CronJob→Job schedule stamp, controller→Pod generateName tail,
   *  Deployment→ReplicaSet pod-template-hash) because the child's in-window
   *  events shipped ownerless (deletion-time cache miss). Establishes PARENTAGE —
   *  stronger than a name-stem membership match, weaker than a real ownerRef. The
   *  UI annotates the child. */
  nestedByContract?: boolean
}

/**
 * Options for building resource hierarchy.
 */
export interface HierarchyOptions {
  events: TimelineEvent[]
  topology?: Topology
  /** If provided, returns only the hierarchy rooted at this resource */
  rootResource?: { kind: string; namespace: string; name: string }
  /** Legacy toggle. Kept for back-compat (WorkloadView): true→'app', false→'owner'
   *  when `grouping` is omitted. Prefer `grouping`. */
  groupByApp?: boolean
  /** Grouping mode. Defaults from groupByApp (true→'app', false→'owner'). */
  grouping?: TimelineGrouping
  /** Server app-membership index. When present and grouping='app', the
   *  membership cascade replaces the legacy label grouping (which stays as the
   *  fallback when this is absent). */
  appIndex?: AppMembershipIndex
}

// Event-label keys scanned for app-membership evidence, in pkg/subject tier
// order (TierFluxHelmRelease 1 > TierArgoInstance 4 > TierInstance 6 >
// TierPartOf 7 > TierAppName 8 > TierBareApp 9 — pkg/subject/overlay.go). The
// prefix matches the server's matchKeys kinds (applications.go
// signalMatchKind), so an event's label value keys straight into
// AppMembershipIndex.byEvidence. Events carry labels (not annotations), so a
// native-Helm 'helm:' matchKey (meta.helm.sh/release-name, an annotation) is
// matchable only through the Flux HelmRelease label below.
const EVENT_EVIDENCE_LABELS: readonly (readonly [string, string])[] = [
  ['helm', 'helm.toolkit.fluxcd.io/name'],
  ['argo', 'argocd.argoproj.io/instance'],
  ['instance', 'app.kubernetes.io/instance'],
  ['part-of', 'app.kubernetes.io/part-of'],
  ['name', 'app.kubernetes.io/name'],
  ['app', 'app'],
]

/** Distinct evidence keys ("kind:namespace:value") a lane's events carry,
 *  highest tier first — the cascade's tier-2 lookup keys. Keys are
 *  namespace-scoped with the EVENT's own namespace so a shared label value in
 *  two namespaces can't cross-join to the wrong app (matches the server's
 *  scoped matchKeys form). */
function evidenceKeysForLane(lane: ResourceLane): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  const events = lane.allEventsSorted ?? lane.events
  for (const [prefix, labelKey] of EVENT_EVIDENCE_LABELS) {
    for (const e of events) {
      const value = e.labels?.[labelKey]
      if (!value) continue
      const key = `${prefix}:${e.namespace}:${value}`
      if (seen.has(key)) continue
      seen.add(key)
      out.push(key)
    }
  }
  return out
}

interface RootGroupAssignment {
  membership: AppMembership
  /** Set when the join came from the name-prefix fallback (tier 2.5) — a weaker
   *  match the UI annotates. */
  matchedByName?: boolean
  /** Set for tier-1 joins — the server's app identity declares this resource. */
  structural?: boolean
}

// Workload kinds whose names a generated-name lane can prefix-match against.
// These are the app members that spawn hash-suffixed children (a Deployment owns
// ReplicaSets owns Pods; a Job owns Pods; a CronJob owns Jobs).
const NAME_PREFIX_MEMBER_KINDS = new Set([
  'Deployment', 'StatefulSet', 'DaemonSet', 'ReplicaSet', 'Job', 'CronJob', 'Rollout',
])

// Lane kinds that carry K8s generated names (parent + hash suffix) and so are
// safe to prefix-match. A Service/ConfigMap named like a member is a real name
// collision, NOT a generated child — never prefix-match those.
const GENERATED_NAME_LANE_KINDS = new Set(['Pod', 'ReplicaSet', 'Job'])

// One hash-ish suffix segment: a ReplicaSet pod-template-hash, a Pod's 5-char
// random suffix, or a Job's timestamp/hash. 4-11 lowercase alphanumerics.
const NAME_SUFFIX_SEGMENT = /^[a-z0-9]{4,11}$/

/** True when `laneName` looks like a K8s generated child of `memberName`:
 *  `memberName` + '-' + a suffix of 1 or 2 hash-ish segments. Each segment must
 *  match NAME_SUFFIX_SEGMENT AND the suffix must contain at least one digit — the
 *  digit gate is what separates a real hash ("6dc59db657", "6jm4n") from a word
 *  variant ("canary", "stable"), which the plain length regex would otherwise
 *  accept. K8s generated names always carry a digit (pod-template-hash, job
 *  timestamps), so requiring one anywhere in the suffix is safe. */
function isGeneratedNameChildOf(laneName: string, memberName: string): boolean {
  const prefix = memberName + '-'
  if (!laneName.startsWith(prefix)) return false
  const suffix = laneName.slice(prefix.length)
  if (!suffix) return false
  const segs = suffix.split('-')
  if (segs.length < 1 || segs.length > 2) return false
  if (!segs.every((s) => NAME_SUFFIX_SEGMENT.test(s))) return false
  return /[0-9]/.test(suffix)
}

/** Match a generated-name lane against the app-membership index by prefixing its
 *  name with a known workload member's name (cascade tier 2.5). Catches lanes
 *  that fail every other tier — e.g. a Pod whose deletion-time event shipped with
 *  owner:null + labels:null (connector cache miss) but whose name still encodes
 *  its Deployment. Scoped to the SAME namespace; only workload-ish member kinds
 *  count. Longest member-name wins (so "web-admin" beats "web" for a
 *  web-admin-xyz pod); ties resolve to the first key in index order. */
export function matchLaneByMemberNamePrefix(
  lane: { kind: string; namespace: string; name: string },
  index: AppMembershipIndex,
): AppMembership | null {
  if (!GENERATED_NAME_LANE_KINDS.has(lane.kind)) return null
  let best: AppMembership | null = null
  let bestLen = -1
  for (const [key, membership] of index.byResource) {
    // key form: 'Kind/namespace/name'. Namespaces + names can't contain '/', so
    // the first two slashes bound the kind and namespace segments.
    const slash1 = key.indexOf('/')
    if (slash1 < 0) continue
    const slash2 = key.indexOf('/', slash1 + 1)
    if (slash2 < 0) continue
    const kind = key.slice(0, slash1)
    if (!NAME_PREFIX_MEMBER_KINDS.has(kind)) continue
    const ns = key.slice(slash1 + 1, slash2)
    if (ns !== lane.namespace) continue
    const memberName = key.slice(slash2 + 1)
    if (!isGeneratedNameChildOf(lane.name, memberName)) continue
    if (memberName.length > bestLen) {
      best = membership
      bestLen = memberName.length
    }
  }
  return best
}

// Parent-driven naming contracts. Confidence ladder: ownerRef (fact) >
// kind-contract naming (strong) > name-stem (weak, membership only, never
// parentage). Kubernetes controllers name their children deterministically, so a
// PRESENT parent lane of the contract kind lets us re-derive an edge for a child
// whose only in-window events shipped ownerless (a deletion-time cache miss drops
// owner + labels). Each contract fixes the child kind, the acceptable parent
// kinds, and the single generated-name suffix segment K8s appends.
interface KindContract {
  childKind: string
  /** Candidate parent kinds, in match preference (a Pod's Job beats its RS). */
  parentKinds: readonly string[]
  /** The one trailing name segment this contract appends (no hyphens — the
   *  boundary hyphen separating it from the parent stem is the ONLY hyphen). */
  suffix: RegExp
}
const KIND_CONTRACTS: readonly KindContract[] = [
  // CronJob names a Job `<cronjob>-<schedule-stamp>`; the stamp is strictly numeric.
  { childKind: 'Job', parentKinds: ['CronJob'], suffix: /^\d+$/ },
  // A controller (Job/ReplicaSet) names a Pod `<parent>-<5-char generateName tail>`.
  { childKind: 'Pod', parentKinds: ['Job', 'ReplicaSet'], suffix: /^[a-z0-9]{5}$/ },
  // A Deployment names a ReplicaSet `<deploy>-<8-10 char pod-template-hash>`.
  { childKind: 'ReplicaSet', parentKinds: ['Deployment'], suffix: /^[a-z0-9]{8,10}$/ },
]

/** True when walking `from` up the parent chain reaches `target` — i.e. `from`
 *  sits inside `target`'s subtree. Guards contract nesting against cycles: never
 *  reparent a lane under one of its own descendants. */
function chainReaches(from: string, target: string, laneParent: Map<string, string>): boolean {
  let cur: string | undefined = from
  const seen = new Set<string>()
  while (cur && !seen.has(cur)) {
    if (cur === target) return true
    seen.add(cur)
    cur = laneParent.get(cur)
  }
  return false
}

/** The parent lane id for a still-at-root lane derivable from a Kubernetes naming
 *  contract, or null. Splits the name on its LAST hyphen: the suffix shapes forbid
 *  hyphens, so that's the only decomposition that can match and it yields the
 *  longest/exact parent stem. The parent lane must already exist, share the
 *  namespace, and be a contract-appropriate kind; a two-hyphen name resolves to
 *  the exact present parent (`a-b-123` → CronJob `a-b`, never `a`). */
function contractParentId(
  lane: ResourceLane,
  laneMap: Map<string, ResourceLane>,
  laneParent: Map<string, string>,
  laneIdByResourceKey: Map<string, string>,
): string | null {
  const contract = KIND_CONTRACTS.find((c) => c.childKind === lane.kind)
  if (!contract) return null
  const cut = lane.name.lastIndexOf('-')
  if (cut <= 0 || cut >= lane.name.length - 1) return null
  const parentName = lane.name.slice(0, cut)
  const suffix = lane.name.slice(cut + 1)
  if (!contract.suffix.test(suffix)) return null
  for (const parentKind of contract.parentKinds) {
    // Resolve the parent's canonical (possibly group-qualified) lane id via the
    // group-less registry — contract parent kinds are built-in, but going through
    // the registry keeps this correct regardless of id shape.
    const parentId = laneIdByResourceKey.get(laneResourceKey(parentKind, lane.namespace, parentName))
    if (!parentId || parentId === lane.id) continue
    if (!laneMap.has(parentId)) continue
    if (chainReaches(parentId, lane.id, laneParent)) continue
    return parentId
  }
  return null
}

/** The app-membership cascade for one lane ROOT (post owner/topology parenting):
 *  1. byResource — the root IS a known workload/satellite.
 *  2. byEvidence — the root's events carry a label matching a known app (catches
 *     resources DELETED before the snapshot; the live app still owns the key).
 *  2.5 name-prefix — a generated-name lane (Pod/ReplicaSet/Job) whose name
 *     prefixes a known member workload's name in the same namespace (catches
 *     deletion-time events shipped with owner:null + labels:null).
 *  fallback — null; the root renders on its own (owner grouping / ungrouped).
 *
 *  The server is the ONLY authority on what an application is: grouping keys off
 *  server-declared apps, never client-synthesized groups from raw event labels
 *  (those would be a second grouping definition, producing meaningless singleton
 *  groups for any label-bearing resource). Deleted MEMBERS of a live app still
 *  group via tier 2 (the live app owns the matchKey); whole-deleted-app history
 *  falls back to owner grouping until hub-side membership snapshots (the
 *  retention-grade fix) exist. */
function cascadeRootMembership(lane: ResourceLane, appIndex: AppMembershipIndex): RootGroupAssignment | null {
  // byResource is keyed group-less (AppRow workloads carry no group), so a
  // CRD lane's group-qualified id must join via its group-less resource key.
  const direct = appIndex.byResource.get(laneResourceKey(lane.kind, lane.namespace, lane.name))
  if (direct) return { membership: direct, structural: true }
  for (const key of evidenceKeysForLane(lane)) {
    const m = appIndex.byEvidence.get(key)
    if (m) return { membership: m }
  }
  const byName = matchLaneByMemberNamePrefix(lane, appIndex)
  if (byName) return { membership: byName, matchedByName: true }
  return null
}

/** Build an app-group header lane over its member root lanes. The members keep
 *  their own sub-hierarchy; the header aggregates their events for the collapsed
 *  track and the interestingness sort. */
function makeAppGroupLane(m: AppMembership, members: ResourceLane[]): ResourceLane {
  const allEvents = members.flatMap((c) => c.allEventsSorted ?? c.events)
  const uniqueEvents = Array.from(new Map(allEvents.map((e) => [e.id, e])).values())
  const nss = new Set(members.map((c) => c.namespace).filter(Boolean))
  return {
    id: `app:${m.appKey}`,
    kind: 'AppGroup',
    namespace: nss.size === 1 ? [...nss][0] : '',
    name: m.appName,
    events: [],
    isWorkload: false,
    isAppGroup: true,
    appKey: m.appKey,
    title: m.appName,
    env: m.env,
    evidence: m.evidence,
    children: members,
    childEventCount: members.reduce((n, c) => n + (c.allEventsSorted?.length ?? c.events.length), 0),
    allEventsSorted: sortEventsForRendering(uniqueEvents),
  }
}

/** Wrap top-level root lanes into app-group header lanes via the cascade. A group
 *  needs ≥2 member roots to earn a header (a single-resource app renders as its
 *  plain lane, same as today); headers emit at their first member's position so
 *  the caller's interestingness sort still governs placement. */
function applyAppGrouping(topLevelLanes: ResourceLane[], appIndex: AppMembershipIndex): ResourceLane[] {
  const assignment = new Map<string, RootGroupAssignment>()
  const membersByApp = new Map<string, ResourceLane[]>()
  for (const lane of topLevelLanes) {
    const g = cascadeRootMembership(lane, appIndex)
    if (!g) continue
    // Mark the lane so the UI can annotate the weaker name-prefix join. Only
    // surfaced once the lane actually lands in a ≥2-member group (below).
    if (g.matchedByName) lane.matchedByName = true
    if (g.structural) lane.structuralMember = true
    assignment.set(lane.id, g)
    const arr = membersByApp.get(g.membership.appKey) ?? []
    arr.push(lane)
    membersByApp.set(g.membership.appKey, arr)
  }
  const out: ResourceLane[] = []
  const emitted = new Set<string>()
  for (const lane of topLevelLanes) {
    const g = assignment.get(lane.id)
    const members = g ? membersByApp.get(g.membership.appKey) : undefined
    if (!g || !members || members.length < 2) {
      out.push(lane)
      continue
    }
    if (emitted.has(g.membership.appKey)) continue
    emitted.add(g.membership.appKey)
    out.push(makeAppGroupLane(g.membership, members))
  }
  return out
}

/**
 * Event reasons that indicate problems even if eventType is "Normal"
 * Comprehensive list based on Kubernetes source code and documentation
 */
const PROBLEMATIC_REASONS = new Set([
  // Container state issues
  'BackOff', 'CrashLoopBackOff', 'Failed', 'Error',
  'OOMKilling', 'OOMKilled',
  'CreateContainerConfigError', 'CreateContainerError', 'RunContainerError',
  'InvalidImageName', 'ErrImagePull', 'ImagePullBackOff',
  'ContainerStatusUnknown',

  // Pod scheduling/lifecycle issues
  'FailedScheduling', 'FailedMount', 'FailedAttachVolume',
  'FailedCreate', 'FailedDelete', 'Unhealthy', 'Killing', 'Evicted',
  'FailedSync', 'FailedValidation',
  'FailedPreStopHook', 'FailedPostStartHook',
  'HostPortConflict', 'InsufficientMemory', 'InsufficientCPU',

  // Node conditions
  'NodeNotReady', 'NetworkNotReady', 'KubeletNotReady',
  'MemoryPressure', 'DiskPressure', 'PIDPressure',
  'NodeStatusUnknown',

  // Deployment/workload issues
  'ProgressDeadlineExceeded', 'ReplicaFailure',
  'MinimumReplicasUnavailable',

  // HPA issues
  'FailedGetScale', 'FailedRescale', 'FailedUpdateScale',
  'FailedGetResourceMetric', 'FailedComputeMetricsReplicas',

  // PVC/storage issues
  'ProvisioningFailed', 'FailedBinding', 'VolumeFailedDelete',

  // Job issues
  'DeadlineExceeded', 'BackoffLimitExceeded',
])

/**
 * Check if an event is problematic (warning or error condition)
 */
export function isProblematicEvent(event: TimelineEvent): boolean {
  if (event.eventType === 'Warning') return true
  if (event.reason && PROBLEMATIC_REASONS.has(event.reason)) return true
  return false
}

/**
 * Convert topology node ID to lane ID format.
 * Node IDs are formatted as: kind/namespace/name (e.g., "pod/default/nginx-abc123")
 * Lane IDs are formatted as: Kind/namespace/name (e.g., "Pod/default/nginx-abc123")
 */
function nodeIdToLaneId(nodeId: string): string | null {
  const parts = nodeId.split('/')
  if (parts.length < 3) return null
  const kind = parts[0]
  const namespace = parts[1]
  const name = parts[2]
  // Maps lowercase topology node IDs to PascalCase kind names used in timeline lane IDs.
  const kindMap: Record<string, string> = {
    pod: 'Pod', service: 'Service', deployment: 'Deployment',
    replicaset: 'ReplicaSet', statefulset: 'StatefulSet', daemonset: 'DaemonSet',
    ingress: 'Ingress', gateway: 'Gateway', httproute: 'HTTPRoute',
    grpcroute: 'GRPCRoute', tcproute: 'TCPRoute', tlsroute: 'TLSRoute',
    configmap: 'ConfigMap', secret: 'Secret',
    persistentvolumeclaim: 'PersistentVolumeClaim',
    persistentvolume: 'PersistentVolume', storageclass: 'StorageClass',
    job: 'Job', cronjob: 'CronJob',
    horizontalpodautoscaler: 'HorizontalPodAutoscaler',
    verticalpodautoscaler: 'VerticalPodAutoscaler',
    poddisruptionbudget: 'PodDisruptionBudget',
    podgroup: 'PodGroup', rollout: 'Rollout', namespace: 'Namespace',
    node: 'Node',
    application: 'Application', applicationset: 'ApplicationSet', appproject: 'AppProject',
    kustomization: 'Kustomization',
    helmrelease: 'HelmRelease', helmrepository: 'HelmRepository',
    helmchart: 'HelmChart', gitrepository: 'GitRepository',
    ocirepository: 'OCIRepository', certificate: 'Certificate',
    // Istio
    virtualservice: 'VirtualService', destinationrule: 'DestinationRule',
    istiogateway: 'IstioGateway', serviceentry: 'ServiceEntry',
    peerauthentication: 'PeerAuthentication', authorizationpolicy: 'AuthorizationPolicy',
    // KEDA
    scaledobject: 'ScaledObject', scaledjob: 'ScaledJob',
    // Karpenter
    nodepool: 'NodePool', nodeclaim: 'NodeClaim',
    // cert-manager
    issuer: 'Issuer', clusterissuer: 'ClusterIssuer',
    // Knative
    knativeservice: 'KnativeService', knativeconfiguration: 'KnativeConfiguration',
    knativerevision: 'KnativeRevision', knativeroute: 'KnativeRoute',
    broker: 'Broker', trigger: 'Trigger', channel: 'Channel',
    pingsource: 'PingSource', apiserversource: 'ApiServerSource',
    containersource: 'ContainerSource', sinkbinding: 'SinkBinding',
    // Traefik
    ingressroute: 'IngressRoute', ingressroutetcp: 'IngressRouteTCP',
    ingressrouteudp: 'IngressRouteUDP', middleware: 'Middleware',
    middlewaretcp: 'MiddlewareTCP', traefikservice: 'TraefikService',
    serverstransport: 'ServersTransport', serverstransporttcp: 'ServersTransportTCP',
    tlsoption: 'TLSOption', tlsstore: 'TLSStore',
    // Contour
    httpproxy: 'HTTPProxy',
  }
  return `${kindMap[kind] || kind}/${namespace}/${name}`
}

/**
 * Sort events for rendering (important events render on top).
 * Priority: updates (0) < adds (1) < deletes (2) < problematic/warnings (3)
 * Lower priority renders first (behind), higher renders last (on top)
 */
function sortEventsForRendering(events: TimelineEvent[]): TimelineEvent[] {
  return [...events].sort((a, b) => {
    const getPriority = (e: TimelineEvent) => {
      if (isProblematicEvent(e)) return 3
      if (e.eventType === 'delete') return 2
      if (e.eventType === 'add') return 1
      return 0 // updates and others
    }
    return getPriority(a) - getPriority(b)
  })
}

/**
 * Build hierarchical resource lanes from timeline events.
 *
 * This function groups events by resource, establishes parent-child relationships,
 * and returns a flat list of top-level lanes (each with nested children).
 *
 * @param options - Configuration options
 * @returns Array of top-level resource lanes with nested children
 */
// Child ordering within a lane: representative kinds first (Service/Ingress),
// then workloads, then their generated children (ReplicaSet/Pod), config last.
// Applied at EVERY depth so a Job's Pods sort the same way a root's children do.
const CHILD_SORT_KIND_PRIORITY: Record<string, number> = {
  Service: 1, Gateway: 1, HTTPRoute: 2, GRPCRoute: 2, TCPRoute: 2, TLSRoute: 2,
  Deployment: 2, Rollout: 2, StatefulSet: 2, DaemonSet: 2,
  Job: 3, CronJob: 3,
  ReplicaSet: 3, Pod: 4, ConfigMap: 5, Secret: 5,
  KnativeService: 1, KnativeRoute: 2, Broker: 1, Channel: 1,
  KnativeConfiguration: 2, KnativeRevision: 3, Trigger: 2,
  PingSource: 3, ApiServerSource: 3, ContainerSource: 3, SinkBinding: 3,
  IngressRoute: 1, IngressRouteTCP: 1, IngressRouteUDP: 1,
  TraefikService: 2, Middleware: 3, MiddlewareTCP: 3,
  HTTPProxy: 1, // Contour
}

/** Sort a lane's children by kind priority then most-recent-event, recursing into
 *  every descendant so a multi-level chain (CronJob → Job → Pod) is ordered at
 *  each depth, not just the top level. */
function sortLaneChildrenDeep(lane: ResourceLane): void {
  const children = lane.children
  if (!children || children.length === 0) return
  // Precompute each child's latest event time once — otherwise the comparator
  // reparses every child's timestamps on every comparison.
  const latestByChildId = new Map<string, number>()
  for (const c of children) {
    let latest = 0
    for (const e of c.events) {
      const t = new Date(e.timestamp).getTime()
      if (t > latest) latest = t
    }
    latestByChildId.set(c.id, latest)
  }
  children.sort((a, b) => {
    const aPriority = CHILD_SORT_KIND_PRIORITY[a.kind] || 10
    const bPriority = CHILD_SORT_KIND_PRIORITY[b.kind] || 10
    if (aPriority !== bPriority) return aPriority - bPriority
    return (latestByChildId.get(b.id) ?? 0) - (latestByChildId.get(a.id) ?? 0)
  })
  for (const c of children) sortLaneChildrenDeep(c)
}

export function buildResourceHierarchy(options: HierarchyOptions): ResourceLane[] {
  const { events, topology, rootResource, appIndex } = options
  // grouping defaults from the legacy boolean: true (or unset) → 'app', false → 'owner'.
  const grouping: TimelineGrouping = options.grouping ?? (options.groupByApp === false ? 'owner' : 'app')
  const laneMap = new Map<string, ResourceLane>()

  // API group lookup by group-less resource key (Kind/ns/name) sourced from
  // topology nodes — the fallback group for lanes/refs whose events don't carry
  // apiVersion (owner refs and topology endpoints never do).
  const topoGroupByKey = new Map<string, string>()
  if (topology?.nodes) {
    for (const node of topology.nodes) {
      const key = nodeIdToLaneId(node.id)
      if (!key) continue
      const group = apiVersionToGroup(node.data?.apiVersion as string | undefined)
      if (group) topoGroupByKey.set(key, group)
    }
  }
  // First apiVersion-derived group seen per resource key — the fallback used to
  // attribute a same-resource event that shipped WITHOUT apiVersion (a stored
  // event) onto the group-qualified lane its apiVersion-carrying siblings built,
  // instead of forking a bare duplicate. It does NOT collapse a genuine
  // collision: colliding events differ in apiVersion, so each still qualifies to
  // its own group; only truly group-less events fall back here.
  const groupByKey = new Map<string, string>()
  for (const event of events) {
    if (event.kind === 'Event' && event.owner) continue
    const g = apiVersionToGroup(event.apiVersion)
    if (g) {
      const rk = laneResourceKey(event.kind, event.namespace, event.name)
      if (!groupByKey.has(rk)) groupByKey.set(rk, g)
    }
  }
  // The group for a resource key when the reference itself carries none.
  const fallbackGroup = (rk: string): string => groupByKey.get(rk) ?? topoGroupByKey.get(rk) ?? ''

  // Group-less resource key → canonical (possibly group-qualified) lane id. Lets
  // group-less references (owner refs, K8s-event involvedObject, topology node
  // ids — none carry a group) reconcile onto the real lane instead of forking a
  // bare duplicate. First writer wins: when two CRDs collide on kind+ns+name the
  // map holds one, and a group-agnostic owner/topology match resolving to it is
  // the accepted residual (see the confidence-ladder note above).
  const laneIdByKey = new Map<string, string>()
  const registerLaneKey = (rk: string, id: string): void => {
    if (!laneIdByKey.has(rk)) laneIdByKey.set(rk, id)
  }
  // Get-or-create the lane for a group-less reference, returning its canonical id.
  const ensureRefLane = (rk: string, seedEvent?: TimelineEvent): string => {
    const existing = laneIdByKey.get(rk)
    if (existing) {
      if (seedEvent) laneMap.get(existing)!.events.push(seedEvent)
      return existing
    }
    const p = parseLaneId(rk)!
    const group = fallbackGroup(rk)
    const id = laneId(p.kind, group, p.namespace, p.name)
    laneMap.set(id, {
      id,
      kind: p.kind,
      group,
      namespace: p.namespace,
      name: p.name,
      events: seedEvent ? [seedEvent] : [],
      isWorkload: isWorkloadKind(p.kind),
      children: [],
      childEventCount: 0,
    })
    registerLaneKey(rk, id)
    return id
  }
  // The canonical id a group-less key resolves to, WITHOUT creating a lane —
  // agrees with ensureRefLane so guards keyed by canonical id line up.
  const resolveId = (rk: string): string => {
    const existing = laneIdByKey.get(rk)
    if (existing) return existing
    const p = parseLaneId(rk)!
    return laneId(p.kind, fallbackGroup(rk), p.namespace, p.name)
  }

  // Track events that should be attached to their owner instead of their own lane
  const eventsToAttach: { event: TimelineEvent; ownerKey: string }[] = []

  // First pass: create lanes from events (but not for Events with owners)
  for (const event of events) {
    // K8s Events with an owner (involvedObject) should attach to that resource, not get their own lane
    if (event.kind === 'Event' && event.owner) {
      eventsToAttach.push({ event, ownerKey: laneResourceKey(event.owner.kind, event.namespace, event.owner.name) })
      continue
    }

    const rk = laneResourceKey(event.kind, event.namespace, event.name)
    // Per-event group qualifies the id, so two same-kind CRDs from different
    // vendors (CAPI vs CNPG `Cluster`) never merge. A group-less event reconciles
    // onto whatever lane already exists for the resource (or a bare one).
    const group = apiVersionToGroup(event.apiVersion) || fallbackGroup(rk)
    const id = groupQualifiesLaneId(group)
      ? laneId(event.kind, group, event.namespace, event.name)
      : (laneIdByKey.get(rk) ?? laneResourceKey(event.kind, event.namespace, event.name))
    const existing = laneMap.get(id)
    if (!existing) {
      laneMap.set(id, {
        id,
        kind: event.kind,
        group,
        namespace: event.namespace,
        name: event.name,
        events: [event],
        isWorkload: isWorkloadKind(event.kind),
        children: [],
        childEventCount: 0,
      })
      registerLaneKey(rk, id)
    } else {
      if (!existing.group && group) existing.group = group
      existing.events.push(event)
    }
  }

  // Attach K8s Events to their owner lanes (group-agnostic — the involvedObject
  // ref carries no group, so it resolves onto the first lane for that key).
  for (const { event, ownerKey } of eventsToAttach) {
    ensureRefLane(ownerKey, event)
  }

  // Build parent map from BOTH owner references AND topology edges
  const laneParent = new Map<string, string>() // childLaneId -> parentLaneId

  // Flat mode skips ALL parenting (sources 1-3): every resource is its own lane.
  // The K8s-event→owner attachment above still applies — that's identity, not
  // grouping, so a Pod event stays on its Pod's lane regardless.
  if (grouping !== 'flat') {

  // Source 1: Owner references from events (most reliable for Deployment→RS→Pod).
  // Owner refs carry no group, so parenting is group-agnostic: the child resolves
  // onto the FIRST lane registered for the owner's key. The residual (same
  // kind+name+ns colliding across groups within an ownership match) is accepted —
  // see the confidence-ladder note above the KIND_CONTRACTS block.
  for (const [id, lane] of laneMap) {
    const eventWithOwner = lane.events.find(e => e.owner)
    if (eventWithOwner?.owner) {
      const ownerId = ensureRefLane(laneResourceKey(eventWithOwner.owner.kind, lane.namespace, eventWithOwner.owner.name))
      laneParent.set(id, ownerId)
    }
  }

  // Source 2: Topology edges (for Service→Deployment, Ingress→Service, ConfigMap→Deployment).
  // Node ids are group-less keys; endpoints resolve onto their canonical lanes via
  // the registry (parenting a child under a possibly group-qualified parent), and a
  // missing endpoint is materialized with its topology-known group.
  if (topology?.edges) {
    for (const edge of topology.edges) {
      const sourceKey = nodeIdToLaneId(edge.source)
      const targetKey = nodeIdToLaneId(edge.target)
      if (!sourceKey || !targetKey) continue

      // manages: Deployment→RS→Pod (already covered by owner refs, skip)
      if (edge.type === 'manages') continue

      // At least one side must have events
      const sourceExists = laneIdByKey.has(sourceKey)
      const targetExists = laneIdByKey.has(targetKey)
      if (!sourceExists && !targetExists) continue

      // App membership outranks topology attachment: two members of the SAME
      // application (its Service and its Deployment) are siblings under the
      // app's group header. Parenting one member under the other would swallow
      // a root, drop the group below the 2-member header threshold, and front
      // the whole stack with the attachment (a health-less Service) instead of
      // the app. Only the byResource tier applies — both endpoints here are
      // concrete resources the server either claims or doesn't.
      const sameAppMembers = (aKey: string, bKey: string): boolean => {
        if (grouping !== 'app' || !appIndex) return false
        const toResourceKey = (key: string): string | null => {
          const parsed = parseLaneId(key)
          return parsed ? laneResourceKey(parsed.kind, parsed.namespace, parsed.name) : null
        }
        const aRes = toResourceKey(aKey)
        const a = aRes ? appIndex.byResource.get(aRes) : undefined
        if (!a) return false
        const bRes = toResourceKey(bKey)
        const b = bRes ? appIndex.byResource.get(bRes) : undefined
        return b != null && a.appKey === b.appKey
      }

      // Parent `child` under `parent`, materializing `parent`'s lane if absent.
      // `childHasEvents` mirrors the old guard (only parent an endpoint that
      // actually exists). Guards read canonical ids so laneParent lines up.
      const link = (childKey: string, parentKey: string, childHasEvents: boolean): void => {
        if (!childHasEvents) return
        if (sameAppMembers(childKey, parentKey)) return
        const childId = resolveId(childKey)
        if (laneParent.has(childId)) return
        laneParent.set(childId, ensureRefLane(parentKey))
      }

      // exposes: Service→Deployment (Service is parent of Deployment)
      if (edge.type === 'exposes') {
        link(targetKey, sourceKey, targetExists)
      }

      // routes-to has two cases:
      // 1. Ingress→Service: Service should be parent (representative)
      // 2. Service→Pod/PodGroup: Service should be parent (normal hierarchy)
      if (edge.type === 'routes-to') {
        const sourceKind = sourceKey.split('/')[0]
        const targetKind = targetKey.split('/')[0]

        // Gateway→Route: Gateway is parent of Route
        if (sourceKind === 'Gateway' && (targetKind === 'HTTPRoute' || targetKind === 'GRPCRoute' || targetKind === 'TCPRoute' || targetKind === 'TLSRoute')) {
          link(targetKey, sourceKey, targetExists)
        }
        // Route→Service: reverse (Service is representative, like Ingress)
        else if ((sourceKind === 'HTTPRoute' || sourceKind === 'GRPCRoute' || sourceKind === 'TCPRoute' || sourceKind === 'TLSRoute') && targetKind === 'Service') {
          link(sourceKey, targetKey, sourceExists)
        }
        // Ingress→Service: reverse relationship (Service is representative)
        else if (sourceKind === 'Ingress' && targetKind === 'Service') {
          link(sourceKey, targetKey, sourceExists)
        }
        // Service→Pod/PodGroup: normal direction (Service is parent)
        else if (sourceKind === 'Service') {
          link(targetKey, sourceKey, targetExists)
        }
      }

      // configures/uses/protects: ConfigMap→Deployment, HPA→Deployment, PDB→Deployment (target is parent)
      if (edge.type === 'configures' || edge.type === 'uses' || edge.type === 'protects') {
        link(sourceKey, targetKey, sourceExists)
      }
    }
  }

  } // end grouping !== 'flat' (sources 1 & 2)

  // Source 3 (legacy fallback): raw app-label grouping. Runs only in 'app' mode
  // WITHOUT a server app-membership index — when the index is present the
  // membership cascade below replaces this. Kept so WorkloadView and index-less
  // callers keep today's behavior.
  if (grouping === 'app' && !appIndex && topology?.nodes) {
    // Keyed by group-less resource key (the topology node id form), so a CRD
    // lane's group-qualified id still matches via its key.
    const laneAppLabels = new Map<string, string>()
    for (const node of topology.nodes) {
      const key = nodeIdToLaneId(node.id)
      if (!key) continue
      const labels = node.data?.labels as Record<string, string> | undefined
      const appLabel = labels?.['app.kubernetes.io/name'] || labels?.['app']
      if (appLabel) {
        laneAppLabels.set(key, appLabel)
      }
    }

    // Only include primary resource kinds in app label grouping
    const appLabelEligibleKinds = new Set([
      'Service', 'Deployment', 'Rollout', 'StatefulSet', 'DaemonSet',
      'Job', 'CronJob', 'Ingress', 'Gateway', 'HTTPRoute', 'GRPCRoute',
      'TCPRoute', 'TLSRoute', 'ConfigMap', 'Secret',
      'Application', 'Kustomization', 'HelmRelease', 'GitRepository',
      'Workflow', 'CronWorkflow',
      'KnativeService', 'KnativeConfiguration', 'KnativeRevision', 'KnativeRoute',
      'Broker', 'Trigger',
      'IngressRoute', 'IngressRouteTCP', 'IngressRouteUDP',
      'HTTPProxy', // Contour
    ])

    // Group lanes by app label, scoped to namespace: the same app label in two
    // namespaces (e.g. the same workload deployed to dev and staging) is two
    // distinct apps and must not collapse into one lane.
    const appGroups = new Map<string, string[]>()
    for (const [id, lane] of laneMap) {
      if (laneParent.has(id)) continue
      if (!appLabelEligibleKinds.has(lane.kind)) continue
      const appLabel = laneAppLabels.get(laneResourceKey(lane.kind, lane.namespace, lane.name))
      if (!appLabel) continue
      const groupKey = `${lane.namespace}/${appLabel}`
      if (!appGroups.has(groupKey)) {
        appGroups.set(groupKey, [])
      }
      appGroups.get(groupKey)!.push(id)
    }

    // For each app group with multiple members, pick the best parent
    for (const [, laneIds] of appGroups) {
      if (laneIds.length < 2) continue

      const kindPriority: Record<string, number> = {
        Service: 1, Ingress: 2, Gateway: 2,
        HTTPRoute: 2, GRPCRoute: 2, TCPRoute: 2, TLSRoute: 2,
        Deployment: 3, Rollout: 3, StatefulSet: 3, DaemonSet: 3,
        Job: 4, CronJob: 4, Workflow: 4, CronWorkflow: 4,
        ConfigMap: 5, Secret: 5,
        ReplicaSet: 6, Pod: 7,
        KnativeService: 1, KnativeRoute: 2, Broker: 2, Channel: 2,
        KnativeConfiguration: 3, KnativeRevision: 4, Trigger: 3,
        PingSource: 3, ApiServerSource: 3, ContainerSource: 3, SinkBinding: 3,
        IngressRoute: 2, IngressRouteTCP: 2, IngressRouteUDP: 2,
        TraefikService: 3, Middleware: 4, MiddlewareTCP: 4,
        HTTPProxy: 2, // Contour
      }

      const sorted = [...laneIds].sort((a, b) => {
        const aLane = laneMap.get(a)!
        const bLane = laneMap.get(b)!
        const aPriority = kindPriority[aLane.kind] || 10
        const bPriority = kindPriority[bLane.kind] || 10
        return aPriority - bPriority
      })

      const parentLaneId = sorted[0]
      for (let i = 1; i < sorted.length; i++) {
        const childLaneId = sorted[i]
        if (!laneParent.has(childLaneId)) {
          laneParent.set(childLaneId, parentLaneId)
        }
      }
    }
  }

  // Source 1.5: parent-driven naming contracts. A lane still at root whose only
  // in-window events were ownerless can still nest under a PRESENT parent lane
  // when its generated name encodes the parent (see KIND_CONTRACTS) — e.g. an old
  // CronJob run whose deletion event lost its owner ref. Runs after every real
  // edge source (owner/topology/label) so a genuine parent always wins; skipped
  // in flat mode (no parenting at all). A nested-by-contract lane leaves the root
  // set, so it rides its chain root into app membership instead of joining flat
  // via the tier-2.5 name-prefix fallback (which only catches surviving roots).
  if (grouping !== 'flat') {
    for (const [id, lane] of laneMap) {
      if (laneParent.has(id)) continue
      const parentId = contractParentId(lane, laneMap, laneParent, laneIdByKey)
      if (!parentId) continue
      laneParent.set(id, parentId)
      lane.nestedByContract = true
    }
  }

  // Walk up parent chain to find root
  const findRoot = (id: string, visited = new Set<string>()): string => {
    if (visited.has(id)) return id
    visited.add(id)
    const parentId = laneParent.get(id)
    if (parentId && laneMap.has(parentId)) {
      return findRoot(parentId, visited)
    }
    return id
  }

  // Second pass: attach each lane to its DIRECT owner so multi-level chains
  // (CronJob → Job → Pod) preserve every ownership level. Attaching to the
  // chain root instead — the prior bug — flattened grandchildren (Pods) into
  // siblings of their parent (Jobs), losing one level.
  const topLevelLanes: ResourceLane[] = []
  const childLaneIds = new Set<string>()

  for (const [id] of laneMap) {
    if (!laneParent.has(id)) continue
    // findRoot walks the chain AND breaks cycles: a self-returning root means
    // id sits on a parent cycle — leave it top-level rather than nest it.
    const rootId = findRoot(id)
    if (rootId === id || !laneMap.has(rootId)) continue
    const parentId = laneParent.get(id)!
    const parent = laneMap.get(parentId)
    if (!parent) continue
    parent.children!.push(laneMap.get(id)!)
    parent.childEventCount = (parent.childEventCount || 0) + laneMap.get(id)!.events.length
    childLaneIds.add(id)
  }

  // Collect top-level lanes (not children of anyone)
  for (const [id, lane] of laneMap) {
    if (childLaneIds.has(id)) continue
    // Sort children by kind priority then latest event, recursively (every depth).
    sortLaneChildrenDeep(lane)
    // Pre-compute the collapsed roll-up: own events + EVERY descendant's, deduped
    // and render-sorted. subtreeEvents walks the full tree, so a two-level chain's
    // grandchildren (Pods under Jobs) stay in the parent's aggregate.
    lane.allEventsSorted = subtreeEvents(lane)
    topLevelLanes.push(lane)
  }

  // App-membership cascade — wrap root lanes into app-group header lanes.
  // Only for the main timeline (rootResource views want the resource's own
  // subtree, not an app roll-up).
  if (grouping === 'app' && appIndex && !rootResource) {
    return applyAppGrouping(topLevelLanes, appIndex)
  }

  // If rootResource is specified, filter to only include lanes related to that resource
  if (rootResource) {
    // Resolve to the canonical (possibly group-qualified) id so a CRD root matches
    // the lanes built from its events. rootResource carries no group; resolveId
    // uses the registry (its own events) or the topology/event group fallback.
    const rootLaneId = resolveId(laneResourceKey(rootResource.kind, rootResource.namespace, rootResource.name))
    const rootLane = topLevelLanes.find(l => l.id === rootLaneId)

    if (rootLane) {
      // Return the root lane with all its children
      return [rootLane]
    }

    // If root resource is a child of another lane, find its parent and return that
    for (const lane of topLevelLanes) {
      if (lane.children?.some(c => c.id === rootLaneId)) {
        return [lane]
      }
      // Also check if root is the parent and we're looking at the hierarchy from the parent perspective
    }

    // If not found, check if it's a child somewhere in the hierarchy and return its parent
    const childLane = laneMap.get(rootLaneId)
    if (childLane) {
      const parentId = laneParent.get(rootLaneId)
      if (parentId) {
        const parentLane = topLevelLanes.find(l => l.id === parentId)
        if (parentLane) {
          return [parentLane]
        }
        // Walk up to find the top-level ancestor
        const rootAncestorId = findRoot(rootLaneId)
        const rootAncestor = topLevelLanes.find(l => l.id === rootAncestorId)
        if (rootAncestor) {
          return [rootAncestor]
        }
      }
      // No parent found, return as a standalone lane. Full-subtree roll-up so a
      // nested chain's grandchildren stay in the aggregate.
      childLane.allEventsSorted = subtreeEvents(childLane)
      return [childLane]
    }

    // Resource not found in hierarchy - create a placeholder lane
    // This ensures the detail view always has something to show
    const placeholderLane: ResourceLane = {
      id: rootLaneId,
      kind: rootResource.kind,
      group: parseLaneId(rootLaneId)?.group || '',
      namespace: rootResource.namespace,
      name: rootResource.name,
      events: [],
      isWorkload: isWorkloadKind(rootResource.kind),
      children: [],
      childEventCount: 0,
      allEventsSorted: [],
    }
    return [placeholderLane]
  }

  return topLevelLanes
}

// -----------------------------------------------------------------------------
// Collision chip — the ONLY place a lane's API group surfaces visually. Two
// visible lanes that share kind+namespace+name necessarily differ in API group
// (else they'd share an id and be one lane), i.e. a genuine same-kind cross-group
// collision (CAPI vs CNPG `Cluster`). Both such lanes render a small group chip
// so the user can tell them apart; every other lane shows nothing.
// -----------------------------------------------------------------------------

/** The group-less collision key for a lane — kind + namespace + name. The
 *  pipe delimiter can't appear in any component (DNS-1123 names, alphanumeric
 *  kinds) and keeps this file greppable — a NUL delimiter makes text tooling
 *  classify the whole file as binary. */
export function laneCollisionKey(lane: Pick<ResourceLane, 'kind' | 'namespace' | 'name'>): string {
  return `${lane.kind}|${lane.namespace}|${lane.name}`
}

/** Collision keys (see laneCollisionKey) that appear on 2+ lanes in the given
 *  lane set (roots + every descendant). A lane whose key is in this set shares
 *  kind+ns+name with another visible lane of a different API group, so both
 *  should render the disambiguating group chip. Pure + SSR-safe: no DOM, no
 *  globals. Synthetic app-group headers (no real resource) are skipped. */
export function collidingLaneKeys(lanes: ResourceLane[]): Set<string> {
  const count = new Map<string, number>()
  const walk = (l: ResourceLane): void => {
    if (!l.isAppGroup) {
      const k = laneCollisionKey(l)
      count.set(k, (count.get(k) ?? 0) + 1)
    }
    for (const c of l.children ?? []) walk(c)
  }
  for (const l of lanes) walk(l)
  const out = new Set<string>()
  for (const [k, n] of count) if (n > 1) out.add(k)
  return out
}

/** Own events + every descendant's events, deduped and render-sorted. The
 *  roll-up a COLLAPSED parent row paints. Roots and app-group members already
 *  carry `allEventsSorted`; a nested parent (a Job owning Pods surfaced as an
 *  app-group member's child) does not, so this recomputes it on demand. */
export function subtreeEvents(lane: ResourceLane): TimelineEvent[] {
  const acc: TimelineEvent[] = []
  const walk = (l: ResourceLane): void => {
    acc.push(...l.events)
    for (const c of l.children ?? []) walk(c)
  }
  walk(lane)
  const unique = Array.from(new Map(acc.map((e) => [e.id, e])).values())
  return sortEventsForRendering(unique)
}

/** The events a lane paints on its OWN track row, given whether it's expanded.
 *  - collapsed parent → whole-subtree aggregate (the roll-up)
 *  - expanded parent, or a leaf → only the lane's own events
 *  This is the locked attribution rule: an expanded parent yields just its own
 *  slice while its children render their own slices below it, so no event ever
 *  appears on two visible rows, and no resource that owns events in the window
 *  renders an empty track. Holds at every depth and in every grouping mode. */
export function laneTrackEvents(lane: ResourceLane, expanded: boolean): TimelineEvent[] {
  const hasChildren = !!lane.children?.length
  if (hasChildren && !expanded) return lane.allEventsSorted ?? subtreeEvents(lane)
  return lane.events
}

/** True when the lane's SUBTREE (own events + every descendant's) has at least one
 *  event within [startMs, endMs] inclusive. The window-visibility predicate an
 *  EXPANDED parent applies to each child: render only children that actually move
 *  in the visible lens — the same rule top-level lanes follow. Uses the
 *  precomputed allEventsSorted roll-up when present (roots + app-group members
 *  carry it), else short-circuits a subtree walk. */
export function laneHasEventInWindow(lane: ResourceLane, startMs: number, endMs: number): boolean {
  const inRange = (e: TimelineEvent): boolean => {
    const t = new Date(e.timestamp).getTime()
    return t >= startMs && t <= endMs
  }
  if (lane.allEventsSorted) return lane.allEventsSorted.some(inRange)
  const walk = (l: ResourceLane): boolean => {
    if (l.events.some(inRange)) return true
    for (const c of l.children ?? []) if (walk(c)) return true
    return false
  }
  return walk(lane)
}

/** Whether an EXPANDED parent should render `child`, given the visible window and
 *  the per-child exemptions. Renders when the child's subtree moves in-window, OR
 *  it's pinned (its row is its home), OR the user deliberately expanded it (never
 *  yank a row they opened; an auto-expanded child is NOT exempt). Pure so the
 *  swimlane and its tests share one rule. */
export function isChildVisibleInWindow(
  child: ResourceLane,
  startMs: number,
  endMs: number,
  exempt: { pinned: boolean; userExpanded: boolean },
): boolean {
  if (exempt.pinned || exempt.userExpanded) return true
  // A structural app member (server-declared workload/Service/Ingress) stays
  // visible with an empty window — hiding the app's own Service makes a
  // matched app read as incomplete. Generated/evidence-matched lanes keep the
  // noise filter.
  if (child.structuralMember) return true
  return laneHasEventInWindow(child, startMs, endMs)
}

/**
 * Get all events from a hierarchy, flattened and sorted by timestamp (newest first).
 */
export function getAllEventsFromHierarchy(lanes: ResourceLane[]): TimelineEvent[] {
  const allEvents: TimelineEvent[] = []

  // Recurse the whole tree — a Deployment's Pod events hang two levels down
  // (Deployment → ReplicaSet → Pod); a one-level walk drops them, so the list
  // undercounts the events the swimlane (built from the full tree) shows.
  const collect = (lane: ResourceLane) => {
    allEvents.push(...lane.events)
    lane.children?.forEach(collect)
  }
  lanes.forEach(collect)

  // Deduplicate by event ID
  const uniqueEvents = Array.from(new Map(allEvents.map(e => [e.id, e])).values())

  // Sort by timestamp (newest first)
  return uniqueEvents.sort((a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime())
}

/**
 * Persisted pin record for a single resource. Carries enough to render the lane
 * label (kind badge + name + namespace) with zero event data, so a pinned
 * resource that's been deleted or gone quiet still renders as an empty track.
 * `type` is optional for back-compat: records persisted before app-group pins
 * existed carry no discriminant and default to 'resource'.
 */
export interface PinnedResourceRef {
  type?: 'resource'
  /** "Kind/namespace/name" — the same id form buildResourceHierarchy emits. */
  id: string
  kind: string
  namespace: string
  name: string
}

/**
 * Persisted pin record for an APP-GROUP header. The group's members re-resolve
 * live each render by matching `appKey` against the current lanes, so a group
 * pin survives grouping-mode switches and app churn; when no matching group lane
 * exists a quiet header is synthesized from `appName`.
 */
export interface PinnedAppGroupRef {
  type: 'appGroup'
  /** "app:<appKey>" — the same id form makeAppGroupLane emits. */
  id: string
  appKey: string
  appName: string
}

export type PinnedLaneRef = PinnedResourceRef | PinnedAppGroupRef

/** True when `value` is a well-formed pin record (either shape). Records without
 *  a `type` are treated as legacy resource refs (back-compat with localStorage
 *  entries written before app-group pins existed). */
export function isPinnedLaneRef(value: unknown): value is PinnedLaneRef {
  if (!value || typeof value !== 'object') return false
  const v = value as Record<string, unknown>
  if (typeof v.id !== 'string') return false
  if (v.type === 'appGroup') {
    return typeof v.appKey === 'string' && typeof v.appName === 'string'
  }
  return typeof v.kind === 'string' && typeof v.namespace === 'string' && typeof v.name === 'string'
}

/** Ensure a lane carries a merged allEventsSorted (own + descendants). Roots and
 *  app-group members already have it; a plain child (leaf) may only have events. */
function withAllEventsSorted(lane: ResourceLane): ResourceLane {
  if (lane.allEventsSorted) return { ...lane }
  const allEvents = [...lane.events, ...(lane.children?.flatMap((c) => c.events) ?? [])]
  const uniqueEvents = Array.from(new Map(allEvents.map((e) => [e.id, e])).values())
  return { ...lane, allEventsSorted: sortEventsForRendering(uniqueEvents) }
}

function synthesizeEmptyPinnedLane(ref: PinnedResourceRef): ResourceLane {
  return {
    id: ref.id,
    kind: ref.kind,
    // Recover the group from the id so an absent CRD pin still carries it (the
    // pin record itself stores none). Bare/built-in ids yield ''.
    group: parseLaneId(ref.id)?.group || '',
    namespace: ref.namespace,
    name: ref.name,
    events: [],
    isWorkload: isWorkloadKind(ref.kind),
    children: [],
    childEventCount: 0,
    allEventsSorted: [],
  }
}

/** A pinned app-group whose live group lane isn't in the current view (owner/flat
 *  grouping, or the app vanished): a header-only quiet row from the pin record.
 *  Honest by construction — owner/flat modes never build the group lane, so the
 *  dimmed "not present" header is the correct fallback (the pin still survives
 *  the mode switch). */
function synthesizeQuietAppGroupLane(ref: PinnedAppGroupRef): ResourceLane {
  return {
    id: ref.id,
    kind: 'AppGroup',
    namespace: '',
    name: ref.appName,
    events: [],
    isWorkload: false,
    isAppGroup: true,
    appKey: ref.appKey,
    title: ref.appName,
    absentPinnedApp: true,
    children: [],
    childEventCount: 0,
    allEventsSorted: [],
  }
}

/**
 * Remove pinned lanes from the regular list — pin MOVES a row to the pinned
 * section rather than copying it, so a resource never appears twice. Pinned
 * children are pruned from their parents; a group/parent left with no children
 * and no events of its own is dropped entirely.
 */
export function removePinnedLanes(
  allLanes: ResourceLane[],
  pinnedIds: Set<string>,
  pinnedAppKeys?: Set<string>,
): ResourceLane[] {
  if (pinnedIds.size === 0 && !pinnedAppKeys?.size) return allLanes
  const prune = (lane: ResourceLane): ResourceLane | null => {
    if (pinnedIds.has(lane.id)) return null
    // App-group roots also match by appKey — a group's id is deterministic, but
    // matching the key is robust to any id-shape drift.
    if (lane.isAppGroup && lane.appKey && pinnedAppKeys?.has(lane.appKey)) return null
    if (!lane.children?.length) return lane
    const children = lane.children.map(prune).filter((c): c is ResourceLane => c !== null)
    if (children.length === lane.children.length) return lane
    if (children.length === 0 && lane.events.length === 0) return null
    // A pinned child was pruned, so the parent's roll-up fields (allEventsSorted +
    // childEventCount) were built with the moved child included. Rebuild them from
    // the pruned subtree; otherwise a COLLAPSED parent's laneTrackEvents prefers the
    // stale allEventsSorted and double-paints the pinned child's events (which now
    // also render on the pinned row).
    const pruned = { ...lane, children }
    return {
      ...pruned,
      allEventsSorted: subtreeEvents(pruned),
      childEventCount: children.reduce((n, c) => n + (c.allEventsSorted?.length ?? c.events.length), 0),
    }
  }
  return allLanes.map(prune).filter((l): l is ResourceLane => l !== null)
}

/**
 * Resolve pinned refs into stationary top-level lanes, in pin order.
 *
 * Each ref is looked up among roots, their children, and (for app-group headers)
 * the members' own children — first match wins. A match is cloned into its own
 * top-level lane (carrying its events + descendants' events via the existing
 * merge); `removePinnedLanes` then drops the original from the regular list, so
 * the net effect is a move — the resource shows once, in the pinned section. A ref
 * with no match anywhere (deleted or filtered out of the loaded selection) is
 * synthesized from the record as an empty lane so the row still renders. Pure: no
 * mutation of the input lanes.
 */
export function extractPinnedLanes(allLanes: ResourceLane[], pinnedRefs: PinnedLaneRef[]): ResourceLane[] {
  if (pinnedRefs.length === 0) return []
  const byId = new Map<string, ResourceLane>()
  // App-group headers re-resolve by appKey (live members), independent of the
  // deterministic "app:<appKey>" id, so a group survives grouping-mode churn.
  const groupByAppKey = new Map<string, ResourceLane>()
  const indexLane = (lane: ResourceLane): void => {
    if (!byId.has(lane.id)) byId.set(lane.id, lane)
    if (lane.isAppGroup && lane.appKey && !groupByAppKey.has(lane.appKey)) {
      groupByAppKey.set(lane.appKey, lane)
    }
    for (const child of lane.children ?? []) indexLane(child)
  }
  for (const lane of allLanes) indexLane(lane)

  // A resource can be pinned on its own AND be a live member of a separately
  // pinned app group. Emitting both renders the resource twice — once inside the
  // group's roll-up, once as its own row — and double-counts its events. Dedupe
  // in the group's favor: collect every lane id under any pinned group's live
  // membership and skip a resource ref that falls inside one. Resolved by group
  // membership, not pin order, so pinning the member first or the app first
  // yields the same single occurrence (the app-group wins either way). Among
  // refs of the same kind, the first pin still wins (byId/appKey index above).
  const coveredByPinnedGroup = new Set<string>()
  const collectMemberIds = (lane: ResourceLane): void => {
    coveredByPinnedGroup.add(lane.id)
    for (const child of lane.children ?? []) collectMemberIds(child)
  }
  for (const ref of pinnedRefs) {
    if (ref.type !== 'appGroup') continue
    const group = groupByAppKey.get(ref.appKey)
    if (!group) continue
    for (const child of group.children ?? []) collectMemberIds(child)
  }

  const out: ResourceLane[] = []
  for (const ref of pinnedRefs) {
    if (ref.type === 'appGroup') {
      const found = groupByAppKey.get(ref.appKey)
      out.push(found ? withAllEventsSorted(found) : synthesizeQuietAppGroupLane(ref))
      continue
    }
    if (coveredByPinnedGroup.has(ref.id)) continue // folded into a pinned app group
    const found = byId.get(ref.id)
    out.push(found ? withAllEventsSorted(found) : synthesizeEmptyPinnedLane(ref))
  }
  return out
}

/**
 * Count total events in a hierarchy (full depth — grandchild Pod events under a
 * Deployment→ReplicaSet→Pod chain count too, matching getAllEventsFromHierarchy).
 */
export function countEventsInHierarchy(lanes: ResourceLane[]): number {
  let count = 0
  const walk = (lane: ResourceLane) => {
    count += lane.events.length
    lane.children?.forEach(walk)
  }
  lanes.forEach(walk)
  return count
}
