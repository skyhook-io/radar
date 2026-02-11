/**
 * Shared resource hierarchy building logic.
 *
 * This module provides utilities for building hierarchical resource lanes from timeline events.
 * It's used by both TimelineSwimlanes (for the main timeline view) and ResourceDetailPage
 * (for showing related events in the detail view).
 *
 * The hierarchy is built from:
 * 1. Owner references (event.owner) - most reliable for Deployment→RS→Pod chains
 * 2. Topology edges (Service→Deployment via 'exposes', Ingress→Service and Gateway→Route→Service via 'routes-to', etc.)
 * 3. App label grouping (app.kubernetes.io/name or app label)
 */

import type { TimelineEvent, Topology } from '../types'
import { isWorkloadKind } from '../types'

/**
 * Resource lane representing a single resource and its timeline events.
 * Can have child lanes for related resources (e.g., Deployment has ReplicaSet and Pod children).
 */
export interface ResourceLane {
  id: string
  kind: string
  namespace: string
  name: string
  events: TimelineEvent[]
  isWorkload: boolean
  children?: ResourceLane[]
  childEventCount?: number
  allEventsSorted?: TimelineEvent[]
}

/**
 * Options for building resource hierarchy.
 */
export interface HierarchyOptions {
  events: TimelineEvent[]
  topology?: Topology
  /** If provided, returns only the hierarchy rooted at this resource */
  rootResource?: { kind: string; namespace: string; name: string }
  /** Whether to group by app.kubernetes.io/name or app label (default: true) */
  groupByApp?: boolean
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
    job: 'Job', cronjob: 'CronJob',
    horizontalpodautoscaler: 'HorizontalPodAutoscaler',
    podgroup: 'PodGroup', rollout: 'Rollout', namespace: 'Namespace',
    application: 'Application', applicationset: 'ApplicationSet', appproject: 'AppProject',
    kustomization: 'Kustomization',
    helmrelease: 'HelmRelease', helmrepository: 'HelmRepository',
    helmchart: 'HelmChart', gitrepository: 'GitRepository',
    ocirepository: 'OCIRepository', certificate: 'Certificate',
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
export function buildResourceHierarchy(options: HierarchyOptions): ResourceLane[] {
  const { events, topology, rootResource, groupByApp = true } = options
  const laneMap = new Map<string, ResourceLane>()

  // Track events that should be attached to their owner instead of their own lane
  const eventsToAttach: { event: TimelineEvent; ownerLaneId: string }[] = []

  // First pass: create lanes from events (but not for Events with owners)
  for (const event of events) {
    // K8s Events with an owner (involvedObject) should attach to that resource, not get their own lane
    if (event.kind === 'Event' && event.owner) {
      const ownerLaneId = `${event.owner.kind}/${event.namespace}/${event.owner.name}`
      eventsToAttach.push({ event, ownerLaneId })
      continue
    }

    const laneId = `${event.kind}/${event.namespace}/${event.name}`
    if (!laneMap.has(laneId)) {
      laneMap.set(laneId, {
        id: laneId,
        kind: event.kind,
        namespace: event.namespace,
        name: event.name,
        events: [],
        isWorkload: isWorkloadKind(event.kind),
        children: [],
        childEventCount: 0,
      })
    }
    laneMap.get(laneId)!.events.push(event)
  }

  // Attach K8s Events to their owner lanes
  for (const { event, ownerLaneId } of eventsToAttach) {
    if (laneMap.has(ownerLaneId)) {
      // Owner exists, attach event to it
      laneMap.get(ownerLaneId)!.events.push(event)
    } else {
      // Owner doesn't exist yet, create lane for it
      const parts = ownerLaneId.split('/')
      laneMap.set(ownerLaneId, {
        id: ownerLaneId,
        kind: parts[0],
        namespace: parts[1],
        name: parts.slice(2).join('/'),
        events: [event],
        isWorkload: isWorkloadKind(parts[0]),
        children: [],
        childEventCount: 0,
      })
    }
  }

  // Build parent map from BOTH owner references AND topology edges
  const laneParent = new Map<string, string>() // childLaneId -> parentLaneId

  // Source 1: Owner references from events (most reliable for Deployment→RS→Pod)
  for (const [laneId, lane] of laneMap) {
    const eventWithOwner = lane.events.find(e => e.owner)
    if (eventWithOwner?.owner) {
      const ownerLaneId = `${eventWithOwner.owner.kind}/${lane.namespace}/${eventWithOwner.owner.name}`

      // Create parent lane if it doesn't exist (parent may have no events)
      if (!laneMap.has(ownerLaneId)) {
        laneMap.set(ownerLaneId, {
          id: ownerLaneId,
          kind: eventWithOwner.owner.kind,
          namespace: lane.namespace,
          name: eventWithOwner.owner.name,
          events: [],
          isWorkload: isWorkloadKind(eventWithOwner.owner.kind),
          children: [],
          childEventCount: 0,
        })
      }
      laneParent.set(laneId, ownerLaneId)
    }
  }

  // Source 2: Topology edges (for Service→Deployment, Ingress→Service, ConfigMap→Deployment)
  if (topology?.edges) {
    for (const edge of topology.edges) {
      const sourceLaneId = nodeIdToLaneId(edge.source)
      const targetLaneId = nodeIdToLaneId(edge.target)
      if (!sourceLaneId || !targetLaneId) continue

      // manages: Deployment→RS→Pod (already covered by owner refs, skip)
      if (edge.type === 'manages') continue

      // At least one side must have events
      const sourceExists = laneMap.has(sourceLaneId)
      const targetExists = laneMap.has(targetLaneId)
      if (!sourceExists && !targetExists) continue

      // exposes: Service→Deployment (Service is parent of Deployment)
      if (edge.type === 'exposes') {
        if (!laneParent.has(targetLaneId) && targetExists) {
          if (!sourceExists) {
            const parts = sourceLaneId.split('/')
            laneMap.set(sourceLaneId, {
              id: sourceLaneId,
              kind: parts[0],
              namespace: parts[1],
              name: parts.slice(2).join('/'),
              events: [],
              isWorkload: isWorkloadKind(parts[0]),
              children: [],
              childEventCount: 0,
            })
          }
          laneParent.set(targetLaneId, sourceLaneId)
        }
      }

      // routes-to has two cases:
      // 1. Ingress→Service: Service should be parent (representative)
      // 2. Service→Pod/PodGroup: Service should be parent (normal hierarchy)
      if (edge.type === 'routes-to') {
        const sourceKind = sourceLaneId.split('/')[0]
        const targetKind = targetLaneId.split('/')[0]

        // Gateway→Route: Gateway is parent of Route
        if (sourceKind === 'Gateway' && (targetKind === 'HTTPRoute' || targetKind === 'GRPCRoute' || targetKind === 'TCPRoute' || targetKind === 'TLSRoute')) {
          if (!laneParent.has(targetLaneId) && targetExists) {
            if (!sourceExists) {
              const parts = sourceLaneId.split('/')
              laneMap.set(sourceLaneId, {
                id: sourceLaneId,
                kind: parts[0],
                namespace: parts[1],
                name: parts.slice(2).join('/'),
                events: [],
                isWorkload: isWorkloadKind(parts[0]),
                children: [],
                childEventCount: 0,
              })
            }
            laneParent.set(targetLaneId, sourceLaneId)
          }
        }
        // Route→Service: reverse (Service is representative, like Ingress)
        else if ((sourceKind === 'HTTPRoute' || sourceKind === 'GRPCRoute' || sourceKind === 'TCPRoute' || sourceKind === 'TLSRoute') && targetKind === 'Service') {
          if (!laneParent.has(sourceLaneId) && sourceExists) {
            if (!targetExists) {
              const parts = targetLaneId.split('/')
              laneMap.set(targetLaneId, {
                id: targetLaneId,
                kind: parts[0],
                namespace: parts[1],
                name: parts.slice(2).join('/'),
                events: [],
                isWorkload: isWorkloadKind(parts[0]),
                children: [],
                childEventCount: 0,
              })
            }
            laneParent.set(sourceLaneId, targetLaneId)
          }
        }
        // Ingress→Service: reverse relationship (Service is representative)
        else if (sourceKind === 'Ingress' && targetKind === 'Service') {
          if (!laneParent.has(sourceLaneId) && sourceExists) {
            if (!targetExists) {
              const parts = targetLaneId.split('/')
              laneMap.set(targetLaneId, {
                id: targetLaneId,
                kind: parts[0],
                namespace: parts[1],
                name: parts.slice(2).join('/'),
                events: [],
                isWorkload: isWorkloadKind(parts[0]),
                children: [],
                childEventCount: 0,
              })
            }
            laneParent.set(sourceLaneId, targetLaneId)
          }
        }
        // Service→Pod/PodGroup: normal direction (Service is parent)
        else if (sourceKind === 'Service') {
          if (!laneParent.has(targetLaneId) && targetExists) {
            if (!sourceExists) {
              const parts = sourceLaneId.split('/')
              laneMap.set(sourceLaneId, {
                id: sourceLaneId,
                kind: parts[0],
                namespace: parts[1],
                name: parts.slice(2).join('/'),
                events: [],
                isWorkload: isWorkloadKind(parts[0]),
                children: [],
                childEventCount: 0,
              })
            }
            laneParent.set(targetLaneId, sourceLaneId)
          }
        }
      }

      // configures/uses: ConfigMap→Deployment (Deployment is parent of ConfigMap)
      if (edge.type === 'configures' || edge.type === 'uses') {
        if (!laneParent.has(sourceLaneId) && sourceExists) {
          if (!targetExists) {
            const parts = targetLaneId.split('/')
            laneMap.set(targetLaneId, {
              id: targetLaneId,
              kind: parts[0],
              namespace: parts[1],
              name: parts.slice(2).join('/'),
              events: [],
              isWorkload: isWorkloadKind(parts[0]),
              children: [],
              childEventCount: 0,
            })
          }
          laneParent.set(sourceLaneId, targetLaneId)
        }
      }
    }
  }

  // Source 3: App label grouping (optional)
  if (groupByApp && topology?.nodes) {
    const laneAppLabels = new Map<string, string>()
    for (const node of topology.nodes) {
      const laneId = nodeIdToLaneId(node.id)
      if (!laneId) continue
      const labels = node.data?.labels as Record<string, string> | undefined
      const appLabel = labels?.['app.kubernetes.io/name'] || labels?.['app']
      if (appLabel) {
        laneAppLabels.set(laneId, appLabel)
      }
    }

    // Only include primary resource kinds in app label grouping
    const appLabelEligibleKinds = new Set([
      'Service', 'Deployment', 'Rollout', 'StatefulSet', 'DaemonSet',
      'Job', 'CronJob', 'Ingress', 'Gateway', 'HTTPRoute', 'GRPCRoute',
      'TCPRoute', 'TLSRoute', 'ConfigMap', 'Secret',
      'Application', 'Kustomization', 'HelmRelease', 'GitRepository',
      'Workflow', 'CronWorkflow',
    ])

    // Group lanes by app label
    const appGroups = new Map<string, string[]>()
    for (const [laneId, lane] of laneMap) {
      if (laneParent.has(laneId)) continue
      if (!appLabelEligibleKinds.has(lane.kind)) continue
      const appLabel = laneAppLabels.get(laneId)
      if (!appLabel) continue
      if (!appGroups.has(appLabel)) {
        appGroups.set(appLabel, [])
      }
      appGroups.get(appLabel)!.push(laneId)
    }

    // For each app group with multiple members, pick the best parent
    for (const [, laneIds] of appGroups) {
      if (laneIds.length < 2) continue

      const kindPriority: Record<string, number> = {
        Service: 1, Ingress: 2, Gateway: 2,
        HTTPRoute: 2, GRPCRoute: 2, TCPRoute: 2, TLSRoute: 2,
        Deployment: 3, Rollout: 3, StatefulSet: 3, DaemonSet: 3,
        Job: 4, CronJob: 4,
        ConfigMap: 5, Secret: 5,
        ReplicaSet: 6, Pod: 7
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

  // Walk up parent chain to find root
  const findRoot = (laneId: string, visited = new Set<string>()): string => {
    if (visited.has(laneId)) return laneId
    visited.add(laneId)
    const parentId = laneParent.get(laneId)
    if (parentId && laneMap.has(parentId)) {
      return findRoot(parentId, visited)
    }
    return laneId
  }

  // Second pass: group children under their root
  const topLevelLanes: ResourceLane[] = []
  const childLaneIds = new Set<string>()

  for (const [laneId] of laneMap) {
    if (!laneParent.has(laneId)) continue
    const rootId = findRoot(laneId)
    if (rootId !== laneId && laneMap.has(rootId)) {
      const root = laneMap.get(rootId)!
      const child = laneMap.get(laneId)!
      root.children!.push(child)
      root.childEventCount = (root.childEventCount || 0) + child.events.length
      childLaneIds.add(laneId)
    }
  }

  // Collect top-level lanes (not children of anyone)
  for (const [laneId, lane] of laneMap) {
    if (!childLaneIds.has(laneId)) {
      // Sort children by kind priority then by latest event
      if (lane.children && lane.children.length > 0) {
        const kindPriority: Record<string, number> = {
          Service: 1, Gateway: 1, HTTPRoute: 2, GRPCRoute: 2, TCPRoute: 2, TLSRoute: 2,
          Deployment: 2, Rollout: 2, StatefulSet: 2, DaemonSet: 2,
          Job: 3, CronJob: 3,
          ReplicaSet: 3, Pod: 4, ConfigMap: 5, Secret: 5
        }
        lane.children.sort((a, b) => {
          const aPriority = kindPriority[a.kind] || 10
          const bPriority = kindPriority[b.kind] || 10
          if (aPriority !== bPriority) return aPriority - bPriority
          const aLatest = a.events.length > 0 ? Math.max(...a.events.map(e => new Date(e.timestamp).getTime())) : 0
          const bLatest = b.events.length > 0 ? Math.max(...b.events.map(e => new Date(e.timestamp).getTime())) : 0
          return bLatest - aLatest
        })
      }

      // Pre-compute sorted events (own + children) for efficient rendering
      // Deduplicate by event ID
      const allEvents = [...lane.events, ...(lane.children?.flatMap(c => c.events) || [])]
      const uniqueEvents = Array.from(new Map(allEvents.map(e => [e.id, e])).values())
      lane.allEventsSorted = sortEventsForRendering(uniqueEvents)

      topLevelLanes.push(lane)
    }
  }

  // If rootResource is specified, filter to only include lanes related to that resource
  if (rootResource) {
    const rootLaneId = `${rootResource.kind}/${rootResource.namespace}/${rootResource.name}`
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
      // No parent found, return as a standalone lane
      const allEvents = [...childLane.events, ...(childLane.children?.flatMap(c => c.events) || [])]
      const uniqueEvents = Array.from(new Map(allEvents.map(e => [e.id, e])).values())
      childLane.allEventsSorted = sortEventsForRendering(uniqueEvents)
      return [childLane]
    }

    // Resource not found in hierarchy - create a placeholder lane
    // This ensures the detail view always has something to show
    const placeholderLane: ResourceLane = {
      id: rootLaneId,
      kind: rootResource.kind,
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

/**
 * Get all events from a hierarchy, flattened and sorted by timestamp (newest first).
 */
export function getAllEventsFromHierarchy(lanes: ResourceLane[]): TimelineEvent[] {
  const allEvents: TimelineEvent[] = []

  for (const lane of lanes) {
    allEvents.push(...lane.events)
    if (lane.children) {
      for (const child of lane.children) {
        allEvents.push(...child.events)
      }
    }
  }

  // Deduplicate by event ID
  const uniqueEvents = Array.from(new Map(allEvents.map(e => [e.id, e])).values())

  // Sort by timestamp (newest first)
  return uniqueEvents.sort((a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime())
}

/**
 * Count total events in a hierarchy.
 */
export function countEventsInHierarchy(lanes: ResourceLane[]): number {
  let count = 0
  for (const lane of lanes) {
    count += lane.events.length
    if (lane.children) {
      for (const child of lane.children) {
        count += child.events.length
      }
    }
  }
  return count
}
