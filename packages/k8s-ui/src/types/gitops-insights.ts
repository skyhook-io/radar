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
  // Latest operation status message — surfaced inline in the status strip
  // when an operation is in flight or just failed.
  operationMessage?: string
  source?: string
  targetRevision?: string
  lastRevision?: string
  lastReconcile?: string
  partialReason?: string
  // Human-readable sync mode for the chip in the status strip.
  // Argo: "Manual" | "Auto" | "Auto · prune" | "Auto · self-heal" | "Auto · prune · self-heal"
  // Flux: "Auto" | "Suspended"
  autoSyncMode?: string
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
  // Per-resource sync failure message (Argo's status.resources[].syncResult).
  // Distinct from `message` (live health). Empty when sync succeeded.
  syncError?: string
  // Sync hook phase: PreSync / PostSync / SyncFail / PostDelete. Empty
  // for non-hook resources.
  hookPhase?: string
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
  initiatedBy?: string
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
