import { describe, expect, it } from 'vitest'
import type { WorkflowExecutionActivity } from '@skyhook-io/k8s-ui/utils/workflow-execution'
import { activityPreviewItems, retentionHistoryCopy, runMessageNeedsDisclosure, workflowDefinitionParameters, workflowRunArguments } from './BatchExecutionView'

function activity(id: string, tone: WorkflowExecutionActivity['tone'] = 'success'): WorkflowExecutionActivity {
  return { id, at: '2026-01-01T00:00:00Z', label: id, tone }
}

describe('activityPreviewItems', () => {
  it('keeps a stable chronological prefix so expansion only appends activity', () => {
    const items = [
      activity('workflow-started', 'info'),
      ...Array.from({ length: 14 }, (_, index) => activity(`node-${index}`)),
      activity('workflow-finished'),
    ]

    expect(activityPreviewItems(items)).toEqual(items.slice(0, 8))
  })

  it('extends the chronological prefix through the last warning or failure', () => {
    const items = Array.from({ length: 12 }, (_, index) => activity(`event-${index}`, index === 9 ? 'warning' : 'success'))

    expect(activityPreviewItems(items)).toEqual(items.slice(0, 10))
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

describe('runMessageNeedsDisclosure', () => {
  it('keeps short successful messages inline', () => {
    expect(runMessageNeedsDisclosure('Reached expected number of succeeded pods', false)).toBe(false)
  })

  it('uses a disclosure for failures, multiline messages, and long messages', () => {
    expect(runMessageNeedsDisclosure('permission denied', true)).toBe(true)
    expect(runMessageNeedsDisclosure('first line\nsecond line', false)).toBe(true)
    expect(runMessageNeedsDisclosure('x'.repeat(141), false)).toBe(true)
  })
})

describe('workflow parameters', () => {
  const definition = {
    spec: {
      arguments: {
        parameters: [
          { name: 'region', value: 'us-east-1', description: 'Deployment region' },
          { name: 'mode' },
        ],
      },
    },
  }

  it('reads the input contract from workflow definitions', () => {
    expect(workflowDefinitionParameters('WorkflowTemplate', definition)).toEqual(definition.spec.arguments.parameters)
    expect(workflowDefinitionParameters('Workflow', definition)).toEqual([])
  })

  it('shows only selected-run arguments that differ from definition defaults', () => {
    const run = {
      spec: {
        arguments: {
          parameters: [
            { name: 'region', value: 'eu-west-1' },
            { name: 'mode', value: 'safe' },
            { name: 'unchanged', value: 'same' },
          ],
        },
      },
    }
    const definitionWithUnchanged = {
      spec: {
        arguments: {
          parameters: [...definition.spec.arguments.parameters, { name: 'unchanged', value: 'same' }],
        },
      },
    }

    expect(workflowRunArguments(run, 'WorkflowTemplate', definitionWithUnchanged).map((parameter) => parameter.name)).toEqual(['region', 'mode'])
    expect(workflowRunArguments(run, 'Workflow', run)).toHaveLength(3)
  })
})
