// Agent runtimes: Agent Substrate (ate.dev) and Agent Sandbox
// (agents.x-k8s.io, extensions.agents.x-k8s.io).

import type { HealthLevel, StatusBadge } from './resource-utils'
import { FATAL_WAITING_REASONS, formatDuration, healthColors } from './resource-utils'
import { isApiGroup } from './resource-utils-cnpg'

export const SUBSTRATE_GROUP = 'ate.dev'
const AGENT_SANDBOX_GROUP = 'agents.x-k8s.io'
const AGENT_SANDBOX_EXTENSIONS_GROUP = 'extensions.agents.x-k8s.io'

// Set by Substrate's controller on every worker Pod of a WorkerPool. Keys under
// ate.dev are reserved for Substrate's controllers: WorkerPool validation
// rejects them in a user-supplied template.
export const SUBSTRATE_WORKER_POOL_LABEL = 'ate.dev/worker-pool'

export function isSubstrateResource(resource: any): boolean {
  return isApiGroup(resource?.apiVersion, SUBSTRATE_GROUP)
}

export function isAgentSandboxResource(resource: any): boolean {
  return isApiGroup(resource?.apiVersion, AGENT_SANDBOX_GROUP)
}

export function isAgentSandboxExtensionResource(resource: any): boolean {
  return isApiGroup(resource?.apiVersion, AGENT_SANDBOX_EXTENSIONS_GROUP)
}

function badge(text: string, level: HealthLevel): StatusBadge {
  return { text, color: healthColors[level], level }
}

function findCondition(conditions: unknown, type: string): any | undefined {
  return Array.isArray(conditions) ? conditions.find((c: any) => c?.type === type) : undefined
}

const SUSPENSION_BLOCKED = 'Suspension blocked'

// The Pod the controller looks for; mirrors pkg/health sandboxPodName.
function sandboxPodName(resource: any): string {
  const annotated = resource?.metadata?.annotations?.['agents.x-k8s.io/pod-name']
  return typeof annotated === 'string' && annotated ? annotated : resource?.metadata?.name ?? ''
}

// Mirrors pkg/health PodOwnersCommand.
function podOwnersCommand(namespace: string, name: string): string {
  return `kubectl get pod ${name}${namespace ? ` -n ${namespace}` : ''} -o jsonpath='{.metadata.ownerReferences}'`
}

// Mirrors pkg/health suspensionRefusedMessage.
function suspensionRefusedMessage(namespace: string, name: string, controllerMessage: unknown): string {
  const next = `The conflicting Pod is "${name}". Check who owns it (${podOwnersCommand(namespace, name)}) before removing it, or recreate the Sandbox under another name.`
  return typeof controllerMessage === 'string' && controllerMessage
    ? `${controllerMessage.replace(/\.$/, '')}. ${next}`
    : next
}

function count(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

// ============================================================================
// AGENT SUBSTRATE — WorkerPool
// ============================================================================

// Mirrors pkg/health WorkerPoolClasses.
function workerPoolResolvedConditions(resource: any): { condition: any; stale: boolean }[] {
  const classes: any[] = Array.isArray(resource?.status?.sandboxClasses) ? resource.status.sandboxClasses : []
  const generation = count(resource?.metadata?.generation)
  return classes.map(sandboxClass => {
    const condition = findCondition(sandboxClass?.conditions, 'Resolved')
    const observed = count(condition?.observedGeneration)
    return { condition, stale: generation !== undefined && observed !== undefined && observed > 0 && observed < generation }
  })
}

// Mirrors pkg/health workerPoolProvisionGrace.
const WORKER_POOL_PROVISION_GRACE_MS = 10 * 60 * 1000

/**
 * Mirrors pkg/health SubstrateWorkerPoolStalled: a pool that wants workers,
 * has every class resolved on its current generation, and still has no worker
 * Deployment past the grace period.
 */
function workerPoolStalled(resource: any, now: number): boolean {
  if (resource?.metadata?.deletionTimestamp) return false
  const desired = count(resource?.spec?.replicas) ?? 0
  const pods = count(resource?.status?.replicas) ?? 0
  const resolved = workerPoolResolvedConditions(resource)
  if (desired <= 0 || resource?.status?.selector || pods > 0 || resolved.length === 0) return false
  const created = Date.parse(resource?.metadata?.creationTimestamp ?? '')
  let since = Number.isNaN(created) ? 0 : created
  for (const { condition, stale } of resolved) {
    if (condition?.status !== 'True' || stale) return false
    const at = Date.parse(condition.lastTransitionTime ?? '')
    if (!Number.isNaN(at) && at > since) since = at
  }
  return since !== 0 && now - since >= WORKER_POOL_PROVISION_GRACE_MS
}

/**
 * Mirrors pkg/health SubstrateWorkerPool, which AI summaries use;
 * pkg/health/testdata/substrate_workerpool.json pins both. The UI shows
 * `alert` where Go's levels say degraded.
 *
 * WorkerPool has no Ready condition and no top-level observedGeneration. Its
 * one condition, Resolved, sits per sandbox class in status.sandboxClasses[]
 * and records the generation it was computed for. While a class does not
 * resolve, the controller keeps applying an existing worker Deployment, so
 * workers that are ready stay up on the last resolved config.
 */
export function getWorkerPoolStatus(resource: any, now = Date.now()): StatusBadge {
  if (resource?.metadata?.deletionTimestamp) return badge('Terminating', 'degraded')
  const status = resource?.status
  const resolved = workerPoolResolvedConditions(resource)
  const ready = count(status?.readyReplicas) ?? 0
  const pods = count(status?.replicas) ?? 0

  const desired = count(resource?.spec?.replicas)
  for (const { condition, stale } of resolved) {
    if (condition?.status !== 'False') continue
    // Reported for an older spec: the controller has not judged the current one yet.
    if (stale) return badge(condition.reason || 'Unresolved', 'degraded')
    // Nothing can host the pool's actors only when workers are wanted and none is ready.
    return badge(condition.reason || 'Unresolved', desired !== undefined && desired > 0 && ready === 0 ? 'unhealthy' : 'alert')
  }

  if (desired === 0) return pods > 0 ? badge('Scaling down', 'degraded') : badge('Scaled to zero', 'neutral')
  if (!status || resolved.length === 0) return badge('Pending', 'degraded')
  if (resolved.some(r => r.stale || r.condition?.status !== 'True')) return badge('Resolving', 'degraded')
  if (desired !== undefined && ready >= desired) return badge('Ready', 'healthy')
  if (workerPoolStalled(resource, now)) return badge('Provisioning stalled', 'unhealthy')
  return badge('Scaling', 'degraded')
}

function workerPoolStatusMessage(resource: any, now = Date.now()): string | undefined {
  const generation = count(resource?.metadata?.generation)
  for (const { condition, stale } of workerPoolResolvedConditions(resource)) {
    if (condition?.status !== 'False') continue
    const message = typeof condition.message === 'string' ? condition.message.trim() : ''
    if (!stale) return message || undefined
    const note = `Reported for generation ${condition.observedGeneration}; the pool is now at generation ${generation}.`
    return message ? `${note} ${message}` : note
  }
  // No condition explains it: say what the reading means rather than repeat it.
  const ready = count(resource?.status?.readyReplicas) ?? 0
  const desired = count(resource?.spec?.replicas)
  switch (getWorkerPoolStatus(resource, now).text) {
    case 'Provisioning stalled':
      return `${desired} workers wanted, but no worker Deployment has appeared since every sandbox class resolved. Substrate's controller only logs a rejected create, so check its logs for the reason.`
    case 'Pending':
      return "Substrate's controller hasn't reported on this pool yet."
    case 'Resolving':
      return "Substrate's controller hasn't confirmed a SandboxConfig for every sandbox class of the current spec yet."
    case 'Scaling':
      return desired === undefined ? undefined : `${ready} of ${desired} workers ready.`
    case 'Scaling down':
      return `Scaled to zero; ${count(resource?.status?.replicas) ?? 0} worker Pods are still running.`
  }
  return undefined
}

/** Ready of desired replicas ("3/5"), for a WorkerPool's workers or a SandboxWarmPool's sandboxes. */
export function getReadyOfDesired(resource: any): string {
  const desired = count(resource?.spec?.replicas)
  const ready = count(resource?.status?.readyReplicas) ?? 0
  return desired === undefined ? `${ready}` : `${ready}/${desired}`
}

export function getWorkerPoolSandboxClass(resource: any): string {
  const classes = resource?.spec?.sandboxClasses
  return Array.isArray(classes) && classes[0]?.name ? classes[0].name : '-'
}

/**
 * The SandboxConfig of the pool's class: `current → pending` during a switch,
 * either name alone when only one is reported, '-' when neither is.
 */
export function getWorkerPoolSandboxConfig(resource: any): string {
  const specClass = getWorkerPoolSandboxClass(resource)
  const classes: any[] = Array.isArray(resource?.status?.sandboxClasses) ? resource.status.sandboxClasses : []
  const current = classes.find(c => c?.name === specClass) ?? classes[0]
  const name = current?.configRef?.name
  const pending = current?.pendingConfigRef?.name
  if (name && pending && pending !== name) return `${name} → ${pending}`
  return name || pending || '-'
}

// ============================================================================
// AGENT SANDBOX — Sandbox and SandboxClaim
// ============================================================================

// Mirrors pkg/health ReadAgentSandbox, which the Issues and AI summaries use;
// pkg/health/testdata/agent_sandbox.json pins both. The UI differs from the Go
// levels in one place: `alert` for a retried controller error or an
// unschedulable Pod.

// Ready=False reasons that describe a state someone chose and the controller
// completed: an expiry, a Pod that ran to completion, a claim whose lifetime
// ended. A suspension is read separately, since Ready reports it as soon as it
// is requested.
const SANDBOX_SETTLED_REASONS: Record<string, string> = {
  SandboxExpired: 'Expired',
  PodSucceeded: 'Finished',
  ClaimExpired: 'Expired',
}

// Ready=False reasons the controller reports while it is still bringing the
// sandbox up, tearing it down or binding a claim.
const SANDBOX_PROGRESS_REASONS: Record<string, string> = {
  DependenciesNotReady: 'Provisioning',
  PodTerminating: 'Terminating',
  SandboxCreatePending: 'Provisioning',
  SandboxNotReady: 'Provisioning',
  AdoptionConflict: 'Binding',
}

function sandboxReadyStatus(resource: any, isClaim: boolean): StatusBadge {
  if (resource?.metadata?.deletionTimestamp) return badge('Terminating', 'degraded')
  const conditions = resource?.status?.conditions
  const ready = findCondition(conditions, 'Ready')
  if (!ready) return badge('Pending', 'degraded')
  const generation = count(resource?.metadata?.generation)
  const observed = count(ready.observedGeneration)
  if (generation !== undefined && observed !== undefined && observed > 0 && observed < generation) return badge('Updating', 'degraded')
  if (ready.status === 'True') return badge('Ready', 'healthy')
  if (ready.status !== 'False') return badge('Unknown', 'unknown')
  const reason = typeof ready.reason === 'string' ? ready.reason : ''
  if (reason === 'SandboxSuspended') {
    // The Suspended condition says when the Pod is actually gone, and whether
    // the controller refused to delete the Pod it tracks for the Sandbox, which it
    // does not own.
    const suspended = findCondition(conditions, 'Suspended')
    if (isClaim || suspended?.status === 'True') return badge('Suspended', 'neutral')
    if (suspended?.status === 'False' && suspended?.reason === 'PodNotOwned') return badge(SUSPENSION_BLOCKED, 'alert')
    return badge('Suspending', 'degraded')
  }
  if (SANDBOX_SETTLED_REASONS[reason]) return badge(SANDBOX_SETTLED_REASONS[reason], 'neutral')
  if (reason === 'DependenciesNotReady') {
    // The controller names a container that is not starting normally in its
    // message: `container "agent" is waiting: ImagePullBackOff`, or `container
    // "agent" is in CrashLoopBackOff (last exit: Error)`. A hard failure is not
    // provisioning.
    const match = typeof ready.message === 'string' ? /is waiting: ([A-Za-z]+)|is in (CrashLoopBackOff)/.exec(ready.message) : null
    const waiting = match ? match[1] ?? match[2] : undefined
    if (waiting && FATAL_WAITING_REASONS.has(waiting)) return badge(waiting, 'unhealthy')
    const scheduled = findCondition(conditions, 'PodScheduled')
    if (scheduled?.status === 'False' && scheduled.reason) return badge(scheduled.reason, 'alert')
  }
  if (SANDBOX_PROGRESS_REASONS[reason]) return badge(SANDBOX_PROGRESS_REASONS[reason], 'degraded')
  // The controller retries these; a persistent one still deserves attention.
  if (reason === 'ReconcilerError') return badge(reason, 'alert')
  // A claim whose warm pool or template is missing binds once the reference is
  // fixed, so it reads as a warning, as its Issue and the workspace's row do.
  if (isClaim && (reason === 'WarmPoolNotFound' || reason === 'TemplateNotFound')) return badge(reason, 'degraded')
  return badge(reason || 'Not Ready', 'unhealthy')
}

export function getSandboxStatus(resource: any): StatusBadge {
  return sandboxReadyStatus(resource, false)
}

export function getSandboxClaimStatus(resource: any): StatusBadge {
  return sandboxReadyStatus(resource, true)
}

export function getSandboxOperatingMode(resource: any): string {
  return resource?.spec?.operatingMode || 'Running'
}

export function getSandboxNode(resource: any): string {
  return resource?.status?.nodeName || '-'
}

export function getSandboxExpiry(resource: any): string | undefined {
  const value = resource?.spec?.shutdownTime ?? resource?.spec?.lifecycle?.shutdownTime
  return typeof value === 'string' && value ? value : undefined
}

export function getSandboxClaimWarmPool(resource: any): string {
  return resource?.spec?.warmPoolRef?.name || '-'
}

export function getSandboxClaimSandbox(resource: any): string {
  return resource?.status?.sandbox?.name || '-'
}

// Mirrors pkg/agentsandbox registryDNSFailure and imagePullNextStep;
// pkg/agentsandbox/testdata/image_pull_next_steps.json pins both. The server
// reads the controller's message, which names only the waiting reason; a
// drawer that read the Pod has the pull error itself.
const REGISTRY_DNS_FAILURE = /no such host|server misbehaving|\blookup \S+ on \S+/i
export const REGISTRY_DNS_NEXT_STEP = 'Check the registry hostname in the Pod template’s image, and that the nodes’ DNS resolves it'
const IMAGE_PULL_REASONS = new Set(['ImagePullBackOff', 'ErrImagePull'])

/** The next step an image pull failure's message gives evidence for, or undefined when it names no cause Radar recognizes. */
export function imagePullNextStep(reason: unknown, message: unknown): string | undefined {
  return typeof reason === 'string' && IMAGE_PULL_REASONS.has(reason) && typeof message === 'string' && REGISTRY_DNS_FAILURE.test(message)
    ? REGISTRY_DNS_NEXT_STEP
    : undefined
}

/**
 * The next step a Sandbox's Pod gives evidence for, where the reading is an
 * image pull failure: one every container that can't pull its image agrees on.
 * Undefined otherwise (another reading, no such container, errors that differ
 * or name no cause), and the server's step stands.
 */
export function sandboxPodImagePullNextStep(reason: unknown, pod: any): string | undefined {
  if (typeof reason !== 'string' || !IMAGE_PULL_REASONS.has(reason)) return undefined
  const statuses = [pod?.status?.initContainerStatuses, pod?.status?.containerStatuses].flatMap((s) => (Array.isArray(s) ? s : []))
  const pulls = statuses.map((s: any) => s?.state?.waiting).filter((w: any) => IMAGE_PULL_REASONS.has(w?.reason))
  if (pulls.length === 0) return undefined
  const steps = pulls.map((w: any) => imagePullNextStep(w.reason, w.message))
  return steps.every((s) => s !== undefined && s === steps[0]) ? steps[0] : undefined
}

// ============================================================================
// AGENT SANDBOX — SandboxWarmPool
// ============================================================================

export function getSandboxWarmPoolStatus(resource: any): StatusBadge {
  if (resource?.metadata?.deletionTimestamp) return badge('Terminating', 'degraded')
  const status = resource?.status
  if (!status) return badge('Pending', 'degraded')
  const generation = count(resource?.metadata?.generation)
  const observed = count(status.observedGeneration)
  if (generation !== undefined && observed !== undefined && observed < generation) {
    return badge('Progressing', 'degraded')
  }
  const desired = count(resource?.spec?.replicas)
  if (desired === 0) return (count(status.replicas) ?? 0) > 0 ? badge('Scaling down', 'degraded') : badge('Scaled to zero', 'neutral')
  const ready = count(status.readyReplicas) ?? 0
  if (desired === undefined) return ready > 0 ? badge('Ready', 'healthy') : badge('Pending', 'degraded')
  return ready >= desired ? badge('Ready', 'healthy') : badge('Warming', 'degraded')
}

export function getSandboxWarmPoolTemplate(resource: any): string {
  return resource?.spec?.sandboxTemplateRef?.name || '-'
}

/** "in 2h" before an expiry, "3h ago" after it, or '-' when none is set. */
export function formatSandboxExpiry(value: string | undefined, now = Date.now()): string {
  if (!value) return '-'
  const time = Date.parse(value)
  if (!Number.isFinite(time)) return value
  const diff = time - now
  return diff >= 0 ? `in ${formatDuration(diff)}` : `${formatDuration(-diff)} ago`
}

/**
 * The curated status of an agent runtime object, with the controller's message
 * when it explains a state that is not healthy; null for any other resource.
 * The generic renderer's banner reads this so it cannot call a suspended
 * sandbox a failure while the header calls it Suspended.
 */
export function getAgentRuntimeStatus(resource: any): (StatusBadge & { message?: string }) | null {
  const kind = resource?.kind
  let status: StatusBadge | null = null
  let message: string | undefined
  if (kind === 'WorkerPool' && isSubstrateResource(resource)) {
    status = getWorkerPoolStatus(resource)
    message = workerPoolStatusMessage(resource)
  } else if (kind === 'Sandbox' && isAgentSandboxResource(resource)) {
    status = getSandboxStatus(resource)
    // Ready keeps its ordinary suspension message when the suspension is refused.
    // The badge decides: an edit or a deletion outranks a refused suspension.
    message = status.text === SUSPENSION_BLOCKED
      ? suspensionRefusedMessage(resource?.metadata?.namespace ?? '', sandboxPodName(resource), findCondition(resource?.status?.conditions, 'Suspended')?.message)
      : findCondition(resource?.status?.conditions, 'Ready')?.message
  } else if (kind === 'SandboxClaim' && isAgentSandboxExtensionResource(resource)) {
    status = getSandboxClaimStatus(resource)
    message = findCondition(resource?.status?.conditions, 'Ready')?.message
  } else if (kind === 'SandboxWarmPool' && isAgentSandboxExtensionResource(resource)) {
    status = getSandboxWarmPoolStatus(resource)
  }
  if (!status) return null
  return status.level === 'healthy' || typeof message !== 'string' || !message.trim()
    ? status
    : { ...status, message: message.trim() }
}

/**
 * Filter text for the agent runtime columns other than status, matching what
 * the cells show; undefined for any other resource or column.
 */
export function getAgentRuntimeColumnValue(resource: any, column: string): string | undefined {
  const kind = resource?.kind
  if (kind === 'WorkerPool' && isSubstrateResource(resource)) {
    switch (column) {
      case 'workers': return getReadyOfDesired(resource)
      case 'sandboxClass': return getWorkerPoolSandboxClass(resource)
      case 'sandboxConfig': return getWorkerPoolSandboxConfig(resource)
    }
  } else if (kind === 'Sandbox' && isAgentSandboxResource(resource)) {
    switch (column) {
      case 'operatingMode': return getSandboxOperatingMode(resource)
      case 'sandboxNode': return getSandboxNode(resource)
      case 'expires': return formatSandboxExpiry(getSandboxExpiry(resource))
    }
  } else if (kind === 'SandboxClaim' && isAgentSandboxExtensionResource(resource)) {
    switch (column) {
      case 'warmPool': return getSandboxClaimWarmPool(resource)
      case 'claimedSandbox': return getSandboxClaimSandbox(resource)
      case 'expires': return formatSandboxExpiry(getSandboxExpiry(resource))
    }
  } else if (kind === 'SandboxWarmPool' && isAgentSandboxExtensionResource(resource)) {
    switch (column) {
      case 'warmReady': return getReadyOfDesired(resource)
      case 'template': return getSandboxWarmPoolTemplate(resource)
    }
  }
  return undefined
}

/** Sort keys for the same columns: counts and expiry times sort as numbers. */
export function getAgentRuntimeSortValue(resource: any, column: string): string | number | undefined {
  const text = getAgentRuntimeColumnValue(resource, column)
  if (text === undefined) return undefined
  if (column === 'workers' || column === 'warmReady') return count(resource?.status?.readyReplicas) ?? 0
  if (column === 'expires') {
    const at = Date.parse(getSandboxExpiry(resource) ?? '')
    return Number.isFinite(at) ? at : Number.MAX_SAFE_INTEGER
  }
  return text === '-' ? '' : text
}
