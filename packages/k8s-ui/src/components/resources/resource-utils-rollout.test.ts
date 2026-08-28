import { describe, expect, it } from 'vitest'
import { getCellFilterValue, getRolloutStep, getWorkloadDisplayStatus, getWorkloadStatus, workloadStatusLabel } from './resource-utils'

describe('getRolloutStep', () => {
  it('presents Argo currentStepIndex as a one-based step number', () => {
    expect(getRolloutStep({
      spec: { strategy: { canary: { steps: [{ setWeight: 25 }, { pause: {} }] } } },
      status: { currentStepIndex: 0 },
    })).toBe('1/2')
  })

  it('clamps Argo completed steps to the declared step count', () => {
    expect(getRolloutStep({
      spec: { strategy: { canary: { steps: [{ setWeight: 25 }, { pause: {} }] } } },
      status: { currentStepIndex: 2 },
    })).toBe('2/2')
  })
})

describe('getWorkloadStatus', () => {
  it('keeps a capacity-preserving surge healthy', () => {
    expect(getWorkloadStatus({
      spec: { replicas: 1 },
      status: { readyReplicas: 2, availableReplicas: 1, updatedReplicas: 1 },
    }, 'deployments')).toMatchObject({ text: '1/1', level: 'healthy' })
  })

  it('shows health instead of Stable for an idle under-replicated workload', () => {
    expect(getWorkloadDisplayStatus({
      metadata: { generation: 2 },
      spec: { replicas: 3 },
      status: {
        observedGeneration: 2,
        replicas: 2,
        updatedReplicas: 3,
        readyReplicas: 2,
        availableReplicas: 2,
      },
    }, 'deployments').status).toMatchObject({ text: '2/3', level: 'degraded' })
  })

  it('shows rollout activity while a revision is progressing', () => {
    expect(getWorkloadDisplayStatus({
      metadata: { generation: 2 },
      spec: { replicas: 3 },
      status: {
        observedGeneration: 2,
        replicas: 4,
        updatedReplicas: 2,
        readyReplicas: 3,
        availableReplicas: 3,
      },
    }, 'deployments').status).toMatchObject({ text: 'Rolling out', level: 'neutral' })
  })

  it('shows health instead of an inactive pause policy', () => {
    expect(getWorkloadDisplayStatus({
      metadata: { generation: 2 },
      spec: { replicas: 3, paused: true },
      status: {
        observedGeneration: 2,
        replicas: 3,
        updatedReplicas: 3,
        readyReplicas: 0,
        availableReplicas: 0,
      },
    }, 'deployments').status).toMatchObject({ text: '0/3', level: 'unhealthy' })
  })

  it('keeps active rollout copy while preserving a worse health tone', () => {
    expect(getWorkloadDisplayStatus({
      metadata: { generation: 2 },
      spec: { replicas: 3, paused: true },
      status: {
        observedGeneration: 2,
        replicas: 3,
        updatedReplicas: 0,
        readyReplicas: 0,
        availableReplicas: 0,
      },
    }, 'deployments').status).toMatchObject({ text: 'Unhealthy · Rollout paused', level: 'unhealthy' })
  })

  it('preserves unhealthy serving capacity while an Argo Rollout progresses', () => {
    const resource = {
      apiVersion: 'argoproj.io/v1alpha1',
      metadata: { generation: 2 },
      spec: { replicas: 3 },
      status: {
        observedGeneration: '2',
        phase: 'Progressing',
        updatedReplicas: 1,
        readyReplicas: 0,
        availableReplicas: 0,
      },
    }
    const display = getWorkloadDisplayStatus(resource, 'rollouts')

    expect(display.status).toMatchObject({ text: 'Degraded · Rolling out', level: 'degraded' })
    expect(getCellFilterValue(resource, 'status', 'rollouts')).toBe('Degraded · Rolling out')
  })

  it('keeps Argo generation lag informational when observedGeneration is a string', () => {
    const resource = {
      apiVersion: 'argoproj.io/v1alpha1',
      metadata: { generation: 3 },
      spec: { replicas: 3 },
      status: {
        observedGeneration: '2',
        phase: 'Healthy',
        updatedReplicas: 0,
        readyReplicas: 0,
        availableReplicas: 0,
      },
    }

    expect(getWorkloadDisplayStatus(resource, 'rollouts').status).toMatchObject({ text: 'Applying change', level: 'neutral' })
    expect(getCellFilterValue(resource, 'status', 'rollouts')).toBe('Applying change')
  })

  it('uses semantic health for an idle Argo Rollout', () => {
    const resource = {
      apiVersion: 'argoproj.io/v1alpha1',
      metadata: { generation: 2 },
      spec: { replicas: 3 },
      status: {
        observedGeneration: '2',
        phase: 'Healthy',
        updatedReplicas: 3,
        readyReplicas: 3,
        availableReplicas: 3,
      },
    }

    expect(getWorkloadDisplayStatus(resource, 'rollouts').status).toMatchObject({ text: '3/3', level: 'healthy' })
    expect(getCellFilterValue(resource, 'status', 'rollouts')).toBe('Healthy')
  })

  it('does not fabricate Argo rollout state for a colliding CRD', () => {
    const display = getWorkloadDisplayStatus({
      apiVersion: 'rollouts.kruise.io/v1alpha1',
      kind: 'Rollout',
      metadata: { generation: 1 },
      spec: { replicas: 3 },
      status: { phase: 'Progressing', readyReplicas: 3, availableReplicas: 3, updatedReplicas: 3 },
    }, 'rollouts')

    expect(display.status).toMatchObject({ text: 'Progressing', level: 'unknown' })
    expect(workloadStatusLabel(display.status)).toBe('Unknown')
  })

  it('shows health instead of a reached StatefulSet partition', () => {
    const resource = {
      metadata: { generation: 2 },
      spec: { replicas: 5, updateStrategy: { rollingUpdate: { partition: 2 } } },
      status: {
        observedGeneration: 2,
        updatedReplicas: 3,
        readyReplicas: 4,
        availableReplicas: 4,
      },
    }

    expect(getWorkloadDisplayStatus(resource, 'statefulsets').status).toMatchObject({ text: '4/5', level: 'degraded' })
    expect(getCellFilterValue(resource, 'status', 'statefulsets')).toBe('Degraded')
  })

  it('uses serving health as the filter value for an inactive pause policy', () => {
    const resource = {
      metadata: { generation: 2 },
      spec: { replicas: 3, paused: true },
      status: {
        observedGeneration: 2,
        replicas: 3,
        updatedReplicas: 3,
        readyReplicas: 0,
        availableReplicas: 0,
      },
    }

    expect(getCellFilterValue(resource, 'status', 'deployments')).toBe('Unhealthy')
  })
})
