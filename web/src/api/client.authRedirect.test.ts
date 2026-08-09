import { beforeEach, describe, expect, it, vi } from 'vitest'

// Deterministic config so the 401 handler's basename/routePath math is fixed and
// independent of window/env. client.ts only consumes these six exports.
vi.mock('./config', () => ({
  getApiBase: () => '/api',
  getAuthHeaders: () => ({}),
  getCredentialsMode: () => 'include' as RequestCredentials,
  getBasename: () => '',
  routePath: (p: string) => p,
  stripBasename: (p: string) => p,
}))

interface NavRecorder {
  count: number
  urls: string[]
}

// Stub window.location (counting href assignments + reloads) and sessionStorage.
// Runs in vitest's node environment — there is no real DOM.
function installDom(pathname = '/traffic'): NavRecorder {
  const rec: NavRecorder = { count: 0, urls: [] }
  const location: Record<string, unknown> = {
    pathname,
    search: '',
    _href: `http://radar.local${pathname}`,
    reload: () => {
      rec.count++
      rec.urls.push('[reload]')
    },
  }
  Object.defineProperty(location, 'href', {
    get() {
      return location._href as string
    },
    set(v: string) {
      rec.count++
      rec.urls.push(v)
      location._href = v
    },
  })
  vi.stubGlobal('window', { location })

  const store = new Map<string, string>()
  vi.stubGlobal('sessionStorage', {
    getItem: (k: string) => (store.has(k) ? (store.get(k) as string) : null),
    setItem: (k: string, v: string) => {
      store.set(k, String(v))
    },
  })
  return rec
}

// A 401 whose body advertises the auth mode, matching what the backend returns
// on a protected /api/* call with no session.
function make401(authMode: string): Response {
  const body = JSON.stringify({ authMode })
  return {
    status: 401,
    ok: false,
    clone() {
      return make401(authMode)
    },
    async json() {
      return JSON.parse(body)
    },
  } as unknown as Response
}

beforeEach(() => {
  // Reset module state so the redirect gate starts fresh.
  vi.resetModules()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('apiFetch OIDC 401 -> login redirect (single-flight)', () => {
  it('navigates to /auth/login exactly once for a burst of concurrent 401s', async () => {
    const rec = installDom('/traffic')
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => make401('oidc')),
    )
    const { apiFetch } = await import('./client')

    // Mirror first paint / mid-session expiry: many protected calls 401 together.
    const paths = [
      '/connection',
      '/capabilities',
      '/namespaces',
      '/portforwards',
      '/dashboard',
      '/issues',
      '/auth/me',
      '/topology',
    ]
    await Promise.all(paths.map((p) => apiFetch(`/api${p}`)))

    expect(rec.count).toBe(1)
    expect(rec.urls).toEqual(['/auth/login'])
  })

  it('still redirects on the first 401 (guard does not suppress the initial navigation)', async () => {
    const rec = installDom('/traffic')
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => make401('oidc')),
    )
    const { apiFetch } = await import('./client')

    await apiFetch('/api/connection')

    expect(rec.count).toBe(1)
    expect(rec.urls).toEqual(['/auth/login'])
  })

  it('re-redirects after the throttle window (self-heals canceled nav / bfcache Back)', async () => {
    const rec = installDom('/traffic')
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => make401('oidc')),
    )
    const nowSpy = vi.spyOn(Date, 'now')
    const { apiFetch } = await import('./client')

    nowSpy.mockReturnValue(1_000_000)
    await apiFetch('/api/connection')
    expect(rec.count).toBe(1)

    // Within the window a repeat 401 is suppressed (no state rotation).
    nowSpy.mockReturnValue(1_000_000 + 2_000)
    await apiFetch('/api/connection')
    expect(rec.count).toBe(1)

    // Past the window it redirects again instead of stalling until a hard reload.
    nowSpy.mockReturnValue(1_000_000 + 6_000)
    await apiFetch('/api/connection')
    expect(rec.count).toBe(2)
  })

  it('still redirects (once) when sessionStorage is blocked', async () => {
    // Simulate private-mode / sandboxed storage where every access throws.
    const rec = installDom('/traffic')
    vi.stubGlobal('sessionStorage', {
      getItem: () => {
        throw new DOMException('blocked', 'SecurityError')
      },
      setItem: () => {
        throw new DOMException('blocked', 'SecurityError')
      },
    })
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => make401('oidc')),
    )
    const { apiFetch } = await import('./client')

    // Fail-open: the throwing read must not abort the redirect...
    await Promise.all(
      ['/connection', '/capabilities', '/namespaces'].map((p) => apiFetch(`/api${p}`)),
    )
    // ...and the in-memory fallback still collapses the burst to one navigation.
    expect(rec.count).toBe(1)
    expect(rec.urls).toEqual(['/auth/login'])
  })

  it('does not navigate to /auth/login when already on an /auth path', async () => {
    const rec = installDom('/auth/login')
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => make401('oidc')),
    )
    const { apiFetch } = await import('./client')

    await apiFetch('/api/connection')

    expect(rec.count).toBe(0)
    expect(rec.urls).toEqual([])
  })

  it('proxy/unknown mode reloads (once) instead of hitting /auth/login', async () => {
    const rec = installDom('/traffic')
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => make401('proxy')),
    )
    const { apiFetch } = await import('./client')

    await apiFetch('/api/connection')

    expect(rec.urls).toEqual(['[reload]'])
    expect(rec.urls).not.toContain('/auth/login')
  })
})
