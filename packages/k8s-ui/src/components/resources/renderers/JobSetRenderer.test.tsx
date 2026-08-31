import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { JobSetRenderer, getJobSetConditionTone } from './JobSetRenderer'

function render(data: any): string {
  return renderToString(<JobSetRenderer data={data} />).replace(/<!-- -->/g, '')
}

function currentJobSet(): any {
  return {
    apiVersion: 'jobset.x-k8s.io/v1alpha2',
    kind: 'JobSet',
    metadata: { name: 'training', namespace: 'ml' },
    spec: {
      suspend: false,
      replicatedJobs: [
        {
          name: 'leader',
          groupName: 'control',
          replicas: 1,
          template: { spec: { parallelism: 1, completions: 1 } },
        },
        {
          name: 'workers',
          groupName: 'training',
          replicas: 3,
          dependsOn: [{ name: 'leader', status: 'Ready' }],
          template: {
            spec: {
              parallelism: 2,
              completions: 2,
              completionMode: 'Indexed',
              backoffLimit: 1,
            },
          },
        },
      ],
      successPolicy: { operator: 'Any', targetReplicatedJobs: ['workers'] },
      failurePolicy: {
        maxRestarts: 3,
        restartStrategy: 'Recreate',
        rules: [
          {
            name: 'restart-workers',
            action: 'RestartJob',
            targetReplicatedJobs: ['workers'],
            onJobFailureReasons: ['BackoffLimitExceeded'],
            onJobFailureMessagePatterns: ['temporary.*'],
          },
        ],
      },
      coordinator: { replicatedJob: 'leader', jobIndex: 0, podIndex: 0 },
      network: {
        subdomain: 'training-workers',
        enableDNSHostnames: true,
        publishNotReadyAddresses: false,
      },
    },
    status: {
      restarts: 1,
      restartsCountTowardsMax: 1,
      replicatedJobsStatus: [
        {
          name: 'leader',
          ready: 1,
          active: 1,
          succeeded: 0,
          failed: 0,
          suspended: 0,
          jobRestarts: [0],
          jobRestartsCountTowardsMax: [0],
        },
        {
          name: 'workers',
          ready: 2,
          active: 3,
          succeeded: 0,
          failed: 0,
          suspended: 0,
          jobRestarts: [0, 1, 0],
          jobRestartsCountTowardsMax: [0, 1, 0],
        },
      ],
      conditions: [{ type: 'Suspended', status: 'False', reason: 'Resumed' }],
    },
  }
}

describe('JobSetRenderer', () => {
  it('shows each replicated role without flattening away Job indexes or controller counts', () => {
    const html = render(currentJobSet())

    expect(html).toContain('Replicated jobs (2)')
    expect(html).toContain('leader')
    expect(html).toContain('workers')
    expect(html).toContain('Group training')
    expect(html).toContain('0–2')
    expect(html).toContain('Parallelism / Job')
    expect(html).toContain('Indexed')
    expect(html).toContain('Ready</span> <span')
    expect(html).toContain('>2/3</span>')
    expect(html).toContain('Starts after')
    expect(html).toContain('leader · Ready')
  })

  it('preserves current success and ordered failure-policy semantics', () => {
    const html = render(currentJobSet())

    expect(html).toContain('Completion and restart policies')
    expect(html).toContain('Any')
    expect(html).toContain('across workers')
    expect(html).toContain('Restart limit')
    expect(html).toContain('RestartJobSet')
    expect(html).toContain('1. restart-workers')
    expect(html).toContain('RestartJob')
    expect(html).toContain('BackoffLimitExceeded')
    expect(html).toContain('temporary.*')
  })

  it('combines global and individual Job restarts in the shared restart budget', () => {
    const html = render(currentJobSet())

    expect(html).toContain('Restart budget used')
    expect(html).toContain('2 / 3')
  })

  it('keeps zero-valued coordinator indexes and explicit network choices visible', () => {
    const html = render(currentJobSet())

    expect(html).toContain('Coordination and network')
    expect(html).toContain('Coordinator Job index')
    expect(html).toContain('Coordinator Pod index')
    expect(html).toContain('training-workers')
    expect(html).toContain('Enabled')
    expect(html).toContain('No')
  })

  it('does not turn missing role status into zero progress', () => {
    const data = currentJobSet()
    data.status.replicatedJobsStatus = []
    const html = render(data)

    expect(html).toContain('Controller status has not been reported for this role.')
    expect(html).not.toContain('Ready</span> <span')
  })

  it("raises only the JobSet controller's own terminal failure", () => {
    const data = currentJobSet()
    data.status.terminalState = 'Failed'
    data.status.conditions = [
      {
        type: 'Failed',
        status: 'True',
        reason: 'RetriesExhausted',
        message: 'Global restart limit reached',
      },
    ]
    const html = render(data)

    expect(html).toContain('Issue Detected')
    expect(html).toContain('Global restart limit reached')
  })

  it('does not raise a stale failure condition after terminal completion', () => {
    const data = currentJobSet()
    data.status.terminalState = 'Completed'
    data.status.conditions = [{ type: 'Failed', status: 'True', message: 'Stale failure' }]
    const html = render(data)

    expect(html).toContain('Completed')
    expect(html).not.toContain('Issue Detected')
    expect(html).toContain('Stale failure')
  })

  it('renders safely when the controller has not populated optional fields', () => {
    const html = render({
      apiVersion: 'jobset.x-k8s.io/v1alpha2',
      kind: 'JobSet',
      metadata: { name: 'new' },
      spec: {},
    })

    expect(html).toContain('JobSet status')
    expect(html).toContain('Unknown')
    expect(html).not.toContain('Replicated jobs (')
    expect(html).not.toContain('Coordination and network')
  })
})

describe('JobSet condition polarity', () => {
  it('treats normal incomplete work as unknown rather than failing', () => {
    expect(getJobSetConditionTone({ type: 'Completed', status: 'False' })).toBe('unknown')
  })

  it('distinguishes intentional suspension from failure', () => {
    expect(getJobSetConditionTone({ type: 'Suspended', status: 'True' })).toBe('warning')
    expect(getJobSetConditionTone({ type: 'Failed', status: 'True' })).toBe('fail')
  })

  it('does not mark normal restart and startup transitions as failures', () => {
    expect(getJobSetConditionTone({ type: 'RestartingJobSet', status: 'True' })).toBe('warning')
    expect(getJobSetConditionTone({ type: 'RestartingJobSet', status: 'False' })).toBe('ok')
    expect(
      getJobSetConditionTone({
        type: 'StartupPolicyInProgress',
        status: 'True',
      }),
    ).toBe('unknown')
    expect(
      getJobSetConditionTone({
        type: 'StartupPolicyInProgress',
        status: 'False',
      }),
    ).toBe('ok')
    expect(
      getJobSetConditionTone({
        type: 'StartupPolicyCompleted',
        status: 'True',
      }),
    ).toBe('ok')
    expect(
      getJobSetConditionTone({
        type: 'StartupPolicyCompleted',
        status: 'False',
      }),
    ).toBe('unknown')
  })
})
