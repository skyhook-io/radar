import { describe, expect, it } from 'vitest'
import { restartErrorMessage } from './client'

describe('restartErrorMessage', () => {
  it('points a paused refusal at the Resume rollout button instead of the server text', () => {
    const msg = restartErrorMessage({ error: 'Deployment ns/web: the Deployment is paused, so a restart would not roll out; resume it, then restart', error_code: 'workload_paused' }, 409)
    expect(msg).toContain('Resume rollout')
    expect(msg).not.toContain('ns/web')
  })
  it.each([
    ['workload_changed', 'This workload was replaced since you opened it. Refresh and try again.'],
    ['workload_terminating', 'This workload is being deleted and cannot be restarted.'],
    ['restart_not_stored', 'Kubernetes admission changed or removed the restart marker.'],
  ])('explains %s', (error_code, message) => {
    expect(restartErrorMessage({ error: 'server detail', error_code }, 409)).toBe(message)
  })

  it.each(['OnDelete requires manual pod deletion', 'the StatefulSet rolling update partition is 2'])('includes the strategy detail for %s', reason => {
    const error = `the update strategy would not restart all pods: ${reason}`
    expect(restartErrorMessage({ error, error_code: 'restart_not_applicable' }, 409)).toBe(error)
  })

  it('explains an inapplicable strategy when the server omits the detail', () => {
    expect(restartErrorMessage({ error_code: 'restart_not_applicable' }, 409)).toBe('This workload’s update strategy won’t restart all pods.')
  })

  it('keeps the server text for every other failure', () => {
    expect(restartErrorMessage({ error: 'deployments.apps "web" not found' }, 404)).toBe('deployments.apps "web" not found')
    expect(restartErrorMessage({}, 500)).toBe('HTTP 500')
  })
})
