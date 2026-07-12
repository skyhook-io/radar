import { describe, expect, it } from 'vitest'
import { executionDefinitionFingerprint, executionDefinitionSummary } from './execution-definition'

describe('executionDefinitionSummary', () => {
  it('explains a Kubernetes Job pod template and effective defaults', () => {
    const summary = executionDefinitionSummary('CronJob', {
      spec: {
        jobTemplate: {
          spec: {
            completions: 2,
            template: {
              spec: {
                containers: [{
                  name: 'backup',
                  image: 'example/backup:v2',
                  command: ['/bin/backup'],
                  args: ['--full'],
                  envFrom: [{ configMapRef: { name: 'backup-config' } }],
                  resources: { requests: { cpu: '100m', memory: '256Mi' }, limits: { cpu: '1', memory: '1Gi' } },
                }],
                restartPolicy: 'OnFailure',
                imagePullSecrets: [{ name: 'registry-credentials' }],
                volumes: [{ name: 'projected', projected: { sources: [{ secret: { name: 'projected-secret' } }, { configMap: { name: 'projected-config' } }] } }],
              },
            },
          },
        },
      },
    })

    expect(summary).toMatchObject({
      shape: 'Pod · 1 container',
      serviceAccount: 'default',
      retry: 'Backoff limit 6 · restart OnFailure',
      parallelism: '1 parallel · 2 completions · NonIndexed',
      configMaps: ['backup-config', 'projected-config'],
      secrets: ['projected-secret', 'registry-credentials'],
      imagePullSecrets: ['registry-credentials'],
      externalTemplates: [],
      units: [{
        name: 'backup',
        type: 'Container',
        image: 'example/backup:v2',
        command: '/bin/backup --full',
        requests: 'CPU 0.10 cores · memory 256 MiB',
        limits: 'CPU 1 core · memory 1.00 GiB',
      }],
    })
  })

  it('explains Argo DAG shape, executable templates, policy, and dependencies', () => {
    const summary = executionDefinitionSummary('WorkflowTemplate', {
      spec: {
        entrypoint: 'main',
        serviceAccountName: 'migrator',
        parallelism: 3,
        activeDeadlineSeconds: 600,
        imagePullSecrets: [{ name: 'workflow-registry' }],
        templates: [
          { name: 'main', dag: { tasks: [{ name: 'prepare' }, { name: 'publish' }] } },
          {
            name: 'worker',
            imagePullSecrets: [{ name: 'template-registry' }],
            retryStrategy: { limit: 2, retryPolicy: 'OnError', backoff: { duration: '5s' } },
            container: {
              image: 'example/worker:v3',
              command: ['worker'],
              env: [{ name: 'TOKEN', valueFrom: { secretKeyRef: { name: 'worker-secret', key: 'token' } } }],
            },
          },
        ],
      },
    })

    expect(summary).toMatchObject({
      shape: 'DAG · main · 2 tasks',
      serviceAccount: 'migrator',
      retry: 'worker: 2 retries · OnError · 5s backoff',
      deadline: '600s',
      parallelism: '3 maximum',
      secrets: ['template-registry', 'worker-secret', 'workflow-registry'],
      imagePullSecrets: ['template-registry', 'workflow-registry'],
      units: [{ name: 'worker', type: 'Container', image: 'example/worker:v3', command: 'worker' }],
    })
  })

  it('uses the stored WorkflowTemplate snapshot for a retained Workflow run', () => {
    const run = {
      spec: { workflowTemplateRef: { name: 'current-definition' } },
      status: {
        storedWorkflowTemplateSpec: {
          entrypoint: 'main',
          templates: [{ name: 'main', container: { image: 'example/job:old', command: ['old-command'] } }],
        },
      },
    }
    const current = executionDefinitionSummary('WorkflowTemplate', {
      spec: { entrypoint: 'main', templates: [{ name: 'main', container: { image: 'example/job:new', command: ['new-command'] } }] },
    })
    const stored = executionDefinitionSummary('Workflow', run)

    expect(stored?.units[0]).toMatchObject({ image: 'example/job:old', command: 'old-command' })
    expect(executionDefinitionFingerprint(stored)).not.toBe(executionDefinitionFingerprint(current))
  })

  it('does not report drift for an equivalent stored template snapshot', () => {
    const current = executionDefinitionSummary('WorkflowTemplate', {
      spec: { entrypoint: 'main', templates: [{ name: 'main', container: { name: 'job', image: 'example/job:v1', resources: { limits: { cpu: '1000m' } } } }] },
    })
    const stored = executionDefinitionSummary('Workflow', {
      status: {
        storedWorkflowTemplateSpec: {
          entrypoint: 'main',
          workflowTemplateRef: { name: 'definition' },
          templates: [{ name: 'main', container: { name: 'job', image: 'example/job:v1', resources: { limits: { cpu: '1' } } } }],
        },
      },
    })

    expect(executionDefinitionFingerprint(stored)).toBe(executionDefinitionFingerprint(current))
  })

  it('reads a ScaledJob embedded Job target', () => {
    const summary = executionDefinitionSummary('ScaledJob', {
      spec: {
        jobTargetRef: {
          backoffLimit: 2,
          activeDeadlineSeconds: 90,
          template: { spec: { serviceAccountName: 'worker', containers: [{ name: 'worker', image: 'example/worker:v4' }] } },
        },
      },
    })

    expect(summary).toMatchObject({
      shape: 'Pod · 1 container',
      serviceAccount: 'worker',
      retry: 'Backoff limit 2 · restart Never',
      deadline: '90s',
      units: [{ image: 'example/worker:v4' }],
    })
  })

  it('summarizes init and multiple application containers', () => {
    const summary = executionDefinitionSummary('Job', {
      spec: {
        template: {
          spec: {
            initContainers: [{ name: 'prepare', image: 'example/prepare:v1' }],
            containers: [{ name: 'worker', image: 'example/worker:v1' }, { name: 'sidecar', image: 'example/sidecar:v1' }],
          },
        },
      },
    })

    expect(summary).toMatchObject({
      shape: 'Pod · 2 containers · 1 init',
      units: [
        { name: 'prepare', type: 'Init container' },
        { name: 'worker', type: 'Container' },
        { name: 'sidecar', type: 'Container' },
      ],
    })
  })

  it('summarizes an inline CronWorkflow steps entrypoint', () => {
    const summary = executionDefinitionSummary('CronWorkflow', {
      spec: {
        workflowSpec: {
          entrypoint: 'main',
          templates: [
            { name: 'main', steps: [[{ name: 'first', template: 'run' }, { name: 'second', template: 'run' }], [{ name: 'third', template: 'run' }]] },
            { name: 'run', script: { image: 'example/script:v1', command: ['sh'], source: 'echo done' } },
          ],
        },
      },
    })

    expect(summary).toMatchObject({
      shape: 'Steps · main · 3 steps in 2 groups',
      units: [{ name: 'run', type: 'Script', image: 'example/script:v1', command: 'sh' }],
    })
  })

  it('summarizes a container set entrypoint', () => {
    const summary = executionDefinitionSummary('WorkflowTemplate', {
      spec: {
        entrypoint: 'main',
        templates: [{
          name: 'main',
          containerSet: {
            containers: [
              { name: 'first', image: 'example/first:v1' },
              { name: 'second', image: 'example/second:v1', command: ['second'] },
            ],
          },
        }],
      },
    })

    expect(summary).toMatchObject({
      shape: 'Container set · main · 2 containers',
      units: [
        { name: 'first', type: 'Container', image: 'example/first:v1' },
        { name: 'second', type: 'Container', image: 'example/second:v1', command: 'second' },
      ],
    })
  })
})
