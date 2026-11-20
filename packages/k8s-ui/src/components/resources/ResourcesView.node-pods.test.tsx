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

it('shows an unknown Pod count, not zero, when node metrics are not loaded', async () => {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const kind: APIResource = { group: '', version: 'v1', kind: 'Node', name: 'nodes', namespaced: false, isCrd: false, verbs: ['get', 'list'] }
  const node = { apiVersion: 'v1', kind: 'Node', metadata: { name: 'gpu-a100-1', uid: 'n1' }, status: { allocatable: { pods: '110', cpu: '96', memory: '1100Gi' }, conditions: [{ type: 'Ready', status: 'True' }] } }
  await act(async () => root.render(<VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 48 }}><KeyboardShortcutProvider><ResourcesView namespaces={['ml-research']} apiResources={[kind]} resourceCounts={{ Node: 1 }} selectedKindQuery={{ isLoading: false, data: [node], resourceName: kind.name, group: kind.group }} locationPathname="/resources/nodes" locationSearch="" onNavigate={() => {}} /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>))
  const table = host.querySelector('table')!
  expect(table.textContent).toContain('gpu-a100-1')
  expect(table.textContent).not.toContain('0 / 110')
})
