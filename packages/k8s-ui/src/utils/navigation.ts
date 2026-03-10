import type { SelectedResource, ResourceRef } from '../types/core'

/**
 * Canonical callback type for navigating to a resource.
 * All components that trigger resource navigation should use this type.
 */
export type NavigateToResource = (resource: SelectedResource) => void

// Known plural API resource names → singular PascalCase kind.
// Shared between kindToPlural (idempotency guard) and pluralToKind (reverse lookup).
const PLURAL_TO_KIND: Record<string, string> = {
  pods: 'Pod',
  services: 'Service',
  deployments: 'Deployment',
  daemonsets: 'DaemonSet',
  statefulsets: 'StatefulSet',
  replicasets: 'ReplicaSet',
  ingresses: 'Ingress',
  gateways: 'Gateway',
  httproutes: 'HTTPRoute',
  grpcroutes: 'GRPCRoute',
  tcproutes: 'TCPRoute',
  tlsroutes: 'TLSRoute',
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
  rollouts: 'Rollout',
  applications: 'Application',
  kustomizations: 'Kustomization',
  helmreleases: 'HelmRelease',
  gitrepositories: 'GitRepository',
  certificates: 'Certificate',
  roles: 'Role',
  clusterroles: 'ClusterRole',
  rolebindings: 'RoleBinding',
  clusterrolebindings: 'ClusterRoleBinding',
  serviceaccounts: 'ServiceAccount',
  networkpolicies: 'NetworkPolicy',
  verticalpodautoscalers: 'VerticalPodAutoscaler',
  virtualservices: 'VirtualService',
  destinationrules: 'DestinationRule',
  serviceentries: 'ServiceEntry',
  peerauthentications: 'PeerAuthentication',
  authorizationpolicies: 'AuthorizationPolicy',
}

/**
 * Convert a singular kind (e.g., "Deployment") to plural API resource name (e.g., "deployments").
 * Single source of truth — uses English pluralization rules with a small alias map for
 * abbreviations and special mappings that aren't simple plurals.
 * Idempotent: already-plural inputs (e.g., "secrets") are returned as-is.
 */
export function kindToPlural(kind: string): string {
  const kindLower = kind.toLowerCase()

  // Already a known plural — return as-is to prevent double-pluralization
  if (kindLower in PLURAL_TO_KIND) return kindLower

  // Aliases: abbreviations or mappings to a different resource name
  const aliases: Record<string, string> = {
    horizontalpodautoscaler: 'horizontalpodautoscalers',
    pvc: 'persistentvolumeclaims',
    podgroup: 'pods',
  }
  if (aliases[kindLower]) return aliases[kindLower]

  // English pluralization rules (covers *Class→*classes, *Policy→*policies, *Repository→*repositories, etc.)
  if (kindLower.endsWith('s') || kindLower.endsWith('x') || kindLower.endsWith('ch') || kindLower.endsWith('sh')) {
    return kindLower + 'es'
  }
  if (kindLower.endsWith('y') && !/[aeiou]y$/.test(kindLower)) {
    return kindLower.slice(0, -1) + 'ies'
  }
  return kindLower + 's'
}

/**
 * Convert a plural API resource name (e.g., "deployments") back to singular PascalCase kind (e.g., "Deployment").
 * Inverse of kindToPlural. Converts plural API resource names from URLs back to
 * singular PascalCase form for internal logic (health checks, badge colors, hierarchy matching).
 */
export function pluralToKind(plural: string): string {
  const lower = plural.toLowerCase()

  if (PLURAL_TO_KIND[lower]) return PLURAL_TO_KIND[lower]

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
