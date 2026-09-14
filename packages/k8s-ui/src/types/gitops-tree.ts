import type { HealthStatus } from './core'
import type { GitOpsHealthStatus, SyncStatus } from './gitops'

export type GitOpsTreeTool = 'argocd' | 'fluxcd'
export type GitOpsTreeNodeRole = 'root' | 'declared' | 'generated' | 'group'

export interface GitOpsTreeRef {
  group?: string
  kind: string
  namespace: string
  name: string
  uid?: string
}

export interface GitOpsTreeInfoItem {
  name: string
  value: string
}

export interface GitOpsTreeNode {
  id: string
  ref: GitOpsTreeRef
  role: GitOpsTreeNodeRole
  tool: GitOpsTreeTool
  sync?: SyncStatus
  health?: GitOpsHealthStatus
  // Who assessed `health`. 'controller' is the GitOps controller's own
  // verdict; 'radar' is Radar's read of the live object (topology status, or
  // the issues engine's finding when the controller's verdict isn't
  // available). The UI labels 'radar' values so they never pass as the
  // controller's. Absent when `health` is absent. Kept open for sources a
  // newer backend may add — unknown values render without a label.
  healthSource?: GitOpsHealthSource
  healthReason?: string
  healthMessage?: string
  healthSeverity?: 'critical' | 'warning' | (string & {})
  topologyStatus?: HealthStatus
  info?: GitOpsTreeInfoItem[]
  resource?: unknown
  groupedNodeIDs?: string[]
  count?: number
  data?: Record<string, unknown>
}

// 'controllerApi' is the controller's own verdict read from its API server
// (argocd-server) because the CR doesn't carry it; same authority as
// 'controller'.
export type GitOpsHealthSource = 'controller' | 'controllerApi' | 'radar' | (string & {})

// Where an Argo CD Application keeps per-resource health. 'appTree' is the
// Argo CD 3 default: the controller's per-resource verdicts are not in the
// Application object, so any node health present came from Radar.
export type GitOpsHealthMode = 'inline' | 'appTree' | (string & {})

export type GitOpsTreeEdgeType = 'owns' | 'source' | 'dependsOn'

export interface GitOpsTreeEdge {
  source: string
  target: string
  type: GitOpsTreeEdgeType
}

export interface GitOpsTreeSummary {
  declared: number
  generated: number
  grouped: number
  degraded: number
  outOfSync: number
}

export interface GitOpsResourceTree {
  root: GitOpsTreeNode
  nodes: GitOpsTreeNode[]
  edges: GitOpsTreeEdge[]
  warnings?: string[]
  summary?: GitOpsTreeSummary
  healthMode?: GitOpsHealthMode
  // Per-resource health came from the controller's API server, so in
  // appTree mode the verdicts are still the controller's.
  healthFromApi?: boolean
  // The controller's API server was asked and didn't answer usefully.
  healthApiError?: string
  // The Application deploys to another cluster; Radar derives nothing about
  // its resources from here.
  remoteDestination?: boolean
}
