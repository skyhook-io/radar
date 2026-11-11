import { describe, expect, it } from 'vitest'
import { restartErrorMessage } from './client'

describe('restartErrorMessage', () => {
  it('points a paused refusal at the Resume rollout button instead of the server text', () => {
    const msg = restartErrorMessage({ error: 'Deployment ns/web: the Deployment is paused, so a restart would not roll out; resume it, then restart', error_code: 'workload_paused' }, 409)
    expect(msg).toContain('Resume rollout')
    expect(msg).not.toContain('ns/web')
  })
  it('keeps the server text for every other failure', () => {
    expect(restartErrorMessage({ error: 'deployments.apps "web" not found' }, 404)).toBe('deployments.apps "web" not found')
    expect(restartErrorMessage({}, 500)).toBe('HTTP 500')
  })
})
