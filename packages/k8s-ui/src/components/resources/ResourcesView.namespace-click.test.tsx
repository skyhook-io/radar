// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { VirtuosoMockContext } from 'react-virtuoso'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ResourcesView } from './ResourcesView'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import type { APIResource } from '../../types'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
Element.prototype.scrollIntoView = vi.fn()

const pods: APIResource = { group: '', version: 'v1', kind: 'Pod', name: 'pods', namespaced: true, isCrd: false, verbs: ['get', 'list'] }
const pod = (namespace: string, name: string) => ({
  apiVersion: 'v1', kind: 'Pod',
  metadata: { name, namespace, uid: `${namespace}/${name}`, creationTimestamp: '2026-01-01T00:00:00Z' },
  spec: { containers: [{ name: 'app', image: 'nginx' }] },
  status: { phase: 'Running' },
})
const data = [pod('shop', 'web-1'), pod('billing', 'api-1')]

let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
afterEach(() => { act(() => root?.unmount()); host?.remove(); localStorage.clear(); sessionStorage.clear(); window.history.replaceState(null, '', '/') })

function render(props: { namespaces?: string[]; search?: string; onResourceClick?: () => void; onNavigate?: (path: string, opts?: { replace?: boolean }) => void } = {}) {
  const search = props.search ?? ''
  window.history.replaceState(null, '', `/resources/pods${search}`)
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  act(() => root.render(
    <VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 48 }}>
    <KeyboardShortcutProvider>
      <ResourcesView
        namespaces={props.namespaces ?? []}
        apiResources={[pods]}
        resourceCounts={{ Pod: data.length }}
        selectedKindQuery={{ isLoading: false, data, resourceName: 'pods', group: '' }}
        locationPathname="/resources/pods"
        locationSearch={search}
        onNavigate={props.onNavigate ?? (() => {})}
        onResourceClick={props.onResourceClick ?? vi.fn()}
      />
    </KeyboardShortcutProvider>
    </VirtuosoMockContext.Provider>,
  ))
}

const namespaceLink = (ns: string) =>
  host.querySelector<HTMLAnchorElement>(`td[data-column-key="namespace"] a[aria-label="Filter to ${ns}"]`)
const listedNamespaces = () =>
  [...host.querySelectorAll('td[data-column-key="namespace"]')].map(td => td.textContent)
const click = (el: Element, init: MouseEventInit = {}) => {
  const e = new MouseEvent('click', { bubbles: true, cancelable: true, button: 0, ...init })
  act(() => { el.dispatchEvent(e) })
  return e
}

describe('clicking a namespace cell', () => {
  it("sets this list's Namespace filter without opening the row", () => {
    const onResourceClick = vi.fn()
    const onNavigate = vi.fn()
    render({ onResourceClick, onNavigate })
    const e = click(namespaceLink('billing')!)
    expect(e.defaultPrevented).toBe(true)
    expect(onResourceClick).not.toHaveBeenCalled()
    expect(listedNamespaces()).toEqual(['billing'])
    // Its own history entry, so Back returns to the unfiltered list.
    const [to, opts] = onNavigate.mock.calls.at(-1)!
    expect(new URLSearchParams((to as string).split('?')[1]).get('filters')).toBe('namespace:billing')
    expect(opts).toEqual({ replace: false })
  })

  it('replaces an exclude filter on Namespace rather than hiding the clicked namespace', () => {
    render({ search: '?filters=namespace:exclude:kube-system' })
    click(namespaceLink('shop')!)
    expect(listedNamespaces()).toEqual(['shop'])
  })

  it('keeps Enter on the cell from opening the highlighted row', () => {
    const onResourceClick = vi.fn()
    render({ onResourceClick })
    act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'j', bubbles: true })))
    const link = namespaceLink('shop')!
    link.focus()
    act(() => link.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })))
    expect(onResourceClick).not.toHaveBeenCalled()
  })

  it('filters on Space, which a link does not activate by itself', () => {
    render()
    act(() => namespaceLink('shop')!.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true })))
    expect(listedNamespaces()).toEqual(['shop'])
  })

  it('is plain text once the list is narrowed to that one namespace', () => {
    render({ namespaces: ['shop'] })
    expect(namespaceLink('shop')).toBeNull()
    expect(listedNamespaces()).toContain('shop')
    expect(namespaceLink('billing')).not.toBeNull()
  })

  it('links to this list filtered to the namespace, without the open drawer', () => {
    render({ search: '?search=web&resource=shop/web-1' })
    const params = new URLSearchParams(namespaceLink('shop')!.getAttribute('href')!)
    expect(params.get('filters')).toBe('namespace:shop')
    expect(params.get('search')).toBe('web')
    expect(params.has('resource')).toBe(false)
  })

  it('leaves a modified click to the browser, so it opens a new tab', () => {
    const onResourceClick = vi.fn()
    render({ onResourceClick })
    for (const mod of [{ metaKey: true }, { ctrlKey: true }]) {
      expect(click(namespaceLink('billing')!, mod).defaultPrevented).toBe(false)
    }
    expect(listedNamespaces()).toHaveLength(2)
    expect(onResourceClick).not.toHaveBeenCalled()
  })
})
