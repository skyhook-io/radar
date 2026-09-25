import { apiUrl, getAuthHeaders, getCredentialsMode } from '../api/config'
import { apiVersionToGroup, kindToPluralWithGroup } from '@skyhook-io/k8s-ui/utils/navigation'
import { topologyNodeResourceKind } from '@skyhook-io/k8s-ui/utils/topology-neighborhood'
import type { SelectedResource, Topology } from '@skyhook-io/k8s-ui/types/core'
import type { SearchHit } from '../api/client'

/**
 * Map a resource-search Hit to a SelectedResource. Hit.kind is the singular
 * Kind (e.g. "Deployment"); downstream openers pluralize. `group` is carried so
 * CRD/core collisions disambiguate (Service vs Knative Service), and the
 * namespace defaults to '' for cluster-scoped hits (Node/Namespace/PV).
 */
export function searchHitToSelectedResource(hit: SearchHit): SelectedResource {
  return { kind: hit.kind, namespace: hit.namespace ?? '', name: hit.name, group: hit.group || undefined }
}

// Re-export shared navigation utilities from @skyhook-io/k8s-ui.
export { kindToPlural, kindToPluralWithGroup, pluralToKind, knownKindForPluralWithGroup, refToSelectedResource, apiVersionToGroup } from '@skyhook-io/k8s-ui/utils/navigation'
export type { NavigateToResource } from '@skyhook-io/k8s-ui/utils/navigation'

const NETWORK_POLICY_TOPOLOGY_KINDS = new Set([
  'NetworkPolicy',
  'CalicoNetworkPolicy',
  'CalicoGlobalNetworkPolicy',
  'CalicoStagedNetworkPolicy',
  'CalicoStagedGlobalNetworkPolicy',
  'CalicoStagedKubernetesNetworkPolicy',
  'CiliumNetworkPolicy',
  'CiliumClusterwideNetworkPolicy',
  'ClusterNetworkPolicy',
])

function networkPolicyGroup(node: Topology['nodes'][number]): string | undefined {
  const apiVersionGroup = apiVersionToGroup(node.data.apiVersion as string | undefined)
  if (apiVersionGroup) return apiVersionGroup

  const sourceGroup = node.data.sourceGroup
  if (typeof sourceGroup === 'string' && sourceGroup) return sourceGroup

  if (node.kind === 'NetworkPolicy') return 'networking.k8s.io'
  return undefined
}

/** Return a resource route only when the policy aggregate has one target. */
export function getNetworkPolicyResourceTarget(topology: Topology | null): { kind: string; group?: string } | undefined {
  const targets = new Map<string, { kind: string; group?: string }>()
  for (const node of topology?.nodes ?? []) {
    if (!NETWORK_POLICY_TOPOLOGY_KINDS.has(node.kind)) continue

    const group = networkPolicyGroup(node)
    const target = {
      kind: kindToPluralWithGroup(topologyNodeResourceKind(node), group ?? ''),
      ...(group ? { group } : {}),
    }
    targets.set(`${target.kind}\u0000${target.group ?? ''}`, target)
  }

  if (targets.size !== 1) return undefined
  for (const target of targets.values()) return target
  return undefined
}

/**
 * Build a /workload/:kind/:namespace/:name URL, preserving the API group as a
 * query param so the WorkloadView can resolve CRDs with colliding kind names.
 * Cluster-scoped resources (Node, PersistentVolume, Namespace, …) have no
 * namespace; they're encoded with a '_' sentinel segment so the path stays
 * positional and WorkloadViewRoute can parse it back. '_' is safe — it's not a
 * valid DNS-1123 namespace label, so it can never collide with a real one.
 */
export type ResourceNavigationTarget = SelectedResource & { run?: string; tab?: string }

export function buildWorkloadPath(resource: ResourceNavigationTarget): string {
  const kind = encodeURIComponent(resource.kind)
  const namespace = encodeURIComponent(resource.namespace || '_')
  const name = encodeURIComponent(resource.name)
  const base = `/workload/${kind}/${namespace}/${name}`
  const params = new URLSearchParams()
  if (resource.group) params.set('apiGroup', resource.group)
  if (resource.run) params.set('run', resource.run)
  if (resource.tab) params.set('tab', resource.tab)
  return params.size ? `${base}?${params}` : base
}

/**
 * Build a /resources/:plural?resource=:namespace/:name URL — the deep link that
 * opens a resource's detail drawer in the resources view. Cluster-scoped
 * resources use ?resource=:name (no slash); the API group rides in ?apiGroup=
 * to disambiguate CRD/core kind collisions. This is the exact form the
 * ResourcesView mount effect parses (the `?resource=` reader in
 * packages/k8s-ui/src/components/resources/ResourcesView.tsx) — keep the two in
 * lockstep.
 *
 * Unlike buildWorkloadPath, this opens the detail drawer for ANY kind,
 * including cluster-scoped resources. Returns a basename-relative path;
 * embedders (Radar Hub) prepend their cluster prefix (e.g. /c/:id).
 */
export function resourcePath(resource: SelectedResource): string {
  const params = new URLSearchParams()
  // No name → nothing to open; the kind list is the sane fallback.
  if (resource.name) {
    params.set('resource', resource.namespace ? `${resource.namespace}/${resource.name}` : resource.name)
  }
  if (resource.group) params.set('apiGroup', resource.group)
  const query = params.toString()
  return `/resources/${kindToPluralWithGroup(resource.kind, resource.group ?? '')}${query ? `?${query}` : ''}`
}

const FULLSCREEN_RESOURCE_KINDS = new Set(['pods', 'deployments', 'statefulsets', 'daemonsets', 'jobs', 'cronjobs', 'nodes'])

export function relatedResourcePath(resource: ResourceNavigationTarget): string {
  const apiKind = kindToPluralWithGroup(resource.kind, resource.group ?? '').toLowerCase()
  if (FULLSCREEN_RESOURCE_KINDS.has(apiKind) || (apiKind === 'jobsets' && resource.group === 'jobset.x-k8s.io')) {
    return buildWorkloadPath({ ...resource, kind: apiKind })
  }
  return resourcePath(resource)
}

// radar-specific: open URL in system browser (desktop app support)
export function openExternal(url: string): void {
  fetch(apiUrl('/desktop/open-url'), {
    method: 'POST',
    credentials: getCredentialsMode(),
    headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
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

// setMainView's cross-view params. A navigation whose search omits
// ?namespaces= reads as an empty pick to App's URL sync, which clears the
// user's namespace scope.
const CROSS_VIEW_PARAMS = ['namespaces', 'ai-run'] as const

/** Carries the current cross-view params onto an in-app path that doesn't set them itself. */
export function withCrossViewParams(path: string, currentSearch: string): string {
  const destination = new URL(path, 'http://radar.invalid')
  const current = new URLSearchParams(currentSearch)
  for (const key of CROSS_VIEW_PARAMS) {
    const value = current.get(key)
    if (value && !destination.searchParams.has(key)) destination.searchParams.set(key, value)
  }
  return `${destination.pathname}${destination.search}${destination.hash}`
}
