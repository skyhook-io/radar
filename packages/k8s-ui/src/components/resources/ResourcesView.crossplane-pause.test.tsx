// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { VirtuosoMockContext } from 'react-virtuoso'
import { afterEach, expect, it, vi } from 'vitest'
import { ResourcesView } from './ResourcesView'
import { getCellFilterValue, serializeColumnFilters } from './resource-utils'
import { getResourceStatus } from '../shared/ResourceRendererDispatch'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import type { APIResource } from '../../types'
import type { PrinterTable } from './printer-columns'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
Element.prototype.scrollIntoView = vi.fn()
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement

afterEach(() => { act(() => root?.unmount()); host?.remove(); localStorage.clear(); window.history.replaceState(null, '', '/') })

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


it('preserves curated package install failure ahead of a recorded pause in the table and filter', async () => {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const group = 'pkg.crossplane.io'
  const kind: APIResource = { group, version: 'v1', kind: 'Provider', name: 'providers', namespaced: false, isCrd: true, verbs: ['get', 'list'] }
  const resource = {
    apiVersion: `${group}/v1`, kind: 'Provider',
    metadata: { name: 'install-failure', uid: 'package-fixture', annotations: { 'crossplane.io/paused': 'true' } },
    spec: { package: 'example.invalid/provider:v1' },
    status: { conditions: [pause, { type: 'Installed', status: 'False', reason: 'UnpackFailed' }, { type: 'Healthy', status: 'True' }] },
  }
  await act(async () => root.render(<VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 48 }}><KeyboardShortcutProvider><ResourcesView
    namespaces={[]}
    apiResources={[kind]}
    resourceCounts={{ Provider: 1 }}
    selectedKindQuery={{ isLoading: false, data: [resource], resourceName: kind.name, group }}
    locationPathname="/resources/providers"
    locationSearch={`?apiGroup=${group}`}
    onNavigate={() => {}}
  /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>))
  const row = host.querySelector('tbody tr')!
  expect(row.textContent).toContain('UnpackFailed')
  expect(row.querySelector('.status-unhealthy')).not.toBeNull()
  expect(row.textContent).not.toContain('Readiness unknown')
  expect(getCellFilterValue(resource, 'status', 'providers')).toBe('UnpackFailed')
  expect(getCellFilterValue(resource, 'status', 'providers')).toBe(getResourceStatus('providers', resource)?.text)
})


it.each([
  ['ref-less generic', 'example.com', 'Archive', 'archives', {}, 'Ready (reported)', 'Ready (reported)'],
  ['managed-name collision', 'acm.aws.upbound.io', 'Certificate', 'certificates', mr, 'Ready (reported)', 'Ready (reported)'],
  ['curated Knative', 'serving.knative.dev', 'Revision', 'revisions', {}, 'Ready', 'Scaled to Zero'],
] as const)('routes the actual %s status filter through the existing column policy', async (_name, group, kindName, plural, spec, filterText, cellText) => {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const kind: APIResource = { group, version: 'v1', kind: kindName, name: plural, namespaced: true, isCrd: true, verbs: ['get', 'list'] }
  const resource = {
    apiVersion: `${group}/v1`, kind: kindName,
    metadata: { name: 'kept', namespace: 'test', uid: 'kept-fixture', annotations: { 'crossplane.io/paused': 'true' } },
    spec, status: { conditions: [pause, { type: 'Ready', status: 'True' }, { type: 'Active', status: 'False', reason: 'NoTraffic' }] },
  }
  const excluded = { ...resource, metadata: { ...resource.metadata, name: 'excluded', uid: 'excluded-fixture' }, status: { conditions: [pause, { type: 'Ready', status: 'False', reason: 'Unavailable' }] } }
  const search = `?${new URLSearchParams({ apiGroup: group, filters: serializeColumnFilters({ status: [filterText] }) })}`
  window.history.replaceState(null, '', `/resources/${plural}${search}`)
  await act(async () => root.render(<VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 48 }}><KeyboardShortcutProvider><ResourcesView
    namespaces={['test']}
    apiResources={[kind]}
    resourceCounts={{ [kindName]: 2 }}
    selectedKindQuery={{ isLoading: false, data: [resource, excluded], resourceName: plural, group }}
    locationPathname={`/resources/${plural}`}
    locationSearch={search}
    onNavigate={() => {}}
  /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>))
  const rows = host.querySelectorAll('tbody tr')
  expect(rows).toHaveLength(1)
  expect(rows[0].textContent).toContain('kept')
  expect(rows[0].textContent).toContain(cellText)
  expect(rows[0].textContent).not.toContain('excluded')
})
