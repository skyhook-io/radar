// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { VirtuosoMockContext } from 'react-virtuoso'
import { afterEach, expect, it, vi } from 'vitest'
import { ResourcesView } from './ResourcesView'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import type { APIResource } from '../../types'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
Element.prototype.scrollIntoView = vi.fn()
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
afterEach(() => { act(() => root?.unmount()); host?.remove(); localStorage.clear() })

it('shows definition columns and only served versions with storage/deprecation markers', async () => {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const kind: APIResource = { group: 'apiextensions.k8s.io', version: 'v1', kind: 'CustomResourceDefinition', name: 'customresourcedefinitions', namespaced: false, isCrd: false, verbs: ['get', 'list'] }
  const data = { apiVersion: 'apiextensions.k8s.io/v1', kind: 'CustomResourceDefinition', metadata: { name: 'widgets.example.io' }, spec: { group: 'example.io', names: { kind: 'Widget' }, scope: 'Namespaced', versions: [{ name: 'v1', served: true, storage: true }, { name: 'v1beta1', served: true, deprecated: true }, { name: 'v1alpha1', served: false }] }, status: { conditions: [{ type: 'Established', status: 'True' }] } }
  await act(async () => root.render(<VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 48 }}><KeyboardShortcutProvider><ResourcesView namespaces={[]} apiResources={[kind]} resourceCounts={{ CustomResourceDefinition: 1 }} selectedKindQuery={{ isLoading: false, data: [data], resourceName: kind.name, group: kind.group }} locationPathname="/resources/customresourcedefinitions" locationSearch="?apiGroup=apiextensions.k8s.io" onNavigate={() => {}} /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>))
  const table = host.querySelector('table')!
  expect(table).not.toBeNull()
  for (const text of ['Name', 'Kind', 'Scope', 'Served Versions', 'Status', 'Age', 'Widget', 'Namespaced', 'storage', 'deprecated', 'Established']) expect(table.textContent).toContain(text)
  expect(table.textContent).not.toContain('v1alpha1')
  expect(Array.from(table.querySelectorAll('th')).map(th => th.textContent).join(',')).not.toContain('Namespace')
})
