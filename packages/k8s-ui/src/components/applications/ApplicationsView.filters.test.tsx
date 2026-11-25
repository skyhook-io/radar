// @vitest-environment jsdom
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import { FilterLocationProvider } from '../../filter-state'
import { buildSingleAppEntry } from '../../utils/applications'
import { ApplicationsView } from './ApplicationsView'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root | undefined
function mountList(search: string, loading: boolean) {
  const host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  const entries = loading ? [] : [buildSingleAppEntry({ key: 'shop/api', name: 'api', health: 'healthy', workloads: [] })]
  function List() {
    const [searchParams, update] = useState(() => new URLSearchParams(search))
    return (
      <FilterLocationProvider value={{ searchParams, update }}>
        <KeyboardShortcutProvider>
          <ApplicationsView entries={entries} variant="single" loading={loading} onSelect={() => {}} />
        </KeyboardShortcutProvider>
      </FilterLocationProvider>
    )
  }
  act(() => root!.render(<List />))
  return host
}

beforeEach(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} unobserve() {} })
  vi.stubGlobal('matchMedia', (query: string) => ({ matches: false, media: query, addEventListener: () => {}, removeEventListener: () => {} }))
  Element.prototype.scrollIntoView = () => {}
})
afterEach(() => {
  act(() => root?.unmount())
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

describe.each([true, false])('selected filters with loading=%s', (loading) => {
  it('clears a bookmarked health tile before or after the inventory arrives', () => {
    const host = mountList('?health=unknown', loading)
    const tile = [...host.querySelectorAll<HTMLButtonElement>('button[aria-pressed="true"]')].find((b) => !b.closest('aside'))!
    expect(tile.getAttribute('aria-pressed')).toBe('true')
    expect(tile.textContent).toBe(loading ? 'Unknown' : '0Unknown')
    act(() => tile.click())
    expect(host.contains(tile)).toBe(false)
    if (!loading) expect(host.querySelector('tbody')?.textContent).toContain('api')
  })

  it('clears each selected facet independently when its value has no rows', () => {
    const host = mountList('?health=neutral&class=worker&type=addon&env=staging&source=skyhook', loading)
    for (const label of ['Idle', 'Worker', 'Add-on', 'staging', 'Skyhook']) {
      const option = [...host.querySelectorAll<HTMLButtonElement>('aside button[aria-pressed="true"]')].find((b) => b.textContent === label)
      expect(option, label).toBeDefined()
      act(() => option!.click())
      expect(host.contains(option!)).toBe(false)
    }
    if (!loading) expect(host.querySelector('tbody')?.textContent).toContain('api')
  })
})
