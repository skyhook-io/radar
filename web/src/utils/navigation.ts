import type { SelectedResource, ResourceRef } from '../types'

/**
 * Open a URL in the system browser.
 * In the Wails desktop app, window.open() is swallowed by the webview
 * (the Wails JS runtime is lost after the redirect to localhost), so we
 * call a backend endpoint that opens the URL via the OS. Falls back to
 * window.open() in browser mode (the endpoint returns 404).
 */
export function openExternal(url: string): void {
  fetch('/api/desktop/open-url', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ url }),
  })
    .then((res) => {
      if (!res.ok) {
        window.open(url, '_blank')
      }
    })
    .catch(() => {
      window.open(url, '_blank')
    })
}

/**
 * Canonical callback type for navigating to a resource.
 * All components that trigger resource navigation should use this type.
 */
export type NavigateToResource = (resource: SelectedResource) => void

/**
 * Convert a singular kind (e.g., "Deployment") to plural API resource name (e.g., "deployments").
 * Single source of truth — uses English pluralization rules with a small alias map for
 * abbreviations and special mappings that aren't simple plurals.
 */
export function kindToPlural(kind: string): string {
  const kindLower = kind.toLowerCase()

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
 * Inverse of kindToPlural. Used by ResourceDetailPage which receives the plural form from the URL
 * but needs the singular PascalCase form for internal logic (health checks, badge colors, hierarchy matching).
 */
export function pluralToKind(plural: string): string {
  const lower = plural.toLowerCase()

  // Explicit reverse mappings for irregular/aliased plurals
  const reverseMap: Record<string, string> = {
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

  if (reverseMap[lower]) return reverseMap[lower]

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
