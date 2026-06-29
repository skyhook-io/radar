import { describe, expect, it } from 'vitest'
import { getPodProblems } from './resource-utils'

describe('getPodProblems', () => {
  it('includes the pod status message for evicted pods', () => {
    const detail = 'Usage of EmptyDir volume "logs-nginx" exceeds the limit "2Gi".'

    expect(
      getPodProblems({
        status: {
          phase: 'Failed',
          reason: 'Evicted',
          message: detail,
        },
      }),
    ).toContainEqual({ severity: 'high', message: 'Evicted', detail })
  })

  it('keeps exit-code labels stable while surfacing terminated messages', () => {
    const detail = 'Container process exited after receiving SIGKILL.'

    expect(
      getPodProblems({
        status: {
          phase: 'Running',
          containerStatuses: [
            {
              name: 'api',
              restartCount: 0,
              state: {
                terminated: {
                  exitCode: 137,
                  reason: 'Error',
                  message: detail,
                },
              },
            },
          ],
        },
      }),
    ).toContainEqual({ severity: 'high', message: 'Exit Code 137', detail })
  })

  it('keeps waiting-state labels stable while surfacing kubelet messages', () => {
    const detail = 'Back-off pulling image "registry.example.com/api:missing".'

    expect(
      getPodProblems({
        status: {
          phase: 'Pending',
          containerStatuses: [
            {
              name: 'api',
              restartCount: 0,
              state: {
                waiting: {
                  reason: 'ImagePullBackOff',
                  message: detail,
                },
              },
            },
          ],
        },
      }),
    ).toContainEqual({ severity: 'critical', message: 'ImagePullBackOff', detail })
  })

  it('infers sandbox startup stalls only for scheduled pods and follows backend severity gates', () => {
    expect(
      getPodProblems({
        metadata: { creationTimestamp: new Date(Date.now() - 20 * 60 * 1000).toISOString() },
        spec: { nodeName: 'worker-1' },
        status: {
          phase: 'Pending',
          containerStatuses: [
            {
              name: 'api',
              restartCount: 0,
              state: { waiting: { reason: 'ContainerCreating' } },
            },
          ],
        },
      }),
    ).toContainEqual({ severity: 'high', message: 'Sandbox Startup Stalled' })

    expect(
      getPodProblems({
        metadata: { creationTimestamp: new Date(Date.now() - 45 * 60 * 1000).toISOString() },
        spec: { nodeName: 'worker-1' },
        status: {
          phase: 'Pending',
          containerStatuses: [
            {
              name: 'api',
              restartCount: 0,
              state: { waiting: { reason: 'ContainerCreating' } },
            },
          ],
        },
      }),
    ).toContainEqual({ severity: 'critical', message: 'Sandbox Startup Stalled' })

    expect(
      getPodProblems({
        metadata: { creationTimestamp: new Date(Date.now() - 45 * 60 * 1000).toISOString() },
        spec: { nodeName: 'worker-1' },
        status: {
          phase: 'Pending',
          conditions: [
            {
              type: 'PodScheduled',
              status: 'False',
              reason: 'Unschedulable',
            },
          ],
          containerStatuses: [
            {
              name: 'api',
              restartCount: 0,
              state: { waiting: { reason: 'ContainerCreating' } },
            },
          ],
        },
      }),
    ).not.toContainEqual(expect.objectContaining({ message: 'Sandbox Startup Stalled' }))
  })

  it('does not flag a completing Job pod (Running, container exited 0, Ready=false) as Not Ready', () => {
    expect(
      getPodProblems({
        status: {
          phase: 'Running',
          containerStatuses: [
            {
              name: 'job',
              ready: false,
              restartCount: 0,
              state: { terminated: { reason: 'Completed', exitCode: 0 } },
            },
          ],
        },
      }),
    ).not.toContainEqual(expect.objectContaining({ message: 'Not Ready' }))
  })
})
