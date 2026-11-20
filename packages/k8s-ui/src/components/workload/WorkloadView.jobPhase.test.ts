import { describe, expect, it } from 'vitest'
import { jobPhase } from './WorkloadView'

describe('jobPhase', () => {
  const queued = { apiVersion: 'batch/v1', kind: 'Job', metadata: { labels: { 'kueue.x-k8s.io/queue-name': 'team-a' } }, spec: { suspend: true } }

  it('agrees with the Job status label for a Kueue-queued Job', () => {
    expect(jobPhase(queued)).toBe('Held by Kueue')
    expect(jobPhase({ ...queued, metadata: {} })).toBe('Suspended')
  })

  it('keeps terminal conditions ahead of suspension', () => {
    expect(jobPhase({ ...queued, status: { conditions: [{ type: 'Failed', status: 'True' }] } })).toBe('Failed')
  })
})
