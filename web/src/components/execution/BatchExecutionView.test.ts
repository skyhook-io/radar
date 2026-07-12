import { describe, expect, it } from 'vitest'
import type { WorkflowExecutionActivity } from '@skyhook-io/k8s-ui/utils/workflow-execution'
import { activityPreviewItems, retentionHistoryCopy } from './BatchExecutionView'

function activity(id: string, tone: WorkflowExecutionActivity['tone'] = 'success'): WorkflowExecutionActivity {
  return { id, at: '2026-01-01T00:00:00Z', label: id, tone }
}

describe('activityPreviewItems', () => {
  it('keeps workflow lifecycle events and fills the preview with recent activity', () => {
    const items = [
      activity('workflow-started', 'info'),
      ...Array.from({ length: 14 }, (_, index) => activity(`node-${index}`)),
      activity('workflow-finished'),
    ]

    expect(activityPreviewItems(items).map((item) => item.id)).toEqual([
      'workflow-started',
      'node-8',
      'node-9',
      'node-10',
      'node-11',
      'node-12',
      'node-13',
      'workflow-finished',
    ])
  })

  it('never hides warning or failure events to satisfy the preview limit', () => {
    const items = Array.from({ length: 12 }, (_, index) => activity(`event-${index}`, index < 9 ? 'warning' : 'success'))

    expect(activityPreviewItems(items).slice(0, 9)).toEqual(items.slice(0, 9))
  })
})

describe('retentionHistoryCopy', () => {
  it('puts configured history limits beside runs and calls out a reached phase limit', () => {
    const resource = { spec: { successfulJobsHistoryLimit: 1, failedJobsHistoryLimit: 3 } }

    expect(retentionHistoryCopy('CronJob', resource, { succeeded: 1, failed: 0 })).toBe(
      'Keeps latest 1 succeeded / latest 3 failed. Success limit reached.',
    )
  })

  it('uses controller defaults for scheduled workflows', () => {
    expect(retentionHistoryCopy('CronWorkflow', { spec: {} }, { succeeded: 1, failed: 1 })).toBe(
      'Keeps latest 3 succeeded / latest 1 failed. Failure limit reached.',
    )
  })

  it('does not invent limits for ScaledJobs that do not configure them', () => {
    expect(retentionHistoryCopy('ScaledJob', { spec: {} }, { succeeded: 4, failed: 2 })).toBeNull()
  })

  it('does not show retention settings for template-level histories', () => {
    expect(retentionHistoryCopy('WorkflowTemplate', { spec: {} }, { succeeded: 4, failed: 2 })).toBeNull()
  })
})
