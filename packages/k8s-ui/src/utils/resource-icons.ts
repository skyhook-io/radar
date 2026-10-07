// Canonical icon mapping for Kubernetes resource kinds.
// This is the single source of truth — all views should use this mapping.
import type { LucideIcon } from 'lucide-react'
import {
  // Workloads
  Box,
  Rocket,
  Rows3,
  DatabaseZap,
  Copy,
  Play,
  Timer,
  Boxes,
  Workflow,

  // Networking
  Plug,
  DoorOpen,
  Shield,
  ShieldCheck,
  ShieldAlert,
  Radio,
  Globe,
  Network,

  // Config
  FileSliders,
  KeyRound,

  // Storage
  HardDrive,
  Cylinder,
  Database,
  DatabaseBackup,
  Waypoints,
  FileSearch,
  Fingerprint,
  Wand2,
  Sparkles,
  Trash2,
  ShieldOff,

  // Cluster
  Cpu,
  Server,
  FolderOpen,
  UserCog,
  Activity,

  // Scaling
  Scaling,

  // GitOps
  GitBranch,
  Layers,
  Anchor,
  FolderGit2,

  // Knative
  Zap,
  Clock,
  Container,
  Link,
  Route,
  Settings,

  // Traefik
  Split,
  SlidersHorizontal,
  Lock,
  ArrowRightLeft,

  // Cluster API
  HeartPulse,
  BookOpen,

  // Velero
  Cloud,
  Camera,
  Package,

  // Fallback
  Puzzle,
} from 'lucide-react'

const KIND_ICON_MAP: Record<string, LucideIcon> = {
  // Workloads
  pod: Box,
  deployment: Rocket,
  daemonset: Rows3,
  statefulset: DatabaseZap,
  replicaset: Copy,
  job: Play,
  cronjob: Timer,
  rollout: Rocket,

  // Networking
  service: Plug,
  ingress: DoorOpen,
  networkpolicy: ShieldCheck,
  caliconetworkpolicy: ShieldCheck,
  calicoglobalnetworkpolicy: ShieldCheck,
  calicostagednetworkpolicy: ShieldCheck,
  calicostagedglobalnetworkpolicy: ShieldCheck,
  calicostagedkubernetesnetworkpolicy: ShieldCheck,
  ciliumnetworkpolicy: ShieldCheck,
  ciliumclusterwidenetworkpolicy: ShieldCheck,
  clusternetworkpolicy: ShieldCheck,
  endpoints: Radio,
  endpointslice: Radio,
  gateway: DoorOpen,
  httproute: Globe,
  grpcroute: Globe,
  tcproute: Globe,
  tlsroute: Globe,

  // Config
  configmap: FileSliders,
  secret: KeyRound,
  sealedsecret: KeyRound,

  // Storage
  persistentvolumeclaim: HardDrive,
  pvc: HardDrive,
  persistentvolume: Cylinder,
  storageclass: Database,

  // Cluster
  node: Cpu,
  namespace: FolderOpen,
  serviceaccount: UserCog,
  event: Activity,

  // Scaling
  horizontalpodautoscaler: Scaling,
  hpa: Scaling,

  // RBAC
  role: ShieldCheck,
  clusterrole: ShieldCheck,
  rolebinding: ShieldCheck,
  clusterrolebinding: ShieldCheck,

  // Cert-manager
  certificate: ShieldCheck,
  certificaterequest: ShieldCheck,
  clusterissuer: ShieldCheck,

  // Argo
  workflow: Activity,
  cronworkflow: Activity,
  workflowtemplate: Activity,
  clusterworkflowtemplate: Activity,
  application: GitBranch, // ArgoCD Application
  applicationset: GitBranch, // ArgoCD ApplicationSet

  // Tekton
  pipeline: Workflow,
  pipelinerun: Workflow,
  taskrun: Play,

  // FluxCD
  kustomization: Layers, // FluxCD Kustomization
  helmrelease: Anchor, // FluxCD HelmRelease
  gitrepository: FolderGit2, // FluxCD GitRepository
  ocirepository: FolderGit2, // FluxCD OCIRepository
  helmrepository: Anchor, // FluxCD HelmRepository

  // Karpenter
  nodepool: Server,
  nodeclaim: Server,
  ec2nodeclass: Server,
  aksnodeclass: Server,
  gcenodeclass: Server,

  // KEDA
  scaledobject: Scaling,
  scaledjob: Scaling,
  triggerauthentication: KeyRound,
  clustertriggerauthentication: KeyRound,

  // Prometheus Operator
  servicemonitor: Radio,
  prometheusrule: ShieldAlert,
  podmonitor: Radio,
  alertmanager: ShieldAlert,

  // PDB
  poddisruptionbudget: ShieldCheck,

  // Knative Serving
  knativeservice: Layers,
  knativeconfiguration: Settings,
  knativerevision: GitBranch,
  knativeroute: Route,

  // Knative Eventing & Messaging
  broker: Radio,
  trigger: Zap,
  channel: Radio,

  // Knative Sources
  pingsource: Clock,
  apiserversource: Server,
  containersource: Container,
  sinkbinding: Link,

  // Traefik
  ingressroute: Globe,
  ingressroutetcp: Globe,
  ingressrouteudp: Globe,
  middleware: SlidersHorizontal,
  middlewaretcp: SlidersHorizontal,
  traefikservice: Split,
  serverstransport: ArrowRightLeft,
  serverstransporttcp: ArrowRightLeft,
  tlsoption: Lock,
  tlsstore: Lock,

  // Contour
  httpproxy: Globe,

  // CloudNativePG. Pooler is unambiguous so it keys directly; CNPG's colliding
  // kinds resolve through GROUP_QUALIFIED_KIND_ICONS below. Topology
  // pseudo-kinds (cnpgcluster/…) belong here only once pkg/topology's
  // KindForGVK emits them — it has no CNPG case today.
  pooler: Waypoints,

  // Cluster API
  capicluster: Server,
  machinedeployment: Layers,
  machineset: Layers,
  machine: Cpu,
  machinepool: Layers,
  kubeadmcontrolplane: Shield,
  clusterclass: BookOpen,
  machinehealthcheck: HeartPulse,

  // AWS CAPI Infrastructure Provider
  awsmanagedcontrolplane: Shield,
  awsmanagedmachinepool: Layers,
  awsmachine: Cpu,
  awsmachinetemplate: Cpu,
  awsmanagedcluster: Server,

  // GCP CAPI Infrastructure Provider
  gcpmanagedcontrolplane: Shield,
  gcpmanagedmachinepool: Layers,
  gcpmachine: Cpu,
  gcpmachinetemplate: Cpu,
  gcpmanagedcluster: Server,

  // Azure CAPI Infrastructure Provider
  azuremanagedcontrolplane: Shield,
  azuremanagedmachinepool: Layers,
  azuremachine: Cpu,
  azuremachinetemplate: Cpu,
  azuremanagedcluster: Server,

  // Kyverno — legacy kyverno.io family, modern policies.kyverno.io CEL
  // family, and the wgpolicyk8s.io reports every engine writes into.
  policy: Shield,
  clusterpolicy: Shield,
  policyreport: FileSearch,
  clusterpolicyreport: FileSearch,
  validatingpolicy: ShieldCheck,
  namespacedvalidatingpolicy: ShieldCheck,
  imagevalidatingpolicy: Fingerprint,
  namespacedimagevalidatingpolicy: Fingerprint,
  mutatingpolicy: Wand2,
  namespacedmutatingpolicy: Wand2,
  generatingpolicy: Sparkles,
  namespacedgeneratingpolicy: Sparkles,
  deletingpolicy: Trash2,
  namespaceddeletingpolicy: Trash2,
  policyexception: ShieldOff,
  cleanuppolicy: Trash2,
  clustercleanuppolicy: Trash2,

  // Trivy Operator
  vulnerabilityreport: Shield,
  configauditreport: ShieldCheck,
  exposedsecretreport: ShieldAlert,
  sbomreport: FileSearch,

  // Velero. Only kinds whose names no other operator claims: this map is keyed
  // on kind alone with no group awareness, so a shared name would give a foreign
  // resource a Velero icon. That rules out `backup` (CNPG), `restore`
  // (rancher/backup-restore-operator) and `schedule` (several operators).
  backupstoragelocation: Cloud,
  volumesnapshotlocation: Camera,
  backuprepository: Package,
}

/** Get the icon for a Kubernetes resource kind (case-insensitive). */
/**
 * Kinds shared by more than one operator, where the icon can only be chosen
 * once the API group is known. The resource browser passes the group; callers
 * that don't have one fall through to the ungrouped map.
 */
const GROUP_QUALIFIED_KIND_ICONS: Record<string, Record<string, LucideIcon>> = {
  cluster: { 'postgresql.cnpg.io': Database, 'cluster.x-k8s.io': Server },
  backup: { 'postgresql.cnpg.io': DatabaseBackup },
  scheduledbackup: { 'postgresql.cnpg.io': DatabaseBackup },
  pooler: { 'postgresql.cnpg.io': Waypoints },
  networkpolicy: {
    'networking.k8s.io': ShieldCheck,
    'crd.projectcalico.org': ShieldCheck,
    'projectcalico.org': ShieldCheck,
  },
  hostendpoint: { 'crd.projectcalico.org': Network, 'projectcalico.org': Network },
  ippool: { 'crd.projectcalico.org': Network, 'projectcalico.org': Network },
  tier: { 'crd.projectcalico.org': Network, 'projectcalico.org': Network },
  globalnetworkpolicy: { 'crd.projectcalico.org': ShieldCheck, 'projectcalico.org': ShieldCheck },
  stagednetworkpolicy: { 'crd.projectcalico.org': ShieldCheck, 'projectcalico.org': ShieldCheck },
  stagedglobalnetworkpolicy: { 'crd.projectcalico.org': ShieldCheck, 'projectcalico.org': ShieldCheck },
  stagedkubernetesnetworkpolicy: { 'crd.projectcalico.org': ShieldCheck, 'projectcalico.org': ShieldCheck },
}

const TOPOLOGY_KIND_ICONS: Record<string, LucideIcon> = {
  CalicoNetworkPolicy: ShieldCheck,
  CalicoGlobalNetworkPolicy: ShieldCheck,
  CalicoStagedNetworkPolicy: ShieldCheck,
  CalicoStagedGlobalNetworkPolicy: ShieldCheck,
  CalicoStagedKubernetesNetworkPolicy: ShieldCheck,
}

export function getResourceIcon(kind: string, group?: string): LucideIcon {
  const k = kind.toLowerCase()
  if (group) {
    const groupMap = GROUP_QUALIFIED_KIND_ICONS[k]
    if (groupMap) return groupMap[group] ?? DEFAULT_RESOURCE_ICON
  }
  return KIND_ICON_MAP[k] ?? Puzzle
}

/** Get the icon for a topology node kind, including virtual kinds (Internet, PodGroup). */
export function getTopologyIcon(kind: string): LucideIcon {
  if (kind === 'Internet') return Globe
  if (kind === 'PodGroup') return Boxes
  if (TOPOLOGY_KIND_ICONS[kind]) return TOPOLOGY_KIND_ICONS[kind]
  return getResourceIcon(kind)
}

export const DEFAULT_RESOURCE_ICON: LucideIcon = Puzzle
