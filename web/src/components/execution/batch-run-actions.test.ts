import { describe, expect, it } from 'vitest'
import type { WorkloadRun } from '../../api/client'
import { batchRunNextStep } from './batch-run-actions'

function run(phase: string, counts: Partial<Pick<WorkloadRun, 'podFailed' | 'podSucceeded' | 'podRunning' | 'podPending'>> = {}): WorkloadRun {
  return {
    kind: 'jobs',
    namespace: 'jobs',
    name: 'example',
    phase,
    active: phase === 'Running' || phase === 'Pending',
    ...counts,
  }
}

describe('batchRunNextStep', () => {
  it.each([
    ['failed Pod', { podFailed: 1 }],
    ['succeeded Pod', { podSucceeded: 1 }],
    ['running Pod', { podRunning: 1 }],
  ])('sends a failed run with a %s to logs', (_label, counts) => {
    expect(batchRunNextStep(run('Failed', counts), true)).toBe('logs')
  })

  it('uses timeline for an error without a container outcome', () => {
    expect(batchRunNextStep(run('Error'), true)).toBe('timeline')
  })

  it('does not treat a pending Pod as log evidence', () => {
    expect(batchRunNextStep(run('Failed', { podPending: 1 }), true)).toBe('timeline')
  })

  it('uses timeline when log access is unavailable', () => {
    expect(batchRunNextStep(run('Failed', { podFailed: 1 }), false)).toBe('timeline')
  })

  it.each(['Running', 'Pending', 'Succeeded', 'Suspended', 'Unknown'])('does not suggest an action for %s runs', (phase) => {
    expect(batchRunNextStep(run(phase, { podFailed: 1, podRunning: 1 }), true)).toBeNull()
  })
})
