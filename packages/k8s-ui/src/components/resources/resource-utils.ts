// Utility functions for resource display in tables

import { formatCPUString, formatMemoryString, formatBytes } from '../../utils/format'
import { pluralize } from '../../utils/pluralize'

// Import functions from sub-modules used internally by getCellFilterValue
import { getCertificateStatus, getCertificateRequestStatus, getClusterIssuerStatus, getClusterIssuerType, getOrderState, getChallengeState, getChallengeType } from './resource-utils-certmanager'
import { getNodePoolStatus, getNodeClaimStatus } from './resource-utils-karpenter'
import { getScaledObjectStatus, getScaledJobStatus } from './resource-utils-keda'
import { getGitRepositoryStatus, getOCIRepositoryStatus, getHelmRepositoryStatus, getHelmRepositoryType, getKustomizationStatus, getFluxHelmReleaseStatus, getFluxAlertStatus } from './resource-utils-flux'
import { getArgoApplicationStatus, getArgoApplicationSetStatus, getArgoApplicationSync, getArgoApplicationHealth, getArgoApplicationProject } from './resource-utils-argo'
import { getPolicyReportStatus as _getPolicyReportStatus, getKyvernoPolicyStatus as _getKyvernoPolicyStatus } from './resource-utils-kyverno'
import { getResourceClaimStatus as _getResourceClaimStatus, getResourceClaimDeviceClasses as _getResourceClaimDeviceClasses, getResourceClaimTemplateDeviceClasses as _getResourceClaimTemplateDeviceClasses, getResourceClaimAllocation as _getResourceClaimAllocation, getResourceClaimReservedFor as _getResourceClaimReservedFor } from './resource-utils-dra'
import { getNvidiaClusterPolicyStatus as _getNvidiaClusterPolicyStatus, getNvidiaClusterPolicyEnabledComponents as _getNvidiaClusterPolicyEnabledComponents, getNvidiaDriverStatus as _getNvidiaDriverStatus } from './resource-utils-nvidia'
import { getBackupStatus as _getBackupStatus, getRestoreStatus as _getRestoreStatus, getScheduleStatus as _getScheduleStatus, getBSLStatus as _getBSLStatus } from './resource-utils-velero'
import { getExternalSecretStatus as _getExternalSecretStatus, getClusterExternalSecretStatus as _getClusterExternalSecretStatus, getSecretStoreStatus as _getSecretStoreStatus, getClusterSecretStoreStatus as _getClusterSecretStoreStatus, getSecretStoreProviderType as _getSecretStoreProviderType } from './resource-utils-eso'
import { getHPATableState, hpaStatusFromState } from './resource-utils-hpa'

// ============================================================================
// STATUS & HEALTH UTILITIES
// ============================================================================

// Six health levels in escalating urgency order:
//   healthy < neutral < unknown < degraded < alert < unhealthy
// `alert` (orange) is the intermediate tier between degraded (amber) and
// unhealthy (red). Used for severity gradients like Problems/Audit
// (critical/high/medium → unhealthy/alert/degraded), where collapsing
// `high` into either neighbor erases real signal.
export type HealthLevel = 'healthy' | 'degraded' | 'alert' | 'unhealthy' | 'unknown' | 'neutral'

export interface StatusBadge {
  text: string
  color: string
  level: HealthLevel
}

// Color classes for different health levels (theme-aware)
export const healthColors: Record<HealthLevel, string> = {
  healthy: 'status-healthy',
  degraded: 'status-degraded',
  alert: 'status-alert',
  unhealthy: 'status-unhealthy',
  unknown: 'status-unknown',
  neutral: 'status-neutral',
}

// ============================================================================
// POD UTILITIES
// ============================================================================

export interface PodProblem {
  severity: 'critical' | 'high' | 'medium'
  message: string
  // detail carries extra human context shown after the short message (e.g.
  // the scheduler's verdict for an Unschedulable pod). message stays the
  // stable short label so filter-chip matching (podMatchesProblemCategory)
  // and known-pattern checks keep working on exact strings.
  detail?: string
}

/**
 * Condense a kube-scheduler verdict (the PodScheduled=False / FailedScheduling
 * message) for display: drop the "0/N nodes are available:" prefix and the
 * "preemption: …" tail, keeping the per-predicate clause list — which already
 * names untolerated taints, insufficient resources, and affinity/selector
 * misses. Presentation-only; the backend `scheduling` issue source does the
 * structured decomposition + node-label resolution (e.g. naming arm64).
 */
export function summarizeSchedulerMessage(message?: string): string {
  if (!message) return ''
  let m = message.split('. preemption:')[0].split(' preemption:')[0].trim()
  const colon = m.indexOf(':')
  if (colon >= 0) m = m.slice(colon + 1).trim()
  return m.replace(/\.\s*$/, '').trim()
}

/** Tailwind classes for severity dot indicators (used in tooltips and alert banners) */
export const SEVERITY_DOT_COLOR: Record<PodProblem['severity'], string> = {
  critical: 'bg-red-400',
  high: 'bg-orange-400',
  medium: 'bg-yellow-400',
}

/** Check whether a pod's problems match a given problem category (filter chip label) */
export function podMatchesProblemCategory(problems: PodProblem[], restarts: number, category: string): boolean {
  const msgs = problems.map(p => p.message)
  switch (category) {
    case 'CrashLoopBackOff':
      return msgs.includes('CrashLoopBackOff')
    case 'ImagePullBackOff':
      return msgs.some(m => m.includes('ImagePull') && !m.startsWith('Init:'))
    case 'OOMKilled':
      return msgs.includes('OOMKilled')
    case 'Unschedulable':
      return msgs.includes('Unschedulable')
    case 'Not Ready':
      return msgs.includes('Not Ready') || msgs.some(m => m.includes('Probe'))
    case 'High Restarts':
      return restarts > 5
    case 'Init Failed':
      return msgs.some(m => m.startsWith('Init:'))
    case 'Exit Code Error':
      return msgs.some(m => m.startsWith('Exit Code'))
    case 'Failed':
      return msgs.includes('Failed') || msgs.includes('Unknown')
    case 'Other': {
      const knownPatterns = ['CrashLoopBackOff', 'ImagePull', 'ErrImagePull', 'OOMKilled', 'Unschedulable', 'Not Ready', 'Probe', 'Init:', 'Exit Code', 'Failed', 'Unknown']
      return problems.some(p => !knownPatterns.some(pat => p.message.includes(pat)) && p.message !== `${restarts} restarts`)
    }
    default:
      return false
  }
}

// A transient lifecycle state (plain Pending, Terminating, awaiting an address)
// is not a fault while it's young — it becomes one only once it's stuck past
// these windows, mirroring the backend health thresholds (ClassifyPodHealth:
// pending 5m; detect.go: terminating 10m, LB/PVC pending 5m). Two things escalate
// immediately, regardless of age, because they're definitive failures the backend
// also flags at once: fatal *container* states (CrashLoopBackOff, ImagePull,
// InvalidImageName, OOMKilled, …) and Unschedulable (the scheduler tried and
// could not place the pod).
const PENDING_STUCK_MINUTES = 5
const TERMINATING_STUCK_MINUTES = 10

// minutesSince returns minutes elapsed since an ISO timestamp, or 0 when it's
// missing/invalid — so "unknown age" is treated as not-yet-stuck (benign).
function minutesSince(timestamp?: string): number {
  if (!timestamp) return 0
  const t = new Date(timestamp).getTime()
  if (Number.isNaN(t)) return 0
  return (Date.now() - t) / 60000
}

function podUnschedulable(pod: any): boolean {
  const conds = pod?.status?.conditions || []
  return conds.some(
    (c: any) => c.type === 'PodScheduled' && c.status === 'False' && c.reason === 'Unschedulable'
  )
}

// A container that has terminated successfully (exit 0) is done, not "not ready".
// A completing Job pod (Running phase, container Completed, Ready=false) must not
// read as degraded just because ready<total — this mirrors ClassifyPodHealth, so
// the badge agrees with the timeline/Problems verdict.
function containerSettledOk(cs: any): boolean {
  return cs?.ready === true || cs?.state?.terminated?.exitCode === 0
}

// Hard-failure waiting reasons that won't self-resolve — mirrors the backend's
// isFatalWaitingReason. These escalate immediately regardless of pod age or a
// Terminating overlay (a bad image / config error is a real failure now, not a
// benign young-Pending pod).
const FATAL_WAITING_REASONS = new Set([
  'CrashLoopBackOff', 'ImagePullBackOff', 'ErrImagePull', 'InvalidImageName',
  'ImageInspectError', 'CreateContainerConfigError', 'CreateContainerError', 'RunContainerError',
])

// firstFatalContainer returns the first init- or main-container in a hard-failure
// state (or null). Init containers are walked first: when init is failing the pod
// stays Pending and main ContainerStatuses haven't populated yet, so checking main
// alone would miss it and fall through to a benign "Pending".
function firstFatalContainer(pod: any): { name: string; reason: string } | null {
  const init = pod?.status?.initContainerStatuses || []
  const main = pod?.status?.containerStatuses || []
  for (const cs of [...init, ...main]) {
    const w = cs?.state?.waiting?.reason
    if (w && FATAL_WAITING_REASONS.has(w)) return { name: cs?.name, reason: w }
    if (cs?.state?.terminated?.reason === 'OOMKilled') return { name: cs?.name, reason: 'OOMKilled' }
  }
  return null
}

export function getPodStatus(pod: any): StatusBadge {
  const phase = pod.status?.phase || 'Unknown'
  const containerStatuses = pod.status?.containerStatuses || []

  // Fatal init/main container states win over the Pending grace AND a Terminating
  // overlay — a crash-loop, bad image, or config error is a real failure now,
  // whatever the pod's age or deletion state. Skip for Succeeded: a terminal
  // success must not flip unhealthy on a sidecar that OOMed before the main
  // container finished (matches getPodPhaseDisplay + ClassifyPodHealth).
  if (phase !== 'Succeeded') {
    const fatal = firstFatalContainer(pod)
    if (fatal) {
      return { text: fatal.reason, color: healthColors.unhealthy, level: 'unhealthy' }
    }
  }

  // A terminal failure stays unhealthy even while the pod is being deleted — the
  // Terminating overlay below must not mask a Failed pod (the Problems panel and
  // dashboard report it as an error regardless).
  if (phase === 'Failed') {
    return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  // Terminating: neutral while gracefully shutting down, degraded once stuck
  // (a wedged finalizer / preStop hook holding the pod open).
  if (pod.metadata?.deletionTimestamp) {
    if (minutesSince(pod.metadata.deletionTimestamp) >= TERMINATING_STUCK_MINUTES) {
      return { text: 'Terminating', color: healthColors.degraded, level: 'degraded' }
    }
    return { text: 'Terminating', color: healthColors.neutral, level: 'neutral' }
  }

  switch (phase) {
    case 'Running': {
      // Degrade only on containers that are neither ready nor successfully done —
      // a completed container (exit 0) on a Running pod is finishing, not a fault.
      const ready = containerStatuses.filter((c: any) => c.ready).length
      const total = containerStatuses.length
      const unsettled = containerStatuses.filter((c: any) => !containerSettledOk(c)).length
      if (unsettled > 0) {
        return { text: `Running (${ready}/${total})`, color: healthColors.degraded, level: 'degraded' }
      }
      return { text: 'Running', color: healthColors.healthy, level: 'healthy' }
    }
    case 'Succeeded':
      return { text: 'Completed', color: healthColors.neutral, level: 'neutral' }
    case 'Pending':
      // Unschedulable = the scheduler tried and failed to place this pod; the
      // backend flags it immediately (severity ramps with duration), so the badge
      // does too. Plain Pending (not yet placed) keeps the young-grace window.
      if (podUnschedulable(pod)) {
        return { text: 'Unschedulable', color: healthColors.degraded, level: 'degraded' }
      }
      if (minutesSince(pod.metadata?.creationTimestamp) >= PENDING_STUCK_MINUTES) {
        return { text: 'Pending', color: healthColors.degraded, level: 'degraded' }
      }
      return { text: 'Pending', color: healthColors.neutral, level: 'neutral' }
    case 'Failed':
      return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
    default:
      return { text: phase, color: healthColors.unknown, level: 'unknown' }
  }
}

/**
 * Pod phase enriched with readiness/restart/crash signals so the Phase
 * row doesn't show a bare "Running" on a 0/1 or crash-looping pod.
 */
export interface PodPhaseDisplay {
  /** Verbatim `pod.status.phase` (or "Unknown") — never lost. */
  phase: string
  /** Phase + derived qualifier, e.g. "Running — Not Ready (0/1)". */
  text: string
  /** Severity tier for tinting / icon choice, mirrors getPodStatus. */
  level: HealthLevel
  /** Optional one-line explanation surfaced as a tooltip / muted suffix. */
  hint?: string
}

const RESTART_CYCLING_THRESHOLD = 5

export function getPodPhaseDisplay(pod: any): PodPhaseDisplay {
  const phase: string = pod?.status?.phase || 'Unknown'
  const containerStatuses: any[] = pod?.status?.containerStatuses || []
  const totalContainers = containerStatuses.length
  const readyContainers = containerStatuses.filter((c) => c?.ready).length
  const restartTotal = containerStatuses.reduce(
    (sum, c) => sum + (c?.restartCount || 0),
    0
  )

  // Container-state failures take precedence over phase AND over a Terminating
  // overlay: a CrashLoopBackOff pod can still report phase: Running, an init
  // failure keeps the pod Pending, and a pod crashing while being deleted is
  // still a failure worth surfacing. Skip for Succeeded — a Job pod whose sidecar
  // was OOMKilled before the main container completed should not read unhealthy
  // after terminal success.
  if (phase !== 'Succeeded') {
    const fatal = firstFatalContainer(pod)
    if (fatal) {
      return {
        phase,
        text: `${phase} — ${fatal.reason}`,
        level: 'unhealthy',
        hint: fatal.reason === 'OOMKilled'
          ? `Container "${fatal.name}" was OOMKilled.`
          : `Container "${fatal.name}" is stuck in ${fatal.reason}.`,
      }
    }
  }

  // A terminal failure stays unhealthy even while the pod is being deleted — don't
  // let the Terminating overlay mask a Failed pod.
  if (phase === 'Failed') {
    return { phase, text: 'Failed', level: 'unhealthy' }
  }

  // Terminating: neutral while gracefully shutting down, degraded once stuck.
  if (pod?.metadata?.deletionTimestamp) {
    const stuck = minutesSince(pod.metadata.deletionTimestamp) >= TERMINATING_STUCK_MINUTES
    return {
      phase,
      text: `${phase} — Terminating`,
      level: stuck ? 'degraded' : 'neutral',
      hint: stuck
        ? 'Pod has been terminating for a while — a finalizer or preStop hook may be wedged.'
        : 'Pod has a deletionTimestamp set; awaiting graceful termination.',
    }
  }

  switch (phase) {
    case 'Running': {
      // A successfully-completed container (exit 0) is settled, not "not ready" —
      // don't degrade a completing pod over it (matches getPodStatus / timeline).
      const unsettled = containerStatuses.filter((c) => !containerSettledOk(c)).length
      const notReady = totalContainers > 0 && unsettled > 0
      const cycling = restartTotal > RESTART_CYCLING_THRESHOLD
      if (notReady && cycling) {
        return {
          phase,
          text: `Running — Not Ready (${readyContainers}/${totalContainers}), ${restartTotal} restarts`,
          level: 'unhealthy',
          hint: 'Containers report not-ready and have restarted many times — likely crash-looping.',
        }
      }
      if (notReady) {
        return {
          phase,
          text: `Running — Not Ready (${readyContainers}/${totalContainers})`,
          level: 'degraded',
          hint: 'Pod is in the Running phase but at least one container is not ready (probes failing or still starting).',
        }
      }
      if (cycling) {
        return {
          phase,
          text: `Running — Restarting (${restartTotal} restarts)`,
          level: 'degraded',
          hint: 'Containers are ready right now but have restarted many times — investigate stability.',
        }
      }
      return { phase, text: 'Running', level: 'healthy' }
    }
    case 'Succeeded':
      return { phase, text: 'Completed', level: 'neutral' }
    case 'Pending':
      // Unschedulable = the scheduler tried and failed; surface it immediately
      // (the backend does, with severity ramping by duration). Plain Pending
      // (not yet placed) keeps the young-grace window.
      if (podUnschedulable(pod)) {
        return {
          phase,
          text: `${phase} — Unschedulable`,
          level: 'degraded',
          hint: 'No node can accept this pod (insufficient resources, taints, affinity, or quota).',
        }
      }
      if (minutesSince(pod?.metadata?.creationTimestamp) >= PENDING_STUCK_MINUTES) {
        return {
          phase,
          text: 'Pending',
          level: 'degraded',
          hint: 'Pod has been Pending for several minutes — check scheduling and image pulls.',
        }
      }
      return { phase, text: 'Pending', level: 'neutral' }
    case 'Failed':
      return { phase, text: 'Failed', level: 'unhealthy' }
    default:
      return { phase, text: phase, level: 'unknown' }
  }
}

export function getPodProblems(pod: any): PodProblem[] {
  const problems: PodProblem[] = []
  const containerStatuses = pod.status?.containerStatuses || []
  const initContainerStatuses = pod.status?.initContainerStatuses || []
  const conditions = pod.status?.conditions || []
  const phase = pod.status?.phase
  const podStatusMessage = pod.status?.message || undefined
  const hasPodIP = Boolean(pod.status?.podIP || pod.status?.podIPs?.some((ip: any) => ip?.ip))
  const hasScheduledNode = Boolean(pod.spec?.nodeName)
  const hasUnschedulableCondition = conditions.some((cond: any) => cond.type === 'PodScheduled' && cond.status === 'False')
  const hasContainerCreating = containerStatuses.some((cs: any) => cs.state?.waiting?.reason === 'ContainerCreating')
  const createdAtMs = pod.metadata?.creationTimestamp ? new Date(pod.metadata.creationTimestamp).getTime() : NaN
  const podAgeMs = Number.isFinite(createdAtMs) ? Date.now() - createdAtMs : 0
  const sandboxStartupStallAgeMs = 10 * 60 * 1000
  const sandboxStartupStallCriticalAgeMs = 30 * 60 * 1000
  const inferredSandboxStartupStall = phase === 'Pending' && hasScheduledNode && !hasUnschedulableCondition && hasContainerCreating && !hasPodIP && podAgeMs > sandboxStartupStallAgeMs
  const inferredSandboxStartupStallSeverity: PodProblem['severity'] = podAgeMs >= sandboxStartupStallCriticalAgeMs ? 'critical' : 'high'
  let hasSandboxStartupStallProblem = false

  // Failed or Unknown phase
  if (phase === 'Failed' && pod.status?.reason !== 'Evicted') {
    problems.push({ severity: 'critical', message: 'Failed', detail: podStatusMessage })
  } else if (phase === 'Unknown') {
    problems.push({ severity: 'high', message: 'Unknown', detail: podStatusMessage })
  }

  // Init container failures
  for (const cs of initContainerStatuses) {
    if (cs.state?.waiting?.reason && cs.state.waiting.reason !== 'PodInitializing') {
      const reason = cs.state.waiting.reason
      const detail = cs.state.waiting.message || undefined
      if (['CrashLoopBackOff', 'ImagePullBackOff', 'ErrImagePull'].includes(reason)) {
        problems.push({ severity: 'critical', message: `Init: ${reason}`, detail })
      } else {
        problems.push({ severity: 'high', message: `Init: ${reason}`, detail })
      }
    }
    if (cs.state?.terminated?.exitCode && cs.state.terminated.exitCode !== 0) {
      problems.push({ severity: 'high', message: `Init: Exit Code ${cs.state.terminated.exitCode}`, detail: cs.state.terminated.message || undefined })
    }
  }

  for (const cs of containerStatuses) {
    // Check waiting state
    if (cs.state?.waiting?.reason) {
      const reason = cs.state.waiting.reason
      const detail = cs.state.waiting.message || undefined
      if (['CrashLoopBackOff', 'ImagePullBackOff', 'ErrImagePull'].includes(reason)) {
        problems.push({ severity: 'critical', message: reason, detail })
      } else if (reason === 'CreateContainerConfigError') {
        problems.push({ severity: 'critical', message: 'Config Error', detail })
      } else if (reason === 'ContainerCannotRun') {
        problems.push({ severity: 'critical', message: 'Cannot Run', detail })
      } else if (reason !== 'ContainerCreating' && reason !== 'PodInitializing') {
        problems.push({ severity: 'high', message: reason, detail })
      }
    }
    // Check terminated state
    if (cs.state?.terminated?.reason === 'OOMKilled') {
      problems.push({ severity: 'critical', message: 'OOMKilled', detail: cs.state.terminated.message || undefined })
    } else if (cs.state?.terminated?.exitCode && cs.state.terminated.exitCode !== 0) {
      problems.push({ severity: 'high', message: `Exit Code ${cs.state.terminated.exitCode}`, detail: cs.state.terminated.message || undefined })
    }
    // High restart count
    if (cs.restartCount > 5) {
      problems.push({ severity: 'medium', message: `${cs.restartCount} restarts` })
    }
    // Volume mount issues from last state
    const lastMsgRaw = cs.lastState?.terminated?.message || ''
    const lastMsg = lastMsgRaw.toLowerCase()
    if (lastMsg.includes('failed to mount') || lastMsg.includes('failedattachvolume')) {
      problems.push({ severity: 'high', message: 'Volume Mount Failed', detail: lastMsgRaw || undefined })
    }
  }

  // Check conditions
  for (const cond of conditions) {
    if (cond.type === 'PodScheduled' && cond.status === 'False') {
      if (cond.reason === 'Unschedulable') {
        problems.push({ severity: 'high', message: 'Unschedulable', detail: summarizeSchedulerMessage(cond.message) || undefined })
      }
    }
    // Readiness/Liveness probe failures
    if (cond.type === 'ContainersReady' && cond.status === 'False') {
      const msg = (cond.message || '').toLowerCase()
      if (msg.includes('readiness')) {
        problems.push({ severity: 'medium', message: 'Readiness Probe Failing', detail: cond.message || undefined })
      } else if (msg.includes('liveness')) {
        problems.push({ severity: 'high', message: 'Liveness Probe Failing', detail: cond.message || undefined })
      }
    }
    // IP allocation failures (subnet exhaustion)
    if (cond.type === 'PodReadyToStartContainers' && cond.status === 'False') {
      const msg = (cond.message || '').toLowerCase()
      if (msg.includes('failed to assign an ip')) {
        problems.push({ severity: 'critical', message: 'IP Allocation Failed', detail: cond.message || undefined })
      } else if (msg.includes('pod sandbox')) {
        problems.push({ severity: 'critical', message: 'Sandbox Startup Stalled', detail: cond.message || undefined })
        hasSandboxStartupStallProblem = true
      }
    }
  }
  if (inferredSandboxStartupStall && !hasSandboxStartupStallProblem) {
    problems.push({ severity: inferredSandboxStartupStallSeverity, message: 'Sandbox Startup Stalled' })
  }

  // Evicted pods
  if (phase === 'Failed' && pod.status?.reason === 'Evicted') {
    problems.push({ severity: 'high', message: 'Evicted', detail: podStatusMessage })
  }

  // Stuck terminating (zombie pod). Use the same threshold as the badge
  // (TERMINATING_STUCK_MINUTES) so the drawer problem and the table badge flip
  // together — firing at 60s while the badge stayed calm to 10m was a mismatch.
  if (pod.metadata?.deletionTimestamp) {
    if (minutesSince(pod.metadata.deletionTimestamp) >= TERMINATING_STUCK_MINUTES) {
      problems.push({ severity: 'medium', message: 'Stuck Terminating' })
    }
  }

  // Not ready (Running but containers not ready). Use the same containerSettledOk
  // gate as getPodStatus so a completing Job pod (Running, container terminated
  // exit 0, Ready=false) doesn't raise a drawer problem while the table badge
  // stays calm — a settled/completed container is not "Not Ready".
  if (phase === 'Running') {
    const unsettled = containerStatuses.filter((c: any) => !containerSettledOk(c)).length
    const totalContainers = containerStatuses.length
    if (totalContainers > 0 && unsettled > 0) {
      // Only add if we haven't already flagged a more specific issue
      const hasSpecificIssue = problems.some(p =>
        p.message.includes('Probe') || p.message.includes('CrashLoop') || p.message.includes('OOM')
      )
      if (!hasSpecificIssue) {
        problems.push({ severity: 'medium', message: 'Not Ready' })
      }
    }
  }

  return problems
}

export function getPodReadiness(pod: any): { ready: number; total: number } {
  const containerStatuses = pod.status?.containerStatuses || []
  const initContainerStatuses = pod.status?.initContainerStatuses || []

  // Check init containers first
  const initNotComplete = initContainerStatuses.filter((c: any) =>
    !c.state?.terminated || c.state.terminated.exitCode !== 0
  ).length

  if (initNotComplete > 0 && initContainerStatuses.length > 0) {
    const initComplete = initContainerStatuses.length - initNotComplete
    return { ready: initComplete, total: initContainerStatuses.length }
  }

  const ready = containerStatuses.filter((c: any) => c.ready).length
  return { ready, total: containerStatuses.length || pod.spec?.containers?.length || 0 }
}

export function getPodRestarts(pod: any): number {
  const containerStatuses = pod.status?.containerStatuses || []
  return containerStatuses.reduce((sum: number, c: any) => sum + (c.restartCount || 0), 0)
}

export interface ContainerSquareState {
  name: string
  status: 'ready' | 'running' | 'waiting' | 'completed' | 'terminated' | 'unknown'
  restarts: number
  reason?: string
  message?: string
  exitCode?: number
  startedAt?: string
  finishedAt?: string
  isInit?: boolean
  /** Last termination info — crucial for debugging CrashLoopBackOff */
  lastTermination?: {
    reason?: string
    exitCode?: number
    startedAt?: string
    finishedAt?: string
  }
}

/**
 * The container to default to for exec / logs on a multi-container pod. Honors
 * the kubectl.kubernetes.io/default-container annotation (the convention
 * kubectl, k9s, and Lens follow, and what service meshes like Istio set to
 * point past their injected sidecar), falling back to the first container.
 * Without this, mesh-injected pods default to their distroless sidecar — which
 * has no shell — making the terminal appear broken.
 */
export function getDefaultContainerName(pod: any): string | undefined {
  const containers = pod?.spec?.containers || []
  const annotated = pod?.metadata?.annotations?.['kubectl.kubernetes.io/default-container']
  if (annotated && containers.some((c: any) => c.name === annotated)) {
    return annotated
  }
  return containers[0]?.name
}

export function getContainerSquareStates(pod: any): ContainerSquareState[] {
  const result: ContainerSquareState[] = []
  const initStatuses = pod.status?.initContainerStatuses || []
  const containerStatuses = pod.status?.containerStatuses || []
  const specContainers = pod.spec?.containers || []

  for (const cs of initStatuses) {
    const stateKey = cs.state ? Object.keys(cs.state)[0] : 'unknown'
    let status: ContainerSquareState['status'] = 'unknown'
    if (stateKey === 'terminated' && cs.state?.terminated?.exitCode === 0) {
      status = 'completed'
    } else if (stateKey === 'running') {
      status = cs.ready ? 'ready' : 'running'
    } else if (stateKey === 'waiting') {
      status = 'waiting'
    } else if (stateKey === 'terminated') {
      status = 'terminated'
    }
    const stateDetail = cs.state?.[stateKey]
    const lastTerm = cs.lastState?.terminated
    result.push({
      name: cs.name,
      status,
      restarts: cs.restartCount || 0,
      reason: stateDetail?.reason,
      message: stateDetail?.message,
      exitCode: stateDetail?.exitCode,
      startedAt: stateDetail?.startedAt,
      finishedAt: stateDetail?.finishedAt,
      isInit: true,
      lastTermination: lastTerm ? { reason: lastTerm.reason, exitCode: lastTerm.exitCode, startedAt: lastTerm.startedAt, finishedAt: lastTerm.finishedAt } : undefined,
    })
  }

  if (containerStatuses.length > 0) {
    for (const cs of containerStatuses) {
      const stateKey = cs.state ? Object.keys(cs.state)[0] : 'unknown'
      let status: ContainerSquareState['status'] = 'unknown'
      if (stateKey === 'running' && cs.ready) {
        status = 'ready'
      } else if (stateKey === 'running') {
        status = 'running'
      } else if (stateKey === 'terminated' && cs.state?.terminated?.exitCode === 0) {
        status = 'completed'
      } else if (stateKey === 'terminated') {
        status = 'terminated'
      } else if (stateKey === 'waiting') {
        status = 'waiting'
      }
      const stateDetail = cs.state?.[stateKey]
      const lastTerm = cs.lastState?.terminated
      result.push({
        name: cs.name,
        status,
        restarts: cs.restartCount || 0,
        reason: stateDetail?.reason,
        message: stateDetail?.message,
        exitCode: stateDetail?.exitCode,
        startedAt: stateDetail?.startedAt,
        finishedAt: stateDetail?.finishedAt,
        lastTermination: lastTerm ? { reason: lastTerm.reason, exitCode: lastTerm.exitCode, startedAt: lastTerm.startedAt, finishedAt: lastTerm.finishedAt } : undefined,
      })
    }
  } else {
    for (const c of specContainers) {
      result.push({ name: c.name, status: 'unknown', restarts: 0 })
    }
  }

  return result
}

// ============================================================================
// WORKLOAD UTILITIES (Deployment, StatefulSet, DaemonSet, ReplicaSet)
// ============================================================================

export function getWorkloadStatus(resource: any, kind: string): StatusBadge {
  const status = resource.status || {}
  const spec = resource.spec || {}

  if (kind === 'daemonsets') {
    const desired = status.desiredNumberScheduled || 0
    const ready = status.numberReady || 0
    const updated = status.updatedNumberScheduled || 0

    // 0 desired = the node selector matches no nodes — intentional/idle, not a
    // fault and not "unknown" (matches pkg/health.Workload). Sky.
    if (desired === 0) return { text: '0 nodes', color: healthColors.neutral, level: 'neutral' }
    if (ready === desired && updated === desired) {
      return { text: `${ready}/${desired}`, color: healthColors.healthy, level: 'healthy' }
    }
    if (ready > 0) {
      return { text: `${ready}/${desired}`, color: healthColors.degraded, level: 'degraded' }
    }
    return { text: `${ready}/${desired}`, color: healthColors.unhealthy, level: 'unhealthy' }
  }

  // Deployment, StatefulSet, ReplicaSet
  const desired = spec.replicas ?? status.replicas ?? 0
  const ready = status.readyReplicas || 0
  const updated = status.updatedReplicas || 0
  const available = status.availableReplicas || 0

  if (desired === 0) {
    return { text: 'Scaled to 0', color: healthColors.neutral, level: 'neutral' }
  }

  // Check if updating
  if (updated < desired && updated > 0) {
    return { text: `Updating ${updated}/${desired}`, color: healthColors.degraded, level: 'degraded' }
  }

  if (ready === desired && available === desired) {
    return { text: `${ready}/${desired}`, color: healthColors.healthy, level: 'healthy' }
  }
  if (ready > 0) {
    return { text: `${ready}/${desired}`, color: healthColors.degraded, level: 'degraded' }
  }
  return { text: `${ready}/${desired}`, color: healthColors.unhealthy, level: 'unhealthy' }
}

/** Detect problems for Deployments, StatefulSets, DaemonSets. Parallel to getPodProblems. */
export function getWorkloadProblems(resource: any, kind: string): PodProblem[] {
  const problems: PodProblem[] = []
  const status = resource.status || {}
  const spec = resource.spec || {}
  const k = kind.toLowerCase()

  if (k === 'daemonsets') {
    const desired = status.desiredNumberScheduled || 0
    const ready = status.numberReady || 0
    if (desired > 0 && ready === 0) {
      problems.push({ severity: 'critical', message: 'No pods ready' })
    } else if (desired > 0 && ready < desired) {
      problems.push({ severity: 'high', message: `${desired - ready} pods unavailable` })
    }
    return problems
  }

  // Deployment, StatefulSet
  const desired = spec.replicas ?? status.replicas ?? 0
  const ready = status.readyReplicas || 0
  const available = status.availableReplicas ?? ready
  const updated = status.updatedReplicas || 0

  if (desired === 0) return problems

  if (ready === 0 && desired > 0) {
    problems.push({ severity: 'critical', message: 'No pods ready' })
  } else if (available < desired) {
    problems.push({ severity: 'high', message: `${desired - available} pods unavailable` })
  }

  if (updated > 0 && updated < desired) {
    problems.push({ severity: 'medium', message: 'Rollout in progress' })
  }

  // Check conditions for stuck rollouts (Deployment only)
  if (k === 'deployments') {
    const conditions = status.conditions || []
    for (const cond of conditions) {
      if (cond.type === 'Progressing' && cond.status === 'False' && cond.reason === 'ProgressDeadlineExceeded') {
        problems.push({ severity: 'critical', message: 'Rollout stuck' })
      }
    }
  }

  return problems
}

/** Check whether a workload's problems match a given problem category */
export function workloadMatchesProblemCategory(problems: PodProblem[], category: string): boolean {
  const msgs = problems.map(p => p.message)
  switch (category) {
    case 'Unavailable':
      return msgs.some(m => m.includes('unavailable') || m.includes('No pods ready'))
    case 'Rollout Stuck':
      return msgs.includes('Rollout stuck')
    case 'Rollout In Progress':
      return msgs.includes('Rollout in progress')
    default:
      return false
  }
}

export function getWorkloadImages(resource: any): string[] {
  const containers = resource.spec?.template?.spec?.containers || []
  return containers.map((c: any) => {
    const image = c.image || ''
    // Extract just image:tag, remove registry prefix
    const parts = image.split('/')
    return parts[parts.length - 1] || image
  })
}

export function getWorkloadConditions(resource: any): { conditions: string[]; hasIssues: boolean } {
  const conditions = resource.status?.conditions || []
  const activeConditions: string[] = []
  let hasIssues = false

  for (const cond of conditions) {
    if (cond.status === 'True') {
      activeConditions.push(cond.type)
      // Progressing and Available are good, others might indicate issues
      if (!['Progressing', 'Available'].includes(cond.type)) {
        hasIssues = true
      }
    } else if (cond.status === 'False') {
      // Available=False is an issue
      if (cond.type === 'Available') {
        hasIssues = true
      }
    }
  }

  return { conditions: activeConditions, hasIssues }
}

export function getReplicaSetOwner(rs: any): string | null {
  const ownerRefs = rs.metadata?.ownerReferences || []
  const owner = ownerRefs[0]
  if (owner) {
    return `${owner.kind}/${owner.name}`
  }
  return null
}

export function isReplicaSetActive(rs: any): boolean {
  const replicas = rs.spec?.replicas || 0
  return replicas > 0
}

// ============================================================================
// SERVICE UTILITIES
// ============================================================================

export function getServiceStatus(service: any): StatusBadge {
  const type = service.spec?.type || 'ClusterIP'
  const color = type === 'LoadBalancer' || type === 'NodePort'
    ? 'status-violet'
    : healthColors.neutral
  return { text: type, color, level: 'neutral' }
}

export function getServicePorts(service: any): string {
  const ports = service.spec?.ports || []
  if (ports.length === 0) return '-'
  if (ports.length <= 2) {
    return ports.map((p: any) => {
      const port = p.port
      const target = p.targetPort !== p.port ? `:${p.targetPort}` : ''
      const proto = p.protocol !== 'TCP' ? `/${p.protocol}` : ''
      return `${port}${target}${proto}`
    }).join(', ')
  }
  return `${ports.length} ports`
}

export function getServiceExternalIP(service: any): string | null {
  const type = service.spec?.type
  if (type === 'LoadBalancer') {
    const ingress = service.status?.loadBalancer?.ingress || []
    // Show all IPs, not just first
    if (ingress.length > 0) {
      const ips = ingress.map((i: any) => i.ip || i.hostname).filter(Boolean)
      if (ips.length > 2) return `${ips[0]} +${ips.length - 1}`
      return ips.join(', ') || null
    }
    return 'Pending'
  }
  if (type === 'NodePort') {
    const ports = service.spec?.ports || []
    const nodePorts = ports.map((p: any) => p.nodePort).filter(Boolean)
    if (nodePorts.length > 0) {
      return `NodePort: ${nodePorts.join(', ')}`
    }
  }
  // Also check spec.externalIPs (can be set on any service type)
  if (service.spec?.externalIPs?.length > 0) {
    const ips = service.spec.externalIPs
    if (ips.length > 2) return `${ips[0]} +${ips.length - 1}`
    return ips.join(', ')
  }
  return null
}

export function getServiceSelector(service: any): string {
  // For ExternalName services, show the external DNS name
  if (service.spec?.type === 'ExternalName') {
    return service.spec.externalName || '-'
  }
  const selector = service.spec?.selector || {}
  const pairs = Object.entries(selector).map(([k, v]) => `${k}=${v}`)
  if (pairs.length === 0) return 'None'
  if (pairs.length <= 2) return pairs.join(', ')
  return `${pairs.slice(0, 2).join(', ')} +${pairs.length - 2}`
}

export function getServiceEndpointsStatus(service: any): { status: string; color: string } {
  const type = service.spec?.type
  if (type === 'ExternalName') {
    return { status: 'External', color: 'status-violet' }
  }
  const selector = service.spec?.selector || {}
  const hasSelector = Object.keys(selector).length > 0
  if (!hasSelector) {
    return { status: 'None', color: 'status-unknown' }
  }
  // If it has a selector, it should have endpoints (we assume active since we can't check endpoints from service alone)
  return { status: 'Active', color: 'status-healthy' }
}

// ============================================================================
// INGRESS UTILITIES
// ============================================================================

export function getIngressStatus(ingress: any): StatusBadge {
  const lbIngress = ingress.status?.loadBalancer?.ingress || []
  if (lbIngress.length > 0) {
    return { text: 'Active', color: healthColors.healthy, level: 'healthy' }
  }
  // Awaiting an external address is normal right after creation; only flag it
  // once the ingress/LB controller has had time and still hasn't assigned one.
  if (minutesSince(ingress.metadata?.creationTimestamp) >= PENDING_STUCK_MINUTES) {
    return { text: 'Pending', color: healthColors.degraded, level: 'degraded' }
  }
  return { text: 'Pending', color: healthColors.neutral, level: 'neutral' }
}

export function getIngressHosts(ingress: any): string {
  const rules = ingress.spec?.rules || []
  const hosts = rules.map((r: any) => r.host).filter(Boolean)
  if (hosts.length === 0) return '*'
  if (hosts.length <= 2) return hosts.join(', ')
  return `${hosts[0]} +${hosts.length - 1} more`
}

export function getIngressClass(ingress: any): string | null {
  return ingress.spec?.ingressClassName ||
    ingress.metadata?.annotations?.['kubernetes.io/ingress.class'] ||
    null
}

export function hasIngressTLS(ingress: any): boolean {
  return (ingress.spec?.tls?.length || 0) > 0
}

export function getIngressAddress(ingress: any): string | null {
  const lbIngress = ingress.status?.loadBalancer?.ingress || []
  if (lbIngress.length > 0) {
    return lbIngress[0].ip || lbIngress[0].hostname || null
  }
  return null
}

export function getIngressRules(ingress: any): string {
  const rules = ingress.spec?.rules || []
  if (rules.length === 0) return 'No rules'

  const formattedRules = rules.map((rule: any) => {
    const host = rule.host || '*'
    const paths = rule.http?.paths || []
    if (paths.length === 0) return host

    const pathMappings = paths.map((p: any) => {
      const path = p.path || '/'
      // Support both new (service.name) and legacy (serviceName) formats
      const backend = p.backend?.service?.name || (p.backend as any)?.serviceName || 'unknown'
      return `${path}→${backend}`
    })

    if (pathMappings.length === 1) {
      return `${host}: ${pathMappings[0]}`
    }
    return `${host}: ${pathMappings.join(', ')}`
  })

  if (formattedRules.length === 1) return formattedRules[0]
  if (formattedRules.length === 2) return formattedRules.join('; ')
  return `${formattedRules[0]}; +${formattedRules.length - 1} more`
}

// ============================================================================
// CONFIGMAP / SECRET UTILITIES
// ============================================================================

export function getConfigMapKeys(cm: any): { count: number; preview: string } {
  const data = cm.data || {}
  const binaryData = cm.binaryData || {}
  const keys = [...Object.keys(data), ...Object.keys(binaryData)]
  const count = keys.length

  if (count === 0) return { count: 0, preview: 'Empty' }
  if (count <= 3) return { count, preview: keys.join(', ') }
  return { count, preview: `${keys.slice(0, 2).join(', ')} +${count - 2}` }
}

export function getConfigMapSize(cm: any): string {
  const data = cm.data || {}
  const binaryData = cm.binaryData || {}

  let totalBytes = 0
  for (const value of Object.values(data)) {
    totalBytes += (value as string).length
  }
  for (const value of Object.values(binaryData)) {
    // Base64 encoded, actual size is ~75% of encoded
    totalBytes += Math.floor((value as string).length * 0.75)
  }

  return formatBytes(totalBytes)
}

export function getSecretType(secret: any): { type: string; color: string } {
  const type = secret.type || 'Opaque'
  const typeMap: Record<string, { type: string; color: string }> = {
    'Opaque': { type: 'Opaque', color: 'status-unknown' },
    'kubernetes.io/tls': { type: 'TLS', color: 'status-neutral' },
    'kubernetes.io/dockercfg': { type: 'Docker', color: 'status-purple' },
    'kubernetes.io/dockerconfigjson': { type: 'Docker', color: 'status-purple' },
    'kubernetes.io/basic-auth': { type: 'Basic Auth', color: 'status-orange' },
    'kubernetes.io/ssh-auth': { type: 'SSH', color: 'status-cyan' },
    'kubernetes.io/service-account-token': { type: 'SA Token', color: 'status-healthy' },
    'bootstrap.kubernetes.io/token': { type: 'Bootstrap', color: 'status-healthy' },
  }
  return typeMap[type] || { type: type.split('/').pop() || type, color: 'status-unknown' }
}

export function getSecretKeyCount(secret: any): number {
  const data = secret.data || {}
  return Object.keys(data).length
}

// ============================================================================
// JOB / CRONJOB UTILITIES
// ============================================================================

export function getJobStatus(job: any): StatusBadge {
  const status = job.status || {}
  const conditions = status.conditions || []

  // Check conditions first
  const failedCond = conditions.find((c: any) => c.type === 'Failed' && c.status === 'True')
  if (failedCond) {
    return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  const completeCond = conditions.find((c: any) => c.type === 'Complete' && c.status === 'True')
  if (completeCond) {
    // A completed Job is done by design — neutral/idle (sky), not the green of a
    // serving workload (matches pkg/health.Workload).
    return { text: 'Complete', color: healthColors.neutral, level: 'neutral' }
  }

  if (job.spec?.suspend) {
    return { text: 'Suspended', color: healthColors.neutral, level: 'neutral' }
  }

  if (status.active > 0) {
    return { text: 'Running', color: healthColors.neutral, level: 'neutral' }
  }

  return { text: 'Pending', color: healthColors.degraded, level: 'degraded' }
}

export function getJobCompletions(job: any): { succeeded: number; total: number } {
  const completions = job.spec?.completions || 1
  const succeeded = job.status?.succeeded || 0
  return { succeeded, total: completions }
}

export function getJobDuration(job: any): string | null {
  const startTime = job.status?.startTime
  const completionTime = job.status?.completionTime

  if (!startTime) return null

  const start = new Date(startTime)
  const end = completionTime ? new Date(completionTime) : new Date()
  const durationMs = end.getTime() - start.getTime()

  return formatDuration(durationMs)
}

export function getCronJobStatus(cj: any): StatusBadge {
  if (cj.spec?.suspend) {
    return { text: 'Suspended', color: healthColors.neutral, level: 'neutral' }
  }
  const activeJobs = cj.status?.active?.length || 0
  if (activeJobs > 0) {
    return { text: `Active (${activeJobs})`, color: healthColors.neutral, level: 'neutral' }
  }
  return { text: 'Scheduled', color: healthColors.healthy, level: 'healthy' }
}

export function getCronJobSchedule(cj: any): { cron: string; readable: string } {
  const schedule = cj.spec?.schedule || ''
  return { cron: schedule, readable: cronToHuman(schedule) }
}

export function getCronJobLastRun(cj: any): string | null {
  const lastSchedule = cj.status?.lastScheduleTime
  if (!lastSchedule) return null
  return formatAge(lastSchedule)
}

// ============================================================================
// HPA UTILITIES
// ============================================================================

export function getHPAStatus(hpa: any): StatusBadge {
  return hpaStatusFromState(getHPATableState(hpa), healthColors)
}

export function getHPAReplicas(hpa: any): { current: number; min: number; max: number } {
  return {
    current: hpa.status?.currentReplicas || 0,
    min: hpa.spec?.minReplicas || 1,
    max: hpa.spec?.maxReplicas || 0,
  }
}

export function getHPATarget(hpa: any): string {
  const ref = hpa.spec?.scaleTargetRef
  if (!ref) return '-'
  return `${ref.kind}/${ref.name}`
}

export function getHPAMetrics(hpa: any): { cpu?: number; memory?: number; custom: number } {
  const currentMetrics = hpa.status?.currentMetrics || []
  const result: { cpu?: number; memory?: number; custom: number } = { custom: 0 }

  for (const metric of currentMetrics) {
    if (metric.type === 'Resource') {
      const current = metric.resource?.current?.averageUtilization
      if (metric.resource?.name === 'cpu' && current !== undefined) {
        result.cpu = current
      } else if (metric.resource?.name === 'memory' && current !== undefined) {
        result.memory = current
      }
    } else {
      result.custom++
    }
  }

  return result
}

// ============================================================================
// NODE UTILITIES
// ============================================================================

export function getNodeStatus(node: any): StatusBadge {
  const conditions = node.status?.conditions || []
  const readyCondition = conditions.find((c: any) => c.type === 'Ready')

  const isReady = readyCondition?.status === 'True'
  const isUnschedulable = node.spec?.unschedulable === true

  if (isReady && isUnschedulable) {
    // Cordon is intentional but consequential — it's lost scheduling capacity, and
    // a forgotten cordon strands nodes — so it stays on the warning axis (matching
    // the backend Cordoned issue), unlike no-op intentional states (suspended/idle).
    return { text: 'Ready,SchedulingDisabled', color: healthColors.degraded, level: 'degraded' }
  }
  if (isReady) {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCondition?.status === 'False') {
    return { text: 'NotReady', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getNodeRoles(node: any): string {
  const labels = node.metadata?.labels || {}
  const roles: string[] = []

  for (const [key, value] of Object.entries(labels)) {
    if (key.startsWith('node-role.kubernetes.io/')) {
      let role = key.replace('node-role.kubernetes.io/', '')
      // Normalize master to control-plane
      if (role === 'master') role = 'control-plane'
      if (role && value !== 'false') {
        roles.push(role)
      }
    }
  }

  if (roles.length === 0) return 'worker'
  return roles.join(', ')
}

export interface NodeCondition {
  type: string
  status: 'True' | 'False' | 'Unknown'
  message?: string
}

// Problem conditions that indicate node issues
const NODE_PROBLEM_CONDITIONS = ['DiskPressure', 'MemoryPressure', 'PIDPressure', 'NetworkUnavailable']

export function getNodeConditions(node: any): { problems: string[]; healthy: boolean } {
  const conditions = node.status?.conditions || []
  const problems: string[] = []

  for (const cond of conditions) {
    // Ready=False is a problem
    if (cond.type === 'Ready' && cond.status !== 'True') {
      problems.push('NotReady')
    }
    // Other conditions are problems when True
    if (NODE_PROBLEM_CONDITIONS.includes(cond.type) && cond.status === 'True') {
      // Format: "DiskPressure" -> "Disk Pressure"
      const formatted = cond.type.replace(/([A-Z])/g, ' $1').trim()
      problems.push(formatted)
    }
  }

  return { problems, healthy: problems.length === 0 }
}

export function getNodeTaints(node: any): { count: number; text: string } {
  const taints = node.spec?.taints || []
  const count = taints.length
  if (count === 0) return { count: 0, text: 'None' }
  return { count, text: pluralize(count, 'taint') }
}

export function getNodeVersion(node: any): string {
  return node.status?.nodeInfo?.kubeletVersion || '-'
}

// ============================================================================
// FORMATTING UTILITIES
// ============================================================================

export function formatAge(timestamp: string): string {
  if (!timestamp) return '-'
  const created = new Date(timestamp)
  const now = new Date()
  const diffMs = now.getTime() - created.getTime()
  return formatDuration(diffMs)
}

export function formatDuration(ms: number, detailed: boolean = false): string {
  const seconds = Math.floor(ms / 1000)
  const minutes = Math.floor(seconds / 60)
  const hours = Math.floor(minutes / 60)
  const days = Math.floor(hours / 24)

  if (detailed) {
    // Detailed format: "2d 5h", "5h 30m", "30m 15s"
    if (days > 0) return `${days}d ${hours % 24}h`
    if (hours > 0) return `${hours}h ${minutes % 60}m`
    if (minutes > 0) return `${minutes}m ${seconds % 60}s`
    return `${seconds}s`
  }

  // Short format: "2d", "5h", "30m"
  if (days > 0) return `${days}d`
  if (hours > 0) return `${hours}h`
  if (minutes > 0) return `${minutes}m`
  if (seconds > 0) return `${seconds}s`
  return '<1s'
}

// formatBytes is available from @skyhook/k8s-ui utils — no re-export needed

export function cronToHuman(cron: string): string {
  if (!cron) return '-'
  const parts = cron.split(' ')
  if (parts.length !== 5) return cron

  const [minute, hour, dayOfMonth, month, dayOfWeek] = parts

  // Common patterns
  if (minute === '0' && hour === '0' && dayOfMonth === '*' && month === '*' && dayOfWeek === '*') {
    return 'Daily at midnight'
  }
  if (minute === '0' && hour.startsWith('*/') && dayOfMonth === '*' && month === '*' && dayOfWeek === '*') {
    const interval = hour.slice(2)
    return interval === '1' ? 'Every hour' : `Every ${interval} hours`
  }
  if (minute === '0' && hour !== '*' && dayOfMonth === '*' && month === '*' && dayOfWeek === '*') {
    return `Daily at ${hour}:00`
  }
  // Exclude step-minute ("*/N") here so it falls through to the interval branch
  // below — otherwise "*/5 * * * *" rendered as "Every hour at :*/5" instead of
  // "Every 5 minutes". A literal minute like "30" still reads "Every hour at :30".
  if (minute !== '*' && !minute.startsWith('*/') && hour === '*' && dayOfMonth === '*' && month === '*' && dayOfWeek === '*') {
    return `Every hour at :${minute.padStart(2, '0')}`
  }
  if (minute === '*' && hour === '*' && dayOfMonth === '*' && month === '*' && dayOfWeek === '*') {
    return 'Every minute'
  }
  // Only claim a plain "Every N minutes" when nothing else constrains the window;
  // otherwise "*/5 9 * * *" (only at 09:xx) or "*/5 * * * 1-5" (weekdays only)
  // would read as an unconstrained interval. Constrained shapes fall through to
  // the raw cron rather than assert something misleading.
  if (minute.startsWith('*/') && hour === '*' && dayOfMonth === '*' && month === '*' && dayOfWeek === '*') {
    const interval = minute.slice(2)
    return interval === '1' ? 'Every minute' : `Every ${interval} minutes`
  }
  // Weekday phrasing needs a literal hour:minute — a wildcard/step in either field
  // (e.g. "*/5 * * * 1-5") would render "Weekdays at *:*/5", so let it fall to raw.
  if (
    (dayOfWeek === '1-5' || dayOfWeek === 'MON-FRI') &&
    hour !== '*' && !hour.startsWith('*/') &&
    minute !== '*' && !minute.startsWith('*/')
  ) {
    return `Weekdays at ${hour}:${minute.padStart(2, '0')}`
  }

  return cron
}

// ============================================================================
// PVC UTILITIES
// ============================================================================

export function getPVCStatus(pvc: any): StatusBadge {
  const phase = pvc.status?.phase || 'Unknown'
  switch (phase) {
    case 'Bound':
      return { text: 'Bound', color: healthColors.healthy, level: 'healthy' }
    case 'Pending':
      // Pending is benign here: a WaitForFirstConsumer claim stays Pending by
      // design until a pod needs it (could be forever for a scaled-to-zero
      // workload). We can't see the StorageClass binding mode from the PVC alone,
      // so age can't distinguish that from genuinely-stuck — the Problems panel,
      // which has that context, owns the stuck-PVC alarm.
      return { text: 'Pending', color: healthColors.neutral, level: 'neutral' }
    case 'Lost':
      return { text: 'Lost', color: healthColors.unhealthy, level: 'unhealthy' }
    default:
      return { text: phase, color: healthColors.unknown, level: 'unknown' }
  }
}

export function getPVCCapacity(pvc: any): string {
  return pvc.status?.capacity?.storage || pvc.spec?.resources?.requests?.storage || '-'
}

export function getPVCAccessModes(pvc: any): string {
  const modes = pvc.status?.accessModes || pvc.spec?.accessModes || []
  const shortModes = modes.map((m: string) => {
    switch (m) {
      case 'ReadWriteOnce': return 'RWO'
      case 'ReadOnlyMany': return 'ROX'
      case 'ReadWriteMany': return 'RWX'
      case 'ReadWriteOncePod': return 'RWOP'
      default: return m
    }
  })
  return shortModes.join(', ') || '-'
}

// ============================================================================
// ROLLOUT UTILITIES (Argo Rollouts CRD)
// ============================================================================

export function getRolloutStatus(rollout: any): StatusBadge {
  const phase = rollout.status?.phase || 'Unknown'
  switch (phase) {
    case 'Healthy':
      return { text: 'Healthy', color: healthColors.healthy, level: 'healthy' }
    case 'Paused':
      return { text: 'Paused', color: healthColors.degraded, level: 'degraded' }
    case 'Progressing':
      return { text: 'Progressing', color: healthColors.degraded, level: 'degraded' }
    case 'Degraded':
      return { text: 'Degraded', color: healthColors.unhealthy, level: 'unhealthy' }
    default:
      return { text: phase, color: healthColors.unknown, level: 'unknown' }
  }
}

export function getRolloutStrategy(rollout: any): string {
  if (rollout.spec?.strategy?.canary) return 'Canary'
  if (rollout.spec?.strategy?.blueGreen) return 'BlueGreen'
  return 'Unknown'
}

export function getRolloutReady(rollout: any): string {
  const ready = rollout.status?.availableReplicas || 0
  const desired = rollout.spec?.replicas || 0
  return `${ready}/${desired}`
}

export function getRolloutStep(rollout: any): string | null {
  const steps = rollout.spec?.strategy?.canary?.steps || []
  const currentIndex = rollout.status?.currentStepIndex
  if (steps.length === 0 || currentIndex === undefined) return null
  return `${currentIndex}/${steps.length}`
}

// ============================================================================
// WORKFLOW UTILITIES (Argo Workflows CRD)
// ============================================================================

export function getWorkflowStatus(workflow: any): StatusBadge {
  const phase = workflow.status?.phase || 'Unknown'
  switch (phase) {
    case 'Succeeded':
      return { text: 'Succeeded', color: healthColors.healthy, level: 'healthy' }
    case 'Running':
      return { text: 'Running', color: healthColors.neutral, level: 'neutral' }
    case 'Failed':
      return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
    case 'Error':
      return { text: 'Error', color: healthColors.unhealthy, level: 'unhealthy' }
    case 'Pending':
      return { text: 'Pending', color: healthColors.degraded, level: 'degraded' }
    default:
      return { text: phase, color: healthColors.unknown, level: 'unknown' }
  }
}

export function getWorkflowDuration(workflow: any): string | null {
  const startedAt = workflow.status?.startedAt
  const finishedAt = workflow.status?.finishedAt
  if (!startedAt) return null
  const start = new Date(startedAt)
  const end = finishedAt ? new Date(finishedAt) : new Date()
  return formatDuration(end.getTime() - start.getTime())
}

export function getWorkflowProgress(workflow: any): string | null {
  const nodes = workflow.status?.nodes
  if (!nodes) return null
  const nodeList = Object.values(nodes) as any[]
  const podNodes = nodeList.filter((n: any) => n.type === 'Pod')
  if (podNodes.length === 0) return null
  const succeeded = podNodes.filter((n: any) => n.phase === 'Succeeded').length
  return `${succeeded}/${podNodes.length}`
}

export function getWorkflowTemplate(workflow: any): string | null {
  return workflow.spec?.workflowTemplateRef?.name || null
}

export function getCronWorkflowStatus(cwf: any): StatusBadge {
  const spec = cwf.spec || {}
  const status = cwf.status || {}
  const active = Array.isArray(status.active) ? status.active.length : 0
  if (spec.suspend === true) {
    return { text: 'Suspended', color: healthColors.degraded, level: 'degraded' }
  }
  if (active > 0) {
    return { text: `${active} active`, color: healthColors.neutral, level: 'neutral' }
  }
  if (status.lastScheduledTime) {
    return { text: 'Idle', color: healthColors.neutral, level: 'neutral' }
  }
  return { text: 'Never run', color: healthColors.unknown, level: 'unknown' }
}

export function getCronWorkflowSchedule(cwf: any): string {
  const schedules = cwf.spec?.schedules
  if (Array.isArray(schedules) && schedules.length > 0) return schedules.join(', ')
  return cwf.spec?.schedule || '-'
}

export function getCronWorkflowLastRun(cwf: any): string {
  return cwf.status?.lastScheduledTime ? formatAge(cwf.status.lastScheduledTime) : 'Never'
}

export function getCronWorkflowTemplate(cwf: any): string {
  return cwf.spec?.workflowSpec?.workflowTemplateRef?.name || cwf.spec?.workflowSpec?.entrypoint || '-'
}

// ============================================================================
// CERT-MANAGER UTILITIES — re-exported from resource-utils-certmanager.ts
// ============================================================================
export {
  getCertificateStatus,
  getCertificateDomains,
  getCertificateIssuer,
  getCertificateExpiry,
  getCertificateRequestStatus,
  getCertificateRequestIssuer,
  getCertificateRequestApproved,
  getClusterIssuerStatus,
  getClusterIssuerType,
  getIssuerStatus,
  getIssuerType,
  getOrderState,
  getOrderDomains,
  getOrderIssuer,
  getChallengeState,
  getChallengeType,
  getChallengeDomain,
  getChallengePresented,
} from './resource-utils-certmanager'

// ============================================================================
// FLUXCD UTILITIES — re-exported from resource-utils-flux.ts
// ============================================================================
export {
  getFluxResourceStatus,
  getGitRepositoryStatus,
  getOCIRepositoryStatus,
  getHelmRepositoryStatus,
  getFluxAlertStatus,
  getKustomizationStatus,
  getFluxHelmReleaseStatus,
  getGitRepositoryUrl,
  getGitRepositoryRef,
  getGitRepositoryRevision,
  getOCIRepositoryUrl,
  getOCIRepositoryRef,
  getOCIRepositoryRevision,
  getHelmRepositoryUrl,
  getHelmRepositoryType,
  getKustomizationSource,
  getKustomizationPath,
  getKustomizationInventory,
  getFluxHelmReleaseChart,
  getFluxHelmReleaseVersion,
  getFluxHelmReleaseRevision,
  getFluxHelmReleaseMessage,
  getFluxAlertProvider,
  getFluxAlertEventCount,
} from './resource-utils-flux'

// ============================================================================
// ARGOCD UTILITIES — re-exported from resource-utils-argo.ts
// ============================================================================
export {
  getArgoApplicationStatus,
  getArgoApplicationProject,
  getArgoApplicationSync,
  getArgoApplicationHealth,
  getArgoApplicationRepo,
  getArgoApplicationSetGenerators,
  getArgoApplicationSetTemplate,
  getArgoApplicationSetAppCount,
  getArgoApplicationSetStatus,
  getArgoAppProjectDescription,
  getArgoAppProjectDestinations,
  getArgoAppProjectSources,
} from './resource-utils-argo'

// ============================================================================
// PERSISTENT VOLUME UTILITIES
// ============================================================================

export function getPVStatus(pv: any): StatusBadge {
  const phase = pv.status?.phase
  switch (phase) {
    case 'Bound':
      return { text: 'Bound', color: healthColors.healthy, level: 'healthy' }
    case 'Available':
      return { text: 'Available', color: healthColors.healthy, level: 'healthy' }
    case 'Released':
      return { text: 'Released', color: healthColors.degraded, level: 'degraded' }
    case 'Failed':
      return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
    default:
      return { text: phase || 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
}

export function getPVAccessModes(pv: any): string {
  const shorthand: Record<string, string> = {
    ReadWriteOnce: 'RWO', ReadOnlyMany: 'ROX', ReadWriteMany: 'RWX', ReadWriteOncePod: 'RWOP',
  }
  const modes = pv.spec?.accessModes || []
  return modes.map((m: string) => shorthand[m] || m).join(', ') || '-'
}

export function getPVClaim(pv: any): string {
  const ref = pv.spec?.claimRef
  if (!ref) return '-'
  return ref.namespace ? `${ref.namespace}/${ref.name}` : ref.name || '-'
}

// ============================================================================
// STORAGE CLASS UTILITIES
// ============================================================================

export function getStorageClassProvisioner(sc: any): string {
  return sc.provisioner || '-'
}

export function getStorageClassReclaimPolicy(sc: any): string {
  return sc.reclaimPolicy || '-'
}

export function getStorageClassBindingMode(sc: any): string {
  const mode = sc.volumeBindingMode
  if (mode === 'WaitForFirstConsumer') return 'WaitForConsumer'
  return mode || '-'
}

export function getStorageClassExpansion(sc: any): string {
  return sc.allowVolumeExpansion ? 'Yes' : 'No'
}



// ============================================================================
// GATEWAY UTILITIES (Gateway API)
// ============================================================================

export function getGatewayStatus(gw: any): StatusBadge {
  const conditions = gw.status?.conditions || []
  const programmed = conditions.find((c: any) => c.type === 'Programmed')
  const accepted = conditions.find((c: any) => c.type === 'Accepted')
  if (programmed?.status === 'True') return { text: 'Programmed', color: healthColors.healthy, level: 'healthy' }
  if (accepted?.status === 'True') return { text: 'Accepted', color: healthColors.degraded, level: 'degraded' }
  if (accepted?.status === 'False') return { text: 'Not Accepted', color: healthColors.unhealthy, level: 'unhealthy' }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getGatewayClass(gw: any): string {
  return gw.spec?.gatewayClassName || '-'
}

export function getGatewayListeners(gw: any): string {
  const listeners = gw.spec?.listeners || []
  if (listeners.length === 0) return '-'
  return listeners.map((l: any) => `${l.protocol}:${l.port}`).join(', ')
}

export function getGatewayAttachedRoutes(gw: any): number {
  const statusListeners = gw.status?.listeners || []
  return statusListeners.reduce((sum: number, l: any) => sum + (l.attachedRoutes ?? 0), 0)
}

export function getGatewayAddresses(gw: any): string {
  const addrs = gw.status?.addresses || gw.spec?.addresses || []
  return addrs.map((a: any) => a.value).join(', ') || '-'
}

// ============================================================================
// GATEWAYCLASS UTILITIES (Gateway API)
// ============================================================================

export function getGatewayClassStatus(gc: any): StatusBadge {
  const conditions = gc.status?.conditions || []
  const accepted = conditions.find((c: any) => c.type === 'Accepted')
  if (accepted?.status === 'True') return { text: 'Accepted', color: healthColors.healthy, level: 'healthy' }
  if (accepted?.status === 'False') return { text: accepted.reason || 'Not Accepted', color: healthColors.unhealthy, level: 'unhealthy' }
  return { text: 'Pending', color: healthColors.degraded, level: 'degraded' }
}

export function getGatewayClassController(gc: any): string {
  return gc.spec?.controllerName || '-'
}

export function getGatewayClassDescription(gc: any): string {
  return gc.spec?.description || '-'
}

// ============================================================================
// GATEWAY API ROUTE UTILITIES (shared by HTTPRoute, GRPCRoute, TCPRoute, TLSRoute)
// ============================================================================

// All Gateway API route types share the same status/parents/rules/hostnames structure
export function getRouteStatus(route: any): StatusBadge {
  const parents = route.status?.parents || []
  if (parents.length === 0) return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
  const allAccepted = parents.every((p: any) =>
    (p.conditions || []).some((c: any) => c.type === 'Accepted' && c.status === 'True')
  )
  const anyRejected = parents.some((p: any) =>
    (p.conditions || []).some((c: any) => c.type === 'Accepted' && c.status === 'False')
  )
  if (allAccepted) return { text: 'Accepted', color: healthColors.healthy, level: 'healthy' }
  if (anyRejected) return { text: 'Not Accepted', color: healthColors.unhealthy, level: 'unhealthy' }
  return { text: 'Pending', color: healthColors.degraded, level: 'degraded' }
}

export function getRouteParents(route: any): string {
  const refs = route.spec?.parentRefs || []
  return refs.map((r: any) => r.name).join(', ') || '-'
}

export function getRouteHostnames(route: any): string {
  const hostnames = route.spec?.hostnames || []
  return hostnames.join(', ') || 'Any'
}

export function getRouteRulesCount(route: any): number {
  return (route.spec?.rules || []).length
}

export function getRouteBackends(route: any): string {
  const rules = route.spec?.rules || []
  // Collect unique backend name:port pairs across all rules
  const seen = new Set<string>()
  for (const rule of rules) {
    for (const ref of rule.backendRefs || []) {
      const key = ref.port ? `${ref.name}:${ref.port}` : ref.name
      seen.add(key)
    }
  }
  if (seen.size === 0) return '-'
  return Array.from(seen).join(', ')
}

// Legacy aliases for HTTPRoute (used by HTTPRouteRenderer)
export const getHTTPRouteStatus = getRouteStatus
export const getHTTPRouteParents = getRouteParents
export const getHTTPRouteHostnames = getRouteHostnames
export const getHTTPRouteRulesCount = getRouteRulesCount

// ============================================================================
// SEALED SECRET UTILITIES (Bitnami)
// ============================================================================

export function getSealedSecretStatus(ss: any): StatusBadge {
  const conditions = ss.status?.conditions || []
  const synced = conditions.find((c: any) => c.type === 'Synced')
  if (synced?.status === 'True') return { text: 'Synced', color: healthColors.healthy, level: 'healthy' }
  if (synced?.status === 'False') return { text: 'Not Synced', color: healthColors.unhealthy, level: 'unhealthy' }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getSealedSecretKeyCount(ss: any): number {
  return Object.keys(ss.spec?.encryptedData || {}).length
}

// ============================================================================
// WORKFLOW TEMPLATE UTILITIES (Argo)
// ============================================================================

export function getWorkflowTemplateCount(wt: any): number {
  return (wt.spec?.templates || []).length
}

export function getWorkflowTemplateEntrypoint(wt: any): string {
  return wt.spec?.entrypoint || '-'
}

// ============================================================================
// NETWORK POLICY UTILITIES
// ============================================================================

export function getNetworkPolicyTypes(np: any): string {
  const types = np.spec?.policyTypes || []
  return types.join(', ') || '-'
}

export function getNetworkPolicyRuleCount(np: any): { ingress: number; egress: number } {
  return {
    ingress: (np.spec?.ingress || []).length,
    egress: (np.spec?.egress || []).length,
  }
}

export function getNetworkPolicySelector(np: any): string {
  const labels = np.spec?.podSelector?.matchLabels
  if (!labels || Object.keys(labels).length === 0) return 'All pods'
  return Object.entries(labels).map(([k, v]) => `${k}=${v}`).join(', ')
}

// ============================================================================
// POD DISRUPTION BUDGET UTILITIES
// ============================================================================

export function getPDBStatus(pdb: any): StatusBadge {
  const status = pdb.status || {}
  const allowed = status.disruptionsAllowed
  const healthy = status.currentHealthy || 0
  const desired = status.desiredHealthy || 0

  if (healthy < desired) return { text: 'Unhealthy', color: healthColors.unhealthy, level: 'unhealthy' }
  if (allowed === 0 && (status.expectedPods || 0) > 0) return { text: 'Blocked', color: healthColors.degraded, level: 'degraded' }
  if (allowed > 0) return { text: 'OK', color: healthColors.healthy, level: 'healthy' }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getPDBBudget(pdb: any): string {
  const spec = pdb.spec || {}
  if (spec.minAvailable !== undefined) return `min: ${spec.minAvailable}`
  if (spec.maxUnavailable !== undefined) return `max unavail: ${spec.maxUnavailable}`
  return '-'
}

export function getPDBHealthy(pdb: any): string {
  const status = pdb.status || {}
  return `${status.currentHealthy || 0}/${status.expectedPods || 0}`
}

export function getPDBAllowed(pdb: any): number {
  return pdb.status?.disruptionsAllowed ?? 0
}

// ============================================================================
// SERVICE ACCOUNT UTILITIES
// ============================================================================

export function getServiceAccountSecretCount(sa: any): number {
  return (sa.secrets || []).length
}

export function getServiceAccountAutomount(sa: any): string {
  return sa.automountServiceAccountToken === false ? 'No' : 'Yes'
}

// ============================================================================
// ROLE / CLUSTER ROLE UTILITIES
// ============================================================================

export function getRoleRuleCount(role: any): number {
  return (role.rules || []).length
}

// ============================================================================
// ROLE BINDING / CLUSTER ROLE BINDING UTILITIES
// ============================================================================

export function getRoleBindingRole(rb: any): string {
  const ref = rb.roleRef
  if (!ref) return '-'
  return ref.name || '-'
}

export function getRoleBindingSubjectCount(rb: any): number {
  return (rb.subjects || []).length
}

// ============================================================================
// WORKLOAD PROBLEM DETECTION (for table row indicators)
// ============================================================================


// ============================================================================
// FORMATTING UTILITIES
// ============================================================================

export function truncate(str: string, length: number): string {
  if (!str || str.length <= length) return str
  return str.slice(0, length - 1) + '…'
}

export function formatResources(resources: any): string {
  const parts: string[] = []
  if (resources.cpu) {
    parts.push(`CPU: ${formatCPUString(resources.cpu)}`)
  }
  if (resources.memory) {
    parts.push(`Mem: ${formatMemoryString(resources.memory)}`)
  }
  for (const [key, value] of Object.entries(resources)) {
    if (key === 'cpu' || key === 'memory') continue
    parts.push(`${key}: ${value}`)
  }
  return parts.join(', ') || '-'
}

// ============================================================================
// GENERIC COLUMN FILTER VALUE EXTRACTION
// ============================================================================

/**
 * Extracts a string filter value for a given column key and resource kind.
 * Used by the generic column filter system to match resources against filter values.
 * Reuses existing utility functions for kind-specific columns.
 */
// Parse column filters from URL `filters` param (format: "col:val1,val2|col2:val3")
// Uses `|` as pair separator between columns, `,` between values within a column.
// Multi-select: each column key maps to an array of selected values.
export function parseColumnFilters(filtersParam: string | null): Record<string, string[]> {
  if (!filtersParam) return {}
  const filters: Record<string, string[]> = {}
  for (const pair of filtersParam.split('|')) {
    const colonIdx = pair.indexOf(':')
    if (colonIdx > 0) {
      const rawKey = pair.slice(0, colonIdx).trim()
      const valStr = pair.slice(colonIdx + 1).trim()
      // Keys are URI-encoded so a custom-column key's own colon (e.g.
      // "label:tier") doesn't collide with the key:value delimiter.
      let key: string
      try { key = decodeURIComponent(rawKey) } catch { key = rawKey }
      if (key && valStr) {
        filters[key] = valStr.split(',').map(v => {
          try { return decodeURIComponent(v.trim()) } catch { return v.trim() }
        }).filter(Boolean)
      }
    }
  }
  return filters
}

// Serialize column filters to URL param format. Keys and values are both
// URI-encoded so a colon inside a custom-column key (e.g. "label:tier") or a
// comma inside a value (e.g. "Ready,SchedulingDisabled") survives the round-trip.
export function serializeColumnFilters(filters: Record<string, string[]>): string {
  const result = Object.entries(filters)
    .filter(([, v]) => v.length > 0)
    .map(([k, vals]) => `${encodeURIComponent(k)}:${vals.map(v => encodeURIComponent(v)).join(',')}`)
    .join('|')
  return result
}

export function getCellFilterValue(resource: any, column: string, kind: string): string {
  try {
  const kindLower = kind.toLowerCase()

  switch (column) {
    case 'namespace':
      return resource.metadata?.namespace || ''
    case 'type':
      if (kindLower === 'secrets' || kindLower === 'sealedsecrets') return getSecretType(resource).type
      if (kindLower === 'services') return resource.spec?.type || ''
      if (kindLower === 'events') return resource.type || ''
      if (kindLower === 'helmrepositories') return getHelmRepositoryType(resource)
      return resource.spec?.type || resource.type || ''
    case 'status':
      if (kindLower === 'pods') return getPodStatus(resource).text
      if (['deployments', 'statefulsets', 'daemonsets', 'replicasets'].includes(kindLower)) {
        const status = getWorkloadStatus(resource, kindLower)
        if (status.text === 'Scaled to 0') return 'Scaled to 0'
        if (status.level === 'healthy') return 'Healthy'
        if (status.level === 'degraded') return 'Degraded'
        if (status.level === 'unhealthy') return 'Unhealthy'
        return 'Unknown'
      }
      if (kindLower === 'nodes') return getNodeStatus(resource).text
      if (kindLower === 'jobs') return getJobStatus(resource).text
      if (kindLower === 'cronjobs') return getCronJobStatus(resource).text
      if (kindLower === 'certificates') return getCertificateStatus(resource).text
      if (kindLower === 'certificaterequests') return getCertificateRequestStatus(resource).text
      if (kindLower === 'clusterissuers' || kindLower === 'issuers') return getClusterIssuerStatus(resource).text
      if (kindLower === 'persistentvolumeclaims') return getPVCStatus(resource).text
      if (kindLower === 'persistentvolumes') return getPVStatus(resource).text
      if (kindLower === 'rollouts') return getRolloutStatus(resource).text
      if (kindLower === 'workflows') return getWorkflowStatus(resource).text
      if (kindLower === 'cronworkflows') return getCronWorkflowStatus(resource).text
      if (kindLower === 'hpas' || kindLower === 'horizontalpodautoscalers') return getHPAStatus(resource).text
      if (kindLower === 'gateways') return getGatewayStatus(resource).text
      if (kindLower === 'gatewayclasses') return getGatewayClassStatus(resource).text
      if (['httproutes', 'grpcroutes', 'tcproutes', 'tlsroutes'].includes(kindLower)) return getRouteStatus(resource).text
      if (kindLower === 'sealedsecrets') return getSealedSecretStatus(resource).text
      if (kindLower === 'poddisruptionbudgets') return getPDBStatus(resource).text
      if (kindLower === 'applications') return getArgoApplicationStatus(resource).text
      if (kindLower === 'applicationsets') return getArgoApplicationSetStatus(resource).text
      if (kindLower === 'gitrepositories') return getGitRepositoryStatus(resource).text
      if (kindLower === 'ocirepositories') return getOCIRepositoryStatus(resource).text
      if (kindLower === 'helmrepositories') return getHelmRepositoryStatus(resource).text
      if (kindLower === 'kustomizations') return getKustomizationStatus(resource).text
      if (kindLower === 'helmreleases') return getFluxHelmReleaseStatus(resource).text
      if (kindLower === 'alerts') return getFluxAlertStatus(resource).text
      if (kindLower === 'nodepools') return getNodePoolStatus(resource).text
      if (kindLower === 'nodeclaims') return getNodeClaimStatus(resource).text
      if (kindLower === 'scaledobjects') return getScaledObjectStatus(resource).text
      if (kindLower === 'scaledjobs') return getScaledJobStatus(resource).text
      if (kindLower === 'policyreports' || kindLower === 'clusterpolicyreports') return _getPolicyReportStatus(resource).text
      if (kindLower === 'kyvernopolicies' || kindLower === 'clusterpolicies') return _getKyvernoPolicyStatus(resource).text
      if (kindLower === 'nvidiaclusterpolicies') return _getNvidiaClusterPolicyStatus(resource).text
      if (kindLower === 'nvidiadrivers') return _getNvidiaDriverStatus(resource).text
      if (kindLower === 'resourceclaims') return _getResourceClaimStatus(resource).text
      if (kindLower === 'backups') return _getBackupStatus(resource).text
      if (kindLower === 'restores') return _getRestoreStatus(resource).text
      if (kindLower === 'schedules') return _getScheduleStatus(resource).text
      if (kindLower === 'backupstoragelocations') return _getBSLStatus(resource).text
      if (kindLower === 'externalsecrets') return _getExternalSecretStatus(resource).text
      if (kindLower === 'clusterexternalsecrets') return _getClusterExternalSecretStatus(resource).text
      if (kindLower === 'secretstores') return _getSecretStoreStatus(resource).text
      if (kindLower === 'clustersecretstores') return _getClusterSecretStoreStatus(resource).text
      // Generic CRDs: try status.phase, then Ready condition
      if (resource.status?.phase) return resource.status.phase
      {
        const conditions = resource.status?.conditions || []
        const ready = conditions.find((c: any) => c.type === 'Ready')
        if (ready?.status === 'True') return 'Ready'
        if (ready?.status === 'False') return 'Not Ready'
      }
      return ''
    case 'state':
      if (kindLower === 'orders') return getOrderState(resource).text
      if (kindLower === 'challenges') return getChallengeState(resource).text
      return resource.status?.state || ''
    case 'challengeType':
      return getChallengeType(resource)
    case 'issuerType':
      return getClusterIssuerType(resource)
    case 'class':
      return resource.spec?.ingressClassName || resource.spec?.gatewayClassName || ''
    case 'roles':
      return getNodeRoles(resource)
    case 'strategy':
      return getRolloutStrategy(resource)
    case 'storageClass':
      return resource.spec?.storageClassName || ''
    case 'reclaimPolicy':
      return resource.reclaimPolicy || resource.spec?.persistentVolumeReclaimPolicy || ''
    case 'bindingMode':
      return getStorageClassBindingMode(resource)
    case 'expansion':
      return getStorageClassExpansion(resource)
    case 'automount':
      return getServiceAccountAutomount(resource)
    case 'policyTypes':
      return getNetworkPolicyTypes(resource)
    case 'node':
      return resource.spec?.nodeName || ''
    case 'version':
      if (kindLower === 'nodes') return getNodeVersion(resource)
      return resource.spec?.version || ''
    case 'health':
      if (kindLower === 'applications') return getArgoApplicationHealth(resource).status
      return resource.status?.health?.status || ''
    case 'sync':
      if (kindLower === 'applications') return getArgoApplicationSync(resource).status
      return resource.status?.sync?.status || ''
    case 'project':
      if (kindLower === 'applications') return getArgoApplicationProject(resource)
      return resource.spec?.project || ''
    case 'provider':
      if (kindLower === 'secretstores' || kindLower === 'clustersecretstores') return _getSecretStoreProviderType(resource)
      return resource.spec?.provider || ''
    // DRA + NVIDIA columns. Unmatched kinds break to the generic fallback —
    // these keys are shared (e.g. 'components' is also a Trivy SBOM column).
    case 'deviceClass':
      if (kindLower === 'resourceclaims') return _getResourceClaimDeviceClasses(resource).join(', ')
      if (kindLower === 'resourceclaimtemplates') return _getResourceClaimTemplateDeviceClasses(resource).join(', ')
      break
    case 'allocated':
      if (kindLower === 'resourceclaims') return _getResourceClaimAllocation(resource).map(r => r.driver).join(', ')
      break
    case 'reservedFor':
      if (kindLower === 'resourceclaims') return _getResourceClaimReservedFor(resource).map(r => r.name).join(', ')
      break
    case 'pool':
      if (kindLower === 'resourceslices') return resource.spec?.pool?.name || ''
      break
    case 'devices':
      if (kindLower === 'resourceslices') return String((resource.spec?.devices || []).length)
      break
    case 'selectors':
      if (kindLower === 'deviceclasses') return String((resource.spec?.selectors || []).length)
      break
    case 'components':
      if (kindLower === 'nvidiaclusterpolicies') return _getNvidiaClusterPolicyEnabledComponents(resource).filter(c => c.enabled).map(c => c.label).join(', ')
      break
    case 'mig':
      if (kindLower === 'nvidiaclusterpolicies') return resource.spec?.mig?.strategy || ''
      break
  }

  // Fallback: try common paths
  const val = resource.status?.[column] ?? resource.spec?.[column] ?? resource[column]
  if (val === undefined || val === null) return ''
  if (typeof val === 'boolean') return val ? 'Yes' : 'No'
  if (typeof val === 'string') return val
  if (typeof val === 'number') return String(val)
  if (typeof val === 'object') return '' // Skip objects/arrays — not filterable
  return String(val)
  } catch (err) {
    console.warn(`[getCellFilterValue] Failed for kind=${kind} column=${column}:`, err)
    return ''
  }
}

// ============================================================================
// TRIVY OPERATOR UTILITIES — re-exported from resource-utils-trivy.ts
// ============================================================================
export {
  getVulnerabilityReportSummary,
  getVulnerabilityReportStatus,
  getVulnerabilityReportImage,
  getVulnerabilityReportContainer,
  getConfigAuditReportSummary,
  getConfigAuditReportStatus,
  getExposedSecretReportSummary,
  getExposedSecretReportContainer,
  getExposedSecretReportImage,
  getExposedSecretReportStatus,
  getRbacAssessmentReportSummary,
  getRbacAssessmentReportStatus,
  getClusterComplianceReportStatus,
  getSbomReportStatus,
  getSbomReportContainer,
} from './resource-utils-trivy'
export type { VulnerabilitySummary, ConfigAuditSummary } from './resource-utils-trivy'

// ============================================================================
// KARPENTER UTILITIES — re-exported from resource-utils-karpenter.ts
// ============================================================================
export {
  getNodePoolStatus,
  getNodePoolNodeClassRef,
  getNodePoolLimits,
  getNodePoolDisruptionPolicy,
  getNodePoolRequirements,
  getNodePoolWeight,
  getNodeClaimStatus,
  getNodeClaimInstanceType,
  getNodeClaimNodeName,
  getNodeClaimCapacity,
  getNodeClaimNodePoolRef,
} from './resource-utils-karpenter'

// ============================================================================
// KEDA UTILITIES — re-exported from resource-utils-keda.ts
// ============================================================================
export {
  getScaledObjectStatus,
  getScaledObjectTarget,
  getScaledObjectTargetKind,
  getScaledObjectTargetName,
  getScaledObjectReplicas,
  getScaledObjectTriggers,
  getScaledObjectTriggerCount,
  getScaledObjectHpaName,
  getScaledObjectLastActiveTime,
  getScaledObjectPollingInterval,
  getScaledObjectCooldownPeriod,
  getScaledJobStatus,
  getScaledJobTarget,
  getScaledJobStrategy,
  getScaledJobTriggerCount,
  getScaledJobTriggers,
} from './resource-utils-keda'

// ============================================================================
// KYVERNO / POLICY REPORT UTILITIES — re-exported from resource-utils-kyverno.ts
// ============================================================================
export {
  getPolicyReportStatus,
  getPolicyReportSummary,
  getPolicyReportResults,
  getPolicyReportResultCount,
  getPolicyReportScope,
  getPolicyReportSource,
  getClusterPolicyReportStatus,
  getKyvernoPolicyStatus,
  getKyvernoPolicyAction,
  getKyvernoPolicyRuleCount,
  getKyvernoPolicyRuleTypes,
  getKyvernoPolicyRules,
  getKyvernoPolicyBackground,
  getKyvernoPolicyRuleCountByType,
  getClusterPolicyStatus,
  getClusterPolicyAction,
  getClusterPolicyRuleCount,
} from './resource-utils-kyverno'

// ============================================================================
// EXTERNAL SECRETS OPERATOR UTILITIES — re-exported from resource-utils-eso.ts
// ============================================================================
export {
  getExternalSecretStatus,
  getExternalSecretStore,
  getExternalSecretRefreshInterval,
  getExternalSecretSecretCount,
  getExternalSecretLastSync,
  getExternalSecretTargetName,
  getExternalSecretProvider,
  getClusterExternalSecretStatus,
  getClusterExternalSecretNamespaceCount,
  getClusterExternalSecretFailedCount,
  getSecretStoreStatus,
  getSecretStoreProviderType,
  getSecretStoreProviderKey,
  getClusterSecretStoreStatus,
} from './resource-utils-eso'
