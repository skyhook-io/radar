import { describe, expect, it } from 'vitest'
import { getSkeletonYaml } from './skeleton-yaml'

describe('getSkeletonYaml', () => {
  it('starts a CloudNativePG Cluster from a spec the operator accepts', () => {
    const yaml = getSkeletonYaml('Cluster', 'postgresql.cnpg.io')
    expect(yaml).toContain('apiVersion: postgresql.cnpg.io/v1')
    expect(yaml).toContain('instances: 3')
    expect(yaml).toContain('size: 1Gi')
  })

  it('leaves a Cluster API Cluster on the generic template', () => {
    const yaml = getSkeletonYaml('Cluster', 'cluster.x-k8s.io')
    expect(yaml).toContain('apiVersion: cluster.x-k8s.io/v1')
    expect(yaml).not.toContain('instances')
  })

  it('keeps core kinds on their own skeleton', () => {
    expect(getSkeletonYaml('Pod')).toContain('containers:')
  })
})
