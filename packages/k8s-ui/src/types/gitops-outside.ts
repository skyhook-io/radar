// GET /api/gitops/outside-changes/{kind}/{namespace}/{name}: fields of a Flux
// root's managed objects last written by a person's tool rather than Flux.

export type GitOpsOutsideOutcome = 'reverted' | 'reported' | 'stays' | 'may-overwrite' | 'ignored' | 'suspended' | 'unknown'

export interface GitOpsOutsideRef {
  group: string
  kind: string
  namespace: string
  name: string
}

export interface GitOpsOutsideChange {
  manager: string
  tool: string
  operation: string
  time?: string
  paths: string[]
  morePaths: number
  // Current values for allowlisted scalar paths only (replicas, images,
  // resource requests and limits). managedFields carry no old value.
  values?: Record<string, string>
  outcome: GitOpsOutsideOutcome
  harmless: boolean
}

export interface GitOpsOutsideObject {
  ref: GitOpsOutsideRef
  changes: GitOpsOutsideChange[]
}

export interface GitOpsOutsideCoverage {
  listed: number
  denied?: { resource: string; namespace: string }[]
  skipped?: string[]
  notChecked?: string[]
  labelScan?: boolean
  truncated?: boolean
}

export interface GitOpsOutsideChanges {
  root: GitOpsOutsideRef
  policy?: { tool: string; driftDetection?: string; suspended: boolean; interval?: string }
  count: number
  objects: GitOpsOutsideObject[]
  harmless: { tool: string; count: number }[]
  otherWriters: { manager: string; objects: number }[]
  driftEvents: { type: string; reason: string; message: string; count?: number; lastTimestamp: string }[]
  coverage: GitOpsOutsideCoverage
}
