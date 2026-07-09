import { describe, expect, it } from 'vitest'
import { buildWorkflowExecutionModel, collectWorkflowTemplateRefs } from './workflow-execution'

describe('workflow execution model', () => {
  it('builds lineage from Argo child edges and counts pods/steps', () => {
    const model = buildWorkflowExecutionModel({
      metadata: {
        namespace: 'demo',
        annotations: { 'workflows.argoproj.io/scheduled-time': '2026-07-05T10:00:00Z' },
      },
      spec: {
        workflowTemplateRef: { name: 'main-template' },
      },
      status: {
        phase: 'Failed',
        startedAt: '2026-07-05T10:00:05Z',
        finishedAt: '2026-07-05T10:01:00Z',
        nodes: {
          root: {
            id: 'root',
            displayName: 'root',
            type: 'DAG',
            phase: 'Failed',
            startedAt: '2026-07-05T10:00:05Z',
            finishedAt: '2026-07-05T10:01:00Z',
            children: ['step-a', 'step-b'],
          },
          'step-a': {
            id: 'step-a',
            displayName: 'step-a',
            type: 'Pod',
            phase: 'Succeeded',
            startedAt: '2026-07-05T10:00:10Z',
            finishedAt: '2026-07-05T10:00:20Z',
          },
          'step-b': {
            id: 'step-b',
            displayName: 'step-b',
            type: 'Pod',
            phase: 'Failed',
            message: 'exit code 1',
            startedAt: '2026-07-05T10:00:30Z',
            finishedAt: '2026-07-05T10:00:40Z',
          },
        },
      },
    })

    expect(model.counts.podTotal).toBe(2)
    expect(model.counts.podSucceeded).toBe(1)
    expect(model.counts.podFailed).toBe(1)
    expect(model.counts.stepTotal).toBe(3)
    expect(model.focusPaths[0].nodes.map((node) => node.id)).toEqual(['root', 'step-b'])
    expect(model.templateRefs).toMatchObject([{ name: 'main-template', resourceKind: 'workflowtemplates', namespace: 'demo' }])
    expect(model.activity.map((item) => item.id)).toContain('workflow-scheduled')
  })

  it('uses boundaryID as a fallback parent when children are missing', () => {
    const model = buildWorkflowExecutionModel({
      status: {
        nodes: {
          group: { displayName: 'group', type: 'Steps', phase: 'Failed' },
          child: { displayName: 'child', type: 'Pod', phase: 'Failed', boundaryID: 'group' },
        },
      },
    })

    expect(model.edges).toEqual([{ source: 'group', target: 'child' }])
    expect(model.focusPaths[0].nodes.map((node) => node.id)).toEqual(['group', 'child'])
  })

  it('collects task-level ClusterWorkflowTemplate refs', () => {
    const refs = collectWorkflowTemplateRefs({
      metadata: { namespace: 'demo' },
      spec: {
        templates: [
          {
            name: 'main',
            dag: {
              tasks: [
                { name: 'one', templateRef: { name: 'cluster-lib', template: 'worker', clusterScope: true } },
              ],
            },
          },
        ],
      },
    })

    expect(refs).toEqual([
      {
        name: 'cluster-lib',
        kind: 'ClusterWorkflowTemplate',
        resourceKind: 'clusterworkflowtemplates',
        namespace: '',
        clusterScope: true,
        source: 'task',
        template: 'worker',
        taskName: 'one',
      },
    ])
  })
})
