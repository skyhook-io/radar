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

  it('offers a CloudNativePG Cluster its merged instance logs, but not other Cluster kinds', () => {
    expect(supportsLogsWithoutPods('clusters', 'Cluster', 'postgresql.cnpg.io', 'postgresql.cnpg.io/v1')).toBe(true)
    expect(supportsLogsWithoutPods('clusters', 'Cluster', undefined, 'postgresql.cnpg.io/v1')).toBe(true)
    expect(supportsLogsWithoutPods('clusters', 'Cluster', 'cluster.x-k8s.io', 'cluster.x-k8s.io/v1beta1')).toBe(false)
  })
})
