// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const state = vi.hoisted(() => ({
  support: 'supported',
  mutate: vi.fn(),
  error: null as Error | null,
  workspace: undefined as unknown,
  deletion: undefined as unknown,
  deletionError: null as Error | null,
  mutationData: undefined as { request: string } | undefined,
  fetching: false,
  success: vi.fn(),
  failure: vi.fn(),
}))
vi.mock('../../api/client', () => ({ useRadarFeature: () => ({ support: state.support }) }))
vi.mock('../../api/velero', () => ({
  useVeleroDeleteBackup: () => ({ mutate: state.mutate, reset: vi.fn(), isPending: false, error: state.error, data: state.mutationData }),
  useVeleroBackupDeletion: () => ({
    data: state.deletion,
    error: state.deletionError,
    isFetching: state.fetching,
  }),
  useVeleroWorkspace: () => ({ data: state.workspace }),
}))
vi.mock('../ui/Toast', () => ({ showApiSuccess: state.success, showApiError: state.failure }))

const { VeleroBackupDeletionRow, VeleroDeleteBackup, deleteRefusal } = await import('./VeleroDeleteBackup')

const backup = (phase = 'Completed') => ({
  metadata: { namespace: 'velero', name: 'nightly-1', uid: 'uid-1' },
  spec: { storageLocation: 'primary' },
  status: { phase },
})
const workspace = (location: { accessMode?: string; phase?: string }, replicas = 1) => ({
  locations: [{ namespace: 'velero', name: 'primary', ...location }],
  installations: [{ namespace: 'velero', server: { source: 'deployment', replicas } }],
})

let client = new QueryClient()
const roots: ReturnType<typeof createRoot>[] = []
function render(node: React.ReactNode) {
  const host = document.createElement('div')
  document.body.append(host)
  const root = createRoot(host)
  roots.push(root)
  act(() => root.render(<QueryClientProvider client={client}>{node}</QueryClientProvider>))
  return root
}
afterEach(() => {
  act(() => roots.splice(0).forEach((r) => r.unmount()))
  document.body.innerHTML = ''
  Object.assign(state, { support: 'supported', error: null, workspace: undefined, deletion: undefined, deletionError: null, mutationData: undefined, fetching: false })
  client = new QueryClient()
  state.mutate.mockReset()
  state.success.mockReset()
  state.failure.mockReset()
})

const buttons = (label: string) => [...document.querySelectorAll('button')].filter((b) => b.textContent?.trim() === label)
function typeName(value: string) {
  const input = document.querySelector('input') as HTMLInputElement
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  act(() => {
    setter.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

describe('Delete backup', () => {
  it('says what Velero deletes, needs the name typed, and binds the request to the backup reviewed', () => {
    state.workspace = workspace({ accessMode: 'ReadWrite', phase: 'Available' })
    render(<VeleroDeleteBackup backup={backup()} />)
    act(() => buttons('Delete backup')[0].click())
    const text = document.body.textContent ?? ''
    expect(text).toContain('storage location primary')
    expect(text).toContain('volume snapshots')
    expect(text).toContain('restores made from it')
    expect(text).toContain('Velero would bring the object back')
    const confirm = buttons('Delete backup').at(-1)!
    expect(confirm.disabled).toBe(true)
    typeName('nightly-1')
    expect(confirm.disabled).toBe(false)
    act(() => confirm.click())
    expect(state.mutate).toHaveBeenCalledWith(
      expect.objectContaining({ namespace: 'velero', name: 'nightly-1', uid: 'uid-1' }),
      expect.anything(),
    )
  })

  const refusals: [string, ReturnType<typeof backup>, unknown, string][] = [
    ['a running backup', backup('InProgress'), workspace({ accessMode: 'ReadWrite', phase: 'Available' }), 'still running'],
    ['a read-only location', backup(), workspace({ accessMode: 'ReadOnly', phase: 'Available' }), 'read-only'],
    ['an unavailable location', backup(), workspace({ accessMode: 'ReadWrite', phase: 'Unavailable' }), 'not available'],
    ['a stopped Velero server', backup(), workspace({ accessMode: 'ReadWrite', phase: 'Available' }, 0), 'isn’t running'],
  ]
  it.each(refusals)('is unavailable for %s, saying why', (_case, b, ws, why) => {
    state.workspace = ws
    render(<VeleroDeleteBackup backup={b} />)
    expect(buttons('Delete backup')[0].disabled).toBe(true)
    const w = ws as ReturnType<typeof workspace>
    expect(deleteRefusal(b, w.locations[0], w.installations[0].server.replicas === 0)).toContain(why)
  })

  it('leaves the refusal to Velero when the location can’t be read', () => {
    expect(deleteRefusal(backup(), undefined, false)).toBeUndefined()
  })

  it('leaves completed deletion to the page gone state', () => {
    state.mutate.mockImplementation((_vars: unknown, opts: { onSuccess: (r: unknown) => void }) => opts.onSuccess({ request: 'nightly-1-abc', uid: 'uid-1' }))
    state.deletion = { state: 'inProgress', request: 'nightly-1-abc' }
    const root = render(<VeleroDeleteBackup backup={backup()} />)
    expect(document.body.textContent).toContain('Deleting…')
    state.deletion = { state: 'deleted' }
    act(() => root.render(<QueryClientProvider client={client}><VeleroDeleteBackup backup={{ ...backup() }} /></QueryClientProvider>))
    expect(state.success).not.toHaveBeenCalled()
    expect(document.body.textContent).not.toContain('Deleted')
    expect(document.body.textContent).not.toContain('Deleting…')
  })

  it('reports Velero refusing, and offers the action again', () => {
    state.mutate.mockImplementation((_vars: unknown, opts: { onSuccess: (r: unknown) => void }) => opts.onSuccess({ request: 'nightly-1-abc', uid: 'uid-1' }))
    const root = render(<VeleroDeleteBackup backup={backup()} />)
    act(() => buttons('Delete backup')[0].click())
    typeName('nightly-1')
    act(() => buttons('Delete backup').at(-1)!.click())
    state.deletion = { state: 'refused', request: 'nightly-1-abc', errors: ['backup storage location primary not found'] }
    act(() => root.render(<QueryClientProvider client={client}><VeleroDeleteBackup backup={{ ...backup() }} /></QueryClientProvider>))
    expect(state.failure).toHaveBeenCalledWith('Velero refused to delete backup nightly-1', 'backup storage location primary not found')
    expect(buttons('Delete backup')[0].disabled).toBe(false)
  })

  it('reports a deletion that failed partway, and that data may be gone', () => {
    state.mutate.mockImplementation((_vars: unknown, opts: { onSuccess: (r: unknown) => void }) => opts.onSuccess({ request: 'nightly-1-abc', uid: 'uid-1' }))
    const root = render(<VeleroDeleteBackup backup={backup()} />)
    act(() => buttons('Delete backup')[0].click())
    typeName('nightly-1')
    act(() => buttons('Delete backup').at(-1)!.click())
    state.deletion = { state: 'failed', request: 'nightly-1-abc', errors: ['backup storage location primary not found'] }
    act(() => root.render(<QueryClientProvider client={client}><VeleroDeleteBackup backup={{ ...backup() }} /></QueryClientProvider>))
    expect(state.failure).toHaveBeenCalledWith('Deleting backup nightly-1 failed', 'Some of its data may already have been removed. backup storage location primary not found')
    expect(buttons('Delete backup')[0].disabled).toBe(false)
  })


  it('keeps following the deletion when the drawer is closed and reopened, or expanded', () => {
    state.deletion = { state: 'inProgress', request: 'nightly-1-abc' }
    const root = render(<VeleroDeleteBackup backup={backup()} />)
    act(() => root.unmount())
    roots.splice(roots.indexOf(root), 1)
    render(<VeleroDeleteBackup backup={backup()} />)
    expect(document.body.textContent).toContain('Deleting…')
    expect(buttons('Delete backup')).toHaveLength(0)
  })

  it('says the deletion was requested when its status can’t be read, without offering it again', () => {
    state.mutationData = { request: 'nightly-1-abc' }
    state.fetching = true
    state.deletionError = new Error('no access to list Velero delete requests')
    render(<VeleroDeleteBackup backup={backup()} />)
    expect(document.body.textContent).toContain('Deletion requested')
    expect(buttons('Delete backup')).toHaveLength(0)
  })

  it('does not claim a deletion was requested when the initial lookup is denied', () => {
    state.deletionError = new Error('no access to list Velero delete requests')
    render(<VeleroDeleteBackup backup={backup()} />)
    expect(document.body.textContent).toContain('Deletion status unreadable')
    expect(document.body.textContent).not.toContain('Deletion requested')
  })

  it('keeps a recorded reading while a refresh fails', () => {
    state.deletion = { state: 'inProgress', request: 'nightly-1-abc' }
    state.deletionError = new Error('temporarily unavailable')
    render(<VeleroDeleteBackup backup={backup()} />)
    expect(document.body.textContent).toContain('Deleting…')
    expect(document.body.textContent).not.toContain('Deletion requested')
  })

  it('announces the outcome of a request sent here once, across remounts', () => {
    state.workspace = workspace({ accessMode: 'ReadWrite', phase: 'Available' })
    state.mutate.mockImplementation((_vars: unknown, opts: { onSuccess: (sent: { request: string }) => void }) => opts.onSuccess({ request: 'failed-request' }))
    const first = render(<VeleroDeleteBackup backup={backup()} />)
    act(() => buttons('Delete backup')[0].click())
    typeName('nightly-1')
    act(() => buttons('Delete backup').at(-1)!.click())
    act(() => first.unmount())
    roots.splice(roots.indexOf(first), 1)
    state.deletion = { state: 'failed', request: 'failed-request', errors: ['snapshot failed'] }
    const second = render(<VeleroDeleteBackup backup={backup()} />)
    act(() => second.unmount())
    roots.splice(roots.indexOf(second), 1)
    render(<VeleroDeleteBackup backup={backup()} />)
    expect(state.failure).toHaveBeenCalledTimes(1)
  })

  it('leaves a settled deletion found on opening to the At a glance, neither a header chip nor news', () => {
    state.deletion = { state: 'refused', request: 'older-request', created: new Date(Date.now() - 2 * 3_600_000).toISOString(), errors: ['backup storage location nowhere not found'] }
    render(<VeleroDeleteBackup backup={backup()} />)
    expect(document.body.textContent).not.toContain('refused')
    expect(state.failure).not.toHaveBeenCalled()
    expect(buttons('Delete backup')).toHaveLength(1)
  })

  it('says when Velero refused, how long ago, and why as it was at the time of the request', () => {
    state.deletion = { state: 'refused', request: 'older-request', created: new Date(Date.now() - 2 * 3_600_000).toISOString(), errors: ['backup storage location nowhere not found'] }
    render(<VeleroBackupDeletionRow backup={backup()} />)
    expect(document.body.textContent).toContain('Deletion')
    expect(document.body.textContent).toContain('Refused 2h ago — at the time, storage location nowhere didn’t exist; nothing was deleted')
    // The name reads as a name.
    expect(document.body.querySelector('.font-mono')?.textContent).toBe('nowhere')
  })

  it('says a refusal it does not recognise in Velero’s words', () => {
    state.deletion = { state: 'refused', request: 'r', errors: ['backup is still in progress'] }
    render(<VeleroBackupDeletionRow backup={backup()} />)
    expect(document.body.textContent).toContain('Refused — the backup was still running; nothing was deleted')
  })

  it('says on the page when a deletion failed partway', () => {
    state.deletion = { state: 'failed', request: 'r', errors: ['error deleting snapshot snap-1: throttled'] }
    render(<VeleroBackupDeletionRow backup={backup()} />)
    expect(document.body.textContent).toContain('Failed — Some of its data may already have been removed. error deleting snapshot snap-1: throttled')
  })

  it('has no row for a backup nobody asked Velero to delete', () => {
    state.deletion = { state: 'unknown' }
    render(<VeleroBackupDeletionRow backup={backup()} />)
    expect(document.body.textContent).toBe('')
    state.deletion = { state: 'waiting', request: 'r' }
    render(<VeleroBackupDeletionRow backup={backup()} />)
    expect(document.body.textContent).toBe('')
  })

  it('is absent on a Radar without the action', () => {
    state.support = 'unsupported'
    render(<VeleroDeleteBackup backup={backup()} />)
    expect(buttons('Delete backup')).toHaveLength(0)
  })
})
