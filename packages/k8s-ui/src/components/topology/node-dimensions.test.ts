import { describe, expect, it } from 'vitest'
import { NODE_DIMENSIONS, NODE_NAME_LENGTH_LIMITS } from './K8sResourceNode'

describe('topology node dimensions', () => {
  it('gives generated batch run names room without widening unrelated nodes', () => {
    expect(NODE_DIMENSIONS.Job.width).toBeGreaterThanOrEqual(300)
    expect(NODE_DIMENSIONS.Workflow.width).toBeGreaterThanOrEqual(300)
    expect(NODE_DIMENSIONS.CronJob.width).toBe(200)
    expect(NODE_DIMENSIONS.ConfigMap.width).toBe(180)
    expect(NODE_NAME_LENGTH_LIMITS.Job).toBeGreaterThan(34)
    expect(NODE_NAME_LENGTH_LIMITS.Workflow).toBeGreaterThan(NODE_NAME_LENGTH_LIMITS.Job!)
    expect(NODE_NAME_LENGTH_LIMITS.CronJob).toBeUndefined()
  })
})
