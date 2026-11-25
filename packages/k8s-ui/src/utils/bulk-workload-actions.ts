import type { Capabilities, WorkloadWritePermissions } from '../types/core'

export type BulkWorkloadKindInfo = {
  name: string
  group?: string
}

export function canBulkRestartKind(
  kind: BulkWorkloadKindInfo | null | undefined,
  writes: WorkloadWritePermissions | undefined,
): boolean {
  return canPatchWorkloadKind(kind, writes)
}

export function canPatchWorkloadKind(
  kind: BulkWorkloadKindInfo | null | undefined,
  writes: WorkloadWritePermissions | undefined,
): boolean {
  switch (kind?.name.toLowerCase()) {
    case 'deployments':
      return kind.group === 'apps' && writes?.deployments === true
    case 'daemonsets':
      return kind.group === 'apps' && writes?.daemonSets === true
    case 'statefulsets':
      return kind.group === 'apps' && writes?.statefulSets === true
    case 'rollouts':
      return kind.group === 'argoproj.io' && writes?.rollouts === true
    default:
      return false
  }
}

export function canSetWorkloadImages(
  kind: BulkWorkloadKindInfo | null | undefined,
  writes: WorkloadWritePermissions | undefined,
  supported: boolean,
): boolean {
  return supported && canPatchWorkloadKind(kind, writes)
}

export function canPauseDeployment(
  kind: BulkWorkloadKindInfo | null | undefined,
  writes: WorkloadWritePermissions | undefined,
  supported: boolean,
): boolean {
  return supported && kind?.name.toLowerCase() === 'deployments' && canPatchWorkloadKind(kind, writes)
}

// Stop and Terminate patch spec.shutdown on an Argo Workflow. A Radar older
// than the stop endpoint sends no workflows permission, which reads as denied.
export function canStopWorkflow(
  kind: BulkWorkloadKindInfo | null | undefined,
  writes: WorkloadWritePermissions | undefined,
  supported: boolean,
): boolean {
  return supported && kind?.name.toLowerCase() === 'workflows' && kind.group === 'argoproj.io' && writes?.workflows === true
}

export function canSubmitWorkflowTemplate(
  kind: BulkWorkloadKindInfo | null | undefined,
  writes: WorkloadWritePermissions | undefined,
  supported: boolean,
): boolean {
  return supported && kind?.name.toLowerCase() === 'workflowtemplates' && kind.group === 'argoproj.io' && writes?.createWorkflows === true
}

export function isPausedDeployment(
  resource: { apiVersion?: string; kind?: string; spec?: { paused?: unknown } } | null | undefined,
): boolean {
  return resource?.kind === 'Deployment' && resource.apiVersion?.startsWith('apps/') === true && resource.spec?.paused === true
}

export function canBulkScaleKind(
  kind: BulkWorkloadKindInfo | null | undefined,
  writes: WorkloadWritePermissions | undefined,
): boolean {
  switch (kind?.name.toLowerCase()) {
    case 'deployments':
      return kind.group === 'apps' && writes?.deployments === true
    case 'statefulsets':
      return kind.group === 'apps' && writes?.statefulSets === true
    default:
      return false
  }
}

export function intersectWorkloadWrites(
  capabilities: Array<Pick<Capabilities, 'workloadWrites'>> | undefined,
): WorkloadWritePermissions | undefined {
  if (!capabilities || capabilities.length === 0) return undefined
  return {
    deployments: capabilities.every(c => c.workloadWrites?.deployments === true),
    daemonSets: capabilities.every(c => c.workloadWrites?.daemonSets === true),
    statefulSets: capabilities.every(c => c.workloadWrites?.statefulSets === true),
    rollouts: capabilities.every(c => c.workloadWrites?.rollouts === true),
  }
}
