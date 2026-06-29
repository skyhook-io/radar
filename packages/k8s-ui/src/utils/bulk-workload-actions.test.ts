import { describe, expect, it } from 'vitest'
import {
  canBulkRestartKind,
  canBulkScaleKind,
  intersectWorkloadWrites,
} from './bulk-workload-actions'
import type { Capabilities, WorkloadWritePermissions } from '../types/core'

const writes = (overrides: Partial<WorkloadWritePermissions> = {}): WorkloadWritePermissions => ({
  deployments: false,
  daemonSets: false,
  statefulSets: false,
  rollouts: false,
  ...overrides,
})

const caps = (workloadWrites: WorkloadWritePermissions): Pick<Capabilities, 'workloadWrites'> => ({
  workloadWrites,
})

describe('bulk workload action gating', () => {
  it('allows restart for patchable workload kinds', () => {
    const all = writes({ deployments: true, daemonSets: true, statefulSets: true, rollouts: true })

    expect(canBulkRestartKind({ name: 'deployments', group: 'apps' }, all)).toBe(true)
    expect(canBulkRestartKind({ name: 'daemonsets', group: 'apps' }, all)).toBe(true)
    expect(canBulkRestartKind({ name: 'statefulsets', group: 'apps' }, all)).toBe(true)
    expect(canBulkRestartKind({ name: 'rollouts', group: 'argoproj.io' }, all)).toBe(true)
  })

  it('requires the apps group for built-in workload restart', () => {
    const all = writes({ deployments: true, daemonSets: true, statefulSets: true })

    expect(canBulkRestartKind({ name: 'deployments', group: 'apps' }, all)).toBe(true)
    expect(canBulkRestartKind({ name: 'deployments', group: 'example.com' }, all)).toBe(false)
    expect(canBulkRestartKind({ name: 'deployments' }, all)).toBe(false)
    expect(canBulkRestartKind({ name: 'daemonsets', group: 'example.com' }, all)).toBe(false)
    expect(canBulkRestartKind({ name: 'statefulsets', group: 'example.com' }, all)).toBe(false)
  })

  it('requires the Argo group for rollout restart', () => {
    const all = writes({ rollouts: true })

    expect(canBulkRestartKind({ name: 'rollouts', group: 'argoproj.io' }, all)).toBe(true)
    expect(canBulkRestartKind({ name: 'rollouts', group: 'example.com' }, all)).toBe(false)
    expect(canBulkRestartKind({ name: 'rollouts' }, all)).toBe(false)
  })

  it('allows scale only for deployments and statefulsets', () => {
    const all = writes({ deployments: true, daemonSets: true, statefulSets: true, rollouts: true })

    expect(canBulkScaleKind({ name: 'deployments', group: 'apps' }, all)).toBe(true)
    expect(canBulkScaleKind({ name: 'statefulsets', group: 'apps' }, all)).toBe(true)
    expect(canBulkScaleKind({ name: 'daemonsets', group: 'apps' }, all)).toBe(false)
    expect(canBulkScaleKind({ name: 'rollouts', group: 'argoproj.io' }, all)).toBe(false)
    expect(canBulkScaleKind({ name: 'deployments', group: 'example.com' }, all)).toBe(false)
    expect(canBulkScaleKind({ name: 'deployments' }, all)).toBe(false)
  })

  it('withholds actions when permissions are absent', () => {
    expect(canBulkRestartKind({ name: 'deployments', group: 'apps' }, undefined)).toBe(false)
    expect(canBulkScaleKind({ name: 'deployments', group: 'apps' }, undefined)).toBe(false)
    expect(intersectWorkloadWrites(undefined)).toBeUndefined()
    expect(intersectWorkloadWrites([])).toBeUndefined()
  })

  it('intersects multi-namespace permissions with AND semantics', () => {
    expect(intersectWorkloadWrites([
      caps(writes({ deployments: true, daemonSets: true, statefulSets: true, rollouts: true })),
      caps(writes({ deployments: true, statefulSets: true })),
    ])).toEqual({
      deployments: true,
      daemonSets: false,
      statefulSets: true,
      rollouts: false,
    })
  })
})
