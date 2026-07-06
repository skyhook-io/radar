import type { SelectedResource, ResourceRef, APIResource } from '../types/core'
import { englishPlural } from './pluralize'

/**
 * Canonical callback type for navigating to a resource.
 * All components that trigger resource navigation should use this type.
 */
export type NavigateToResource = (resource: SelectedResource) => void

// Fallback map for core K8s resources — used before API discovery completes.
const BUILTIN_PLURAL_TO_KIND: Record<string, string> = {
  pods: 'Pod',
  services: 'Service',
  endpoints: 'Endpoints', // already-plural resource name; englishPlural would yield "endpointses"
  endpointslices: 'EndpointSlice',
  deployments: 'Deployment',
  daemonsets: 'DaemonSet',
  statefulsets: 'StatefulSet',
  replicasets: 'ReplicaSet',
  ingresses: 'Ingress',
  configmaps: 'ConfigMap',
  secrets: 'Secret',
  namespaces: 'Namespace',
  events: 'Event',
  nodes: 'Node',
  jobs: 'Job',
  cronjobs: 'CronJob',
  horizontalpodautoscalers: 'HorizontalPodAutoscaler',
  persistentvolumeclaims: 'PersistentVolumeClaim',
  persistentvolumes: 'PersistentVolume',
  storageclasses: 'StorageClass',
  poddisruptionbudgets: 'PodDisruptionBudget',
  roles: 'Role',
  clusterroles: 'ClusterRole',
  rolebindings: 'RoleBinding',
  clusterrolebindings: 'ClusterRoleBinding',
  serviceaccounts: 'ServiceAccount',
  networkpolicies: 'NetworkPolicy',
}

// Dynamic map built from API discovery — populated by initNavigationMap().
// Once populated, this is the source of truth for all kind↔plural lookups.
let discoveredPluralToKind: Record<string, string> | null = null
let discoveredKindToPlural: Record<string, string> | null = null

/**
 * Initialize navigation maps from discovered API resources.
 * Call once when API resources are fetched. After this, kindToPlural/pluralToKind
 * use the real cluster data instead of heuristics.
 */
export function initNavigationMap(resources: APIResource[]) {
  const p2k: Record<string, string> = { ...BUILTIN_PLURAL_TO_KIND }
  const k2p: Record<string, string> = {}
  for (const r of resources) {
    const plural = r.name.toLowerCase()
    // First-wins on plurals: BUILTIN_PLURAL_TO_KIND seeds canonical core mappings
    // (e.g. "pods" → "Pod") so a colliding API resource (metrics.k8s.io exposes
    // "pods" with kind "PodMetrics") cannot hijack the core mapping.
    if (!(plural in p2k)) p2k[plural] = r.kind
    k2p[r.kind.toLowerCase()] = plural
  }
  discoveredPluralToKind = p2k
  discoveredKindToPlural = k2p
}

/** Reset navigation maps to builtin-only state. For testing. */
export function resetNavigationMap() {
  discoveredPluralToKind = null
  discoveredKindToPlural = null
}

function getPluralToKind(): Record<string, string> {
  return discoveredPluralToKind || BUILTIN_PLURAL_TO_KIND
}

/**
 * Convert a singular kind (e.g., "Deployment") to plural API resource name (e.g., "deployments").
 * Single source of truth — uses English pluralization rules with a small alias map for
 * abbreviations and special mappings that aren't simple plurals.
 * Idempotent: already-plural inputs (e.g., "secrets") are returned as-is.
 */
export function kindToPlural(kind: string): string {
  const kindLower = kind.toLowerCase()
  const pluralToKindMap = getPluralToKind()

  // Already a known plural — return as-is to prevent double-pluralization
  if (kindLower in pluralToKindMap) return kindLower

  // Lookup from discovered API resources (singular kind → plural name)
  if (discoveredKindToPlural && kindLower in discoveredKindToPlural) {
    return discoveredKindToPlural[kindLower]
  }

  // Aliases: abbreviations or mappings to a different resource name
  const aliases: Record<string, string> = {
    horizontalpodautoscaler: 'horizontalpodautoscalers',
    pvc: 'persistentvolumeclaims',
    podgroup: 'pods',
  }
  if (aliases[kindLower]) return aliases[kindLower]

  // Fallback: English pluralization rules (shared with pluralize() in
  // utils/pluralize.ts so a rule change updates both call paths).
  return englishPlural(kindLower)
}

/**
 * Convert a plural API resource name (e.g., "deployments") back to singular PascalCase kind (e.g., "Deployment").
 * Inverse of kindToPlural. Converts plural API resource names from URLs back to
 * singular PascalCase form for internal logic (health checks, badge colors, hierarchy matching).
 */
export function pluralToKind(plural: string): string {
  const lower = plural.toLowerCase()
  const pluralToKindMap = getPluralToKind()

  if (pluralToKindMap[lower]) return pluralToKindMap[lower]

  // If it already looks like a singular PascalCase kind (starts with uppercase), return as-is
  if (plural[0] === plural[0].toUpperCase() && plural[0] !== plural[0].toLowerCase()) {
    return plural
  }

  // Fallback: basic de-pluralization + capitalize first letter
  let singular = lower
  if (singular.endsWith('ies')) {
    singular = singular.slice(0, -3) + 'y'
  } else if (singular.endsWith('ses') || singular.endsWith('xes') || singular.endsWith('ches') || singular.endsWith('shes')) {
    singular = singular.slice(0, -2)
  } else if (singular.endsWith('s')) {
    singular = singular.slice(0, -1)
  }
  return singular.charAt(0).toUpperCase() + singular.slice(1)
}

/**
 * Convert a ResourceRef (from backend relationships) to a SelectedResource (for navigation).
 * Handles kind singular→plural conversion.
 */
export function refToSelectedResource(ref: ResourceRef): SelectedResource {
  return {
    kind: kindToPlural(ref.kind),
    namespace: ref.namespace,
    name: ref.name,
    group: ref.group,
  }
}

/**
 * Extract the API group from an apiVersion string.
 * Returns '' for core resources (e.g. "v1") and for missing/empty input.
 * Examples:
 *   "v1"                          → ""
 *   "apps/v1"                     → "apps"
 *   "cluster.x-k8s.io/v1beta1"    → "cluster.x-k8s.io"
 */
export function apiVersionToGroup(apiVersion?: string | null): string {
  if (!apiVersion) return ''
  const i = apiVersion.indexOf('/')
  return i === -1 ? '' : apiVersion.slice(0, i)
}

// -----------------------------------------------------------------------------
// Timeline lane identity. A lane's id includes the API group so two CRDs that
// share a kind name across vendors (CAPI `Cluster` in cluster.x-k8s.io vs CNPG
// `Cluster` in postgresql.cnpg.io) can never merge into one row. The group is an
// INTERNAL identity component only — it never surfaces in the UI except the rare
// on-screen collision chip (see collidingLaneKeys in resource-hierarchy).
// -----------------------------------------------------------------------------

// Built-in Kubernetes API groups. Their kind names are globally reserved and
// never collide across vendors, so their lanes keep the bare `Kind/ns/name` id.
// This keeps existing pins, ?event= URLs, and the applications byResource join
// byte-stable for all core/built-in resources (Pod, Deployment, Job, …) — only
// CRD-group lanes get a group-qualified id, so ONLY CRD pins are affected.
const BUILTIN_API_GROUPS: ReadonlySet<string> = new Set([
  '', // core (v1)
  'apps', 'batch', 'autoscaling', 'policy',
  'networking.k8s.io', 'storage.k8s.io', 'scheduling.k8s.io',
  'coordination.k8s.io', 'node.k8s.io', 'discovery.k8s.io',
  'rbac.authorization.k8s.io', 'admissionregistration.k8s.io',
  'authentication.k8s.io', 'authorization.k8s.io', 'certificates.k8s.io',
  'apiextensions.k8s.io', 'apiregistration.k8s.io', 'events.k8s.io',
  'flowcontrol.apiserver.k8s.io',
])

/** Whether a resource's API group must appear in its lane id to prevent a
 *  cross-group merge. Built-in groups never collide, so they stay bare. */
export function groupQualifiesLaneId(group: string | undefined): boolean {
  return !!group && !BUILTIN_API_GROUPS.has(group)
}

/** Canonical timeline lane id. Group-qualified only for CRD groups
 *  (`Cluster.postgresql.cnpg.io/prod/db`); bare for built-in/core groups
 *  (`Pod/team-a/x`, `Deployment/ns/web`) so those ids stay exactly as before. */
export function laneId(kind: string, group: string | undefined, namespace: string, name: string): string {
  const g = groupQualifiesLaneId(group) ? `.${group}` : ''
  return `${kind}${g}/${namespace}/${name}`
}

/** The group-less resource key (`Kind/ns/name`) — the join key for group-less
 *  server payloads (applications byResource, AppRow workloads) and group-less
 *  references (owner refs, K8s-event involvedObject, topology node ids). For a
 *  built-in-group lane this equals its id; for a CRD lane it's the id minus the
 *  `.group` segment. */
export function laneResourceKey(kind: string, namespace: string, name: string): string {
  return `${kind}/${namespace}/${name}`
}

/** Recover {kind, group, namespace, name} from a lane id. The kind segment may
 *  carry a `.group` suffix (CRD lanes). Kind names never contain '.', and a
 *  qualifying group always does, so the first '.' in the kind segment splits
 *  them; ns and name never contain '/', so the first two '/' bound the rest. */
export function parseLaneId(id: string): { kind: string; group: string; namespace: string; name: string } | null {
  const s1 = id.indexOf('/')
  if (s1 < 0) return null
  const s2 = id.indexOf('/', s1 + 1)
  if (s2 < 0) return null
  const kindSeg = id.slice(0, s1)
  const dot = kindSeg.indexOf('.')
  return {
    kind: dot < 0 ? kindSeg : kindSeg.slice(0, dot),
    group: dot < 0 ? '' : kindSeg.slice(dot + 1),
    namespace: id.slice(s1 + 1, s2),
    name: id.slice(s2 + 1),
  }
}
