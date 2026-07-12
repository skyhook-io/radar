import type { TimelineEvent } from '@skyhook-io/k8s-ui'
import type { WorkloadRun } from '../../api/client'

export function workloadRunTimelineEvents(runs: WorkloadRun[]): TimelineEvent[] {
  const events: TimelineEvent[] = []
  for (const run of runs) {
    const kind = run.kind === 'workflows' ? 'Workflow' : 'Job'
    const apiVersion = run.kind === 'workflows' ? 'argoproj.io/v1alpha1' : 'batch/v1'
    const prefix = `batch-run:${run.kind}:${run.namespace}:${run.name}`
    const createdAt = run.startedAt || run.scheduledAt
    if (run.scheduledAt) {
      events.push(runTimelineEvent(run, kind, apiVersion, `${prefix}:scheduled`, run.scheduledAt, 'Normal', `${kind} scheduled`, createdAt))
    }
    if (run.startedAt) {
      events.push(runTimelineEvent(run, kind, apiVersion, `${prefix}:started`, run.startedAt, 'Normal', `${kind} started`, createdAt))
    }
    if (run.finishedAt) {
      const failed = run.phase === 'Failed' || run.phase === 'Error'
      events.push(runTimelineEvent(run, kind, apiVersion, `${prefix}:finished`, run.finishedAt, failed ? 'Warning' : 'Normal', `${kind} ${runPhaseVerb(run.phase)}`, createdAt, run.message))
    }
  }
  return events.sort((a, b) => Date.parse(a.timestamp) - Date.parse(b.timestamp) || a.id.localeCompare(b.id))
}

function runTimelineEvent(run: WorkloadRun, kind: string, apiVersion: string, id: string, timestamp: string, eventType: 'Normal' | 'Warning', reason: string, createdAt?: string, message?: string): TimelineEvent {
  return {
    id,
    timestamp,
    source: 'historical',
    kind,
    apiVersion,
    namespace: run.namespace,
    name: run.name,
    createdAt,
    eventType,
    reason,
    message,
  }
}

function runPhaseVerb(phase: string): string {
  if (phase === 'Succeeded') return 'succeeded'
  if (phase === 'Failed') return 'failed'
  if (phase === 'Error') return 'errored'
  return phase.toLowerCase()
}
