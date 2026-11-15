import { describe, expect, it } from 'vitest'
import { namespaceScopeRefetchInterval } from './client'

describe('namespaceScopeRefetchInterval', () => {
  it('retries a namespace list the server failed to read', () => {
    expect(namespaceScopeRefetchInterval({ listFailed: true })).toBe(3000)
  })
  it('keeps a real answer, and an older Radar that never says', () => {
    expect(namespaceScopeRefetchInterval({ listFailed: false })).toBe(false)
    expect(namespaceScopeRefetchInterval({})).toBe(false)
    expect(namespaceScopeRefetchInterval(undefined)).toBe(false)
  })
})
