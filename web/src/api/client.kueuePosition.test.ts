import { describe, expect, it } from 'vitest'
import { ApiError, kueuePositionRefetchInterval } from './client'
import { RadarFeatureUnsupportedError } from './radarFeatures'

const answer = (position?: Record<string, unknown>, eligible = true) => ({ data: { uid: 'u', eligible, position: position as never } })

describe('kueuePositionRefetchInterval', () => {
  it('polls answers that change by themselves', () => {
    for (const status of ['listed', 'not_listed', 'limit_reached']) {
      expect(kueuePositionRefetchInterval(answer({ status }))).toBe(30000)
    }
    expect(kueuePositionRefetchInterval(answer({ status: 'unavailable', code: 'read_error' }))).toBe(30000)
    expect(kueuePositionRefetchInterval(answer({ status: 'unavailable', code: 'user_permission_check_failed' }))).toBe(30000)
  })

  it('stops on answers only a change of access or cluster resolves', () => {
    for (const status of ['not_served', 'unsupported', 'no_local_queue', 'queue_unknown']) {
      expect(kueuePositionRefetchInterval(answer({ status }))).toBe(false)
    }
    for (const code of ['user_forbidden', 'radar_forbidden', 'radar_credentials_rejected']) {
      expect(kueuePositionRefetchInterval(answer({ status: 'unavailable', code }))).toBe(false)
    }
    expect(kueuePositionRefetchInterval(answer(undefined, false))).toBe(false)
  })

  it('stops on client errors and an older Radar, keeps polling through transient failures', () => {
    expect(kueuePositionRefetchInterval({ error: new ApiError('forbidden', 403) })).toBe(false)
    expect(kueuePositionRefetchInterval({ error: new RadarFeatureUnsupportedError('kueuePendingPosition', {}) })).toBe(false)
    expect(kueuePositionRefetchInterval({ error: new ApiError('unavailable', 503) })).toBe(30000)
    expect(kueuePositionRefetchInterval({ ...answer({ status: 'listed' }), error: new ApiError('unavailable', 503) })).toBe(30000)
  })
})
