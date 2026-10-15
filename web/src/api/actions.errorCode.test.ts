import { describe, expect, it } from 'vitest'
import { ApiError } from './client'
import { cnpgActionErrorCode } from './cnpg'
import { actionCompleted, actionOutcomeLocked } from './actions'

describe('cnpgActionErrorCode', () => {
  it('keeps the server code when there is one', () => {
    expect(cnpgActionErrorCode(new ApiError('changed', 409, { code: 'changed' }))).toBe('changed')
  })
  it('treats a lost connection or an apiserver timeout as an unknown outcome', () => {
    expect(cnpgActionErrorCode(new TypeError('Failed to fetch'))).toBe('outcome_unknown')
    expect(cnpgActionErrorCode(new ApiError('timeout', 504))).toBe('outcome_unknown')
    expect(cnpgActionErrorCode(new ApiError('internal', 500))).toBe('outcome_unknown')
  })
  it('treats an uncoded 503 as unknown: a proxy may have forwarded the write before the upstream reset', () => {
    const reset = new ApiError('upstream connect error or disconnect/reset before headers', 503)
    expect(cnpgActionErrorCode(reset)).toBe('outcome_unknown')
    expect(actionOutcomeLocked(reset)).toBe(true)
  })
  it('treats refusals before any write as known failures', () => {
    expect(cnpgActionErrorCode(new ApiError('webhook down', 503, { code: 'operator_webhook_unavailable' }))).toBe('operator_webhook_unavailable')
    expect(actionOutcomeLocked(new ApiError('webhook down', 503, { code: 'operator_webhook_unavailable' }))).toBe(false)
    expect(actionOutcomeLocked(new ApiError('Not connected to cluster', 503, { error: 'Not connected to cluster', code: 'unavailable' }))).toBe(false)
    expect(cnpgActionErrorCode(new ApiError('forbidden', 403))).toBeUndefined()
    expect(cnpgActionErrorCode(null)).toBeUndefined()
  })
})

describe('partial outcomes', () => {
  const partial = new ApiError('stopped', 403, { code: 'partial', completed: ['deleted PVC pg-2', 'deleted PVC pg-2-wal'] })
  it('locks confirm and lists what already happened, whatever the status', () => {
    expect(cnpgActionErrorCode(partial)).toBe('partial')
    expect(actionOutcomeLocked(partial)).toBe(true)
    expect(actionCompleted(partial)).toEqual(['deleted PVC pg-2', 'deleted PVC pg-2-wal'])
  })
  it('leaves known refusals unlocked', () => {
    expect(actionOutcomeLocked(new ApiError('changed', 409, { code: 'changed' }))).toBe(false)
    expect(actionCompleted(new ApiError('changed', 409, { code: 'changed' }))).toEqual([])
  })
})
