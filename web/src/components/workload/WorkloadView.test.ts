import { describe, expect, it } from 'vitest'

import { gitOpsOwnerFromRelationships } from '@skyhook-io/k8s-ui'
import type { Relationships } from '../../types'
import { findInheritedGitOpsLookupRef, supportsBatchExecution } from './WorkloadView'

describe('findInheritedGitOpsLookupRef', () => {
  it('follows a referenced ReplicaSet to its parent workload for inherited ownership', () => {
    const relationships: Relationships = {
      managedBy: [
        {
          kind: 'Deployment',
          group: 'apps',
          namespace: 'prod',
          name: 'web',
        },
      ],
    }

    expect(
      findInheritedGitOpsLookupRef(relationships, null, {
        kind: 'ReplicaSet',
        group: 'apps',
        namespace: 'prod',
        name: 'web-7b8d9',
      }),
    ).toEqual({
      kind: 'Deployment',
      group: 'apps',
      namespace: 'prod',
      name: 'web',
    })
  })

  it('does not fetch a parent when the target has a direct GitOps owner', () => {
    const relationships: Relationships = {
      managedBy: [
        {
          kind: 'Application',
          group: 'argoproj.io',
          namespace: 'argocd',
          name: 'web',
        },
      ],
    }

    expect(
      findInheritedGitOpsLookupRef(
        relationships,
        gitOpsOwnerFromRelationships(relationships),
        {
          kind: 'Deployment',
          group: 'apps',
          namespace: 'prod',
          name: 'web',
        },
      ),
    ).toBeNull()
  })
})

describe('supportsBatchExecution', () => {
  it('enables only the supported JobSet API identity', () => {
    expect(supportsBatchExecution('JobSet', 'jobsets', 'jobset.x-k8s.io', 'jobset.x-k8s.io/v1alpha2')).toBe(true)
    expect(supportsBatchExecution('JobSet', 'jobsets', 'jobset.x-k8s.io')).toBe(false)
    expect(supportsBatchExecution('JobSet', 'jobsets', 'example.io', 'example.io/v1alpha2')).toBe(false)
    expect(supportsBatchExecution('JobSet', 'jobsets', 'example.io', 'jobset.x-k8s.io/v1alpha2')).toBe(false)
    expect(supportsBatchExecution('JobSet', 'jobsets', 'jobset.x-k8s.io', 'jobset.x-k8s.io/v1beta1')).toBe(false)
  })

  it('preserves the core Job collision guard and existing scheduled kinds', () => {
    expect(supportsBatchExecution('Job', 'jobs', 'batch')).toBe(true)
    expect(supportsBatchExecution('Job', 'jobs', 'example.io')).toBe(false)
    expect(supportsBatchExecution('CronJob', 'cronjobs', 'batch')).toBe(true)
  })
})
