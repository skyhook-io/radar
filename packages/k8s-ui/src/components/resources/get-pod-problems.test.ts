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
})
