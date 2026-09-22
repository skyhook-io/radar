import { afterEach, describe, expect, it, vi } from 'vitest'
import { handleSSEError } from './log-format'

afterEach(() => vi.restoreAllMocks())
describe('log stream error evidence', () => {
  it('preserves a structured server error and does not guess the opaque transport cause', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const close = vi.fn()
    expect(handleSSEError(new MessageEvent('error', { data: JSON.stringify({ error: 'Pod was deleted' }) }), 'Unable to open log stream', close)).toBe('Pod was deleted')
    expect(handleSSEError(new Event('error'), 'Unable to open log stream', close)).toBe('Unable to open log stream')
    expect(close).toHaveBeenCalledTimes(2)
  })
})
