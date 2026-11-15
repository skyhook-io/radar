import { describe, expect, it } from 'vitest'
import { namespaceScopeRefetchInterval } from './client'

describe('namespaceScopeRefetchInterval', () => {
  it('retries a failed namespace list, backing off to 30s', () => {
    expect([1, 2, 3, 4, 5, 6, 20].map(n => namespaceScopeRefetchInterval({ listFailed: true }, n)))
      .toEqual([3000, 6000, 12000, 24000, 30000, 30000, 30000])
  })
  it('keeps a real answer, and an older Radar that never says', () => {
    expect(namespaceScopeRefetchInterval({ listFailed: false }, 3)).toBe(false)
    expect(namespaceScopeRefetchInterval({}, 0)).toBe(false)
    expect(namespaceScopeRefetchInterval(undefined, 0)).toBe(false)
  })
})
