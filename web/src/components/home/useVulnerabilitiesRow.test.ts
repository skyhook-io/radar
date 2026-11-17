import { describe, expect, it } from 'vitest'
import { ApiError } from '../../api/client'
import { rowOnError } from './useVulnerabilitiesRow'

describe('rowOnError', () => {
  const api = (status: number, code?: string) => new ApiError('x', status, code ? { error: 'x', error_code: code } : { error: 'x' })

  it('keeps the last answer while Radar settles', () => {
    expect(rowOnError(api(409, 'connection_changed'))).toBe('keep')
    expect(rowOnError(api(503, 'kind_sync_pending'))).toBe('keep')
    expect(rowOnError(api(503, 'cluster_connecting'))).toBe('keep')
    expect(rowOnError(api(503))).toBe('keep')
  })

  it("says the read failed when Radar's own identity can't read the reports", () => {
    expect(rowOnError(api(503, 'radar_forbidden'))).toBe('failed')
    expect(rowOnError(api(503, 'list_too_large'))).toBe('failed')
    expect(rowOnError(api(500))).toBe('failed')
    expect(rowOnError(new Error('network'))).toBe('failed')
  })

  it('hides the row when it has nothing to say', () => {
    expect(rowOnError(api(404, 'kind_unknown'))).toBe('hide')
    expect(rowOnError(api(403, 'user_forbidden'))).toBe('hide')
  })
})
