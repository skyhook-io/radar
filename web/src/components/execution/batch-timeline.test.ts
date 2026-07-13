import { describe, expect, it } from 'vitest'
import type { WorkloadRun } from '../../api/client'
import { workloadRunTimelineEvents } from './batch-timeline'

describe('workloadRunTimelineEvents', () => {
  it('builds lifecycle events for every retained run', () => {
    const runs: WorkloadRun[] = [
      {
        kind: 'jobs',
        namespace: 'dev',
        name: 'nightly-1',
        phase: 'Succeeded',
        active: false,
        scheduledAt: '2026-01-01T00:00:00Z',
        startedAt: '2026-01-01T00:00:02Z',
        finishedAt: '2026-01-01T00:01:00Z',
      },
      {
        kind: 'jobs',
        namespace: 'dev',
        name: 'nightly-2',
        phase: 'Failed',
        active: false,
        startedAt: '2026-01-02T00:00:00Z',
        finishedAt: '2026-01-02T00:00:10Z',
        message: 'backoff limit reached',
      },
    ]

    const events = workloadRunTimelineEvents(runs)

    expect(events).toHaveLength(5)
    expect(events.map((event) => event.reason)).toEqual([
      'Job scheduled',
      'Job started',
      'Job succeeded',
      'Job started',
      'Job failed',
    ])
    expect(events.at(-1)).toMatchObject({ eventType: 'Warning', message: 'backoff limit reached', name: 'nightly-2' })
  })

  it('uses Workflow resource identity for Argo runs', () => {
    const events = workloadRunTimelineEvents([{
      kind: 'workflows',
      namespace: 'dev',
      name: 'migration-abc',
      phase: 'Error',
      active: false,
      startedAt: '2026-01-01T00:00:00Z',
      finishedAt: '2026-01-01T00:00:01Z',
    }])

    expect(events[0]).toMatchObject({ kind: 'Workflow', apiVersion: 'argoproj.io/v1alpha1', reason: 'Workflow started' })
    expect(events[1]).toMatchObject({ eventType: 'Warning', reason: 'Workflow errored' })
  })
})
