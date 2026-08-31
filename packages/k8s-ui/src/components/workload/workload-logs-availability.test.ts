import { describe, expect, it } from 'vitest'

import { supportsLogsWithoutPods } from './WorkloadView'

describe('supportsLogsWithoutPods', () => {
  it('allows the supported JobSet identity to select child Job logs', () => {
    expect(supportsLogsWithoutPods('jobsets', 'JobSet', 'jobset.x-k8s.io', 'jobset.x-k8s.io/v1alpha2')).toBe(true)
  })

  it('rejects foreign and unsupported same-kind JobSets', () => {
    expect(supportsLogsWithoutPods('jobsets', 'JobSet', 'example.io', 'example.io/v1alpha2')).toBe(false)
    expect(supportsLogsWithoutPods('jobsets', 'JobSet', 'jobset.x-k8s.io', 'jobset.x-k8s.io/v1beta1')).toBe(false)
  })

  it('preserves existing scheduled workload and core Job behavior', () => {
    expect(supportsLogsWithoutPods('cronjobs', 'CronJob', 'batch', 'batch/v1')).toBe(true)
    expect(supportsLogsWithoutPods('jobs', 'Job', 'batch', 'batch/v1')).toBe(true)
    expect(supportsLogsWithoutPods('jobs', 'Job', 'example.io', 'example.io/v1')).toBe(false)
  })
})
