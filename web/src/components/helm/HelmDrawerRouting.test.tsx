// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { HelmReleaseDrawer } from './HelmReleaseDrawer'

const state = vi.hoisted(() => ({
  error: null as unknown,
  support: 'supported' as 'supported' | 'unsupported',
  detailEnabled: [] as (boolean | undefined)[],
}))

vi.mock('../../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/client')>()),
  useHelmRelease: (_namespace: string, _name: string, options?: { enabled?: boolean }) => {
    state.detailEnabled.push(options?.enabled)
    return { data: undefined, error: state.error, isLoading: false, refetch: vi.fn() }
  },
  useRadarFeature: () => ({ support: state.support }),
}))
vi.mock('./HelmDrawerFrame', () => ({ HelmDrawerFrame: ({ children }: { children: unknown }) => children }))
vi.mock('./ObservedHelmReleaseDrawer', () => ({ ObservedHelmReleaseDrawer: () => 'observed drawer' }))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// The routing is what's under test, so the full drawer's own data hooks are out
// of the way: it throws a recognisable marker as soon as it renders.
vi.mock('react-router-dom', async (importOriginal) => ({
  ...(await importOriginal<typeof import('react-router-dom')>()),
  useNavigate: () => {
    throw new Error('full drawer')
  },
}))

let root: Root | undefined

function render(release: Parameters<typeof HelmReleaseDrawer>[0]['release']): string {
  const container = document.createElement('div')
  root = createRoot(container, { onCaughtError: () => {}, onUncaughtError: () => {} })
  try {
    act(() => root!.render(<HelmReleaseDrawer release={release} onClose={() => {}} />))
  } catch (err) {
    return (err as Error).message
  }
  return container.textContent ?? ''
}

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  state.error = null
  state.support = 'supported'
  state.detailEnabled = []
})

const denied = async () => {
  const { ApiError } = await vi.importActual<typeof import('../../api/client')>('../../api/client')
  return { ApiError, read: new ApiError('no', 403, { error_code: 'release_read_denied' }), other: new ApiError('no', 403) }
}

describe('HelmReleaseDrawer', () => {
  it('opens an observed row without asking for the release', () => {
    expect(render({ namespace: 'apps', name: 'api', observed: true })).toBe('observed drawer')
    expect(state.detailEnabled.every((enabled) => enabled === false)).toBe(true)
  })

  it('follows a shared link to the observed view when the read is the caller’s denial', async () => {
    state.error = (await denied()).read
    expect(render({ namespace: 'apps', name: 'api' })).toBe('observed drawer')
  })

  it('leaves any other 403 to the full drawer, which reports it', async () => {
    state.error = (await denied()).other
    expect(render({ namespace: 'apps', name: 'api' })).toBe('full drawer')
  })

  it('stays with the full drawer on a Radar without the inventory', async () => {
    state.error = (await denied()).read
    state.support = 'unsupported'
    expect(render({ namespace: 'apps', name: 'api' })).toBe('full drawer')
  })

  it('shows the full drawer to someone who can read the release', () => {
    expect(render({ namespace: 'apps', name: 'api' })).toBe('full drawer')
  })
})
