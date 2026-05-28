import { describe, expect, it } from 'vitest'
import { isFetchError, isForbiddenError } from './fetch-error'

function shaped(message: string, status: number) {
  return Object.assign(new Error(message), { status })
}

describe('isFetchError', () => {
  it('accepts an Error decorated with a numeric status', () => {
    expect(isFetchError(shaped('forbidden', 403))).toBe(true)
  })

  it('accepts a plain object with status and message', () => {
    expect(isFetchError({ status: 500, message: 'boom' })).toBe(true)
  })

  it('rejects a network failure without a status field', () => {
    expect(isFetchError(new Error('Failed to fetch'))).toBe(false)
  })

  it('rejects abort/cancel DOMException-style throws (no status)', () => {
    const aborted = new Error('The user aborted a request.')
    aborted.name = 'AbortError'
    expect(isFetchError(aborted)).toBe(false)
  })

  it('rejects undefined, null, primitives', () => {
    expect(isFetchError(undefined)).toBe(false)
    expect(isFetchError(null)).toBe(false)
    expect(isFetchError('forbidden')).toBe(false)
    expect(isFetchError(403)).toBe(false)
  })

  it('rejects an object with a non-numeric status', () => {
    expect(isFetchError({ status: '403', message: 'forbidden' })).toBe(false)
  })
})

describe('isForbiddenError', () => {
  it('is true only for 403 on a fetch-error shape', () => {
    expect(isForbiddenError(shaped('nope', 403))).toBe(true)
    expect(isForbiddenError(shaped('nope', 404))).toBe(false)
    expect(isForbiddenError(new Error('Failed to fetch'))).toBe(false)
    expect(isForbiddenError(null)).toBe(false)
  })
})
