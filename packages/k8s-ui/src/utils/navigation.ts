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
