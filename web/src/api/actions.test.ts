import { describe, expect, it } from 'vitest'
import { capabilityReason, actionErrorCode, actionOutcomeLocked, actionDialogResult } from './actions'
import { ApiError } from './client'
import { RadarFeatureUnsupportedError } from './radarFeatures'

describe('capabilityReason', () => {
  it('says nothing for an allowed action, and the server reason when there is one', () => {
    expect(capabilityReason({ allowed: true, permission: 'allowed' })).toBeUndefined()
    expect(capabilityReason({ allowed: false, permission: 'allowed', reason: 'The Cluster is hibernated' })).toBe('The Cluster is hibernated')
  })
  it('names the missing grant when the reason is absent', () => {
    expect(capabilityReason({ allowed: false, permission: 'denied', grant: { verb: 'create', group: 'postgresql.cnpg.io', resource: 'backups', namespace: 'db' } })).toBe(
      'Your account may not do this (create backups (postgresql.cnpg.io) in namespace db)',
    )
    expect(capabilityReason({ allowed: false, permission: 'unknown' })).toBe('Not available')
  })
})

it('does not call a pre-request feature refusal an unknown write outcome', () => {
  const error = new RadarFeatureUnsupportedError('cnpgWorkspace', {})
  expect(actionErrorCode(error)).toBeUndefined()
  expect(actionOutcomeLocked(error)).toBe(false)
  expect(actionDialogResult(error)?.state).toBe('error')
})

describe('actionDialogResult', () => {
  it.each([
    [new ApiError('Review changed', 409, { code: 'changed' }), 'error'],
    [new ApiError('upstream connect error or disconnect/reset before headers', 503), 'unknown'],
    [new ApiError('The operator webhook did not answer', 503, { code: 'operator_webhook_unavailable' }), 'error'],
    [new ApiError('Gateway timeout', 504), 'unknown'],
    [new Error('Connection lost'), 'unknown'],
    [new ApiError('Write result missing', 500, { code: 'outcome_unknown' }), 'unknown'],
    [new ApiError('One volume removed', 409, { code: 'partial', completed: ['delete volume'] }), 'partial'],
  ] as const)('keeps the result classification for %s', (error, state) => {
    expect(actionDialogResult(error)?.state).toBe(state)
    expect(actionDialogResult(error)?.message).toBe(error.message)
  })

  it('selects the heading belonging to the result and retains no empty result', () => {
    const titles = { error: 'Review could not be loaded', unknown: 'The Cluster may have changed', partial: 'Earlier writes partially completed' }
    expect(actionDialogResult(new ApiError('Refused', 409), titles)?.title).toBe(titles.error)
    expect(actionDialogResult(new Error('Connection lost'), titles)?.title).toBe(titles.unknown)
    expect(actionDialogResult(new ApiError('Partial', 409, { code: 'partial' }), titles)?.title).toBe(titles.partial)
    expect(actionDialogResult(new ApiError('Partial', 409, { code: 'partial' }), { unknown: 'May have changed' })?.title).toBeUndefined()
    expect(actionDialogResult(null)).toBeUndefined()
  })
})
