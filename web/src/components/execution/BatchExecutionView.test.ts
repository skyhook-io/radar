import { describe, expect, it } from 'vitest'
import type { WorkflowExecutionActivity } from '@skyhook-io/k8s-ui/utils/workflow-execution'
import { activityPreviewItems, effectiveDefinitionResource, isDirectRunKind, retentionHistoryCopy, runMessageNeedsDisclosure, workflowDefinitionParameters, workflowDefinitionTarget, workflowRunArguments } from './BatchExecutionView'

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

  it('keeps large failed histories capped while preserving chronological prefix ordering', () => {
    const items = Array.from({ length: 500 }, (_, index) => activity(`event-${index}`, index === 499 ? 'danger' : 'success'))

    expect(activityPreviewItems(items)).toEqual(items.slice(0, 8))
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
    expect(runMessageNeedsDisclosure('Reached expected number of succeeded pods')).toBe(false)
  })

  it('keeps short failures inline and discloses multiline or long messages', () => {
    expect(runMessageNeedsDisclosure('permission denied')).toBe(false)
    expect(runMessageNeedsDisclosure('first line\nsecond line')).toBe(true)
    expect(runMessageNeedsDisclosure('x'.repeat(141))).toBe(true)
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

describe('referenced CronWorkflow definitions', () => {
  const cronWorkflow = {
    metadata: { namespace: 'jobs' },
    spec: {
      workflowSpec: {
        workflowTemplateRef: { name: 'shared' },
        arguments: { parameters: [{ name: 'region', value: 'eu' }] },
      },
    },
  }

  it('fetches the referenced namespaced definition', () => {
    expect(workflowDefinitionTarget('CronWorkflow', cronWorkflow)).toEqual({
      kind: 'workflowtemplates',
      namespace: 'jobs',
      name: 'shared',
      group: 'argoproj.io',
    })
  })

  it('combines referenced execution details with CronWorkflow overrides', () => {
    const referenced = {
      spec: {
        entrypoint: 'main',
        serviceAccountName: 'base-account',
        arguments: { parameters: [{ name: 'region', value: 'us', description: 'Target region' }, { name: 'mode', value: 'safe' }] },
        templates: [{ name: 'main', container: { image: 'example/shared:v1' } }],
      },
    }

    const effective = effectiveDefinitionResource('CronWorkflow', cronWorkflow, referenced)

    expect(effective.spec.workflowSpec).toMatchObject({
      entrypoint: 'main',
      serviceAccountName: 'base-account',
      templates: referenced.spec.templates,
      arguments: {
        parameters: [
          { name: 'region', value: 'eu', description: 'Target region' },
          { name: 'mode', value: 'safe' },
        ],
      },
    })
  })

  it('replaces valueFrom when a schedule supplies a concrete value', () => {
    const referenced = {
      spec: {
        arguments: { parameters: [{ name: 'region', valueFrom: { configMapKeyRef: { name: 'defaults', key: 'region' } } }] },
      },
    }

    const effective = effectiveDefinitionResource('CronWorkflow', cronWorkflow, referenced)

    expect(effective.spec.workflowSpec.arguments.parameters[0]).toEqual({ name: 'region', value: 'eu' })
  })
})

describe('direct run identity', () => {
  it('suppresses captured configuration only when the workload is the run itself', () => {
    expect(isDirectRunKind('Job', 'jobs')).toBe(true)
    expect(isDirectRunKind('Workflow', 'workflows')).toBe(true)
    expect(isDirectRunKind('CronJob', 'jobs')).toBe(false)
    expect(isDirectRunKind('WorkflowTemplate', 'workflows')).toBe(false)
  })
})
