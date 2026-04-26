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
  sync?: SyncStatus | string
  health?: GitOpsHealthStatus | string
  topologyStatus?: HealthStatus | string
  info?: GitOpsTreeInfoItem[]
  resource?: unknown
  groupedNodeIDs?: string[]
  count?: number
  data?: Record<string, unknown>
}

export interface GitOpsTreeEdge {
  source: string
  target: string
  type: string
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
}
