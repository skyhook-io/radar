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

it('shows failed rollout and suspension together in the actual Canary table', async () => {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const kind: APIResource = { group: 'flagger.app', version: 'v1beta1', kind: 'Canary', name: 'canaries', namespaced: true, isCrd: true, verbs: ['get', 'list'] }
  const data = { apiVersion: 'flagger.app/v1beta1', kind: 'Canary', metadata: { name: 'storefront', namespace: 'test' }, spec: { suspend: true, targetRef: { kind: 'Deployment', name: 'storefront' }, analysis: { iterations: 2 } }, status: { phase: 'Failed' } }
  await act(async () => root.render(<VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 48 }}><KeyboardShortcutProvider><ResourcesView namespaces={['test']} apiResources={[kind]} resourceCounts={{ Canary: 1 }} selectedKindQuery={{ isLoading: false, data: [data], resourceName: kind.name, group: kind.group }} locationPathname="/resources/canaries" locationSearch="?apiGroup=flagger.app" onNavigate={() => {}} /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>))
  const table = host.querySelector('table')!
  expect(table).not.toBeNull()
  expect(table.textContent).toContain('Rollout State')
  expect(table.textContent).toContain('Failed')
  expect(table.querySelector('.status-unhealthy')).not.toBeNull()
  expect(table.textContent).toContain('Blue/green')
  expect(table.textContent).toContain('Not weighted')
  expect(table.textContent).toContain('Suspend Requested')
  expect(table.textContent).toContain('Yes')
})
