import { describe, expect, it } from 'vitest'

import { shouldApplyPolledConnection, shouldAutoRetryConnection } from './ConnectionContext'

describe('shouldAutoRetryConnection', () => {
  it('retries runtime authentication loss', () => {
    expect(shouldAutoRetryConnection('auth')).toBe(true)
  })

  it('leaves configuration and RBAC errors for the user to resolve', () => {
    expect(shouldAutoRetryConnection('config')).toBe(false)
    expect(shouldAutoRetryConnection('rbac')).toBe(false)
  })
})

describe('shouldApplyPolledConnection', () => {
  it('recovers a missed disconnected SSE frame', () => {
    expect(shouldApplyPolledConnection('connected', 'disconnected')).toBe(true)
  })

  it('does not flash a connected UI back to startup progress', () => {
    expect(shouldApplyPolledConnection('connected', 'connecting')).toBe(false)
  })
})
