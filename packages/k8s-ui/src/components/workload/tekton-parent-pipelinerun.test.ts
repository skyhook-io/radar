import { describe, expect, it } from 'vitest'

import { findTektonParentPipelineRun } from './WorkloadView'

describe('findTektonParentPipelineRun', () => {
  const taskRun = (ownerApiVersion: string) => ({
    metadata: { namespace: 'ci', ownerReferences: [{ apiVersion: ownerApiVersion, kind: 'PipelineRun', name: 'ship' }] },
  })

  it('links a TaskRun to its tekton.dev PipelineRun', () => {
    expect(findTektonParentPipelineRun('TaskRun', taskRun('tekton.dev/v1'))).toEqual({ name: 'ship', namespace: 'ci' })
  })

  it('ignores a PipelineRun owner from another API group', () => {
    expect(findTektonParentPipelineRun('TaskRun', taskRun('ci.example.com/v1'))).toBeNull()
  })
})
