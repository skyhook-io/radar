// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { APIResource, SelectedResource } from '../../types'
import { KeyboardShortcutProvider, useActiveShortcuts, type KeyboardShortcut } from '../../hooks/useKeyboardShortcuts'
import { ResourcesView } from './ResourcesView'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
vi.stubGlobal('matchMedia', (query: string) => ({
  matches: false,
  media: query,
  onchange: null,
  addEventListener: () => {},
  removeEventListener: () => {},
  addListener: () => {},
  removeListener: () => {},
  dispatchEvent: () => false,
}))
Element.prototype.scrollIntoView = () => {}
vi.stubGlobal(
  'ResizeObserver',
  class {
    observe() {}
    unobserve() {}
    disconnect() {}
  },
)

const cnpgClusters: APIResource = {
  group: 'postgresql.cnpg.io',
  version: 'v1',
  kind: 'Cluster',
  name: 'clusters',
  namespaced: true,
  isCrd: true,
  verbs: ['list', 'get', 'watch'],
}

const pg = (name: string): SelectedResource => ({ kind: 'clusters', group: 'postgresql.cnpg.io', namespace: 'pg', name })

let root: Root | null = null
afterEach(async () => {
  await act(async () => root?.unmount())
  root = null
})

function mount(search: string, selectedResource: SelectedResource | null) {
  // The page reads the deep link from the real URL on mount.
  window.history.replaceState(null, '', `/resources/clusters${search}`)
  const element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
  const onNavigate = vi.fn()
  const onResourceClick = vi.fn()
  let shortcuts: KeyboardShortcut[] = []
  function Probe() {
    shortcuts = useActiveShortcuts()
    return null
  }
  const render = async (selected: SelectedResource | null) =>
    act(async () => {
      root!.render(
        <KeyboardShortcutProvider>
          <Probe />
          <ResourcesView
            namespaces={[]}
            apiResources={[cnpgClusters]}
            locationPathname="/resources/clusters"
            locationSearch={search}
            onNavigate={onNavigate}
            selectedResource={selected}
            onResourceClick={onResourceClick}
            renderKindView={(kind) => (kind.name === 'clusters' && kind.group === 'postgresql.cnpg.io' ? <div data-kind-view>Clusters view</div> : null)}
          />
        </KeyboardShortcutProvider>,
      )
    })
  return { element, onNavigate, onResourceClick, render, shortcuts: () => shortcuts, initial: render(selectedResource) }
}

describe('ResourcesView renderKindView', () => {
  it('renders the kind view in place of the table and switches the table shortcuts off', async () => {
    const writes = vi.spyOn(Storage.prototype, 'setItem')
    const removals = vi.spyOn(Storage.prototype, 'removeItem')
    const m = mount('?apiGroup=postgresql.cnpg.io', null)
    await m.initial
    const tableStorage = (calls: unknown[][]) => calls.map(([key]) => String(key)).filter((key) => key.includes('clusters'))
    expect(tableStorage(writes.mock.calls), 'column/sort settings written while the table is not shown').toEqual([])
    expect(tableStorage(removals.mock.calls), 'column/sort settings cleared while the table is not shown').toEqual([])
    writes.mockRestore()
    removals.mockRestore()
    expect(m.element.querySelector('[data-kind-view]')).not.toBeNull()
    expect(m.element.querySelector('table')).toBeNull()
    expect(m.element.querySelector('input[placeholder^="Search"]')).toBeNull()
    const byId = (id: string) => m.shortcuts().find((s) => s.id === id)
    expect(byId('resources-search')?.enabled).toBe(false)
    expect(byId('resources-nav-down')?.enabled).toBe(false)
    // Kind navigation belongs to the page, not the table.
    expect(byId('resources-next-kind')?.enabled).not.toBe(false)
  })

  it('keeps the drawer: a ?resource= deep link opens it, and switching objects pushes history', async () => {
    const m = mount('?apiGroup=postgresql.cnpg.io&resource=pg/pg-a', null)
    await m.initial
    expect(m.onResourceClick).toHaveBeenCalledWith(expect.objectContaining({ kind: 'clusters', group: 'postgresql.cnpg.io', namespace: 'pg', name: 'pg-a' }))

    m.onNavigate.mockClear()
    await m.render(pg('pg-a'))
    await m.render(pg('pg-b'))
    const toB = m.onNavigate.mock.calls.find(([path]) => String(path).includes('resource=pg%2Fpg-b') || String(path).includes('resource=pg/pg-b'))
    expect(toB, `navigations: ${JSON.stringify(m.onNavigate.mock.calls)}`).toBeDefined()
    expect(toB?.[1]?.replace).not.toBe(true)
  })
})
