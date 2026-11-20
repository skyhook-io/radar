import type { WorkloadRun } from '../../api/client'

// A Job being deleted whose Pods are still terminating has no active Pods, so
// its derived phase falls back to Pending, which reads as "not started yet".
export function runStateLabel(run: Pick<WorkloadRun, 'phase' | 'deleting'>, formatPhase: (phase: string) => string = phase => phase): string {
  if (run.deleting && run.phase === 'Pending') return 'Deleting'
  return `${formatPhase(run.phase)}${run.deleting ? ' · deleting' : ''}`
}
