// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ResourcesView } from './ResourcesView'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import type { APIResource } from '../../types'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
Element.prototype.scrollIntoView = vi.fn()
const pods: APIResource = { group: '', version: 'v1', kind: 'Pod', name: 'pods', namespaced: true, isCrd: false, verbs: ['get', 'list'] }
const pod = { apiVersion: 'v1', kind: 'Pod', metadata: { name: 'example', namespace: 'test', uid: 'pod-id' }, spec: { containers: [{ name: 'app', image: 'example' }] }, status: { phase: 'Running' } }
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement

afterEach(() => { act(() => root?.unmount()); host?.remove(); vi.restoreAllMocks(); localStorage.clear() })

describe('custom resource kind view', () => {
  it('keeps sidebar navigation while disabling table keys and column settings, then restores the table on null', () => {
    host = document.createElement('div'); document.body.append(host); root = createRoot(host)
    const selected = vi.fn()
    const storageRead = vi.spyOn(Storage.prototype, 'getItem')
    const storageWrite = vi.spyOn(Storage.prototype, 'setItem')
    const render = (custom: boolean | undefined) => act(() => root.render(<KeyboardShortcutProvider><ResourcesView
      namespaces={['test']}
      apiResources={[pods]}
      resourceCounts={{ Pod: 1 }}
      selectedKindQuery={{ isLoading: false, data: [pod], resourceName: 'pods', group: '' }}
      locationPathname="/resources/pods"
      locationSearch=""
      onNavigate={() => {}}
      onResourceClick={selected}
      renderKindView={kind => custom === undefined ? false : custom ? <div data-testid="kind-view">Custom {kind.kind}</div> : null}
    /></KeyboardShortcutProvider>))
    render(true)
    expect(host.querySelector('[data-testid="kind-view"]')?.textContent).toBe('Custom Pod')
    expect(host.querySelectorAll('table')).toHaveLength(0)
    expect(host.textContent).toContain('Workloads')
    act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'j', bubbles: true })))
    act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })))
    expect(selected).not.toHaveBeenCalled()
    expect(storageRead.mock.calls.some(([key]) => String(key).startsWith('radar-columns-'))).toBe(false)
    expect(storageWrite.mock.calls.some(([key]) => String(key).startsWith('radar-columns-'))).toBe(false)
    render(false)
    expect(host.querySelector('[data-testid="kind-view"]')).toBeNull()
    expect(host.querySelector('table')).not.toBeNull()
    expect(storageRead.mock.calls.some(([key]) => key === 'radar-columns-pods')).toBe(true)
    act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'j', bubbles: true })))
    act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })))
    expect(selected).toHaveBeenCalledWith(expect.objectContaining({ kind: 'pods', namespace: 'test', name: 'example' }))
    render(undefined)
    expect(host.querySelector('table')).not.toBeNull()
  })
})

it('closes bulk dialogs when the same kind switches to a custom view and keeps the inspected resource', async () => {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const selected = vi.fn()
  const remove = vi.fn()
  const render = (custom: boolean) => act(() => root.render(<KeyboardShortcutProvider><ResourcesView
    namespaces={['test']}
    apiResources={[pods]}
    resourceCounts={{ Pod: 1 }}
    selectedKindQuery={{ isLoading: false, data: [pod], resourceName: 'pods', group: '' }}
    selectedResource={{ kind: 'pods', group: '', namespace: 'test', name: 'example' }}
    locationPathname="/resources/pods"
    locationSearch=""
    onNavigate={() => {}}
    onResourceClick={selected}
    onBulkDelete={remove}
    renderKindView={() => custom ? <div data-testid="custom">Custom</div> : null}
  /></KeyboardShortcutProvider>))
  render(false)
  act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'b', bubbles: true })))
  act(() => host.querySelector<HTMLInputElement>('input[title="Select all"]')!.click())
  expect(host.textContent).toContain('1 selected')
  act(() => [...host.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Delete')!.click())
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain('Delete 1 Pod?')
  render(true)
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 300)) })
  expect(host.querySelector('[data-testid="custom"]')).not.toBeNull()
  expect(document.querySelector('[role="dialog"]')).toBeNull()
  render(false)
  expect(host.textContent).not.toContain('1 selected')
  expect(host.querySelector('input[title="Select all"]')).toBeNull()
  expect(document.querySelector('[role="dialog"]')).toBeNull()
  act(() => host.querySelector<SVGElement>('button .lucide-git-compare')!.closest<HTMLButtonElement>('button')!.click())
  act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'j', bubbles: true })))
  act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })))
  expect(host.querySelector('[aria-label="Compare resources tray"]')?.textContent).toContain('example')
  render(true)
  render(false)
  expect(host.querySelector('[aria-label="Compare resources tray"]')).toBeNull()
  expect(remove).not.toHaveBeenCalled()
  expect(selected).not.toHaveBeenCalled()
})

it('keeps sidebar kind shortcuts active while a custom view is shown', () => {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const deployments: APIResource = { group: 'apps', version: 'v1', kind: 'Deployment', name: 'deployments', namespaced: true, isCrd: false, verbs: ['get', 'list'] }
  act(() => root.render(<KeyboardShortcutProvider><ResourcesView
    namespaces={['test']}
    apiResources={[pods, deployments]}
    resourceCounts={{ Pod: 1, Deployment: 1 }}
    locationPathname="/resources/pods"
    locationSearch=""
    onNavigate={() => {}}
    renderKindView={kind => <div data-testid="kind-view">Custom {kind.kind}</div>}
  /></KeyboardShortcutProvider>))
  expect(host.querySelector('[data-testid="kind-view"]')?.textContent).toBe('Custom Pod')
  act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: ']', bubbles: true })))
  expect(host.querySelector('[data-testid="kind-view"]')?.textContent).not.toBe('Custom Pod')
  act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: '[', bubbles: true })))
  expect(host.querySelector('[data-testid="kind-view"]')?.textContent).toBe('Custom Pod')
})

it('skips hidden-table sort and filter aggregation for a loaded list, then resumes them with the table', () => {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const getSortValue = vi.fn((resource: typeof pod) => resource.metadata.name)
  const getFilterValue = vi.fn((resource: typeof pod) => resource.metadata.name)
  const extraColumns = [{ key: 'fixture-rank', label: 'Fixture rank', render: () => null, getSortValue, getFilterValue }]
  const list = [pod, { ...pod, metadata: { ...pod.metadata, name: 'another', uid: 'another-id' } }]
  localStorage.setItem('radar-sort-pods', JSON.stringify({ column: 'fixture-rank', direction: 'asc' }))
  const render = (custom: boolean, data = list) => act(() => root.render(<KeyboardShortcutProvider><ResourcesView
    namespaces={['test']}
    apiResources={[pods]}
    resourceCounts={{ Pod: data.length }}
    selectedKindQuery={{ isLoading: false, data, resourceName: 'pods', group: '' }}
    locationPathname="/resources/pods"
    locationSearch=""
    onNavigate={() => {}}
    extraLeadingColumns={extraColumns}
    renderKindView={() => custom ? <div data-testid="kind-view">Custom</div> : null}
  /></KeyboardShortcutProvider>))
  render(true)
  expect(getSortValue).not.toHaveBeenCalled()
  expect(getFilterValue).not.toHaveBeenCalled()
  render(false)
  expect(getSortValue).toHaveBeenCalled()
  expect(getFilterValue).toHaveBeenCalled()
  getSortValue.mockClear(); getFilterValue.mockClear()
  render(true, [...list])
  expect(getSortValue).not.toHaveBeenCalled()
  expect(getFilterValue).not.toHaveBeenCalled()
  render(false)
  expect(getSortValue).toHaveBeenCalled()
  expect(getFilterValue).toHaveBeenCalled()
})
