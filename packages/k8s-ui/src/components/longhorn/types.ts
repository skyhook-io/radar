// Wire types for /api/longhorn/workspace. Go is the source of truth:
// internal/longhorn/workspace.go and pkg/longhorn/reading.go.

/** integration.KindCoverage */
export interface LonghornKindCoverage {
  state: 'full' | 'partial' | 'denied' | 'notInstalled' | 'syncing' | 'uncached' | 'error'
  deniedNamespaces?: string[]
  uncachedNamespaces?: string[]
  /** For partial coverage: the namespaces that were read. */
  allowedNamespaces?: string[]
}

export type LonghornWorkspaceKey = 'volumes' | 'engines' | 'persistentVolumes'

export type LonghornVolumeHealth = 'healthy' | 'degraded' | 'faulted' | 'detached' | 'unknown'

/** pkg/longhorn VolumeUse: the PV a Volume backs now, proven by the live PV. */
export interface LonghornVolumeUse {
  /** unknown: PVs unread, or the Volume's name is not unique among those read. */
  state: 'bound' | 'none' | 'unknown'
  persistentVolume?: string
  claim?: { namespace: string; name: string }
  /** The PV is bound to a claim in a namespace the caller can't read. */
  claimWithheld?: boolean
}

/** pkg/longhorn VolumeReading. A field Longhorn didn't write is absent, never zero. */
export interface LonghornVolumeReading {
  namespace: string
  name: string
  uid: string
  health: LonghornVolumeHealth
  state?: string
  robustness?: string
  /** Bytes, as is actualSize. */
  size?: number
  actualSize?: number
  numberOfReplicas?: number
  /** Replicas in mode RW per the Engine; null when unknown, never the requested count. */
  healthyReplicas: number | null
  /** Why healthyReplicas is null. */
  healthyReplicasUnknown?: 'engineUnread' | 'engineAmbiguous' | 'noEngine' | 'noModes'
  lastDegradedAt?: string
  use: LonghornVolumeUse
}

/** Counts over the Volumes read; complete=false makes each a lower bound. */
export interface LonghornHealthCounts {
  healthy: number
  degraded: number
  faulted: number
  detached: number
  unknown: number
  complete: boolean
}

export interface LonghornIssue {
  id: string
  severity: 'critical' | 'warning'
  category: string
  kind: string
  group?: string
  namespace?: string
  name: string
  reason: string
  message?: string
  /** When the fault began; absent when its onset is not known, which does not mean it is new. */
  first_seen?: string
}

/** GET /api/longhorn/workspace */
export interface LonghornWorkspaceResponse {
  observedAt: string
  installed: boolean
  context: string
  forcedNamespace?: string
  coverage: Partial<Record<LonghornWorkspaceKey, LonghornKindCoverage>>
  /** null when no Volume could be read. */
  counts: LonghornHealthCounts | null
  volumes: LonghornVolumeReading[]
  /** The first 100 in Problems order; issueTotal counts every one. */
  issues: LonghornIssue[]
  issueTotal: number
}
