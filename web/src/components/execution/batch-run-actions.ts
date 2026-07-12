import type { WorkloadRun } from '../../api/client'

export type BatchRunNextStep = 'logs' | 'timeline'

export function isFailedRunPhase(phase: string): boolean {
  return phase === 'Failed' || phase === 'Error'
}

export function batchRunNextStep(run: WorkloadRun, canViewLogs: boolean): BatchRunNextStep | null {
  if (!isFailedRunPhase(run.phase)) return null

  const hasContainerOutcome =
    (run.podFailed ?? 0) > 0 ||
    (run.podSucceeded ?? 0) > 0 ||
    (run.podRunning ?? 0) > 0

  return canViewLogs && hasContainerOutcome ? 'logs' : 'timeline'
}
