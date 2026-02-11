import type { SelectedResource, ResourceRef } from '../types'

/**
 * Canonical callback type for navigating to a resource.
 * All components that trigger resource navigation should use this type.
 */
export type NavigateToResource = (resource: SelectedResource) => void

/**
 * Convert a singular kind (e.g., "Deployment") to plural API resource name (e.g., "deployments").
 * Single source of truth — replaces the 3 duplicated maps across the codebase.
 */
export function kindToPlural(kind: string): string {
  const kindLower = kind.toLowerCase()
  const irregulars: Record<string, string> = {
    // Core resources
    ingress: 'ingresses',
    configmap: 'configmaps',
    service: 'services',
    deployment: 'deployments',
    statefulset: 'statefulsets',
    daemonset: 'daemonsets',
    replicaset: 'replicasets',
    pod: 'pods',
    secret: 'secrets',
    namespace: 'namespaces',
    // Jobs & scheduling
    job: 'jobs',
    cronjob: 'cronjobs',
    // Autoscaling
    hpa: 'hpas',
    horizontalpodautoscaler: 'horizontalpodautoscalers',
    // Storage
    persistentvolumeclaim: 'persistentvolumeclaims',
    persistentvolume: 'persistentvolumes',
    storageclass: 'storageclasses',
    pvc: 'persistentvolumeclaims',
    // Networking
    gateway: 'gateways',
    httproute: 'httproutes',
    grpcroute: 'grpcroutes',
    tcproute: 'tcproutes',
    tlsroute: 'tlsroutes',
    networkpolicy: 'networkpolicies',
    // RBAC
    serviceaccount: 'serviceaccounts',
    role: 'roles',
    rolebinding: 'rolebindings',
    clusterrole: 'clusterroles',
    clusterrolebinding: 'clusterrolebindings',
    // cert-manager
    certificate: 'certificates',
    certificaterequest: 'certificaterequests',
    clusterissuer: 'clusterissuers',
    // Argo
    rollout: 'rollouts',
    workflow: 'workflows',
    application: 'applications',
    // FluxCD
    kustomization: 'kustomizations',
    helmrelease: 'helmreleases',
    gitrepository: 'gitrepositories',
    ocirepository: 'ocirepositories',
    helmrepository: 'helmrepositories',
    alert: 'alerts',
    // Misc
    poddisruptionbudget: 'poddisruptionbudgets',
    sealedsecret: 'sealedsecrets',
    workflowtemplate: 'workflowtemplates',
    event: 'events',
    node: 'nodes',
    // Topology-specific
    podgroup: 'pods',
  }
  return irregulars[kindLower] || kindLower + 's'
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
