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

it('shows reconciliation and labeled Pod snapshots in the actual collector table', async () => {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const kind: APIResource = { group: 'opentelemetry.io', version: 'v1beta1', kind: 'OpenTelemetryCollector', name: 'opentelemetrycollectors', namespaced: true, isCrd: true, verbs: ['get', 'list'] }
  const data = { apiVersion: 'opentelemetry.io/v1beta1', kind: 'OpenTelemetryCollector', metadata: { name: 'collector', namespace: 'test' }, spec: { mode: 'deployment', managementState: 'managed' }, status: { conditions: [{ type: 'Ready', status: 'False', reason: 'ReconcileError' }], scale: { statusReplicas: '0/1' }, version: '0.162.0' } }
  await act(async () => root.render(<VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 48 }}><KeyboardShortcutProvider><ResourcesView namespaces={['test']} apiResources={[kind]} resourceCounts={{ OpenTelemetryCollector: 1 }} selectedKindQuery={{ isLoading: false, data: [data], resourceName: kind.name, group: kind.group }} locationPathname="/resources/opentelemetrycollectors" locationSearch="?apiGroup=opentelemetry.io" onNavigate={() => {}} /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>))
  const table = host.querySelector('table')!
  expect(table).not.toBeNull()
  expect(table.textContent).toContain('Reconciliation')
  expect(table.textContent).toContain('Reconcile failed')
  expect(table.textContent).toContain('Reported Pods')
  expect(table.textContent).toContain('0/1')
  expect(table.textContent).toContain('0.162.0')
})
