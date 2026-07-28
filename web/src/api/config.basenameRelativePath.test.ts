import { afterEach, describe, expect, it } from 'vitest'
import { basenameRelativePath, setBasename } from './config'

afterEach(() => setBasename(''))

describe('basenameRelativePath', () => {
  it('returns the path unchanged when no basename is configured (standalone)', () => {
    expect(basenameRelativePath('/resources/pods?namespaces=default')).toBe(
      '/resources/pods?namespaces=default',
    )
  })

  it('strips the basename from a prefixed path, preserving the query', () => {
    setBasename('/c/abc')
    expect(basenameRelativePath('/c/abc/resources/pods?namespaces=kube-system')).toBe(
      '/resources/pods?namespaces=kube-system',
    )
  })

  it('collapses a doubled prefix', () => {
    setBasename('/c/abc')
    expect(basenameRelativePath('/c/abc/c/abc/resources/pods')).toBe('/resources/pods')
  })

  it('maps the bare basename to the root path', () => {
    setBasename('/c/abc')
    expect(basenameRelativePath('/c/abc')).toBe('/')
    expect(basenameRelativePath('/c/abc?namespaces=default')).toBe('/?namespaces=default')
  })

  it('does not strip a segment that merely shares the basename as a prefix', () => {
    setBasename('/c/abc')
    expect(basenameRelativePath('/c/abcdef/resources/pods')).toBe('/c/abcdef/resources/pods')
  })

  it('leaves unrelated paths untouched', () => {
    setBasename('/c/abc')
    expect(basenameRelativePath('/resources/pods')).toBe('/resources/pods')
    expect(basenameRelativePath('/auth/login')).toBe('/auth/login')
  })
})
