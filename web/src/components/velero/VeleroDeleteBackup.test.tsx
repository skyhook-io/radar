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
  restores: [] as { name: string; phase?: string }[],
  deleteObject: vi.fn(),
  restoresPending: false,
  restoresCoverage: 'full',
  restoresError: null as Error | null,
  success: vi.fn(),
  failure: vi.fn(),
}))
vi.mock('../../api/client', () => ({
  useRadarFeature: () => ({ support: state.support }),
  useDeleteResource: () => ({ mutate: state.deleteObject, isPending: false }),
  useCascadeDeletePreview: () => ({ data: undefined, isFetching: false, isError: false }),
}))
vi.mock('../../api/velero', () => ({
  useVeleroDeleteBackup: () => ({ mutate: state.mutate, reset: vi.fn(), isPending: false, error: state.error, data: state.mutationData }),
  useVeleroBackupDeletion: () => ({
    data: state.deletion,
    error: state.deletionError,
    isFetching: state.fetching,
  }),
  useVeleroWorkspace: () => ({ data: state.workspace }),
  useVeleroRuns: (_kind: string, _ns: string, _filter: unknown, options?: { enabled?: boolean }) => {
    const settled = options?.enabled && !state.restoresPending && !state.restoresError
    return {
      data: settled ? { runs: state.restores, coverage: { state: state.restoresCoverage } } : undefined,
      isPending: !settled && !state.restoresError,
      error: options?.enabled ? state.restoresError : null,
    }
  },
}))
vi.mock('../ui/Toast', () => ({ showApiSuccess: state.success, showApiError: state.failure }))

const { VeleroBackupDeletionRow, VeleroDeleteBackup, deleteRefusal } = await import('./VeleroDeleteBackup')

const backup = (phase = 'Completed') => ({
  metadata: { namespace: 'velero', name: 'nightly-1', uid: 'uid-1' },
  spec: { storageLocation: 'primary' },
  status: { phase },
})
const workspace = (location: { accessMode?: string; phase?: string }, replicas = 1, server: Record<string, unknown> = {}) => ({
  locations: [{ namespace: 'velero', name: 'primary', ...location }],
  installations: [{ namespace: 'velero', server: { source: 'deployment', replicas, ...server } }],
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
  Object.assign(state, { support: 'supported', error: null, workspace: undefined, deletion: undefined, deletionError: null, mutationData: undefined, fetching: false, restores: [], restoresPending: false, restoresCoverage: 'full', restoresError: null })
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

  it('is unavailable while Velero’s backup-deletion controller is disabled', () => {
    state.workspace = workspace({ accessMode: 'ReadWrite', phase: 'Available' }, 1, { deletionDisabled: true })
    render(<VeleroDeleteBackup backup={backup()} />)
    expect(buttons('Delete backup')[0].disabled).toBe(true)
    expect(deleteRefusal(backup(), { accessMode: 'ReadWrite', phase: 'Available' }, false, true)).toContain('backup-deletion controller disabled')
  })

  it('names the restores still reading from the backup', () => {
    state.workspace = workspace({ accessMode: 'ReadWrite', phase: 'Available' })
    state.restores = [{ name: 'restore-a', phase: 'InProgress' }, { name: 'restore-b', phase: 'Completed' }]
    render(<VeleroDeleteBackup backup={backup()} />)
    act(() => buttons('Delete backup')[0].click())
    const text = document.body.textContent ?? ''
    expect(text).toContain('Restore restore-a is still restoring from this backup')
    expect(text).not.toContain('restore-b')
  })

  it('holds the confirmation until the restores are read, then warns', () => {
    state.workspace = workspace({ accessMode: 'ReadWrite', phase: 'Available' })
    state.restoresPending = true
    const root = render(<VeleroDeleteBackup backup={backup()} />)
    act(() => buttons('Delete backup')[0].click())
    typeName('nightly-1')
    expect(document.body.textContent).toContain('Checking restores of this backup…')
    expect(buttons('Delete backup').at(-1)!.disabled).toBe(true)
    state.restoresPending = false
    state.restores = [{ name: 'restore-a', phase: 'New' }]
    act(() => root.render(<QueryClientProvider client={client}><VeleroDeleteBackup backup={{ ...backup() }} /></QueryClientProvider>))
    expect(document.body.textContent).toContain('Restore restore-a is still restoring from this backup')
    expect(document.body.textContent).not.toContain('Checking restores')
    expect(buttons('Delete backup').at(-1)!.disabled).toBe(false)
  })

  it.each([
    ['denied', { restoresCoverage: 'denied' }],
    ['failed', { restoresError: new Error('HTTP 500') }],
  ])('says the restores could not be checked when their read was %s', (_case, read) => {
    state.workspace = workspace({ accessMode: 'ReadWrite', phase: 'Available' })
    Object.assign(state, read)
    render(<VeleroDeleteBackup backup={backup()} />)
    act(() => buttons('Delete backup')[0].click())
    expect(document.body.textContent).toMatch(/Radar couldn’t check (every restore|the restores) of this backup/)
    typeName('nightly-1')
    expect(buttons('Delete backup').at(-1)!.disabled).toBe(false)
  })

  // Velero would refuse a request for it; the header's Delete object is offered instead.
  const unstored = (name: string) => ({ metadata: { namespace: 'velero', name, uid: `uid-${name}`, resourceVersion: '41' }, spec: {}, status: { phase: 'New' } })

  it('offers Delete object for a backup Velero never stored, bound to the object reviewed', () => {
    state.workspace = workspace({ accessMode: 'ReadWrite', phase: 'Available' })
    state.deletion = { state: 'unknown' }
    render(<VeleroDeleteBackup backup={unstored('nightly-1')} />)
    expect(buttons('Delete backup')).toHaveLength(0)
    act(() => buttons('Delete object')[0].click())
    const text = document.body.textContent ?? ''
    expect(text).toContain('Deletes the Backup object. Nothing of it is in storage.')
    expect(text).toContain('Nothing of this backup is in storage')
    expect(text).not.toContain('permanently delete')
    expect(text).not.toContain('Force delete')
    act(() => buttons('Delete object').at(-1)!.click())
    expect(state.deleteObject).toHaveBeenCalledWith({ kind: 'backups', group: 'velero.io', namespace: 'velero', name: 'nightly-1', uid: 'uid-nightly-1', resourceVersion: '41' })
  })

  it('offers neither delete until its deletion requests are read, and only follows one that is open', () => {
    state.workspace = workspace({ accessMode: 'ReadWrite', phase: 'Available' })
    render(<VeleroDeleteBackup backup={unstored('nightly-1')} />)
    expect(buttons('Delete backup')).toHaveLength(0)
    expect(buttons('Delete object')).toHaveLength(0)
    state.deletion = { state: 'waiting', request: 'nightly-2-x' }
    render(<VeleroDeleteBackup backup={unstored('nightly-2')} />)
    expect(buttons('Delete object')).toHaveLength(0)
    expect(document.body.textContent).toContain('Deleting…')
  })

  it('offers Delete object, not Delete backup again, after Velero refused the request', () => {
    state.workspace = workspace({ accessMode: 'ReadWrite', phase: 'Available' })
    state.deletion = { state: 'refused', request: 'nightly-1-x', errors: ['backup storage location not found'] }
    render(<VeleroDeleteBackup backup={unstored('nightly-1')} />)
    expect(buttons('Delete backup')).toHaveLength(0)
    expect(buttons('Delete object')).toHaveLength(1)
  })

  it('leaves out the reviewed-object binding a Radar without it would ignore', () => {
    state.support = 'unsupported'
    render(<VeleroDeleteBackup backup={{ ...backup('Completed'), metadata: { ...backup().metadata, resourceVersion: '7' } }} />)
    act(() => buttons('Delete object')[0].click())
    expect(document.body.textContent).toContain('Removes only the Backup object')
    act(() => buttons('Delete object').at(-1)!.click())
    expect(state.deleteObject).toHaveBeenCalledWith({ kind: 'backups', group: 'velero.io', namespace: 'velero', name: 'nightly-1' })
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
