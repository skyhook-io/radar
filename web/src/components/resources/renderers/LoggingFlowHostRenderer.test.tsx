import { describe, expect, it } from 'vitest'
import { controlNamespacesOf, loggingsReadComplete } from './LoggingFlowHostRenderer'

describe('controlNamespacesOf', () => {
  it('lists each Logging control namespace once', () => {
    expect(controlNamespacesOf([{ spec: { controlNamespace: 'a' } }, { spec: { controlNamespace: 'a' } }, { spec: {} }], true)).toEqual(['a'])
  })
  it('is empty when a complete read found no Logging', () => {
    expect(controlNamespacesOf([], true)).toEqual([])
  })
  it('is undefined while unknown, so the drawer claims nothing', () => {
    expect(controlNamespacesOf(undefined, true)).toBeUndefined()
    expect(controlNamespacesOf([{ spec: { controlNamespace: 'a' } }], false)).toBeUndefined()
  })
})

describe('loggingsReadComplete', () => {
  it('trusts only a read that says it is complete', () => {
    expect(loggingsReadComplete({ isError: false, coverage: { status: 'complete' } })).toBe(true)
    expect(loggingsReadComplete({ isError: false, coverage: { status: 'partial' } })).toBe(false)
    expect(loggingsReadComplete({ isError: false, coverage: undefined })).toBe(false) // an older Radar's bare array
    expect(loggingsReadComplete({ isError: true, coverage: { status: 'complete' } })).toBe(false)
  })
})
