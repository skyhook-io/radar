import { describe, expect, it } from 'vitest'
import {
  podStatusClass,
  podStatusLabel,
  workloadPodDetail,
} from './WorkloadView'

describe('workload Pod presentation', () => {
  it('surfaces container failure evidence instead of reporting only the Pod phase', () => {
    const pod = {
      name: 'workers-2-abc',
      phase: 'Running',
      ready: false,
      containers: ['worker'],
      restartCount: 3,
      reason: 'CrashLoopBackOff',
      lastTerminationReason: 'OOMKilled',
      healthLevel: 'unhealthy' as const,
    }

    expect(podStatusLabel(pod.healthLevel, pod.ready)).toBe('Unhealthy')
    expect(podStatusClass(pod.healthLevel, pod.ready)).toBe('status-unhealthy')
    expect(workloadPodDetail(pod)).toBe(
      'Running / CrashLoopBackOff · 1 container · 3 restarts · last OOMKilled',
    )
  })
})
