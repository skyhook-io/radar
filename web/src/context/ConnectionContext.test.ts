import { describe, expect, it } from 'vitest'
import { QueryClient } from '@tanstack/react-query'

import { shouldApplyPolledConnection, shouldAutoRetryConnection, refreshQueriesAfterReconnect } from './ConnectionContext'

it('retains local drain activity during same-context reconnects, but clears it for another context', () => {
  const client = new QueryClient()
  const drainKey = ['node-drain-activity', '/api', 'worker', 'node-uid']
  client.setQueryData(drainKey, { status: 'pending' })
  client.setQueryData(['resources', 'nodes'], ['stale-node'])
  refreshQueriesAfterReconnect(client, 'original', 'original')
  expect(client.getQueryData(drainKey)).toEqual({ status: 'pending' })
  expect(client.getQueryData(['resources', 'nodes'])).toBeUndefined()
  refreshQueriesAfterReconnect(client, 'original', 'other')
  expect(client.getQueryData(drainKey)).toBeUndefined()
  client.clear()
})

it('uses the recorded context when SSE has already updated the connection state', () => {
  const client = new QueryClient()
  const drainKey = ['node-drain-activity', '/api', 'worker', 'node-uid']
  client.setQueryData(drainKey, { status: 'pending', contextName: 'original' })
  refreshQueriesAfterReconnect(client, 'other', 'other')
  expect(client.getQueryData(drainKey)).toBeUndefined()
  client.setQueryData(drainKey, { status: 'pending', contextName: 'same' })
  refreshQueriesAfterReconnect(client, '', 'same')
  expect(client.getQueryData(drainKey)).toMatchObject({ status: 'pending' })
  client.clear()
})

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
