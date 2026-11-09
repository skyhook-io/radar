import { describe, expect, it } from 'vitest'
import { getJobStatus, isKueueQueuedJob } from './resource-utils'
import { getJobSetStatus } from './resource-utils-jobset-lws'

const queued = {
  apiVersion: 'batch/v1',
  kind: 'Job',
  metadata: { labels: { 'kueue.x-k8s.io/queue-name': 'team-a' } },
  spec: { suspend: true },
}

describe('Job status for Kueue-queued Jobs', () => {
  it('names Kueue as the holder of a suspended queued Job', () => {
    expect(getJobStatus(queued)).toMatchObject({ text: 'Held by Kueue', level: 'neutral' })
  })

  it('also recognizes list items that carry no type metadata', () => {
    const { apiVersion: _apiVersion, kind: _kind, ...item } = queued
    expect(getJobStatus(item).text).toBe('Held by Kueue')
  })

  it('keeps Suspended for a Job outside Kueue or of another API group', () => {
    expect(getJobStatus({ ...queued, metadata: {} }).text).toBe('Suspended')
    expect(isKueueQueuedJob({ ...queued, apiVersion: 'batch.volcano.sh/v1alpha1' })).toBe(false)
    expect(getJobStatus({ ...queued, apiVersion: 'batch.volcano.sh/v1alpha1' }).text).toBe('Suspended')
  })

  it('reports terminal and running states the same as before', () => {
    expect(getJobStatus({ ...queued, status: { conditions: [{ type: 'Failed', status: 'True' }] } }).text).toBe('Failed')
    expect(getJobStatus({ ...queued, status: { conditions: [{ type: 'Complete', status: 'True' }] } }).text).toBe('Complete')
    expect(getJobStatus({ ...queued, spec: { suspend: false }, status: { active: 1 } }).text).toBe('Running')
  })
})

describe('JobSet status for Kueue-queued JobSets', () => {
  const suspended = { status: { conditions: [{ type: 'Suspended', status: 'True' }] } }

  it('names Kueue as the holder of a suspended queued JobSet', () => {
    expect(getJobSetStatus({ ...suspended, metadata: { labels: { 'kueue.x-k8s.io/queue-name': 'gpu-training' } } }).text).toBe('Held by Kueue')
    expect(getJobSetStatus({ ...suspended, metadata: {} }).text).toBe('Suspended')
  })
})
