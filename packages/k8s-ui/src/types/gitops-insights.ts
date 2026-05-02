export interface GitOpsInsight {
  summary: GitOpsInsightSummary
  issues?: GitOpsIssue[]
  changes?: GitOpsChange[]
  plan?: GitOpsPlanItem[]
  history?: GitOpsHistoryItem[]
  capabilities?: GitOpsCapabilities
  partial?: boolean
}

export interface GitOpsInsightSummary {
  tool: string
  kind: string
  namespace: string
  name: string
  sync?: string
  health?: string
  operationPhase?: string
  source?: string
  targetRevision?: string
  lastRevision?: string
  lastReconcile?: string
  partialReason?: string
}

export interface GitOpsInsightRef {
  group?: string
  kind: string
  namespace?: string
  name: string
}

export interface GitOpsIssue {
  severity: 'critical' | 'warning' | 'info'
  scope: string
  reason: string
  message: string
  refs?: GitOpsInsightRef[]
  action?: string
}

export interface GitOpsChange {
  ref: GitOpsInsightRef
  category: string
  sync?: string
  health?: string
  message?: string
  hasDesired: boolean
  hasLive: boolean
  diff?: string
  partial: boolean
  partialNote?: string
}

export interface GitOpsPlanItem {
  ref: GitOpsInsightRef
  phase?: string
  wave?: number
  waveSet?: boolean
  order: number
  hook?: string
  relationship?: string
  status?: string
  blockedBy?: GitOpsInsightRef[]
  notes?: string[]
}

export interface GitOpsHistoryItem {
  id?: string
  revision?: string
  deployedAt?: string
  phase?: string
  message?: string
  source?: string
}

export interface GitOpsCapabilities {
  sync: boolean
  refresh: boolean
  terminate: boolean
  suspend: boolean
  resume: boolean
  syncWithSource: boolean
  selectiveSync: boolean
  rollback: boolean
  unsupportedReason?: string
  warnings?: string[]
}
