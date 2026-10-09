import { registerCNPGOperationObserver, summarizeSteps, type CNPGObservation, type CNPGObserverResult, type CNPGOpStep, type CNPGTrackedOperation } from '../operations/model'

const HEALTHY_PHASE = 'Cluster in healthy state'
const FAILING_PHASE = /unrecoverable|unable to|cannot|error|failed/i
const RECOVERY_JOB_ROLES = new Set(['full-recovery', 'snapshot-recovery'])

/**
 * The tracker's view of a restore, from what the operation tracker already
 * reads (the Cluster and its Jobs). The Cluster page's restore panel adds the
 * recovery Pods and their init containers from /recovery.
 */
export function restoreOperationObserver(op: CNPGTrackedOperation, obs: CNPGObservation): CNPGObserverResult {
  const c = obs.cluster
  if (!c) {
    return obs.now - op.startedAt < 60_000
      ? { state: 'requested', detail: `Waiting for Cluster ${op.cluster} to appear`, progressKey: 'absent' }
      : { state: 'unobservable', detail: `Cluster ${op.cluster} is not visible with your access` }
  }
  const phase: string | undefined = c.status?.phase
  const desired: number | undefined = c.spec?.instances
  const ready: number | undefined = c.status?.readyInstances
  const jobs = obs.ha?.jobs.state === 'ok' ? obs.ha.jobs.items.filter((j) => (j.role ? RECOVERY_JOB_ROLES.has(j.role) : j.name.includes('recovery'))) : undefined
  const failed = jobs?.find((j) => j.phase === 'failed')
  if (failed) return { state: 'failed', detail: `Recovery Job ${failed.name} failed${failed.reason ? `: ${failed.reason}` : ''}` }
  if (phase && phase !== HEALTHY_PHASE && FAILING_PHASE.test(phase)) {
    return { state: 'failed', detail: phase + (c.status?.phaseReason ? `: ${c.status.phaseReason}` : '') }
  }
  // A ready restored primary proves recovery finished, whether or not the
  // caller can read the recovery Job.
  const primaryReady = typeof ready === 'number' && ready > 0
  const jobDone = primaryReady ? true : jobs === undefined ? null : jobs.some((j) => j.phase === 'succeeded')
  const steps: CNPGOpStep[] = [
    { label: 'Base backup restored and WAL replayed (recovery Job finished)', done: jobDone },
    { label: 'Restored primary ready', done: typeof ready === 'number' ? ready > 0 : false },
    {
      label: `All ${desired ?? ''} instances ready and the Cluster healthy`.replace('  ', ' '),
      done: typeof ready === 'number' && typeof desired === 'number' ? ready >= desired && phase === HEALTHY_PHASE : false,
    },
  ]
  const state = summarizeSteps(steps)
  return {
    state: state === 'unobservable' ? 'progressing' : state,
    steps,
    detail: [phase, typeof ready === 'number' ? `${ready}/${desired ?? '?'} ready` : null].filter(Boolean).join(' · '),
    progressKey: `${phase ?? ''}|${ready ?? ''}|${jobs?.map((j) => j.phase).join(',') ?? '?'}`,
  }
}

function registerRestoreObserver(): 'restore' {
  registerCNPGOperationObserver('restore', restoreOperationObserver)
  return 'restore'
}

// Registered through a used export, not a bare side-effect import: the package
// declares itself side-effect free, so a bare import would be dropped.
export const CNPG_RESTORE_OPERATION = registerRestoreObserver()
