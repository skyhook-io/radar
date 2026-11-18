// Wire types for /api/velero/workspace and /api/velero/schedules/{ns}/{name}/activity.
// Go is the source of truth: internal/velero/workspace.go and pkg/velero.
// Every time is absolute (RFC3339); the client renders it, never re-derives a state from it.

export type VeleroCoverageState = 'full' | 'partial' | 'denied' | 'notInstalled' | 'syncing' | 'uncached' | 'error'

/** integration.KindCoverage */
export interface VeleroKindCoverage {
  state: VeleroCoverageState
  deniedNamespaces?: string[]
  uncachedNamespaces?: string[]
  /** For partial coverage: the namespaces that were read. */
  allowedNamespaces?: string[]
}

export type VeleroWorkspaceKey = 'schedules' | 'backups' | 'backupStorageLocations' | 'restores' | 'backupRepositories'

/** pkg/velero ZoneSource */
export type VeleroZoneSource = 'cron' | 'server' | 'serverDefault' | 'inferred' | 'unknown'

/** pkg/velero Zone */
export interface VeleroZone {
  /** An IANA zone, or a fixed offset ("UTC+02:00") for an inferred zone. */
  name: string
  source: VeleroZoneSource
}

/** pkg/velero ScheduleState */
export type VeleroScheduleState =
  | 'paused'
  | 'schedulingDisabled'
  | 'rejected'
  | 'cannotRun'
  | 'notValidated'
  | 'unknown'
  | 'alwaysDue'
  | 'skipPending'
  | 'held'
  | 'late'
  | 'probablyLate'
  | 'lateOrHeld'
  | 'possiblyLate'
  | 'due'
  | 'onSchedule'

/** pkg/velero reason codes qualifying a state. */
export type VeleroScheduleReason =
  | 'nameTooLong'
  | 'unparseable'
  | 'unstartedBlocker'
  | 'zoneUnconfirmed'
  | 'backupsUnread'
  | 'noFutureTime'
  | 'validationPending'
  | 'serverDown'

/** pkg/velero Run: one Backup a Schedule submitted. */
export interface VeleroRun {
  name: string
  phase?: string
  created: string
  /** When the run started, else when it was created; backup sync recreates old runs with a fresh creation time. */
  ranAt: string
  started?: string
  completed?: string
  expires?: string
  errors?: number
  warnings?: number
}

/** pkg/velero ScheduleRuns: what the backups Velero still keeps say about a schedule. */
export interface VeleroScheduleRuns {
  /** "unknown": the namespace's Backups were not read, so nothing is known about the runs. */
  read: 'full' | 'unknown'
  kept: number
  newest?: VeleroRun
  newestCompleted?: VeleroRun
  /** Filled only by the schedule activity endpoint. */
  recent?: VeleroRun[]
}

/** pkg/velero ScheduleLocation */
export interface VeleroScheduleLocation {
  name?: string
  /** The template names no location; the installation's default receives the backups. */
  viaDefault?: boolean
}

/** pkg/velero ScheduleReading */
export interface VeleroScheduleReading {
  namespace: string
  name: string
  /** The Schedule's creationTimestamp. */
  created: string
  cron: string
  zone: VeleroZone
  state: VeleroScheduleState
  reason?: VeleroScheduleReason
  attention: boolean
  paused?: boolean
  validationErrors?: string[]
  lastSubmitted?: string
  lastSkipped?: string
  nextRun?: string
  /** nextRun is evaluated in the zone the controller uses. */
  nextRunCertain: boolean
  /** Scheduled times that passed with no submission, when late (capped at 100). */
  missedRuns?: number
  blocker?: VeleroRun
  runs: VeleroScheduleRuns
  location: VeleroScheduleLocation
  /** The template's spec.ttl as written; "" when the server's --default-backup-ttl applies (absent or a zero duration). */
  ttl: string
  /** The template's namespace selection as written: an empty or "*" include list is every namespace; entries may be glob patterns. */
  includedNamespaces: string[]
  excludedNamespaces: string[]
}

/** pkg/velero ServerSource */
export type VeleroServerSource = 'deployment' | 'rolloutInProgress' | 'notFound' | 'unread'

/** pkg/velero ServerConfig */
export interface VeleroServerConfig {
  source: VeleroServerSource
  deployment?: string
  timeZone?: string
  defaultLocation?: string
  validationFrequencySeconds?: number
  schedulingDisabled?: boolean
  /** The server Deployment's running replicas (status.replicas, terminating pods excluded), when the Deployment was found. */
  replicas?: number
  readyReplicas?: number
  backupsDisabled?: boolean
}

/** pkg/velero DefaultSource */
export type VeleroDefaultSource = 'spec' | 'server' | 'unknown' | 'none'

/** pkg/velero ValidationFrequency */
export interface VeleroValidationFrequency {
  seconds: number
  source: 'spec' | 'server'
  disabled?: boolean
}

/** pkg/velero LocationBackups */
export interface VeleroLocationBackups {
  targeting: number
  completedNotExpired: number
  expired: number
}

/** pkg/velero ScheduleRef */
export interface VeleroScheduleRef {
  name: string
  viaDefault?: boolean
}

/** pkg/velero LocationReading */
export interface VeleroLocationReading {
  namespace: string
  name: string
  phase?: string
  message?: string
  provider?: string
  bucket?: string
  accessMode?: string
  lastValidation?: string
  /** Absent when neither spec.validationFrequency nor the server's flag could be read. */
  frequency?: VeleroValidationFrequency
  validationOverdue?: boolean
  /** No validation recorded although the location is older than ten minutes. */
  neverValidated?: boolean
  default: boolean
  defaultSource: VeleroDefaultSource
  /** Absent when the namespace's Backups were not read. */
  backups?: VeleroLocationBackups | null
  /** null when the namespace's Schedules were not read. */
  schedules: VeleroScheduleRef[] | null
  attention: boolean
  /** 'serverDown' when the Velero server having no available pod explains an overdue validation. */
  reason?: 'serverDown'
}

/** internal/velero Installation: one namespace Velero objects were read in. */
export interface VeleroInstallation {
  namespace: string
  server: VeleroServerConfig
  defaultLocation?: string
  defaultSource: VeleroDefaultSource
  /** Absent when the namespace's Backups were not read. */
  unassignedBackups?: number
}

/** internal/velero Issue: the subset of an Issues row the views render. */
export interface VeleroIssue {
  id: string
  severity: 'critical' | 'warning'
  category: string
  kind: string
  group?: string
  namespace?: string
  name: string
  reason: string
  message?: string
  cause?: string
  action?: string
  first_seen?: string
  /** The runs this row covers, which raise no row of their own: for an unavailable location, the runs Velero rejected because of it. */
  affected?: { kind: string; namespace?: string; name: string }[]
}

/** GET /api/velero/workspace */
export interface VeleroWorkspaceResponse {
  observedAt: string
  installed: boolean
  context: string
  forcedNamespace?: string
  coverage: Partial<Record<VeleroWorkspaceKey, VeleroKindCoverage>>
  installations: VeleroInstallation[]
  schedules: VeleroScheduleReading[]
  locations: VeleroLocationReading[]
  issues: VeleroIssue[]
  /** Per installation namespace: the issues visible to the caller before the per-installation cap. */
  issueTotals: Record<string, number>
}

/** GET /api/velero/schedules/{ns}/{name}/activity */
export interface VeleroScheduleActivityResponse {
  observedAt: string
  reading: VeleroScheduleReading
  coverage: Partial<Record<VeleroWorkspaceKey, VeleroKindCoverage>>
  /** The server Deployment was read and has no running replicas. Absent from an older Radar. */
  serverStopped?: boolean
  backupsDisabled?: boolean
}
