import type { APIResource } from '../types'

// Group resources by category for sidebar display
export interface ResourceCategory {
  name: string
  resources: APIResource[]
}

// Known core resource categories
const WORKLOAD_KINDS = ['Pod', 'Deployment', 'Rollout', 'DaemonSet', 'StatefulSet', 'ReplicaSet', 'Job', 'CronJob']
const NETWORKING_KINDS = ['Service', 'Ingress', 'IngressClass', 'NetworkPolicy', 'Endpoints', 'EndpointSlice']
const CONFIG_KINDS = ['ConfigMap', 'Secret', 'HorizontalPodAutoscaler', 'PodDisruptionBudget', 'LimitRange', 'ResourceQuota', 'PriorityClass', 'RuntimeClass', 'Lease', 'MutatingWebhookConfiguration', 'ValidatingWebhookConfiguration']
const STORAGE_KINDS = ['PersistentVolumeClaim', 'PersistentVolume', 'StorageClass', 'VolumeAttachment']
const ACCESS_CONTROL_KINDS = ['ServiceAccount', 'Role', 'ClusterRole', 'RoleBinding', 'ClusterRoleBinding']
const CLUSTER_KINDS = ['Node', 'Namespace', 'Event']
const CORE_CATEGORY_NAMES = new Set(['Workloads', 'Networking', 'Configuration', 'Storage', 'Access Control', 'Cluster'])

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
  { group: 'discovery.k8s.io', version: 'v1', kind: 'EndpointSlice', name: 'endpointslices', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'autoscaling', version: 'v2', kind: 'HorizontalPodAutoscaler', name: 'horizontalpodautoscalers', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: '', version: 'v1', kind: 'Event', name: 'events', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'rbac.authorization.k8s.io', version: 'v1', kind: 'Role', name: 'roles', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'rbac.authorization.k8s.io', version: 'v1', kind: 'ClusterRole', name: 'clusterroles', namespaced: false, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'rbac.authorization.k8s.io', version: 'v1', kind: 'RoleBinding', name: 'rolebindings', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'rbac.authorization.k8s.io', version: 'v1', kind: 'ClusterRoleBinding', name: 'clusterrolebindings', namespaced: false, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'networking.k8s.io', version: 'v1', kind: 'IngressClass', name: 'ingressclasses', namespaced: false, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'admissionregistration.k8s.io', version: 'v1', kind: 'MutatingWebhookConfiguration', name: 'mutatingwebhookconfigurations', namespaced: false, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'admissionregistration.k8s.io', version: 'v1', kind: 'ValidatingWebhookConfiguration', name: 'validatingwebhookconfigurations', namespaced: false, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'scheduling.k8s.io', version: 'v1', kind: 'PriorityClass', name: 'priorityclasses', namespaced: false, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'node.k8s.io', version: 'v1', kind: 'RuntimeClass', name: 'runtimeclasses', namespaced: false, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'coordination.k8s.io', version: 'v1', kind: 'Lease', name: 'leases', namespaced: true, isCrd: false, verbs: ['list', 'get', 'watch'] },
  { group: 'storage.k8s.io', version: 'v1', kind: 'VolumeAttachment', name: 'volumeattachments', namespaced: false, isCrd: false, verbs: ['list', 'get', 'watch'] },
]

// Resources that should be hidden from the sidebar
const HIDDEN_KINDS = ['PodMetrics', 'NodeMetrics']

export function categorizeResources(resources: APIResource[]): ResourceCategory[] {
  const listableResources = resources.filter(r =>
    r.verbs?.includes('list') && !HIDDEN_KINDS.includes(r.kind) &&
    !(r.kind === 'Event' && r.group === 'events.k8s.io')
  )

  const seenKinds = new Map<string, APIResource>()
  const dedupKey = (r: APIResource) => r.isCrd ? `${r.group}/${r.kind}` : r.kind

  for (const resource of CORE_RESOURCES) {
    seenKinds.set(dedupKey(resource), resource)
  }
  for (const resource of listableResources) {
    seenKinds.set(dedupKey(resource), resource)
  }
  const uniqueResources = Array.from(seenKinds.values())

  const categoryMap = new Map<string, APIResource[]>()
  function addToCategory(name: string, items: APIResource[]) {
    if (items.length === 0) return
    const existing = categoryMap.get(name) || []
    categoryMap.set(name, [...existing, ...items])
  }

  const coreResources = uniqueResources.filter(r => !r.isCrd)
  const workloads = coreResources.filter(r => WORKLOAD_KINDS.includes(r.kind))
  const networking = coreResources.filter(r => NETWORKING_KINDS.includes(r.kind))
  const config = coreResources.filter(r => CONFIG_KINDS.includes(r.kind))
  const storage = coreResources.filter(r => STORAGE_KINDS.includes(r.kind))
  const accessControl = coreResources.filter(r => ACCESS_CONTROL_KINDS.includes(r.kind))
  const cluster = coreResources.filter(r => CLUSTER_KINDS.includes(r.kind))

  const crds = uniqueResources.filter(r => r.isCrd)
  const crdGroups = new Map<string, APIResource[]>()
  for (const crd of crds) {
    const group = crd.group || 'custom'
    if (!crdGroups.has(group)) crdGroups.set(group, [])
    crdGroups.get(group)!.push(crd)
  }

  addToCategory('Workloads', workloads)
  addToCategory('Networking', networking)
  addToCategory('Configuration', config)
  addToCategory('Storage', storage)
  addToCategory('Access Control', accessControl)
  addToCategory('Cluster', cluster)

  for (const [group, groupResources] of crdGroups) {
    addToCategory(formatGroupName(group), groupResources)
  }

  return Array.from(categoryMap.entries()).map(([name, items]) => ({
    name,
    resources: sortResources(items),
  }))
}

export function formatGroupName(group: string): string {
  const knownGroups: Record<string, string> = {
    'argoproj.io': 'Argo',
    'apiregistration.k8s.io': 'API Registration',
    'cert-manager.io': 'Cert Manager',
    'acme.cert-manager.io': 'Cert Manager',
    'istio.io': 'Istio',
    'networking.istio.io': 'Istio',
    'security.istio.io': 'Istio',
    'telemetry.istio.io': 'Istio',
    'monitoring.coreos.com': 'Prometheus',
    'monitoring.googleapis.com': 'Google Cloud Monitoring',
    'velero.io': 'Velero',
    'external-secrets.io': 'External Secrets',
    'keda.sh': 'KEDA',
    'gateway.networking.k8s.io': 'Gateway API',
    'gateway.envoyproxy.io': 'Envoy Gateway',
    'traefik.io': 'Traefik',
    'traefik.containo.us': 'Traefik',
    'crossplane.io': 'Crossplane',
    'pkg.crossplane.io': 'Crossplane',
    'apiextensions.crossplane.io': 'Crossplane',
    'helm.crossplane.io': 'Crossplane',
    'kubernetes.crossplane.io': 'Crossplane',
    'source.toolkit.fluxcd.io': 'Flux',
    'helm.toolkit.fluxcd.io': 'Flux',
    'kustomize.toolkit.fluxcd.io': 'Flux',
    'notification.toolkit.fluxcd.io': 'Flux',
    'image.toolkit.fluxcd.io': 'Flux',
    'serving.knative.dev': 'Knative',
    'eventing.knative.dev': 'Knative',
    'messaging.knative.dev': 'Knative',
    'sources.knative.dev': 'Knative',
    'networking.internal.knative.dev': 'Knative',
    'flows.knative.dev': 'Knative',
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
    'cluster.x-k8s.io': 'Cluster API',
    'controlplane.cluster.x-k8s.io': 'Cluster API',
    'bootstrap.cluster.x-k8s.io': 'Cluster API',
    'infrastructure.cluster.x-k8s.io': 'Cluster API',
    'ceph.rook.io': 'Rook',
    'kyverno.io': 'Kyverno',
    'policies.kyverno.io': 'Kyverno',
    'k8s.nginx.org': 'NGINX',
    'networking.gke.io': 'GKE Networking',
    'warden.gke.io': 'GKE Warden',
    'cloud.google.com': 'Google Cloud',
    'sparkoperator.k8s.io': 'Spark',
    'kubeflow.org': 'Kubeflow',
    'snapshot.storage.k8s.io': 'Snapshots',
    'karpenter.sh': 'Karpenter',
    'karpenter.k8s.aws': 'Karpenter',
    'karpenter.azure.com': 'Karpenter',
    'karpenter.k8s.gcp': 'Karpenter',
    'resource.k8s.io': 'Dynamic Resource Allocation',
    'kueue.x-k8s.io': 'Kueue',
    'autoscaling.x-k8s.io': 'Cluster Autoscaler',
    'crd.k8s.amazonaws.com': 'AWS VPC CNI',
    'vpcresources.k8s.aws': 'AWS VPC CNI',
    'elbv2.k8s.aws': 'AWS Load Balancer',
    'eks.amazonaws.com': 'EKS',
    'networking.k8s.aws': 'AWS Networking',
    'acid.zalan.do': 'Zalando Postgres',
    'serving.kserve.io': 'KServe',
    'ray.io': 'KubeRay',
    'leaderworkerset.x-k8s.io': 'LeaderWorkerSet',
    'jobset.x-k8s.io': 'JobSet',
    'inference.networking.k8s.io': 'Inference Gateway',
    'inference.networking.x-k8s.io': 'Inference Gateway',
    'nvidia.com': 'NVIDIA GPU Operator',
    'scheduling.run.ai': 'KAI Scheduler',
    'kai.scheduler': 'KAI Scheduler',
    'kaito.sh': 'KAITO',
    'batch.volcano.sh': 'Volcano',
    'scheduling.volcano.sh': 'Volcano',
    'flow.volcano.sh': 'Volcano',
    'bus.volcano.sh': 'Volcano',
  }
  if (knownGroups[group]) return knownGroups[group]
  // Suffix rules — for unbounded provider group sets (Crossplane providers ship
  // their own per-service groups like s3.aws.upbound.io, compute.gcp.upbound.io).
  if (group.endsWith('.upbound.io')) return 'Crossplane'
  if (group.endsWith('.crossplane.io')) return 'Crossplane'
  if (group === 'cluster.x-k8s.io' || group.endsWith('.cluster.x-k8s.io')) return 'Cluster API'
  if (group.endsWith('.cnrm.cloud.google.com')) return 'Config Connector'
  if (group.endsWith('.openshift.io')) return 'OpenShift'
  return formatUnmappedGroupName(group)
}

export function shortenGroupName(group: string): string {
  return group
    .replace(/\.(io|com|org|dev|sh)$/, '')
    .replace(/\.k8s$/, '')
}

const GROUP_SUFFIX_LABELS = new Set([
  'io', 'com', 'org', 'dev', 'sh', 'net', 'do', 'ai', 'cloud', 'co', 'app',
  'k8s', 'x-k8s', 'k8s-sigs', 'sigs',
])
const GROUP_ACRONYMS: Record<string, string> = {
  api: 'API',
  aws: 'AWS',
  amazonaws: 'AWS',
  cnrm: 'CNRM',
  crd: 'CRD',
  csi: 'CSI',
  dns: 'DNS',
  gcp: 'GCP',
  gke: 'GKE',
  gpu: 'GPU',
  hpa: 'HPA',
  ipam: 'IPAM',
  k8s: 'K8s',
  nfd: 'NFD',
  tcp: 'TCP',
  tls: 'TLS',
  udp: 'UDP',
}

function formatUnmappedGroupName(group: string): string {
  const labels = group
    .split('.')
    .map(label => label.trim().toLowerCase())
    .filter(label => label && !GROUP_SUFFIX_LABELS.has(label))

  if (labels.length === 0) return group
  if (labels.length === 1) return avoidCoreCategoryCollision(formatGroupToken(labels[0]))

  const owner = labels[labels.length - 1]
  const descriptors = labels.slice(0, -1)
  return avoidCoreCategoryCollision([owner, ...descriptors].map(formatGroupToken).join(' '))
}

function avoidCoreCategoryCollision(label: string): string {
  return CORE_CATEGORY_NAMES.has(label) ? `${label} APIs` : label
}

function formatGroupToken(token: string): string {
  return token
    .split(/[-_]/)
    .filter(Boolean)
    .map(part => GROUP_ACRONYMS[part] ?? part.charAt(0).toUpperCase() + part.slice(1))
    .join(' ')
}

function sortResources(resources: APIResource[]): APIResource[] {
  return [...resources].sort((a, b) => a.kind.localeCompare(b.kind))
}

export function getKindLabel(kind: string): string {
  return kind.replace(/([A-Z])/g, ' $1').trim()
}

export function getKindPlural(resource: APIResource): string {
  return resource.name
}
