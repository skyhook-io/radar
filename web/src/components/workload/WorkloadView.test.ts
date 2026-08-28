import { describe, expect, it } from 'vitest'

import { gitOpsOwnerFromRelationships } from '@skyhook-io/k8s-ui'
import type { Relationships } from '../../types'
import { findInheritedGitOpsLookupRef } from './WorkloadView'

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
