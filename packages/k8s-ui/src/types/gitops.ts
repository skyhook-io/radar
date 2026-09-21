// GitOps unified types and mappers for FluxCD and ArgoCD

// ============================================================================
// OPERATION RESPONSE TYPES
// ============================================================================

/** GitOps tool identifier */
export type GitOpsTool = 'argocd' | 'fluxcd'

/** GitOps operation types */
export type GitOpsOperation = 'sync' | 'refresh' | 'terminate' | 'suspend' | 'resume' | 'reconcile'

/** Reference to a GitOps resource */
export interface GitOpsResourceRef {
  kind: string
  name: string
  namespace: string
}

/**
 * Standardized response format for all GitOps operations
 * Returned by: sync, refresh, terminate, suspend, resume, reconcile endpoints
 */
export interface GitOpsOperationResponse {
  message: string
  operation: GitOpsOperation
  tool: GitOpsTool
  resource: GitOpsResourceRef
  requestedAt?: string
  source?: GitOpsResourceRef // For sync-with-source operations
}

// ============================================================================
// STATUS TYPES
// ============================================================================

/**
 * Unified sync status across GitOps tools
 * - Synced: Resources match the desired state in git
 * - OutOfSync: Resources differ from desired state
 * - Reconciling: Sync operation in progress
 * - Unknown: Status cannot be determined
 */
export type SyncStatus = 'Synced' | 'OutOfSync' | 'Reconciling' | 'Unknown'

/**
 * Unified health status across GitOps tools
 * - Healthy: All resources are healthy
 * - Progressing: Resources are being created/updated
 * - Degraded: Some resources are unhealthy
 * - Suspended: Reconciliation is paused
 * - Missing: Expected resources don't exist
 * - Unknown: Health cannot be determined
 */
export type GitOpsHealthStatus = 'Healthy' | 'Progressing' | 'Degraded' | 'Suspended' | 'Missing' | 'Unknown'

/**
 * Unified GitOps status combining sync and health information
 */
export interface GitOpsStatus {
  sync: SyncStatus
  health: GitOpsHealthStatus
  message?: string
  lastSyncTime?: string
  suspended: boolean
  /**
   * A reconcile pass is executing right now. Activity, not sync and not health —
   * it never influences either, because a controller being mid-pass says nothing
   * about whether the declared state is applied. Renderers may show a subtle
   * indicator; nothing that drives a status word.
   */
  reconciling?: boolean
  /** When the in-flight pass started, so a wedged controller can be told from a routine one. */
  reconcilingSince?: string
}

/**
 * A resource managed by a GitOps application/kustomization
 */
export interface ManagedResource {
  group: string
  kind: string
  namespace: string
  name: string
  health?: GitOpsHealthStatus
  sync?: SyncStatus
}

// ============================================================================
// FLUX CONDITION TYPES
// ============================================================================

/** Known FluxCD condition types */
export type FluxConditionType = 'Ready' | 'Reconciling' | 'Stalled' | 'Healthy'

export interface FluxCondition {
  type: FluxConditionType | string // Known types + allow unknown for forward compatibility
  status: 'True' | 'False' | 'Unknown'
  reason?: string
  message?: string
  lastTransitionTime?: string
}

// ============================================================================
// FLUX STATUS MAPPER
// ============================================================================

/**
 * Generation tracking for a Flux object. `observedGeneration` has three states,
 * and the difference matters: absent means the controller never reports it and
 * we have no signal, which must not be read as drift.
 */
export interface FluxGeneration {
  generation?: number
  observedGeneration?: number
}

/**
 * True when the controller has demonstrably not yet acted on the current spec.
 * Absent observedGeneration is no signal, not drift — some Flux kinds and older
 * controllers never set it, and treating absence as drift would mark every
 * healthy object on them as out of date. A present-but-different value counts,
 * including the -1 sentinel Flux writes on an object it has never reconciled.
 */
function hasGenerationDrift(gen?: FluxGeneration): boolean {
  if (!gen) return false
  const { generation, observedGeneration } = gen
  if (typeof generation !== 'number' || typeof observedGeneration !== 'number') return false
  return generation !== observedGeneration
}

/**
 * Maps FluxCD conditions to unified GitOpsStatus.
 *
 * Sync answers "is the declared state applied", which is `Ready` plus
 * `observedGeneration`. `Reconciling` answers "is the controller mid-pass",
 * which is neither — the two are independent conditions upstream, and letting
 * activity outvote readiness reported a healthy object as not synced.
 *
 * Order matters:
 * - suspend                                   → Suspended (sync from Ready, unless drifted)
 * - Stalled=True                              → OutOfSync / Degraded
 * - Ready=True and no generation drift        → Synced, health from Healthy
 * - drift, or Reconciling with Ready not True → Reconciling / Progressing
 * - Ready=False                               → OutOfSync
 */
export function fluxConditionsToGitOpsStatus(
  conditions: FluxCondition[],
  suspended: boolean,
  generation?: FluxGeneration
): GitOpsStatus {
  const readyCondition = conditions.find(c => c.type === 'Ready')
  const reconcilingCondition = conditions.find(c => c.type === 'Reconciling')
  const stalledCondition = conditions.find(c => c.type === 'Stalled')
  const healthyCondition = conditions.find(c => c.type === 'Healthy')

  const drifted = hasGenerationDrift(generation)
  const isReconciling = reconcilingCondition?.status === 'True'
  // Carried on every result so a wedged controller is visible next to an
  // otherwise-correct Synced, without any status word claiming a fault.
  const activity = isReconciling
    ? { reconciling: true, reconcilingSince: reconcilingCondition?.lastTransitionTime }
    : {}

  // Handle suspended state. Suspending does not un-apply what was applied, so a
  // suspended object that was Ready stays Synced — but a spec change made while
  // suspended will never be picked up, so drift downgrades it to Unknown.
  if (suspended) {
    return {
      sync: readyCondition?.status === 'True' && !drifted ? 'Synced' : 'Unknown',
      health: 'Suspended',
      message: 'Reconciliation is suspended',
      lastSyncTime: readyCondition?.lastTransitionTime,
      suspended: true,
      ...activity,
    }
  }

  // Stalled is a terminal failure and outranks readiness: it means the
  // controller has given up, whatever the last successful apply left behind.
  if (stalledCondition?.status === 'True') {
    return {
      sync: 'OutOfSync',
      health: 'Degraded',
      message: stalledCondition.message || 'Reconciliation stalled',
      lastSyncTime: readyCondition?.lastTransitionTime,
      suspended: false,
      ...activity,
    }
  }

  // The applied state. Ahead of the reconciling branch on purpose — this is the
  // whole point of the ordering.
  if (readyCondition?.status === 'True' && !drifted) {
    const isHealthy = healthyCondition?.status !== 'False'
    return {
      sync: 'Synced',
      health: isHealthy ? 'Healthy' : 'Degraded',
      message: isHealthy ? undefined : healthyCondition?.message,
      lastSyncTime: readyCondition.lastTransitionTime,
      suspended: false,
      ...activity,
    }
  }

  // Genuinely not applied: the controller has not observed the current spec, or
  // it is working and has not reached Ready.
  if (drifted || isReconciling) {
    return {
      sync: 'Reconciling',
      health: 'Progressing',
      message: reconcilingCondition?.message || 'Reconciliation in progress',
      lastSyncTime: readyCondition?.lastTransitionTime,
      suspended: false,
      ...activity,
    }
  }

  // Handle not ready state
  if (readyCondition?.status === 'False') {
    const reason = readyCondition.reason || ''
    const isTransient = reason.includes('Progress') || reason.includes('Retry') || reason.includes('Wait')

    return {
      sync: 'OutOfSync',
      health: isTransient ? 'Progressing' : 'Degraded',
      message: readyCondition.message,
      lastSyncTime: readyCondition.lastTransitionTime,
      suspended: false,
      ...activity,
    }
  }

  // Unknown state
  return {
    sync: 'Unknown',
    health: 'Unknown',
    message: 'Status cannot be determined',
    suspended: false,
    ...activity,
  }
}

// ============================================================================
// ARGO STATUS MAPPER
// ============================================================================

/** ArgoCD sync status values */
export type ArgoSyncStatus = 'Synced' | 'OutOfSync' | 'Unknown'

/** ArgoCD health status values */
export type ArgoHealthStatus = 'Healthy' | 'Progressing' | 'Degraded' | 'Suspended' | 'Missing' | 'Unknown'

/** ArgoCD operation phase values */
export type ArgoOperationPhase = 'Running' | 'Succeeded' | 'Failed' | 'Error' | 'Terminating'

/** ArgoCD sync result resource */
export interface ArgoSyncResultResource {
  group?: string
  version?: string
  kind: string
  namespace?: string
  name: string
  status?: string
  message?: string
  hookPhase?: string
}

/** ArgoCD sync result source */
export interface ArgoSyncResultSource {
  repoURL?: string
  path?: string
  targetRevision?: string
  chart?: string
}

export interface ArgoAppStatus {
  sync?: {
    status?: ArgoSyncStatus | string // Known types + allow unknown
    revision?: string
  }
  health?: {
    status?: ArgoHealthStatus | string // Known types + allow unknown
    message?: string
  }
  operationState?: {
    phase?: ArgoOperationPhase | string // Known types + allow unknown
    message?: string
    startedAt?: string
    finishedAt?: string
    retryCount?: number
    syncResult?: {
      revision?: string
      source?: ArgoSyncResultSource
      resources?: ArgoSyncResultResource[]
    }
  }
  reconciledAt?: string
  history?: Array<{
    revision?: string
    deployedAt?: string
    id?: number
    source?: { repoURL?: string; path?: string }
  }>
}

/**
 * Maps ArgoCD Application status to unified GitOpsStatus
 *
 * ArgoCD status patterns:
 * - sync.status: Synced, OutOfSync, Unknown
 * - health.status: Healthy, Progressing, Degraded, Suspended, Missing, Unknown
 * - operationState.phase: Running, Terminating, Succeeded, Failed, Error
 */
export function argoStatusToGitOpsStatus(status: ArgoAppStatus): GitOpsStatus {
  const syncStatus = status.sync?.status
  const healthStatus = status.health?.status
  const opPhase = status.operationState?.phase

  // Map sync status
  let sync: SyncStatus = 'Unknown'
  if (syncStatus === 'Synced') {
    sync = 'Synced'
  } else if (syncStatus === 'OutOfSync') {
    sync = 'OutOfSync'
  } else if (opPhase === 'Running' || opPhase === 'Terminating') {
    sync = 'Reconciling'
  }

  // Map health status
  let health: GitOpsHealthStatus = 'Unknown'
  const suspended = healthStatus === 'Suspended'

  if (suspended) {
    health = 'Suspended'
  } else if (healthStatus === 'Healthy') {
    health = 'Healthy'
  } else if (healthStatus === 'Progressing') {
    health = 'Progressing'
  } else if (healthStatus === 'Degraded') {
    health = 'Degraded'
  } else if (healthStatus === 'Missing') {
    health = 'Missing'
  }

  // Override with operation state if active
  if (opPhase === 'Running' || opPhase === 'Terminating') {
    health = 'Progressing'
  } else if (opPhase === 'Failed' || opPhase === 'Error') {
    health = 'Degraded'
  }

  return {
    sync,
    health,
    message: status.health?.message || status.operationState?.message,
    lastSyncTime: status.reconciledAt || status.operationState?.finishedAt,
    suspended,
  }
}

/**
 * Maps an Argo CD ApplicationSet's conditions to a GitOpsStatus.
 *
 * An ApplicationSet generates Applications; it does not apply declared state
 * to a cluster itself. Sync answers "is the declared state applied", so there
 * is no honest sync value here and it stays Unknown - the generated
 * Applications each carry their own.
 *
 * Health answers whether the generator is doing its job, which its conditions
 * do report, in a vocabulary of their own (ErrorOccurred / ParametersGenerated
 * / ResourcesUpToDate - never Ready):
 * - ErrorOccurred=True      → Degraded, carrying the controller's message
 * - ResourcesUpToDate=True  → Healthy
 * - neither                 → Unknown (status not populated yet)
 *
 * The Resources view renders the same three conditions through
 * getArgoApplicationSetStatus for its own badge vocabulary. The two read the
 * same signals and must agree on which condition wins.
 */
export function argoApplicationSetConditionsToGitOpsStatus(
  conditions: FluxCondition[]
): GitOpsStatus {
  const error = conditions.find(c => c.type === 'ErrorOccurred' && c.status === 'True')
  if (error) {
    return {
      sync: 'Unknown',
      health: 'Degraded',
      message: error.message || 'Generation failed',
      lastSyncTime: error.lastTransitionTime,
      suspended: false,
    }
  }

  const upToDate = conditions.find(c => c.type === 'ResourcesUpToDate')
  if (upToDate?.status === 'True') {
    return {
      sync: 'Unknown',
      health: 'Healthy',
      lastSyncTime: upToDate.lastTransitionTime,
      suspended: false,
    }
  }

  return {
    sync: 'Unknown',
    health: 'Unknown',
    message: 'Status cannot be determined',
    suspended: false,
  }
}

// ============================================================================
// INVENTORY PARSERS
// ============================================================================

/**
 * Parses Flux inventory entries into ManagedResource array
 *
 * Flux inventory format: "namespace_name_group_kind"
 * Example: "default_my-config_core_ConfigMap" or "kube-system_coredns_apps_Deployment"
 * Note: group can be empty for core resources (e.g., "default_my-config__ConfigMap")
 *
 * Malformed entries are filtered out rather than returning invalid resources.
 */
export function parseFluxInventory(entries: Array<{ id: string; v?: string }>): ManagedResource[] {
  return entries
    .map(entry => {
      // Format: namespace_name_group_kind
      // The challenge is that namespace, name, and group can all contain underscores
      // But the format is always: ns_name_group_kind where kind is the last part
      const id = entry.id || ''
      const parts = id.split('_')

      // Minimum: namespace_name_group_kind (4 parts, though name could be empty which is invalid)
      if (parts.length < 4) {
        return null // Filter out malformed entries
      }

      // Kind is always last, group is second-to-last
      const kind = parts[parts.length - 1]
      const group = parts[parts.length - 2]
      // Namespace is first, name is everything in between
      const namespace = parts[0]
      const name = parts.slice(1, -2).join('_')

      // Validate required fields
      if (!kind || !name) {
        return null // Filter out entries with missing required fields
      }

      return {
        group: group || '',
        kind,
        namespace,
        name,
      }
    })
    .filter((r): r is ManagedResource => r !== null)
}

/**
 * Parses ArgoCD resource tree into ManagedResource array
 *
 * ArgoCD format: Array of {group, kind, namespace, name, health, status}
 */
export interface ArgoResource {
  group?: string
  kind: string
  namespace?: string
  name: string
  health?: {
    status?: ArgoHealthStatus | string // Known types + allow unknown
    message?: string
  }
  status?: ArgoSyncStatus | string // Known types + allow unknown
}

export function parseArgoResources(resources: ArgoResource[]): ManagedResource[] {
  return resources.map(r => ({
    group: r.group || '',
    kind: r.kind,
    namespace: r.namespace || '',
    name: r.name,
    health: mapArgoHealth(r.health?.status),
    sync: r.status === 'Synced' ? 'Synced' : r.status === 'OutOfSync' ? 'OutOfSync' : undefined,
  }))
}

/** Maps an ArgoCD health status string to the unified GitOpsHealthStatus type */
export function mapArgoHealth(status?: string): GitOpsHealthStatus | undefined {
  if (!status) return undefined
  switch (status) {
    case 'Healthy': return 'Healthy'
    case 'Progressing': return 'Progressing'
    case 'Degraded': return 'Degraded'
    case 'Suspended': return 'Suspended'
    case 'Missing': return 'Missing'
    default: return 'Unknown'
  }
}

// ============================================================================
// UTILITY FUNCTIONS
// ============================================================================

/**
 * Checks if a GitOps resource has issues (not synced or unhealthy)
 */
export function hasGitOpsIssues(status: GitOpsStatus): boolean {
  return status.sync !== 'Synced' ||
         (status.health !== 'Healthy' && status.health !== 'Suspended')
}

/**
 * Gets a single-line summary of GitOps status
 */
export function getGitOpsStatusSummary(status: GitOpsStatus): string {
  if (status.suspended) {
    return 'Suspended'
  }
  if (status.sync === 'Reconciling') {
    return 'Syncing...'
  }
  if (status.sync === 'Synced' && status.health === 'Healthy') {
    return 'Synced'
  }
  if (status.health === 'Progressing') {
    return 'Progressing'
  }
  if (status.sync === 'OutOfSync') {
    return 'Out of Sync'
  }
  if (status.health === 'Degraded') {
    return 'Degraded'
  }
  return status.sync
}

/**
 * Gets the appropriate color class for a GitOps status
 */
export function getGitOpsStatusColor(status: GitOpsStatus): string {
  if (status.suspended) {
    return 'status-degraded'
  }
  if (status.sync === 'Synced' && status.health === 'Healthy') {
    return 'status-healthy'
  }
  if (status.health === 'Degraded') {
    return 'status-unhealthy'
  }
  if (status.sync === 'OutOfSync' || status.health === 'Progressing') {
    return 'status-degraded'
  }
  return 'status-unknown'
}

/**
 * Groups managed resources by kind for display
 */
export function groupManagedResourcesByKind(resources: ManagedResource[]): Map<string, ManagedResource[]> {
  const grouped = new Map<string, ManagedResource[]>()

  for (const resource of resources) {
    const key = resource.kind
    const existing = grouped.get(key) || []
    existing.push(resource)
    grouped.set(key, existing)
  }

  return grouped
}
