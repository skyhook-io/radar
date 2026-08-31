import { describe, expect, it } from 'vitest'
import {
  getClusterQueueCohort,
  getKueueWorkloadPriority,
  getKueueWorkloadStatus,
} from './resource-utils-kueue'
import { getRayClusterStatus, getRayJobStatus, getRayServiceStatus } from './resource-utils-ray'
import {
  getInferenceServiceStatus,
  getLLMInferenceServiceReplicas,
  getServingRuntimeStatus,
} from './resource-utils-kserve'
import {
  getInferenceObjectivePriority,
  getInferenceObjectiveStatus,
  getInferencePoolStatus,
} from './resource-utils-inference-gateway'
import { getJobSetStatus, getLeaderWorkerSetStatus } from './resource-utils-jobset-lws'
import { getVolcanoJobStatus, getVolcanoPodGroupStatus } from './resource-utils-volcano'
import { getKaiPodGroupStatus, getKaiQueuePriority, getKaiQueueQuota, getKaiQueueStatus } from './resource-utils-kai'
import { getKaitoWorkspaceStatus, getRAGEngineStatus } from './resource-utils-kaito'
import { getNIMCacheStatus, getNIMServiceReplicas } from './resource-utils-nim'
import { getAMDDeviceConfigStatus } from './resource-utils-amd-gpu'
import { getTrainingJobReplicas, getTrainJobStatus, trainingJobStatus } from './resource-utils-kubeflow-training'

describe('GPU ecosystem API contracts', () => {
  it('reads current Kueue v1beta2 fields and condition precedence', () => {
    const workload = {
      spec: { priorityClassRef: { name: 'critical' } },
      status: { conditions: [
        { type: 'Admitted', status: 'True' },
        { type: 'Evicted', status: 'True' },
      ] },
    }
    expect(getClusterQueueCohort({ spec: { cohortName: 'gpu' } })).toBe('gpu')
    expect(getKueueWorkloadPriority(workload)).toBe('critical')
    expect(getKueueWorkloadStatus(workload).text).toBe('Evicted')
  })

  it('distinguishes Kueue terminal failures from successful or replaced workloads', () => {
    for (const reason of ['Failed', 'FailedToStart', 'OutOfSync', 'OwnerNotFound']) {
      expect(getKueueWorkloadStatus({
        status: { conditions: [{ type: 'Finished', status: 'True', reason }] },
      })).toMatchObject({ text: reason, level: 'unhealthy' })
    }

    for (const reason of ['Succeeded', 'WorkloadSliceReplaced']) {
      expect(getKueueWorkloadStatus({
        status: { conditions: [
          { type: 'Finished', status: 'True', reason },
          { type: 'Evicted', status: 'True', reason: 'FlavorMigration' },
        ] },
      })).toMatchObject({ text: 'Finished', level: 'neutral' })
    }
  })

  it('lets KubeRay conditions outrank deprecated state fields', () => {
    expect(getRayClusterStatus({
      status: {
        state: 'ready',
        conditions: [{ type: 'HeadPodReady', status: 'False', reason: 'HeadUnavailable' }],
      },
    }).text).toBe('HeadUnavailable')
    expect(getRayServiceStatus({
      status: {
        serviceStatus: 'Running',
        conditions: [{ type: 'Ready', status: 'False', reason: 'ServeNotReady' }],
      },
    }).text).toBe('ServeNotReady')
    expect(getRayServiceStatus({
      spec: { suspend: true },
      status: { conditions: [{ type: 'Suspending', status: 'True' }] },
    }).text).toBe('Suspending')
  })

  it('requires both the KubeRay head and workers before reporting Ready', () => {
    expect(getRayClusterStatus({
      status: {
        conditions: [{ type: 'HeadPodReady', status: 'True' }],
        availableWorkerReplicas: 1,
        desiredWorkerReplicas: 2,
      },
    }).text).toBe('Workers 1/2')
    expect(getRayClusterStatus({
      status: {
        conditions: [{ type: 'HeadPodReady', status: 'True' }],
        availableWorkerReplicas: 2,
        desiredWorkerReplicas: 2,
      },
    }).text).toBe('Ready')
  })

  it('surfaces current KubeRay replica and RayJob deployment failures', () => {
    expect(getRayClusterStatus({
      status: {
        conditions: [{ type: 'RayClusterReplicaFailure', status: 'True', reason: 'FailedCreateWorkerPod' }],
      },
    }).text).toBe('FailedCreateWorkerPod')
    expect(getRayJobStatus({
      status: { jobStatus: 'PENDING', jobDeploymentStatus: 'ValidationFailed' },
    }).text).toBe('ValidationFailed')
    expect(getRayJobStatus({
      status: { jobStatus: 'RUNNING', jobDeploymentStatus: 'Failed' },
    }).text).toBe('Failed')
  })

  it('does not claim KServe runtime health from enabled configuration alone', () => {
    const enabled = getServingRuntimeStatus({ spec: {} })
    expect(enabled.text).toBe('Enabled')
    expect(enabled.level).toBe('neutral')
  })

  it('presents KServe transition failures as operator-readable status', () => {
    expect(getInferenceServiceStatus({
      status: { modelStatus: { transitionStatus: 'BlockedByFailedLoad' } },
    }).text).toBe('Load failed')
    expect(getInferenceServiceStatus({
      status: { modelStatus: { transitionStatus: 'InvalidSpec' } },
    }).text).toBe('Invalid spec')
  })

  it('reads the inline replicas field in both served LLMInferenceService versions', () => {
    expect(getLLMInferenceServiceReplicas({ spec: { replicas: 3 } })).toBe('3')
  })

  it('understands current llm-d and deployed InferenceObjective conditions', () => {
    expect(getInferenceObjectiveStatus({
      status: { conditions: [{ type: 'Ready', status: 'True' }] },
    }).text).toBe('Accepted')
    expect(getInferenceObjectiveStatus({
      status: { conditions: [{ type: 'Accepted', status: 'False', reason: 'InvalidPool' }] },
    }).text).toBe('InvalidPool')
    expect(getInferenceObjectiveStatus({ status: { conditions: [] } }).text).toBe('Unknown')
    expect(getInferenceObjectivePriority({ spec: {} })).toBe('0')
  })

  it('does not treat the alpha InferencePool sentinel parent as a Gateway', () => {
    expect(getInferencePoolStatus({
      status: { parent: [{ parentRef: { kind: 'Status', name: 'default' } }] },
    }).text).toBe('Not referenced')
  })

  it('distinguishes pending JobSets from active work', () => {
    expect(getJobSetStatus({ status: { replicatedJobsStatus: [{ active: 0, ready: 0 }] } }).text).toBe('Pending')
    expect(getJobSetStatus({ status: { replicatedJobsStatus: [{ active: 1, ready: 0 }] } }).text).toBe('Running')
  })

  it('surfaces an unavailable LeaderWorkerSet before progress', () => {
    expect(getLeaderWorkerSetStatus({
      status: { conditions: [{ type: 'Available', status: 'False', reason: 'PodsNotReady' }] },
    }).text).toBe('PodsNotReady')
  })

  it('classifies Volcano terminal and unschedulable states', () => {
    expect(getVolcanoJobStatus({ status: { state: { phase: 'Failed' } } }).level).toBe('unhealthy')
    expect(getVolcanoPodGroupStatus({
      status: { conditions: [{ type: 'Unschedulable', status: 'True' }] },
    }).text).toBe('Unschedulable')
  })

  it('reads KAI quota units and scheduling failures', () => {
    expect(getKaiQueueQuota({
      spec: { resources: { gpu: { quota: 2 }, cpu: { quota: 500 }, memory: { quota: -1 } } },
    })).toBe('GPU: 2, CPU: 500m, Mem: unlimited')
    expect(getKaiQueuePriority({ spec: {} })).toBe('100')
    expect(getKaiQueueStatus({
      status: { conditions: [{ type: 'Orphan', status: 'True', reason: 'ParentNotFound' }] },
    }).text).toBe('ParentNotFound')
    expect(getKaiPodGroupStatus({
      status: { schedulingConditions: [{ type: 'UnschedulableOnNodePool', status: 'True' }] },
    }).text).toBe('Unschedulable')
  })

  it('uses KAITO v1beta1 state and RAGEngine conditions', () => {
    expect(getKaitoWorkspaceStatus({
      status: { state: 'Failed', conditions: [{ type: 'ResourceReady', status: 'True' }] },
    }).level).toBe('unhealthy')
    expect(getKaitoWorkspaceStatus({ status: { state: 'Pending' } }).text).toBe('Pending')
    expect(getRAGEngineStatus({
      status: { conditions: [{ type: 'ServiceReady', status: 'False', reason: 'DeploymentUnavailable' }] },
    }).text).toBe('DeploymentUnavailable')
    expect(getRAGEngineStatus({
      status: { conditions: [
        { type: 'ResourceReady', status: 'True' },
        { type: 'ServiceReady', status: 'True' },
      ] },
    }).text).toBe('Ready')
  })

  it('reads NIM state and scaling bounds', () => {
    expect(getNIMCacheStatus({ status: { state: 'Failed' } }).level).toBe('unhealthy')
    expect(getNIMServiceReplicas({
      spec: { scale: { enabled: true, hpa: { minReplicas: 1, maxReplicas: 4 } } },
      status: { availableReplicas: 2 },
    })).toBe('2 (1-4)')
  })

  it('lets AMD DeviceConfig readiness conditions outrank rollout counts', () => {
    expect(getAMDDeviceConfigStatus({
      status: {
        conditions: [{ type: 'Ready', status: 'False', reason: 'DriverInstallFailed' }],
        driver: { desiredNumber: 2, availableNumber: 2 },
        devicePlugin: { desiredNumber: 2, availableNumber: 2 },
      },
    }).text).toBe('DriverInstallFailed')
  })

  it('uses semantic precedence for Kubeflow conditions instead of array order', () => {
    expect(trainingJobStatus({
      status: { conditions: [
        { type: 'Failed', status: 'True' },
        { type: 'Running', status: 'True' },
      ] },
    }).text).toBe('Failed')
    expect(getTrainJobStatus({
      spec: { suspend: true },
      status: { conditions: [] },
    }).text).toBe('Suspended')
    expect(getTrainJobStatus({
      status: { conditions: [{ type: 'Suspended', status: 'False' }] },
    }).text).toBe('Pending')
    expect(getTrainJobStatus({
      status: { jobsStatus: [{ name: 'trainer', active: 1 }] },
    }).text).toBe('Running')
    expect(getTrainJobStatus({
      status: { conditions: [{ type: 'Failed', status: 'True' }] },
    }).text).toBe('Failed')
    expect(getTrainJobStatus({
      status: {
        conditions: [{ type: 'Complete', status: 'True' }],
        jobsStatus: [{ name: 'trainer', active: 1 }],
      },
    }).text).toBe('Complete')
    expect(getTrainingJobReplicas({
      spec: { pytorchReplicaSpecs: { Worker: { replicas: 4 } } },
      status: { replicaStatuses: { Worker: { active: 1, succeeded: 1, failed: 2 } } },
    })).toBe('Worker 2/4 (2 failed)')
  })
})
