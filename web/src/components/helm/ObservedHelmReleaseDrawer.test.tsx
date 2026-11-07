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

  it('keeps what it has through a passing failure, and says it did', () => {
    detail.current = { data: seen, error: new ApiError('unavailable', 503) }
    const text = mount(<ObservedHelmReleaseDrawer release={release} onClose={vi.fn()} />).textContent
    expect(text).toContain('Object health')
    expect(text).toContain('couldn’t refresh this, so it shows the last read')
  })
})

describe('tabs on an observed release', () => {
  const tabLabels = (container: HTMLElement) => [...container.querySelectorAll('button')].map((b) => b.textContent?.trim()).filter(Boolean)

  it('offers only what can hold data', () => {
    detail.current = { data: seen }
    const labels = tabLabels(mount(<ObservedHelmReleaseDrawer release={release} onClose={vi.fn()} />))
    expect(labels).toEqual(expect.arrayContaining(['Overview', 'Resources']))
    for (const gone of ['History', 'Manifest', 'Values', 'Hooks']) expect(labels).not.toContain(gone)
  })

  it('adds History where Flux recorded one', () => {
    detail.current = { data: { ...seen, fluxHistory: [{ revision: 1 }] } }
    expect(tabLabels(mount(<ObservedHelmReleaseDrawer release={release} onClose={vi.fn()} />))).toContain('History')
  })

  it('says what the read missed', () => {
    detail.current = { data: { ...seen, objectCoverage: { gaps: [{ code: 'kind_sync_pending' }] } } }
    expect(mount(<ObservedHelmReleaseDrawer release={release} onClose={vi.fn()} />).textContent).toContain('so this release may be missing more than usual')
  })
})
