import { describe, expect, it } from 'vitest'
import { rollbackErrorMessage } from './client'

describe('rollbackErrorMessage', () => {
  it('points a paused refusal at the Resume rollout button instead of the server text', () => {
    const msg = rollbackErrorMessage({ error: 'Deployment ns/web: the Deployment is paused, so a rollback would not roll out; resume it, then roll back', error_code: 'workload_paused' }, 409)
    expect(msg).toContain('Resume rollout')
    expect(msg).not.toContain('ns/web')
  })
  it('keeps the server text for every other failure', () => {
    expect(rollbackErrorMessage({ error: 'revision 9 not found' }, 404)).toBe('revision 9 not found')
    expect(rollbackErrorMessage({}, 500)).toBe('HTTP 500')
  })
})
