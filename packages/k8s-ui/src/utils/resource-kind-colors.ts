import type { LucideIcon } from 'lucide-react'
import type { NodeKind } from '../types'
import { getTopologyIcon } from './resource-icons'
import { CORE_RESOURCES } from './api-resources'

// Resource kind configuration
export const TOPOLOGY_RESOURCE_KINDS: {
  kind: NodeKind
  label: string
  icon: LucideIcon
  color: string
  groups?: readonly string[]
  resourceKind?: string
  category: 'gitops' | 'workloads' | 'networking' | 'config' | 'scaling' | 'custom'
}[] = [
  // GitOps (ArgoCD + FluxCD)
  { kind: 'Application', groups: ['argoproj.io'], label: 'Application', icon: getTopologyIcon('Application'), color: 'text-orange-400', category: 'gitops' },
  { kind: 'Kustomization', groups: ['kustomize.toolkit.fluxcd.io'], label: 'Kustomization', icon: getTopologyIcon('Kustomization'), color: 'text-sky-400', category: 'gitops' },
  { kind: 'HelmRelease', groups: ['helm.toolkit.fluxcd.io'], label: 'HelmRelease', icon: getTopologyIcon('HelmRelease'), color: 'text-sky-400', category: 'gitops' },
  { kind: 'GitRepository', groups: ['source.toolkit.fluxcd.io'], label: 'GitRepository', icon: getTopologyIcon('GitRepository'), color: 'text-teal-400', category: 'gitops' },

  // Networking
  { kind: 'Ingress', label: 'Ingress', icon: getTopologyIcon('Ingress'), color: 'text-purple-400', category: 'networking' },
  { kind: 'Gateway', groups: ['gateway.networking.k8s.io'], label: 'Gateway', icon: getTopologyIcon('Gateway'), color: 'text-purple-400', category: 'networking' },
  { kind: 'HTTPRoute', groups: ['gateway.networking.k8s.io'], label: 'HTTPRoute', icon: getTopologyIcon('HTTPRoute'), color: 'text-purple-300', category: 'networking' },
  { kind: 'GRPCRoute', groups: ['gateway.networking.k8s.io'], label: 'GRPCRoute', icon: getTopologyIcon('GRPCRoute'), color: 'text-purple-300', category: 'networking' },
  { kind: 'TCPRoute', groups: ['gateway.networking.k8s.io'], label: 'TCPRoute', icon: getTopologyIcon('TCPRoute'), color: 'text-purple-300', category: 'networking' },
  { kind: 'TLSRoute', groups: ['gateway.networking.k8s.io'], label: 'TLSRoute', icon: getTopologyIcon('TLSRoute'), color: 'text-purple-300', category: 'networking' },
  { kind: 'UDPRoute', groups: ['gateway.networking.k8s.io'], label: 'UDPRoute', icon: getTopologyIcon('UDPRoute'), color: 'text-purple-300', category: 'networking' },
  { kind: 'Service', label: 'Service', icon: getTopologyIcon('Service'), color: 'text-blue-400', category: 'networking' },
  { kind: 'CalicoNetworkPolicy', groups: ['projectcalico.org', 'crd.projectcalico.org'], resourceKind: 'NetworkPolicy', label: 'Calico NetworkPolicy', icon: getTopologyIcon('CalicoNetworkPolicy'), color: 'text-teal-400', category: 'custom' },
  { kind: 'CalicoGlobalNetworkPolicy', groups: ['projectcalico.org', 'crd.projectcalico.org'], resourceKind: 'GlobalNetworkPolicy', label: 'Calico GlobalNetworkPolicy', icon: getTopologyIcon('CalicoGlobalNetworkPolicy'), color: 'text-teal-400', category: 'custom' },
  { kind: 'CalicoStagedNetworkPolicy', groups: ['projectcalico.org', 'crd.projectcalico.org'], resourceKind: 'StagedNetworkPolicy', label: 'Calico StagedNetworkPolicy', icon: getTopologyIcon('CalicoStagedNetworkPolicy'), color: 'text-amber-400', category: 'custom' },
  { kind: 'CalicoStagedGlobalNetworkPolicy', groups: ['projectcalico.org', 'crd.projectcalico.org'], resourceKind: 'StagedGlobalNetworkPolicy', label: 'Calico StagedGlobalNetworkPolicy', icon: getTopologyIcon('CalicoStagedGlobalNetworkPolicy'), color: 'text-amber-400', category: 'custom' },
  { kind: 'CalicoStagedKubernetesNetworkPolicy', groups: ['projectcalico.org', 'crd.projectcalico.org'], resourceKind: 'StagedKubernetesNetworkPolicy', label: 'Calico StagedKubernetesNetworkPolicy', icon: getTopologyIcon('CalicoStagedKubernetesNetworkPolicy'), color: 'text-amber-400', category: 'custom' },

  // Workloads
  { kind: 'Deployment', label: 'Deployment', icon: getTopologyIcon('Deployment'), color: 'text-emerald-400', category: 'workloads' },
  { kind: 'Rollout', groups: ['argoproj.io'], label: 'Rollout', icon: getTopologyIcon('Rollout'), color: 'text-emerald-400', category: 'workloads' },
  { kind: 'DaemonSet', label: 'DaemonSet', icon: getTopologyIcon('DaemonSet'), color: 'text-teal-400', category: 'workloads' },
  { kind: 'StatefulSet', label: 'StatefulSet', icon: getTopologyIcon('StatefulSet'), color: 'text-cyan-400', category: 'workloads' },
  { kind: 'ReplicaSet', label: 'ReplicaSet', icon: getTopologyIcon('ReplicaSet'), color: 'text-green-400', category: 'workloads' },
  { kind: 'Pod', label: 'Pod', icon: getTopologyIcon('Pod'), color: 'text-lime-400', category: 'workloads' },
  { kind: 'PodGroup', groups: ['scheduling.volcano.sh', 'scheduling.run.ai'], label: 'Pod Group', icon: getTopologyIcon('PodGroup'), color: 'text-lime-400', category: 'workloads' },
  { kind: 'Job', label: 'Job', icon: getTopologyIcon('Job'), color: 'text-orange-400', category: 'workloads' },
  { kind: 'CronJob', label: 'CronJob', icon: getTopologyIcon('CronJob'), color: 'text-orange-300', category: 'workloads' },
  { kind: 'JobSet', groups: ['jobset.x-k8s.io'], label: 'JobSet', icon: getTopologyIcon('JobSet'), color: 'text-orange-400', category: 'workloads' },
  { kind: 'Workflow', groups: ['argoproj.io'], label: 'Workflow', icon: getTopologyIcon('Workflow'), color: 'text-purple-400', category: 'workloads' },
  { kind: 'CronWorkflow', groups: ['argoproj.io'], label: 'CronWorkflow', icon: getTopologyIcon('CronWorkflow'), color: 'text-purple-300', category: 'workloads' },

  // Config
  { kind: 'ConfigMap', label: 'ConfigMap', icon: getTopologyIcon('ConfigMap'), color: 'text-amber-400', category: 'config' },
  { kind: 'Secret', label: 'Secret', icon: getTopologyIcon('Secret'), color: 'text-red-400', category: 'config' },
  { kind: 'ServiceAccount', label: 'Service Account', icon: getTopologyIcon('ServiceAccount'), color: 'text-fuchsia-400', category: 'config' },
  { kind: 'SealedSecret', groups: ['bitnami.com'], label: 'Sealed Secret', icon: getTopologyIcon('SealedSecret'), color: 'text-violet-400', category: 'config' },
  { kind: 'ServiceMonitor', groups: ['monitoring.coreos.com'], label: 'Service Monitor', icon: getTopologyIcon('ServiceMonitor'), color: 'text-sky-400', category: 'config' },
  { kind: 'PodMonitor', groups: ['monitoring.coreos.com'], label: 'Pod Monitor', icon: getTopologyIcon('PodMonitor'), color: 'text-sky-400', category: 'config' },
  { kind: 'WorkflowTemplate', groups: ['argoproj.io'], label: 'WorkflowTemplate', icon: getTopologyIcon('WorkflowTemplate'), color: 'text-purple-300', category: 'config' },
  { kind: 'ClusterWorkflowTemplate', groups: ['argoproj.io'], label: 'ClusterWorkflowTemplate', icon: getTopologyIcon('ClusterWorkflowTemplate'), color: 'text-purple-300', category: 'config' },

  // Scaling
  { kind: 'HorizontalPodAutoscaler', label: 'HPA', icon: getTopologyIcon('HorizontalPodAutoscaler'), color: 'text-pink-400', category: 'scaling' },
  { kind: 'ScaledJob', groups: ['keda.sh'], label: 'ScaledJob', icon: getTopologyIcon('ScaledJob'), color: 'text-pink-300', category: 'scaling' },

  // Knative
  { kind: 'KnativeService', groups: ['serving.knative.dev'], resourceKind: 'Service', label: 'Knative Service', icon: getTopologyIcon('KnativeService'), color: 'text-fuchsia-400', category: 'custom' },
  { kind: 'KnativeConfiguration', groups: ['serving.knative.dev'], resourceKind: 'Configuration', label: 'Knative Config', icon: getTopologyIcon('KnativeConfiguration'), color: 'text-fuchsia-400', category: 'custom' },
  { kind: 'KnativeRevision', groups: ['serving.knative.dev'], resourceKind: 'Revision', label: 'Revision', icon: getTopologyIcon('KnativeRevision'), color: 'text-fuchsia-400', category: 'custom' },
  { kind: 'KnativeRoute', groups: ['serving.knative.dev'], resourceKind: 'Route', label: 'Route', icon: getTopologyIcon('KnativeRoute'), color: 'text-fuchsia-400', category: 'custom' },
  { kind: 'Broker', groups: ['eventing.knative.dev'], label: 'Broker', icon: getTopologyIcon('Broker'), color: 'text-fuchsia-400', category: 'custom' },
  { kind: 'Channel', groups: ['messaging.knative.dev'], label: 'Channel', icon: getTopologyIcon('Channel'), color: 'text-fuchsia-400', category: 'custom' },
  { kind: 'Trigger', groups: ['eventing.knative.dev'], label: 'Trigger', icon: getTopologyIcon('Trigger'), color: 'text-fuchsia-400', category: 'custom' },
  { kind: 'PingSource', groups: ['sources.knative.dev'], label: 'PingSource', icon: getTopologyIcon('PingSource'), color: 'text-fuchsia-400', category: 'custom' },
  { kind: 'ApiServerSource', groups: ['sources.knative.dev'], label: 'ApiServerSource', icon: getTopologyIcon('ApiServerSource'), color: 'text-fuchsia-400', category: 'custom' },
  { kind: 'ContainerSource', groups: ['sources.knative.dev'], label: 'ContainerSource', icon: getTopologyIcon('ContainerSource'), color: 'text-fuchsia-400', category: 'custom' },
  { kind: 'SinkBinding', groups: ['sources.knative.dev'], label: 'SinkBinding', icon: getTopologyIcon('SinkBinding'), color: 'text-fuchsia-400', category: 'custom' },

  // Traefik
  { kind: 'IngressRoute', groups: ['traefik.io', 'traefik.containo.us'], label: 'IngressRoute', icon: getTopologyIcon('IngressRoute'), color: 'text-cyan-400', category: 'networking' },
  { kind: 'IngressRouteTCP', groups: ['traefik.io', 'traefik.containo.us'], label: 'TCP Route', icon: getTopologyIcon('IngressRouteTCP'), color: 'text-cyan-400', category: 'networking' },
  { kind: 'IngressRouteUDP', groups: ['traefik.io', 'traefik.containo.us'], label: 'UDP Route', icon: getTopologyIcon('IngressRouteUDP'), color: 'text-cyan-400', category: 'networking' },
  { kind: 'Middleware', groups: ['traefik.io', 'traefik.containo.us'], label: 'Middleware', icon: getTopologyIcon('Middleware'), color: 'text-cyan-400', category: 'custom' },
  { kind: 'MiddlewareTCP', groups: ['traefik.io', 'traefik.containo.us'], label: 'MW TCP', icon: getTopologyIcon('MiddlewareTCP'), color: 'text-cyan-400', category: 'custom' },
  { kind: 'TraefikService', groups: ['traefik.io', 'traefik.containo.us'], label: 'Traefik Svc', icon: getTopologyIcon('TraefikService'), color: 'text-cyan-400', category: 'custom' },
  { kind: 'ServersTransport', groups: ['traefik.io', 'traefik.containo.us'], label: 'Transport', icon: getTopologyIcon('ServersTransport'), color: 'text-cyan-400', category: 'custom' },
  { kind: 'ServersTransportTCP', groups: ['traefik.io', 'traefik.containo.us'], label: 'Transport TCP', icon: getTopologyIcon('ServersTransportTCP'), color: 'text-cyan-400', category: 'custom' },
  { kind: 'TLSOption', groups: ['traefik.io', 'traefik.containo.us'], label: 'TLS Option', icon: getTopologyIcon('TLSOption'), color: 'text-cyan-400', category: 'custom' },
  { kind: 'TLSStore', groups: ['traefik.io', 'traefik.containo.us'], label: 'TLS Store', icon: getTopologyIcon('TLSStore'), color: 'text-cyan-400', category: 'custom' },

  // Contour
  { kind: 'HTTPProxy', groups: ['projectcontour.io'], label: 'HTTPProxy', icon: getTopologyIcon('HTTPProxy'), color: 'text-violet-400', category: 'networking' },

  // Cluster API
  { kind: 'CAPICluster', groups: ['cluster.x-k8s.io'], resourceKind: 'Cluster', label: 'CAPI Cluster', icon: getTopologyIcon('CAPICluster'), color: 'text-indigo-400', category: 'custom' },
  { kind: 'MachineDeployment', groups: ['cluster.x-k8s.io'], label: 'Machine Deploy', icon: getTopologyIcon('MachineDeployment'), color: 'text-indigo-400', category: 'custom' },
  { kind: 'MachineSet', groups: ['cluster.x-k8s.io'], label: 'MachineSet', icon: getTopologyIcon('MachineSet'), color: 'text-indigo-400', category: 'custom' },
  { kind: 'Machine', groups: ['cluster.x-k8s.io'], label: 'Machine', icon: getTopologyIcon('Machine'), color: 'text-indigo-400', category: 'custom' },
  { kind: 'MachinePool', groups: ['cluster.x-k8s.io'], label: 'MachinePool', icon: getTopologyIcon('MachinePool'), color: 'text-indigo-400', category: 'custom' },
  { kind: 'KubeadmControlPlane', groups: ['controlplane.cluster.x-k8s.io'], label: 'Control Plane', icon: getTopologyIcon('KubeadmControlPlane'), color: 'text-indigo-400', category: 'custom' },
  { kind: 'ClusterClass', groups: ['cluster.x-k8s.io'], label: 'ClusterClass', icon: getTopologyIcon('ClusterClass'), color: 'text-indigo-400', category: 'custom' },
  { kind: 'MachineHealthCheck', groups: ['cluster.x-k8s.io'], label: 'Health Check', icon: getTopologyIcon('MachineHealthCheck'), color: 'text-indigo-400', category: 'custom' },
  // AWS CAPI infrastructure
  { kind: 'AWSManagedControlPlane', groups: ['controlplane.cluster.x-k8s.io'], label: 'AWS Control Plane', icon: getTopologyIcon('AWSManagedControlPlane'), color: 'text-amber-400', category: 'custom' },
  { kind: 'AWSManagedMachinePool', groups: ['infrastructure.cluster.x-k8s.io'], label: 'AWS Machine Pool', icon: getTopologyIcon('AWSManagedMachinePool'), color: 'text-amber-400', category: 'custom' },
  { kind: 'AWSMachine', groups: ['infrastructure.cluster.x-k8s.io'], label: 'AWS Machine', icon: getTopologyIcon('AWSMachine'), color: 'text-amber-400', category: 'custom' },
  { kind: 'EKSConfig', groups: ['bootstrap.cluster.x-k8s.io'], label: 'EKS Config', icon: getTopologyIcon('EKSConfig'), color: 'text-amber-400', category: 'custom' },
]


const iconColors = new Map(TOPOLOGY_RESOURCE_KINDS.map(({ kind, color }) => [kind.toLowerCase(), color]))
const resourceIconColors = new Map(TOPOLOGY_RESOURCE_KINDS.flatMap(({ kind, resourceKind, groups, color }) => {
  const exactGroups = groups ?? CORE_RESOURCES.filter(resource => resource.kind === kind).map(resource => resource.group)
  return exactGroups.map((group): [string, string] => [`${group}/${(resourceKind ?? kind).toLowerCase()}`, color])
}))

export function getResourceKindIconColor(kind: string, group?: string): string {
  const color = group === undefined
    ? iconColors.get(kind.toLowerCase())
    : resourceIconColors.get(`${group}/${kind.toLowerCase()}`)
  return color ?? 'text-gray-400'
}
