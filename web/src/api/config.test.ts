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

  // Standalone Radar sets a basename too once --base-path is configured, so the
  // session-expiry return path round-trips through this in a new shape. Store
  // strips once (client.ts) and restore strips again defensively (App.tsx) before
  // navigate() re-applies the basename — the net effect must be the original
  // route, never a doubled one.
  it('round-trips a return path under a base path without doubling', () => {
    setBasename('/radar')
    const stored = stripBasename('/radar/resources/pods') + '?namespaces=prod'
    expect(stored).toBe('/resources/pods?namespaces=prod')
    // Restore side strips a second time; already-relative values are untouched.
    expect(stripBasename(stored)).toBe('/resources/pods?namespaces=prod')
  })

  it('leaves a nested base path return path relative', () => {
    setBasename('/tools/radar')
    expect(stripBasename('/tools/radar/topology')).toBe('/topology')
    expect(stripBasename(stripBasename('/tools/radar/topology'))).toBe('/topology')
  })

  it('inverts routePath', () => {
    setBasename('/c/abc')
    expect(stripBasename(routePath('/auth/login'))).toBe('/auth/login')
  })
})
