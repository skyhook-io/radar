import { describe, expect, it } from 'vitest'
import { ApiError, isStableClientError, workloadPodsRefetchInterval } from './client'

describe('isStableClientError', () => {
  it('stops on answers that will repeat: rejected, denied, missing, recreated', () => {
    for (const status of [400, 403, 404, 409]) expect(isStableClientError(new ApiError('x', status))).toBe(true)
  })
  it('keeps going on an expired session, throttling, timeouts and server errors', () => {
    for (const status of [401, 408, 429, 500, 503]) expect(isStableClientError(new ApiError('x', status))).toBe(false)
    expect(isStableClientError(new Error('network'))).toBe(false)
  })
})

describe('workloadPodsRefetchInterval', () => {
  const polled = { ownerUID: 'uid', refetchInterval: 5000 }
  it('stops an owner-UID poll on an answer that will repeat and keeps it on one that may pass', () => {
    expect(workloadPodsRefetchInterval(new ApiError('denied', 403), polled)).toBe(false)
    expect(workloadPodsRefetchInterval(new ApiError('recreated', 409), polled)).toBe(false)
    expect(workloadPodsRefetchInterval(new ApiError('warming', 503), polled)).toBe(5000)
    expect(workloadPodsRefetchInterval(null, polled)).toBe(5000)
  })
  it('leaves callers without an owner UID polling as they asked', () => {
    expect(workloadPodsRefetchInterval(new ApiError('missing', 404), { refetchInterval: 5000 })).toBe(5000)
    expect(workloadPodsRefetchInterval(null, undefined)).toBe(false)
  })
})
