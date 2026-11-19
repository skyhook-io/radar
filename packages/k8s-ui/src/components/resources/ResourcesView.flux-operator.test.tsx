// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { VirtuosoMockContext } from 'react-virtuoso'
import { afterEach, expect, it, vi } from 'vitest'
import { ResourcesView, hasCuratedColumns } from './ResourcesView'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import type { APIResource } from '../../types'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
Element.prototype.scrollIntoView = vi.fn()
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
afterEach(() => { act(() => root?.unmount()); host?.remove(); localStorage.clear() })

it('curates Flux Operator kinds only under their own group', () => {
  for (const plural of ['fluxinstances', 'fluxreports', 'resourcesets', 'resourcesetinputproviders']) {
    expect(hasCuratedColumns(plural, 'fluxcd.controlplane.io')).toBe(true)
    expect(hasCuratedColumns(plural, 'example.com')).toBe(false)
  }
})

it('shows the Flux version and failing count in the FluxReport table', async () => {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const kind: APIResource = { group: 'fluxcd.controlplane.io', version: 'v1', kind: 'FluxReport', name: 'fluxreports', namespaced: true, isCrd: true, verbs: ['get', 'list'] }
  const data = { apiVersion: 'fluxcd.controlplane.io/v1', kind: 'FluxReport', metadata: { name: 'flux', namespace: 'flux-system' },
    spec: { distribution: { version: 'v2.7.5' }, operator: { version: 'v0.62.1' }, reconcilers: [{ kind: 'HelmRelease', stats: { running: 143, failing: 2 } }] },
    status: { conditions: [{ type: 'Ready', status: 'True' }] } }
  await act(async () => root.render(<VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 48 }}><KeyboardShortcutProvider><ResourcesView namespaces={['flux-system']} apiResources={[kind]} resourceCounts={{ FluxReport: 1 }} selectedKindQuery={{ isLoading: false, data: [data], resourceName: kind.name, group: kind.group }} locationPathname="/resources/fluxreports" locationSearch="?apiGroup=fluxcd.controlplane.io" onNavigate={() => {}} /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>))
  const table = host.querySelector('table')!
  expect(table).not.toBeNull()
  expect(table.textContent).toContain('v2.7.5')
  expect(table.textContent).toContain('v0.62.1')
  expect(table.textContent).toContain('Ready')
})
