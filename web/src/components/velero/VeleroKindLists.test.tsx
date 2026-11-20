// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { parse } from 'yaml'
import { afterEach, expect, it, vi } from 'vitest'
import { workspace } from './velero.fixtures'
import { VeleroBackupsKindList, VeleroRestoresKindList } from './VeleroKindLists'

const state = vi.hoisted(() => ({ namespaces: [] as string[] }))
vi.mock('../../api/velero', () => ({
  useVeleroWorkspace: () => ({
    data: workspace({
      installations: state.namespaces.map(namespace => ({ namespace, server: { source: 'unread' }, defaultSource: 'unknown' })),
    }),
  }),
  useVeleroRuns: () => ({ data: { runs: [], total: 0, coverage: { state: 'full' } } }),
}))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))

afterEach(() => vi.unstubAllGlobals())

it.each([
  { kind: 'Backup', namespaces: ['velero'], target: 'velero' },
  { kind: 'Restore', namespaces: ['velero', 'kommander'], pick: 'kommander', target: 'kommander' },
  { kind: 'Backup', namespaces: ['velero', 'kommander'], target: '<velero-installation-namespace>' },
  { kind: 'Restore', namespaces: [], target: '<velero-installation-namespace>' },
])('seeds the $kind draft with $target for $namespaces (selection $pick)', ({ kind, namespaces, pick, target }) => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  state.namespaces = namespaces
  const host = document.createElement('div')
  const root = createRoot(host)
  const List = kind === 'Backup' ? VeleroBackupsKindList : VeleroRestoresKindList
  const create = vi.fn()
  const click = (label: string) => {
    const button = [...host.querySelectorAll('button')].find(b => b.textContent?.trim() === label)
    if (!button) throw new Error(`No button for ${label}`)
    act(() => button.click())
  }
  try {
    act(() => root.render(<List namespaces={['application']} inspected={null} onClearNamespaces={() => {}} onInspect={() => {}} onCreate={create} />))
    if (pick) click(pick)
    click(`Create ${kind}`)
    expect(create).toHaveBeenCalledOnce()
    expect(parse(create.mock.calls[0][1])).toMatchObject({ apiVersion: 'velero.io/v1', kind, metadata: { namespace: target } })
  } finally {
    act(() => root.unmount())
  }
})
