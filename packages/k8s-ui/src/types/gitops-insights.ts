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

// How a Drift's field entries were computed. `lastAppliedAnnotation` is the
// built-in path (kubectl.kubernetes.io/last-applied-configuration); `argocd-api`
// means the diff came from a connected Argo CD server's managed-resource diff.
// The `(string & {})` arm keeps the union open: a library consumer may lag the
// backend and receive a source it doesn't recognize — such values MUST render
// with the neutral fallback (no source label), never crash a switch.
export type GitOpsDriftSource = 'lastAppliedAnnotation' | 'argocd-api' | (string & {})

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
  rawOperationMessage?: string
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
  // Argo comparison-coverage disclosure: the field exclusions declared in
  // spec.ignoreDifferences that suppress drift from comparison (both Argo's
  // and Radar's). Undefined for Flux roots and Applications without any
  // exclusions. unsupportedRuleCount counts entries Radar does NOT evaluate
  // (jqPathExpressions / managedFieldsManagers rules) — the
  // drift panel may surface fields Argo's own UI suppresses.
  ignoredDifferences?: GitOpsIgnoredDifferences
}

export interface GitOpsIgnoredDifferences {
  ruleCount: number
  unsupportedRuleCount: number
  // Sorted unique "Group/Kind" targets ("Kind" for core resources,
  // "group/*" for a group-wide rule that omits kind).
  kinds: string[]
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
  rawMessage?: string
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
  rawSyncError?: string
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
  rawMessage?: string
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
  // True on an Argo CD Application detail when the Argo CD integration is
  // connected, meaning the full Git-rendered desired-vs-live diff endpoint
  // (/api/argo/applications/{ns}/{name}/resource-diff) can be called for this
  // app's managed resources. Absent/false → offer only the last-applied field
  // diff and (for disconnected Argo apps) the "connect Argo CD" hint.
  argoDiffAvailable?: boolean
  // True when the Argo CD integration has settings saved, even if the live
  // connection is down / the token is rejected. Paired with argoDiffAvailable to
  // tell "not set up" (offer Connect) from "set up but disconnected" (offer
  // Reconnect) — instead of advertising a diff that would fail.
  argoConfigured?: boolean
  // True on an Argo CD Application detail when the integration is connected,
  // meaning the revision-metadata endpoint can resolve Git commit details
  // (author, message, signature) for this app's deployed revisions.
  revisionMetadataAvailable?: boolean
  unsupportedReason?: string
  warnings?: string[]
}

// Response of GET /api/argo/applications/{ns}/{name}/revision-metadata — the Git
// commit metadata for one deployed revision. Every field is best-effort (varies
// across Argo CD versions); signatureInfo non-empty means a signature was checked.
export interface ArgoRevisionMetadata {
  author?: string
  date?: string
  tags?: string[]
  message?: string
  signatureInfo?: string
}

// Response of GET /api/argo/applications/{ns}/{name}/resource-diff. `desired`
// is the Git-rendered manifest, `live` the normalized cluster state — both YAML
// for the line-by-line view. `fieldEntries` is the same diff structured per
// path for a compact summary. `redacted` is set when Secret values were masked
// server-side; `hook` marks Argo sync-hook resources.
export interface GitOpsResourceDiff {
  source: string
  desired: string
  live: string
  fieldEntries: GitOpsResourceDiffFieldEntry[]
  redacted: boolean
  hook: boolean
}

export interface GitOpsResourceDiffFieldEntry {
  path: string
  op: 'added' | 'removed' | 'changed'
  desired?: string
  live?: string
}
