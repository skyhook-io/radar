import { describe, expect, it } from 'vitest'
import {
  canBulkRestartKind,
  canBulkScaleKind,
  canPatchWorkloadKind,
  canPauseDeployment,
  canSetWorkloadImages,
  intersectWorkloadWrites,
  isPausedDeployment,
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

  it('shares exact group and kind checks with detail patch actions', () => {
    const all = writes({ deployments: true, daemonSets: true, statefulSets: true, rollouts: true })

    expect(canPatchWorkloadKind({ name: 'deployments', group: 'apps' }, all)).toBe(true)
    expect(canPatchWorkloadKind({ name: 'rollouts', group: 'argoproj.io' }, all)).toBe(true)
    expect(canPatchWorkloadKind({ name: 'deployments', group: 'example.com' }, all)).toBe(false)
    expect(canPatchWorkloadKind({ name: 'services', group: '' }, all)).toBe(false)
    expect(canPatchWorkloadKind({ name: 'deployments', group: 'apps' }, undefined)).toBe(false)
  })

  it('requires an explicit server advertisement for image updates', () => {
    const all = writes({ deployments: true })

    expect(canSetWorkloadImages({ name: 'deployments', group: 'apps' }, all, true)).toBe(true)
    expect(canSetWorkloadImages({ name: 'deployments', group: 'apps' }, all, false)).toBe(false)
    expect(canSetWorkloadImages({ name: 'deployments', group: 'apps' }, undefined, true)).toBe(false)
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

describe('Deployment pause', () => {
  const canPatch = writes({ deployments: true, statefulSets: true, daemonSets: true, rollouts: true })

  it('needs the feature, patch permission and a Deployment in the apps group', () => {
    expect(canPauseDeployment({ name: 'deployments', group: 'apps' }, canPatch, true)).toBe(true)
    expect(canPauseDeployment({ name: 'Deployments', group: 'apps' }, canPatch, true)).toBe(true)
    expect(canPauseDeployment({ name: 'deployments', group: 'apps' }, canPatch, false)).toBe(false)
    expect(canPauseDeployment({ name: 'deployments', group: 'apps' }, writes(), true)).toBe(false)
    expect(canPauseDeployment({ name: 'deployments', group: 'apps' }, undefined, true)).toBe(false)
    expect(canPauseDeployment({ name: 'deployments', group: 'vendor.io' }, canPatch, true)).toBe(false)
    expect(canPauseDeployment({ name: 'statefulsets', group: 'apps' }, canPatch, true)).toBe(false)
    expect(canPauseDeployment({ name: 'daemonsets', group: 'apps' }, canPatch, true)).toBe(false)
    expect(canPauseDeployment({ name: 'rollouts', group: 'argoproj.io' }, canPatch, true)).toBe(false)
  })

  it('reports paused only for a built-in Deployment with spec.paused true', () => {
    const paused = { apiVersion: 'apps/v1', kind: 'Deployment', spec: { paused: true } }

    expect(isPausedDeployment(paused)).toBe(true)
    expect(isPausedDeployment({ ...paused, spec: { paused: false } })).toBe(false)
    expect(isPausedDeployment({ ...paused, spec: {} })).toBe(false)
    expect(isPausedDeployment({ ...paused, spec: { paused: 'true' } })).toBe(false)
    expect(isPausedDeployment({ ...paused, apiVersion: 'vendor.io/v1' })).toBe(false)
    expect(isPausedDeployment({ ...paused, kind: 'Rollout', apiVersion: 'argoproj.io/v1alpha1' })).toBe(false)
    expect(isPausedDeployment(undefined)).toBe(false)
  })
})
