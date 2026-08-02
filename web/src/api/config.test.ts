import { describe, expect, it, afterEach } from 'vitest'

import { setBasename, stripBasename, routePath } from './config'

// setBasename mutates module state — restore standalone default after each test.
afterEach(() => {
  setBasename('')
})

describe('stripBasename', () => {
  it('passes paths through unchanged when no basename is configured', () => {
    expect(stripBasename('/resources/pods')).toBe('/resources/pods')
    expect(stripBasename('/')).toBe('/')
  })

  it('strips a configured basename prefix', () => {
    setBasename('/c/abc')
    expect(stripBasename('/c/abc/resources/pods')).toBe('/resources/pods')
    expect(stripBasename('/c/abc/timeline?range=1h')).toBe('/timeline?range=1h')
  })

  it('maps the bare basename to root', () => {
    setBasename('/c/abc')
    expect(stripBasename('/c/abc')).toBe('/')
  })

  it('strips when the basename is followed directly by a query string', () => {
    setBasename('/c/abc')
    expect(stripBasename('/c/abc?tab=events')).toBe('?tab=events')
  })

  it('leaves basename-relative paths unchanged (idempotent)', () => {
    setBasename('/c/abc')
    expect(stripBasename('/resources/pods')).toBe('/resources/pods')
    expect(stripBasename(stripBasename('/c/abc/resources/pods'))).toBe('/resources/pods')
  })

  it('does not strip a path that merely shares a string prefix', () => {
    setBasename('/c/abc')
    expect(stripBasename('/c/abcdef/resources')).toBe('/c/abcdef/resources')
  })

  it('inverts routePath', () => {
    setBasename('/c/abc')
    expect(stripBasename(routePath('/auth/login'))).toBe('/auth/login')
  })
})
