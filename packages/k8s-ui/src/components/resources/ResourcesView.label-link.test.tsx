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

const group = 'velero.io'
const backups: APIResource = { group, version: 'v1', kind: 'Backup', name: 'backups', namespaced: true, isCrd: true, verbs: ['get', 'list'] }
const schedules: APIResource = { group, version: 'v1', kind: 'Schedule', name: 'schedules', namespaced: true, isCrd: true, verbs: ['get', 'list'] }
const nightly = { apiVersion: 'velero.io/v1', kind: 'Schedule', metadata: { name: 'nightly', namespace: 'velero', uid: 's1' }, spec: { schedule: '0 1 * * *' } }
const backup = (name: string, schedule: string) => ({
  apiVersion: 'velero.io/v1', kind: 'Backup',
  metadata: { name, namespace: 'velero', uid: name, labels: { 'velero.io/schedule-name': schedule } },
  status: { phase: 'Completed' },
})
const data = [backup('nightly-1', 'nightly'), backup('hourly-1', 'hourly')]

let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
afterEach(() => { act(() => root?.unmount()); host?.remove(); localStorage.clear(); window.history.replaceState(null, '', '/') })

function view(locationSearch: string, onResourceClick = vi.fn(), plural: 'backups' | 'schedules' = 'backups') {
  window.history.replaceState(null, '', `/resources/${plural}${locationSearch}`)
  return (
    <KeyboardShortcutProvider>
      <ResourcesView
        namespaces={[]}
        apiResources={[backups, schedules]}
        resourceCounts={{ Backup: 2, Schedule: 1 }}
        selectedKindQuery={{ isLoading: false, data: plural === 'backups' ? data : [nightly], resourceName: plural, group }}
        locationPathname={`/resources/${plural}`}
        locationSearch={locationSearch}
        onNavigate={() => {}}
        onResourceClick={onResourceClick}
      />
    </KeyboardShortcutProvider>
  )
}

// A schedule's "All N runs" opens this list filtered by its label, often while
// the list is already mounted for another view.
describe('a label filter in a link', () => {
  it('applies when the list is already open', () => {
    const firstRow = (selected: ReturnType<typeof vi.fn>) => {
      act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'j', bubbles: true })))
      act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })))
      return selected.mock.calls.at(-1)?.[0]?.name
    }
    host = document.createElement('div'); document.body.append(host); root = createRoot(host)
    // Open on the schedules, as the link is followed from a schedule's drawer.
    act(() => root.render(view(`?apiGroup=${group}`, vi.fn(), 'schedules')))
    const filtered = vi.fn()
    act(() => root.render(view(`?apiGroup=${group}&labels=${encodeURIComponent('velero.io/schedule-name=nightly')}`, filtered)))
    expect(firstRow(filtered)).toBe('nightly-1')
  })
})
