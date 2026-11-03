import { describe, expect, it } from 'vitest'
import { ApiError, isStableClientError } from './client'

describe('isStableClientError', () => {
  it('stops on answers that will repeat: rejected, denied, missing, recreated', () => {
    for (const status of [400, 403, 404, 409]) expect(isStableClientError(new ApiError('x', status))).toBe(true)
  })
  it('keeps going on an expired session, throttling, timeouts and server errors', () => {
    for (const status of [401, 408, 429, 500, 503]) expect(isStableClientError(new ApiError('x', status))).toBe(false)
    expect(isStableClientError(new Error('network'))).toBe(false)
  })
})
