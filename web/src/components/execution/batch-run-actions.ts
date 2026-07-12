import type { WorkloadRun } from '../../api/client'

export type BatchRunNextStep = 'logs' | 'timeline'

export function isFailedRunPhase(phase: string): boolean {
  return phase === 'Failed' || phase === 'Error'
}

export function batchRunHasContainerOutcome(run: WorkloadRun): boolean {
  return (
    (run.podFailed ?? 0) > 0 ||
    (run.podSucceeded ?? 0) > 0 ||
    (run.podRunning ?? 0) > 0
  )
}

export function batchRunNextStep(run: WorkloadRun, canViewLogs: boolean, hasLivePods: boolean | undefined): BatchRunNextStep | null {
  if (!isFailedRunPhase(run.phase)) return null

  const hasContainerOutcome = batchRunHasContainerOutcome(run)
  if (canViewLogs && hasContainerOutcome && hasLivePods === undefined) return null

  return canViewLogs && hasContainerOutcome && hasLivePods ? 'logs' : 'timeline'
}
