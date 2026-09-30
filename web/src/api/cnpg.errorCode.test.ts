import { describe, expect, it } from 'vitest'
import { ApiError } from './client'
import { cnpgActionErrorCode } from './cnpg'

describe('cnpgActionErrorCode', () => {
  it('keeps the server code when there is one', () => {
    expect(cnpgActionErrorCode(new ApiError('changed', 409, { code: 'changed' }))).toBe('changed')
  })
  it('treats a lost connection or an apiserver timeout as an unknown outcome', () => {
    expect(cnpgActionErrorCode(new TypeError('Failed to fetch'))).toBe('outcome_unknown')
    expect(cnpgActionErrorCode(new ApiError('timeout', 504))).toBe('outcome_unknown')
    expect(cnpgActionErrorCode(new ApiError('internal', 500))).toBe('outcome_unknown')
  })
  it('treats refusals before any write as known failures', () => {
    expect(cnpgActionErrorCode(new ApiError('not connected', 503))).toBeUndefined()
    expect(cnpgActionErrorCode(new ApiError('forbidden', 403))).toBeUndefined()
    expect(cnpgActionErrorCode(null)).toBeUndefined()
  })
})
