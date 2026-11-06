// @vitest-environment jsdom
import { act, type ReactElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../../api/client'
import type { ObservedHelmReleaseDetail, SelectedHelmRelease } from '../../types'
import { ObservedHelmReleaseDrawer } from './ObservedHelmReleaseDrawer'

const detail = vi.hoisted(() => ({ current: {} as { data?: unknown; error?: unknown } }))

vi.mock('../../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/client')>()),
  useObservedHelmRelease: () => ({ ...detail.current, isLoading: false, refetch: vi.fn() }),
}))

const release = { namespace: 'team-a', name: 'web', observed: true } as SelectedHelmRelease
const seen = { name: 'web', namespace: 'team-a', chart: 'web', resources: [] } as unknown as ObservedHelmReleaseDetail

let root: Root | undefined

function mount(element: ReactElement): HTMLElement {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => root!.render(<MemoryRouter>{element}</MemoryRouter>))
  return container
}

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
})

describe('a refresh that fails while the drawer shows a release', () => {
  it('stops showing a release that is gone', () => {
    detail.current = { data: seen, error: new ApiError('not found', 404) }
    const text = mount(<ObservedHelmReleaseDrawer release={release} onClose={vi.fn()} />).textContent
    expect(text).toContain('No release found on the objects you can read')
    expect(text).not.toContain('Object health')
  })

  it('keeps what it has through a passing failure', () => {
    detail.current = { data: seen, error: new ApiError('unavailable', 503) }
    const text = mount(<ObservedHelmReleaseDrawer release={release} onClose={vi.fn()} />).textContent
    expect(text).toContain('Object health')
  })
})
