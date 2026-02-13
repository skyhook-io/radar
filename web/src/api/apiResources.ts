import { useQuery } from '@tanstack/react-query'
import type { APIResource } from '../types'

const API_BASE = '/api'

async function fetchJSON<T>(path: string): Promise<T> {
  const response = await fetch(`${API_BASE}${path}`)
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Unknown error' }))
    throw new Error(error.error || `HTTP ${response.status}`)
  }
  return response.json()
}

// Fetch all API resources from the cluster
export function useAPIResources() {
  return useQuery<APIResource[]>({
    queryKey: ['api-resources'],
    queryFn: () => fetchJSON('/api-resources'),
    staleTime: 5 * 60 * 1000, // 5 minutes - resources don't change often
  })
}

// Group resources by category for sidebar display
export interface ResourceCategory {
  name: string
  resources: APIResource[]
}

// Known core resource categories
const WORKLOAD_KINDS = ['Pod', 'Deployment', 'Rollout', 'DaemonSet', 'StatefulSet', 'ReplicaSet', 'Job', 'CronJob']
const NETWORKING_KINDS = ['Service', 'Ingress', 'NetworkPolicy', 'Endpoints', 'EndpointSlice']
const CONFIG_KINDS = ['ConfigMap', 'Secret', 'HorizontalPodAutoscaler', 'PodDisruptionBudget', 'LimitRange', 'ResourceQuota']
const STORAGE_KINDS = ['PersistentVolumeClaim', 'PersistentVolume', 'StorageClass', 'VolumeAttachment']
const CLUSTER_KINDS = ['Node', 'Namespace', 'ServiceAccount', 'Event']

// Core resources that must always be present (fallback if API discovery misses them)
export const CORE_RESOURCES: APIResource[] = [
  { group: '', version: 'v1', kind: 'Pod', name: 'pods', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: '', version: 'v1', kind: 'Service', name: 'services', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: '', version: 'v1', kind: 'ConfigMap', name: 'configmaps', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: '', version: 'v1', kind: 'Secret', name: 'secrets', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: '', version: 'v1', kind: 'Node', name: 'nodes', namespaced: false, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: '', version: 'v1', kind: 'Namespace', name: 'namespaces', namespaced: false, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: '', version: 'v1', kind: 'ServiceAccount', name: 'serviceaccounts', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: '', version: 'v1', kind: 'PersistentVolumeClaim', name: 'persistentvolumeclaims', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: '', version: 'v1', kind: 'PersistentVolume', name: 'persistentvolumes', namespaced: false, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'apps', version: 'v1', kind: 'Deployment', name: 'deployments', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'apps', version: 'v1', kind: 'DaemonSet', name: 'daemonsets', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'apps', version: 'v1', kind: 'StatefulSet', name: 'statefulsets', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'apps', version: 'v1', kind: 'ReplicaSet', name: 'replicasets', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'batch', version: 'v1', kind: 'Job', name: 'jobs', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'batch', version: 'v1', kind: 'CronJob', name: 'cronjobs', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'networking.k8s.io', version: 'v1', kind: 'Ingress', name: 'ingresses', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'networking.k8s.io', version: 'v1', kind: 'NetworkPolicy', name: 'networkpolicies', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'autoscaling', version: 'v2', kind: 'HorizontalPodAutoscaler', name: 'horizontalpodautoscalers', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: '', version: 'v1', kind: 'Event', name: 'events', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
]

// Resources that should be hidden from the sidebar
// These are either internal or their data is shown elsewhere (e.g., metrics shown in Pod/Node views)
const HIDDEN_KINDS = ['PodMetrics', 'NodeMetrics']

export function categorizeResources(resources: APIResource[]): ResourceCategory[] {
  // Filter out non-listable resources (e.g., tokenreviews, subjectaccessreviews)
  // Also filter out hidden resources like metrics (shown in Pod/Node views instead)
  // Filter events.k8s.io Event — duplicate of core v1 Event (same data, backend uses core v1)
  const listableResources = resources.filter(r =>
    r.verbs?.includes('list') && !HIDDEN_KINDS.includes(r.kind) &&
    !(r.kind === 'Event' && r.group === 'events.k8s.io')
  )

  // Deduplicate by kind (not plural name) to handle cases like:
  // - Pod (core/v1, name: pods) vs PodMetrics (metrics.k8s.io, name: pods)
  // - HorizontalPodAutoscaler (autoscaling/v1 and autoscaling/v2)
  const seenKinds = new Map<string, APIResource>()

  // Add core resources first as fallback
  for (const resource of CORE_RESOURCES) {
    seenKinds.set(resource.kind, resource)
  }

  // Add discovered resources (will override fallbacks with real data)
  for (const resource of listableResources) {
    seenKinds.set(resource.kind, resource)
  }
  const uniqueResources = Array.from(seenKinds.values())

  // Use a Map to merge categories with the same name
  const categoryMap = new Map<string, APIResource[]>()

  function addToCategory(name: string, items: APIResource[]) {
    if (items.length === 0) return
    const existing = categoryMap.get(name) || []
    categoryMap.set(name, [...existing, ...items])
  }

  const workloads = uniqueResources.filter(r => WORKLOAD_KINDS.includes(r.kind))
  const networking = uniqueResources.filter(r => NETWORKING_KINDS.includes(r.kind))
  const config = uniqueResources.filter(r => CONFIG_KINDS.includes(r.kind))
  const storage = uniqueResources.filter(r => STORAGE_KINDS.includes(r.kind))
  const cluster = uniqueResources.filter(r => CLUSTER_KINDS.includes(r.kind))

  // CRDs grouped by API group
  const crds = uniqueResources.filter(r => r.isCrd)
  const crdGroups = new Map<string, APIResource[]>()
  for (const crd of crds) {
    const group = crd.group || 'custom'
    if (!crdGroups.has(group)) {
      crdGroups.set(group, [])
    }
    crdGroups.get(group)!.push(crd)
  }

  // Build categories (order matters for display)
  addToCategory('Workloads', workloads)
  addToCategory('Networking', networking)
  addToCategory('Configuration', config)
  addToCategory('Storage', storage)
  addToCategory('Cluster', cluster)

  // Add CRD groups (may merge with existing categories if names match)
  for (const [group, groupResources] of crdGroups) {
    addToCategory(formatGroupName(group), groupResources)
  }

  // Convert map to array, preserving insertion order and sorting resources
  return Array.from(categoryMap.entries()).map(([name, items]) => ({
    name,
    resources: sortResources(items),
  }))
}

// Format API group name for display in sidebar categories (e.g., "argoproj.io" -> "Argo")
// This is used where we want friendly category names
export function formatGroupName(group: string): string {
  const knownGroups: Record<string, string> = {
    'argoproj.io': 'Argo',
    'cert-manager.io': 'Cert Manager',
    'acme.cert-manager.io': 'Cert Manager',
    'istio.io': 'Istio',
    'networking.istio.io': 'Istio',
    'security.istio.io': 'Istio',
    'telemetry.istio.io': 'Istio',
    'monitoring.coreos.com': 'Prometheus',
    'velero.io': 'Velero',
    'external-secrets.io': 'External Secrets',
    'keda.sh': 'KEDA',
    'gateway.networking.k8s.io': 'Gateway API',
    'traefik.io': 'Traefik',
    'traefik.containo.us': 'Traefik',
    'pkg.crossplane.io': 'Crossplane',
    'apiextensions.crossplane.io': 'Crossplane',
    'source.toolkit.fluxcd.io': 'Flux',
    'helm.toolkit.fluxcd.io': 'Flux',
    'kustomize.toolkit.fluxcd.io': 'Flux',
    'notification.toolkit.fluxcd.io': 'Flux',
    'image.toolkit.fluxcd.io': 'Flux',
    'serving.knative.dev': 'Knative',
    'eventing.knative.dev': 'Knative',
    'messaging.knative.dev': 'Knative',
    'sources.knative.dev': 'Knative',
    'kafka.strimzi.io': 'Strimzi',
    'tekton.dev': 'Tekton',
    'linkerd.io': 'Linkerd',
    'policy.linkerd.io': 'Linkerd',
    'cilium.io': 'Cilium',
    'aquasecurity.github.io': 'Trivy',
    'bitnami.com': 'Bitnami',
    'elasticsearch.k8s.elastic.co': 'Elastic',
    'kibana.k8s.elastic.co': 'Elastic',
    'apm.k8s.elastic.co': 'Elastic',
    'beat.k8s.elastic.co': 'Elastic',
    'agent.k8s.elastic.co': 'Elastic',
    'maps.k8s.elastic.co': 'Elastic',
    'logstash.k8s.elastic.co': 'Elastic',
    'jaegertracing.io': 'Jaeger',
    'opentelemetry.io': 'OpenTelemetry',
    'projectcalico.org': 'Calico',
    'crd.projectcalico.org': 'Calico',
    'projectcontour.io': 'Contour',
    'ceph.rook.io': 'Rook',
    'kyverno.io': 'Kyverno',
    'k8s.nginx.org': 'NGINX',
    'sparkoperator.k8s.io': 'Spark',
    'kubeflow.org': 'Kubeflow',
    'snapshot.storage.k8s.io': 'Snapshots',
  }

  if (knownGroups[group]) {
    return knownGroups[group]
  }

  // Fallback: return the plain group name as-is
  return group
}

// Shorten group name for compact display (e.g., on cards)
// Just shows the domain without TLD suffix
export function shortenGroupName(group: string): string {
  // Remove common TLD suffixes
  return group
    .replace(/\.(io|com|org|dev|sh)$/, '')
    .replace(/\.k8s$/, '')  // e.g., elasticsearch.k8s.elastic.co -> elasticsearch.elastic
}

function sortResources(resources: APIResource[]): APIResource[] {
  return [...resources].sort((a, b) => a.kind.localeCompare(b.kind))
}

// Get a display-friendly label for a kind
export function getKindLabel(kind: string): string {
  // Split PascalCase into words
  return kind.replace(/([A-Z])/g, ' $1').trim()
}

// Get the plural name for a kind (used in API calls)
export function getKindPlural(resource: APIResource): string {
  return resource.name
}
