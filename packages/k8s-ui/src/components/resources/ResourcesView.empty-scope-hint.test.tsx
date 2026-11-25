// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { VirtuosoMockContext } from 'react-virtuoso'
import { afterEach, expect, it, vi } from 'vitest'
import { ResourcesView } from './ResourcesView'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import type { APIResource } from '../../types'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
Element.prototype.scrollIntoView = vi.fn()

const pods: APIResource = { group: '', version: 'v1', kind: 'Pod', name: 'pods', namespaced: true, isCrd: false, verbs: ['get', 'list'] }

let root: ReturnType<typeof createRoot> | undefined
let host: HTMLDivElement | undefined

afterEach(() => {
  act(() => root?.unmount())
  host?.remove()
  root = undefined
  host = undefined
  localStorage.clear()
  window.history.replaceState({}, '', '/')
})

function renderEmpty(options: { emptyScopeHint?: ReactNode; locationSearch?: string }) {
  const search = options.locationSearch ?? ''
  window.history.replaceState({}, '', `/resources/pods${search}`)
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  act(() => {
    root!.render(
      <VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 40 }}>
        <KeyboardShortcutProvider>
          <ResourcesView
            namespaces={['staging']}
            apiResources={[pods]}
            resourceCounts={{ Pod: 0 }}
            selectedKindQuery={{ isLoading: false, data: [], resourceName: 'pods', group: '' }}
            locationPathname="/resources/pods"
            locationSearch={search}
            onNavigate={() => {}}
            onClearNamespaces={() => {}}
            emptyScopeHint={options.emptyScopeHint}
          />
        </KeyboardShortcutProvider>
      </VirtuosoMockContext.Provider>,
    )
  })
}

function emptyStateClearButtons() {
  return [...host!.querySelectorAll<HTMLButtonElement>('button')].filter(btn => btn.textContent?.trim() === 'Clear filters')
}

it('omits the redundant empty-state Clear filters button when emptyScopeHint is shown and no other filters are active', () => {
  renderEmpty({ emptyScopeHint: <button type="button">Show all namespaces</button> })
  expect(host!.textContent).toContain('Show all namespaces')
  expect(emptyStateClearButtons()).toHaveLength(0)
})

it('keeps the empty-state Clear filters button when a non-namespace filter is active alongside emptyScopeHint', () => {
  renderEmpty({ emptyScopeHint: <button type="button">Show all namespaces</button>, locationSearch: '?search=nginx' })
  expect(host!.textContent).toContain('Show all namespaces')
  expect(emptyStateClearButtons()).toHaveLength(1)
})

it('keeps the empty-state Clear filters button when no emptyScopeHint is provided', () => {
  renderEmpty({})
  expect(host!.textContent).toContain('Searching in namespace: staging')
  expect(emptyStateClearButtons()).toHaveLength(1)
})
