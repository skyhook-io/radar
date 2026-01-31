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

  // Networking
  Plug,
  DoorOpen,
  ShieldCheck,
  Radio,
  Globe,

  // Config
  FileSliders,
  KeyRound,

  // Storage
  HardDrive,
  Cylinder,
  Database,

  // Cluster
  Cpu,
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
  endpoints: Radio,
  endpointslice: Radio,
  gateway: DoorOpen,
  httproute: Globe,

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
  workflowtemplate: Activity,
  application: GitBranch, // ArgoCD Application
  applicationset: GitBranch, // ArgoCD ApplicationSet

  // FluxCD
  kustomization: Layers, // FluxCD Kustomization
  helmrelease: Anchor, // FluxCD HelmRelease
  gitrepository: FolderGit2, // FluxCD GitRepository
  ocirepository: FolderGit2, // FluxCD OCIRepository
  helmrepository: Anchor, // FluxCD HelmRepository

  // PDB
  poddisruptionbudget: ShieldCheck,
}

/** Get the icon for a Kubernetes resource kind (case-insensitive). */
export function getResourceIcon(kind: string): LucideIcon {
  return KIND_ICON_MAP[kind.toLowerCase()] ?? Puzzle
}

/** Get the icon for a topology node kind, including virtual kinds (Internet, PodGroup). */
export function getTopologyIcon(kind: string): LucideIcon {
  if (kind === 'Internet') return Globe
  if (kind === 'PodGroup') return Boxes
  return getResourceIcon(kind)
}

export const DEFAULT_RESOURCE_ICON: LucideIcon = Puzzle
