// @vitest-environment jsdom
import { act, type ComponentProps } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ResourcesView } from './ResourcesView'
import { ResourceActionsBar } from '../shared/ResourceActionsBar'
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
  expect(host.querySelector<HTMLInputElement>('input[title="Select all"]')!.checked).toBe(false)
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

const deployments: APIResource = { group: 'apps', version: 'v1', kind: 'Deployment', name: 'deployments', namespaced: true, isCrd: false, verbs: ['get', 'list'] }
const widgets: APIResource = { group: 'example.com', version: 'v1', kind: 'Widget', name: 'widgets', namespaced: true, isCrd: true, verbs: ['get', 'list'] }
const checkboxKey = 'radar-resource-checkboxes'
const key = (value: string) => act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: value, bubbles: true })))
const required = <T extends Element>(selector: string, parent: ParentNode = host): T => {
  const element = parent.querySelector<T>(selector)
  if (!element) throw new Error(`Missing element: ${selector}`)
  return element
}
const clickButton = (label: string, parent: ParentNode = host) => {
  const button = [...parent.querySelectorAll('button')].find(candidate => candidate.textContent?.trim() === label)
  if (!button) throw new Error(`Missing button: ${label}`)
  act(() => button.click())
}
const renderBulkTable = (initialKind = pods, bulkEnabled = true) => {
  const selected = vi.fn()
  const remove = vi.fn<NonNullable<ComponentProps<typeof ResourcesView>['onBulkDelete']>>()
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const render = (kind = initialKind, items = [pod]) => act(() => root.render(<KeyboardShortcutProvider><ResourcesView
    namespaces={['test']}
    apiResources={[pods, deployments, widgets]}
    resourceCounts={{ [kind.kind]: items.length }}
    selectedKindQuery={{ isLoading: false, data: items, resourceName: kind.name, group: kind.group }}
    locationPathname={`/resources/${kind.name}`}
    locationSearch={kind.group ? `?apiGroup=${kind.group}` : ''}
    onNavigate={() => {}}
    onResourceClick={selected}
    onBulkDelete={bulkEnabled ? remove : undefined}
  /></KeyboardShortcutProvider>))
  render()
  return { selected, remove, render }
}

it.each([pods, deployments, widgets])('hides selection by default on $kind and enables it without an empty action bar', kind => {
  renderBulkTable(kind)
  expect(host.querySelector('input[title="Select all"]')).toBeNull()
  act(() => required<HTMLButtonElement>('button[aria-label="Show selection checkboxes"]').click())
  expect(host.querySelector('input[title="Select all"]')).not.toBeNull()
  expect(host.textContent).not.toContain('0 selected')
  expect(localStorage.getItem(checkboxKey)).toBe('true')
})

it('remembers both visibility choices across remounts without remembering selected rows', () => {
  renderBulkTable()
  key('b')
  act(() => required<HTMLInputElement>('input[title="Select all"]').click())
  expect(host.textContent).toContain('1 selected')
  act(() => root.unmount()); host.remove()
  renderBulkTable(deployments)
  expect(host.querySelector('input[title="Select all"]')).not.toBeNull()
  expect(host.textContent).not.toContain('1 selected')
  key('b')
  expect(localStorage.getItem(checkboxKey)).toBe('false')
  act(() => root.unmount()); host.remove()
  renderBulkTable()
  expect(host.querySelector('input[title="Select all"]')).toBeNull()
})

it('keeps keyboard activation for inspection while checkboxes select', () => {
  const { selected } = renderBulkTable()
  key('b')
  act(() => required<HTMLInputElement>('input[title="Select all"]').click())
  expect(selected).not.toHaveBeenCalled()
  expect(host.textContent).toContain('1 selected')
  key('j'); key('Enter')
  expect(selected).toHaveBeenCalledTimes(1)
  expect(host.textContent).toContain('1 selected')
  key('Escape')
  expect(host.textContent).not.toContain('1 selected')
  expect(host.querySelector('input[title="Select all"]')).not.toBeNull()
  act(() => required<HTMLInputElement>('input[title="Select all"]').click())
  clickButton('Clear selection')
  expect(host.textContent).not.toContain('1 selected')
  expect(localStorage.getItem(checkboxKey)).toBe('true')
})

it('temporarily hides checkboxes for compare and clears selection on kind changes', () => {
  const { render } = renderBulkTable()
  key('b')
  act(() => required<HTMLInputElement>('input[title="Select all"]').click())
  act(() => required<HTMLButtonElement>('button:has(.lucide-git-compare)').click())
  expect(host.querySelector('input[title="Select all"]')).toBeNull()
  key('Escape')
  expect(host.querySelector('input[title="Select all"]')).not.toBeNull()
  expect(host.textContent).not.toContain('1 selected')
  act(() => required<HTMLInputElement>('input[title="Select all"]').click())
  render(deployments)
  expect(host.querySelector('input[title="Select all"]')).not.toBeNull()
  expect(host.textContent).not.toContain('1 selected')
  render(widgets)
  expect(host.querySelector('input[title="Select all"]')).not.toBeNull()
  expect(localStorage.getItem(checkboxKey)).toBe('true')
})

it('can show selection from compare without turning off the saved preference', () => {
  localStorage.setItem(checkboxKey, 'true')
  renderBulkTable()
  act(() => required<HTMLButtonElement>('button:has(.lucide-git-compare)').click())
  key('b')
  expect(host.querySelector('[aria-label="Compare resources tray"]')).toBeNull()
  expect(host.querySelector('input[title="Select all"]')).not.toBeNull()
  expect(localStorage.getItem(checkboxKey)).toBe('true')
})

it('confirms bulk deletion and clears successful selection without hiding checkboxes', () => {
  const { remove } = renderBulkTable()
  key('b')
  act(() => required<HTMLInputElement>('input[title="Select all"]').click())
  clickButton('Delete')
  const dialog = required<HTMLElement>('[role="dialog"]', document)
  expect(dialog.textContent).toContain('Delete 1 Pod?')
  expect(remove).not.toHaveBeenCalled()
  clickButton('Delete 1 resource', dialog)
  expect(remove).toHaveBeenCalledWith([{ kind: 'pods', group: '', namespace: 'test', name: 'example' }], expect.objectContaining({ force: false }))
  act(() => remove.mock.calls[0]?.[1]?.onSuccess?.())
  expect(host.textContent).not.toContain('1 selected')
  expect(host.querySelector('input[title="Select all"]')).not.toBeNull()
  expect(localStorage.getItem(checkboxKey)).toBe('true')
})

it('requires a new bulk confirmation after submission without clearing uncertain selection', async () => {
  const { remove, render } = renderBulkTable()
  const other = { ...pod, metadata: { ...pod.metadata, name: 'remaining', uid: 'remaining-id' } }
  render(pods, [pod, other])
  key('b')
  act(() => required<HTMLInputElement>('input[title="Select all"]').click())
  clickButton('Delete')
  const dialog = required<HTMLElement>('[role="dialog"]', document)
  clickButton('Delete 2 resources', dialog)
  expect(remove).toHaveBeenCalledTimes(1)
  expect(remove.mock.calls[0]?.[0]).toHaveLength(2)
  expect(dialog.closest('[inert]')).not.toBeNull()
  expect(host.textContent).toContain('2 selected')

  render(pods, [other])
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 300)) })
  expect(document.querySelector('[role="dialog"]')).toBeNull()
  expect(host.textContent).toContain('1 selected')
  expect(remove).toHaveBeenCalledTimes(1)
  clickButton('Delete')
  const nextDialog = required<HTMLElement>('[role="dialog"]', document)
  expect(nextDialog.textContent).toContain('Delete 1 Pod?')
  clickButton('Delete 1 resource', nextDialog)
  expect(remove.mock.calls[1]?.[0]).toEqual([{ kind: 'pods', group: '', namespace: 'test', name: 'remaining' }])
})

it.each(['false', 'garbage', 'null', '1'])('ignores invalid or disabled visibility value %s', value => {
  localStorage.setItem(checkboxKey, value)
  renderBulkTable()
  expect(host.querySelector('input[title="Select all"]')).toBeNull()
})

it('keeps toggling usable when browser storage is unavailable', () => {
  vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('Storage unavailable') })
  vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('Storage unavailable') })
  renderBulkTable()
  expect(host.querySelector('input[title="Select all"]')).toBeNull()
  key('b')
  expect(host.querySelector('input[title="Select all"]')).not.toBeNull()
  key('b')
  expect(host.querySelector('input[title="Select all"]')).toBeNull()
})

it('does not offer selection without a bulk action callback even with saved visibility', () => {
  localStorage.setItem(checkboxKey, 'true')
  renderBulkTable(pods, false)
  expect(host.querySelector('input[title="Select all"]')).toBeNull()
  expect(host.querySelector('button .lucide-list-checks')).toBeNull()
  key('b')
  expect(host.querySelector('input[title="Select all"]')).toBeNull()
  expect(localStorage.getItem(checkboxKey)).toBe('true')
})

it('closes single-delete confirmation on submission but keeps the inspected resource until known success', () => {
  const remove = vi.fn<NonNullable<ComponentProps<typeof ResourceActionsBar>['onDelete']>>()
  const close = vi.fn()
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  act(() => root.render(<ResourceActionsBar
    resource={{ kind: 'pods', namespace: 'test', name: 'example' }}
    data={pod}
    onDelete={remove}
    onClose={close}
  />))
  act(() => required<SVGElement>('svg[class*="trash"]').closest<HTMLButtonElement>('button')!.click())
  const dialog = required<HTMLElement>('[role="dialog"]', document)
  clickButton('Delete', dialog)
  expect(remove).toHaveBeenCalledTimes(1)
  expect(dialog.closest('[inert]')).not.toBeNull()
  expect(close).not.toHaveBeenCalled()
  act(() => remove.mock.calls[0]?.[1]?.onSuccess?.())
  expect(close).toHaveBeenCalledTimes(1)
})

it('preserves rows selected after a bulk delete was submitted', () => {
  const { remove, render } = renderBulkTable()
  key('b')
  act(() => required<HTMLInputElement>('input[title="Select all"]').click())
  clickButton('Delete')
  clickButton('Delete 1 resource', required<HTMLElement>('[role="dialog"]', document))
  const other = { ...pod, metadata: { ...pod.metadata, name: 'remaining', uid: 'remaining-id' } }
  render(pods, [pod, other])
  act(() => required<HTMLInputElement>('input[title="Deselect all"]').click())
  expect(host.textContent).toContain('2 selected')
  act(() => remove.mock.calls[0]?.[1]?.onSuccess?.())
  expect(host.textContent).toContain('1 selected')
  render(pods, [other])
  expect(host.textContent).toContain('1 selected')
})

it.each(['name', 'uid'])('does not close a newly inspected resource after an earlier delete succeeds (%s)', change => {
  const remove = vi.fn<NonNullable<ComponentProps<typeof ResourceActionsBar>['onDelete']>>()
  const close = vi.fn()
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const render = (name: string, uid: string, deleting = false) => act(() => root.render(<ResourceActionsBar
    resource={{ kind: 'pods', namespace: 'test', name }}
    data={{ ...pod, metadata: { ...pod.metadata, name, uid } }}
    onDelete={remove}
    onClose={close}
    isDeleting={deleting}
  />))
  render('example', 'original')
  act(() => required<HTMLButtonElement>('button[aria-label="Delete resource"]').click())
  clickButton('Delete', required<HTMLElement>('[role="dialog"]', document))
  render(change === 'name' ? 'other' : 'example', 'replacement', true)
  expect(required<HTMLButtonElement>('button[aria-label="Deleting resource"]').disabled).toBe(true)
  act(() => remove.mock.calls[0]?.[1]?.onSuccess?.())
  expect(close).not.toHaveBeenCalled()
})

it('ignores a delete success callback after its action bar is unmounted', () => {
  const remove = vi.fn<NonNullable<ComponentProps<typeof ResourceActionsBar>['onDelete']>>()
  const close = vi.fn()
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  act(() => root.render(<ResourceActionsBar resource={{ kind: 'pods', namespace: 'test', name: 'example' }} data={pod} onDelete={remove} onClose={close} />))
  act(() => required<HTMLButtonElement>('button[aria-label="Delete resource"]').click())
  clickButton('Delete', required<HTMLElement>('[role="dialog"]', document))
  act(() => root.render(<div>Another view</div>))
  act(() => remove.mock.calls[0]?.[1]?.onSuccess?.())
  expect(close).not.toHaveBeenCalled()
})
