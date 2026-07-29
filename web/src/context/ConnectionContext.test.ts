import { describe, expect, it } from 'vitest'

import { shouldApplyPolledConnection, shouldAutoRetryConnection } from './ConnectionContext'

describe('shouldAutoRetryConnection', () => {
  it('retries transient failures', () => {
    expect(shouldAutoRetryConnection('network')).toBe(true)
    expect(shouldAutoRetryConnection('timeout')).toBe(true)
    expect(shouldAutoRetryConnection(undefined)).toBe(true)
  })

  it('leaves auth-shaped failures to the server-side recovery loop', () => {
    expect(shouldAutoRetryConnection('auth')).toBe(false)
    expect(shouldAutoRetryConnection('auth-rejected')).toBe(false)
  })

  it('leaves configuration and RBAC errors for the user to resolve', () => {
    expect(shouldAutoRetryConnection('config')).toBe(false)
    expect(shouldAutoRetryConnection('rbac')).toBe(false)
  })
})

describe('shouldApplyPolledConnection', () => {
  it('recovers a missed disconnected SSE frame', () => {
    expect(shouldApplyPolledConnection('connected', 'disconnected', 2, 2)).toBe(true)
  })

  it('does not flash a connected UI back to startup progress', () => {
    expect(shouldApplyPolledConnection('connected', 'connecting', 2, 2)).toBe(false)
  })

  it('does not let a poll started before an SSE update overwrite it', () => {
    expect(shouldApplyPolledConnection('disconnected', 'connected', 1, 2)).toBe(false)
  })

  it('allows a fresh fallback poll to observe recovery after an SSE update', () => {
    expect(shouldApplyPolledConnection('disconnected', 'connected', 2, 2)).toBe(true)
  })
})
