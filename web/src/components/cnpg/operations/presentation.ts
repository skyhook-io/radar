import type { HealthLevel } from '@skyhook-io/k8s-ui'
import type { CNPGOpState, CNPGTrackedOperation } from './model'

export const CNPG_OP_STATE_TEXT: Record<CNPGOpState, string> = {
  requested: 'requested',
  observed: 'observed by the operator',
  progressing: 'in progress',
  completed: 'completed',
  failed: 'failed',
  stalled: 'stalled',
  superseded: 'superseded',
  unobservable: 'cannot be followed',
}

export const CNPG_OP_STATE_TONE: Record<CNPGOpState, HealthLevel> = {
  requested: 'neutral',
  observed: 'neutral',
  progressing: 'neutral',
  completed: 'healthy',
  failed: 'unhealthy',
  stalled: 'degraded',
  superseded: 'unknown',
  unobservable: 'unknown',
}

export const CNPG_OP_STATE_SEVERITY: Record<CNPGOpState, 'success' | 'error' | 'warning' | 'info' | 'neutral'> = {
  requested: 'info',
  observed: 'info',
  progressing: 'info',
  completed: 'success',
  failed: 'error',
  stalled: 'warning',
  superseded: 'neutral',
  unobservable: 'neutral',
}

export function cnpgOperationsForCluster(
  ops: CNPGTrackedOperation[],
  subject: { context: string; namespace: string; name: string; uid: string },
): CNPGTrackedOperation[] {
  return ops.filter(
    (op) =>
      op.context === subject.context &&
      op.namespace === subject.namespace &&
      op.cluster === subject.name &&
      (!op.clusterUID || op.clusterUID === subject.uid),
  )
}

export function latestCNPGOperation(
  ops: CNPGTrackedOperation[],
  subject: { context: string; namespace: string; name: string; uid: string },
): CNPGTrackedOperation | undefined {
  return cnpgOperationsForCluster(ops, subject).reduce<CNPGTrackedOperation | undefined>(
    (latest, op) => (!latest || op.startedAt > latest.startedAt ? op : latest),
    undefined,
  )
}

export function cnpgOperationHandoff(op: CNPGTrackedOperation, liveURL: string): string {
  return [
    `CloudNativePG: ${op.namespace}/${op.cluster}`,
    `Context: ${op.context}`,
    `Cluster UID: ${op.clusterUID || 'not recorded; confirm the live identity'}`,
    `Action: ${op.label}`,
    `Requested: ${new Date(op.startedAt).toISOString()}`,
    `State in this tab: ${CNPG_OP_STATE_TEXT[op.state]}`,
    `Last checked: ${op.lastCheckedAt ? new Date(op.lastCheckedAt).toISOString() : 'not yet checked'}`,
    ...(op.detail ? [`Observation: ${op.detail}`] : []),
    ...(op.steps?.map(
      (step) => `[${step.done === true ? 'observed' : step.done === false ? 'pending' : 'unknown'}] ${step.label}`,
    ) ?? []),
    `Live Cluster: ${liveURL}`,
    'This is a local observation record, not a shared operation history. Open the link to inspect current Kubernetes facts and verify the remaining steps.',
  ].join('\n')
}
