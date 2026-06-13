import type { Capabilities, WorkloadWritePermissions } from '../types/core'

export type BulkWorkloadKindInfo = {
  name: string
  group?: string
}

export function canBulkRestartKind(
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
