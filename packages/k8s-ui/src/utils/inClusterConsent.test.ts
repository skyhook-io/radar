import { describe, it, expect, beforeEach, vi } from 'vitest'
import { inClusterConsentGiven, rememberInClusterConsent, consentRequestRows } from './inClusterConsent'

describe('inClusterConsent', () => {
  beforeEach(() => {
    const store: Record<string, string> = {}
    vi.stubGlobal('localStorage', {
      getItem: (k: string) => (k in store ? store[k] : null),
      setItem: (k: string, v: string) => { store[k] = v },
      removeItem: (k: string) => { delete store[k] },
    })
  })

  it('defaults to not-given', () => {
    expect(inClusterConsentGiven('prod')).toBe(false)
  })

  it('remembers consent for a cluster', () => {
    rememberInClusterConsent('prod')
    expect(inClusterConsentGiven('prod')).toBe(true)
  })

  it('is keyed per cluster - consent on one does not grant another', () => {
    rememberInClusterConsent('dev')
    expect(inClusterConsentGiven('dev')).toBe(true)
    expect(inClusterConsentGiven('prod')).toBe(false)
  })

  it('does not throw when localStorage is unavailable, and reports not-given', () => {
    vi.stubGlobal('localStorage', {
      getItem: () => { throw new Error('denied') },
      setItem: () => { throw new Error('denied') },
    })
    expect(() => rememberInClusterConsent('prod')).not.toThrow()
    expect(inClusterConsentGiven('prod')).toBe(false)
  })
})

// No stable cluster identity → consent is never remembered, in ANY store. A
// persisted fallback key is shared by every cluster a single origin (Radar
// Hub) serves, and even an in-memory session flag is shared across every
// identity-less cluster in the session - either would let one cluster's
// "don't ask again" suppress the pod-creating confirm on all of them. So an
// identity-less run always asks: given() is always false, remember() no-ops.
describe('inClusterConsent without a cluster identity', () => {
  it('never writes to localStorage', () => {
    const setItem = vi.fn()
    vi.stubGlobal('localStorage', { getItem: () => null, setItem })
    rememberInClusterConsent(undefined)
    rememberInClusterConsent('')
    expect(setItem).not.toHaveBeenCalled()
  })

  it('never remembers consent - remember() is a no-op and given() stays false', () => {
    vi.stubGlobal('localStorage', { getItem: () => null, setItem: () => {} })
    expect(inClusterConsentGiven(undefined)).toBe(false)
    rememberInClusterConsent(undefined)
    expect(inClusterConsentGiven(undefined)).toBe(false)
    expect(inClusterConsentGiven('')).toBe(false)
  })

  it('does not read a persisted key when no cluster is known', () => {
    // Even if some earlier version persisted a shared sentinel, an identity-less
    // check must not honor it.
    const store: Record<string, string> = { 'radar.inClusterConsent.current': '1' }
    vi.stubGlobal('localStorage', {
      getItem: (k: string) => (k in store ? store[k] : null),
      setItem: (k: string, v: string) => { store[k] = v },
    })
    expect(inClusterConsentGiven(undefined)).toBe(false)
  })

  it('identity-less remember does not leak into a named cluster', () => {
    vi.stubGlobal('localStorage', { getItem: () => null, setItem: () => {} })
    rememberInClusterConsent(undefined)
    expect(inClusterConsentGiven('prod')).toBe(false)
  })
})

describe('consent rows name the traffic the Job actually sends', () => {
  it('a TCP-only candidate is a TCP connection, never a fabricated GET', () => {
    const rows = consentRequestRows(
      [{ route: 'database', target: 'database:6379', inClusterRequest: { protocol: 'tcp' } }],
      '',
    )
    expect(rows).toEqual([{ route: 'database', request: 'TCP connection to database:6379' }])
  })
  it('HTTP(S) candidates keep the full request with dialled address and Host header', () => {
    const rows = consentRequestRows(
      [{ route: '/web', target: 'shop:80', inClusterRequest: { protocol: 'http', scheme: 'http', host: 'shop.example.com', path: '/web' } }],
      '',
    )
    expect(rows[0].request).toBe('GET http://shop:80/web (Host: shop.example.com)')
  })
  it('a path override applies to HTTP only - a TCP dial has no path to override', () => {
    const rows = consentRequestRows(
      [
        { route: 'database', target: 'database:6379', inClusterRequest: { protocol: 'tcp' } },
        { route: '/web', target: 'shop:80', inClusterRequest: { protocol: 'http', scheme: 'http', path: '/' } },
      ],
      '/healthz',
    )
    expect(rows[0].request).toBe('TCP connection to database:6379')
    expect(rows[1].request).toContain('/healthz')
  })
  it('benign dormant routes are never presented for approval', () => {
    expect(consentRequestRows([{ route: 'x', target: 'x:80', benign: true, inClusterRequest: { protocol: 'http' } }], '')).toEqual([])
  })
})
