import { describe, expect, it } from 'vitest'
import { isRBACCacheUnavailable } from './rbac'

function fetchError(status: number, message: string) {
  return Object.assign(new Error(message), { status })
}

describe('isRBACCacheUnavailable', () => {
  it('recognizes the settled answer from a Radar that cannot read RBAC', () => {
    expect(isRBACCacheUnavailable(fetchError(503, 'RBAC cache not available (informers not synced or RBAC reads disabled for the Radar SA)'))).toBe(true)
  })

  it('leaves other outages to the normal retry', () => {
    expect(isRBACCacheUnavailable(fetchError(503, 'Not connected to cluster'))).toBe(false)
    expect(isRBACCacheUnavailable(fetchError(500, 'RBAC cache exploded'))).toBe(false)
    expect(isRBACCacheUnavailable(null)).toBe(false)
  })
})
