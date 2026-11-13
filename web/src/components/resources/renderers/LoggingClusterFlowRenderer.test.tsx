import { describe, expect, it } from 'vitest'
import { controlNamespacesOf } from './LoggingClusterFlowRenderer'

describe('controlNamespacesOf', () => {
  it('lists each Logging control namespace once', () => {
    expect(controlNamespacesOf([{ spec: { controlNamespace: 'a' } }, { spec: { controlNamespace: 'a' } }, { spec: {} }], false)).toEqual(['a'])
  })
  it('is undefined while unknown, so the drawer claims nothing', () => {
    expect(controlNamespacesOf(undefined, false)).toBeUndefined()
    expect(controlNamespacesOf([{ spec: { controlNamespace: 'a' } }], true)).toBeUndefined()
  })
})
