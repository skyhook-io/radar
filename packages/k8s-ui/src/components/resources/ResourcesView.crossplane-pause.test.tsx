// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { VirtuosoMockContext } from 'react-virtuoso'
import { afterEach, expect, it, vi } from 'vitest'
import { ResourcesView } from './ResourcesView'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import type { APIResource } from '../../types'
import type { PrinterTable } from './printer-columns'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
Element.prototype.scrollIntoView = vi.fn()
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement

afterEach(() => { act(() => root?.unmount()); host?.remove(); localStorage.clear() })

const pause = { type: 'Synced', status: 'False', reason: 'ReconcilePaused' }
const mr = { providerConfigRef: { name: 'default' } }
const xr = { resourceRefs: [] }
const claim = { resourceRef: { kind: 'Archive', name: 'xr' }, compositionRef: { name: 'archive' } }

it.each([
  ['managed', 'test.crossplane.io', mr, false],
  ['foreign-group managed', 'example.com', mr, false],
  ['composite default', 'example.com', xr, false],
  ['composite printer', 'example.com', xr, true],
  ['claim printer', 'example.com', claim, true],
  ['ref-less early composite printer', 'example.com', {}, true],
  ['ref-less early claim default', 'example.com', {}, false],
] as const)('keeps pause context and primary evidence on the real %s table path', async (_name, group, spec, printer) => {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const kind: APIResource = { group, version: 'v1', kind: 'Archive', name: 'archives', namespaced: true, isCrd: true, verbs: ['get', 'list'] }
  const resource = {
    apiVersion: `${group}/v1`, kind: 'Archive',
    metadata: { name: 'acknowledged', namespace: 'test', uid: 'fixture', annotations: { 'crossplane.io/paused': 'true' } },
    spec, status: { conditions: [pause, { type: 'Ready', status: 'False', reason: 'Unavailable' }] },
  }
  const printerTable: PrinterTable | undefined = printer ? {
    kind: kind.name, group,
    columns: [
      { name: 'Synced', jsonPath: ".status.conditions[?(@.type=='Synced')].status", type: 'string' },
      { name: 'Ready', jsonPath: ".status.conditions[?(@.type=='Ready')].status", type: 'string' },
    ],
    cells: { fixture: ['False', 'False'] },
  } : undefined
  await act(async () => root.render(<VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 48 }}><KeyboardShortcutProvider><ResourcesView
    namespaces={['test']}
    apiResources={[kind]}
    resourceCounts={{ Archive: 1 }}
    selectedKindQuery={{ isLoading: false, data: [resource], resourceName: kind.name, group }}
    locationPathname="/resources/archives"
    locationSearch={`?apiGroup=${group}`}
    onNavigate={() => {}}
    printerTable={printerTable}
  /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>))
  const row = host.querySelector('tbody tr')!
  expect(row.textContent).toContain('acknowledged')
  expect(row.textContent?.match(/Paused/g)).toHaveLength(1)
  if (printer) {
    const printed = [...row.querySelectorAll('td')].filter(cell => cell.textContent?.trim() === 'False')
    expect(printed).toHaveLength(2)
    expect(printed[0].querySelector('.status-neutral')).not.toBeNull()
    expect(printed[1].querySelector('.status-unhealthy')).not.toBeNull()
    expect(row.textContent).not.toContain('Unavailable')
  } else {
    expect(row.textContent).toContain('Unavailable')
    expect(row.textContent).not.toContain('Out of sync')
  }
})
