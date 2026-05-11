export interface GitOpsInsight {
  summary: GitOpsInsightSummary
  issues?: GitOpsIssue[]
  changes?: GitOpsChange[]
  plan?: GitOpsPlanItem[]
  history?: GitOpsHistoryItem[]
  capabilities?: GitOpsCapabilities
  // Non-fatal reasons the response is incomplete (RBAC short-circuit,
  // controller unreachable, etc.). UI surfaces these so users can tell
  // "no data" from "we couldn't fetch it".
  warnings?: string[]
  partial?: boolean
}

import type { GitOpsTool } from './gitops'

// Closed enums mirroring `pkg/gitops/insights/vocab.go`. Keeping the FE
// vocabulary in lockstep with the Go side means switches over these fields
// are exhaustive and wire-contract drift surfaces at compile time instead of
// at runtime as a missing render branch.
export type GitOpsScope = 'operation' | 'resource' | 'condition' | 'tree' | 'lifecycle'

export type GitOpsCategory =
  | 'Synced'
  | 'OutOfSync'
  | 'Degraded'
  | 'Missing'
  | 'Pruned'
  | 'Hook'
  | 'Progressing'
  | 'Reconciling'
  | 'Suspended'
  | 'Unknown'

export type GitOpsDriftSource = 'lastAppliedAnnotation'

export interface GitOpsInsightSummary {
  tool: GitOpsTool
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
  // True when the resource has metadata.deletionTimestamp set. Drives the
  // [Terminating] chip in the title row + disables mutating action buttons.
  // Backend mirrors this guard in pkg/gitops/operations.go so direct API
  // hits also fail with ErrResourceTerminating.
  terminating?: boolean
  // RFC3339 deletion timestamp; used to compute "21d ago" text in the chip
  // tooltip.
  terminationStartedAt?: string
  // Finalizers blocking deletion. When stuck, naming the finalizer points
  // the user at the controller they need to investigate.
  finalizers?: string[]
}

export interface GitOpsInsightRef {
  group?: string
  kind: string
  namespace?: string
  name: string
}

export interface GitOpsIssue {
  severity: 'critical' | 'alert' | 'warning' | 'info'
  scope: GitOpsScope
  reason: string
  message: string
  refs?: GitOpsInsightRef[]
  action?: string
  // Plain-English root cause when the message matched a recognized error
  // pattern. Empty for unrecognized messages — UI falls back to the raw message.
  cause?: string
  // Argo retry count parsed from "(retried N times)". 0 = no retry info.
  retryCount?: number
  // True when retry count crossed the "no longer transient" threshold.
  // Drives a stronger visual treatment.
  stuck?: boolean
  // Structured one-click remediation. When present, the failure card renders
  // a contextual action button. Nil when no automated remedy applies — the
  // `action` string still describes the manual path in that case.
  remediation?: GitOpsRemediation
}

export type GitOpsRemediationKind = 'create-namespace'

export interface GitOpsRemediation {
  kind: GitOpsRemediationKind
  target?: string
  hint?: string
}

export interface GitOpsChange {
  ref: GitOpsInsightRef
  category: GitOpsCategory
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
  // Structured per-field diff between the desired state (parsed from
  // kubectl.kubernetes.io/last-applied-configuration) and the live spec.
  // Undefined when the diff couldn't be computed (no annotation, SSA-applied
  // resource, Helm-managed). Renderer falls back to the textual explainer
  // when undefined.
  drift?: GitOpsDrift
  // Up to ~5 most recent events involving this resource, newest first.
  // Surfaces the underlying "why is this stuck" cause (ImagePullBackOff,
  // FailedScheduling, FailedMount, webhook denial) inline so the operator
  // doesn't have to drill into the standard resource drawer.
  recentEvents?: GitOpsEventSummary[]
  partial: boolean
  partialNote?: string
}

export interface GitOpsDrift {
  entries: GitOpsDriftEntry[]
  source: GitOpsDriftSource
  truncated?: boolean
}

export interface GitOpsDriftEntry {
  path: string // e.g. "spec.disruption.expireAfter"
  op: 'added' | 'removed' | 'changed'
  desired?: string // JSON-encoded
  live?: string // JSON-encoded
}

export interface GitOpsEventSummary {
  type: GitOpsEventType
  reason: string
  message: string
  count?: number
  lastTimestamp: string // RFC3339
  reportingComponent?: string
}

export type GitOpsEventType = 'Normal' | 'Warning'

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
