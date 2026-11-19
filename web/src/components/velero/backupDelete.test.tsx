import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { location, workspace } from './velero.fixtures'

vi.mock('../../api/client', () => ({ useDeleteResource: () => ({}), useCascadeDeletePreview: () => ({}) }))
const { VeleroObjectDeleteNotice, veleroBackupDeletePath, veleroLocationMissing, veleroObjectDeleteDetails } = await import('./backupDelete')

const backup = (phase: string, storageLocation?: string, meta: Record<string, unknown> = {}) => ({
  metadata: { namespace: 'velero', name: 'nightly-1', ...meta },
  spec: storageLocation ? { storageLocation } : {},
  status: phase ? { phase } : {},
})
const input = (over: Partial<Parameters<typeof veleroBackupDeletePath>[1]> = {}): Parameters<typeof veleroBackupDeletePath>[1] => ({
  deleteBackup: 'supported',
  preconditions: 'supported',
  locationMissing: () => undefined,
  request: 'none',
  ...over,
})

describe('how a Velero Backup is deleted', () => {
  it('deletes the object of a backup Velero never started and gave no location', () => {
    for (const phase of ['', 'New']) {
      expect(veleroBackupDeletePath(backup(phase), input())).toEqual({ kind: 'object', reason: 'unstored' })
    }
    // A schedule's template can name the location before Velero processes the run: Velero acts on a request for it.
    expect(veleroBackupDeletePath(backup('New', 'default'), input())).toEqual({ kind: 'request' })
    // Velero's queue may be handing these to the backup controller.
    for (const phase of ['Queued', 'ReadyToStart']) expect(veleroBackupDeletePath(backup(phase), input()), phase).toEqual({ kind: 'request' })
  })

  it('deletes the object of a rejected backup only when its location is known not to exist', () => {
    expect(veleroBackupDeletePath(backup('FailedValidation', 'gone'), input({ locationMissing: () => true }))).toEqual({ kind: 'object', reason: 'rejectedNoLocation' })
    expect(veleroBackupDeletePath(backup('FailedValidation'), input())).toEqual({ kind: 'object', reason: 'rejectedNoLocation' })
    expect(veleroBackupDeletePath(backup('FailedValidation', 'gone'), input())).toEqual({ kind: 'request' })
    expect(veleroBackupDeletePath(backup('FailedValidation', 'default'), input({ locationMissing: () => false }))).toEqual({ kind: 'request' })
  })

  it('asks Velero for every other backup', () => {
    for (const phase of ['Completed', 'PartiallyFailed', 'Failed', 'InProgress', 'Deleting']) {
      expect(veleroBackupDeletePath(backup(phase, 'default'), input()), phase).toEqual({ kind: 'request' })
    }
  })

  it('never doubles an open request, and waits until the requests are read', () => {
    expect(veleroBackupDeletePath(backup('New'), input({ request: 'open' }))).toEqual({ kind: 'request' })
    expect(veleroBackupDeletePath(backup('New'), input({ request: 'unknown' }))).toEqual({ kind: 'pending' })
  })

  it('offers it only where the delete is bound to the reviewed object, and not for one being deleted or holding finalizers', () => {
    expect(veleroBackupDeletePath(backup('New'), input({ preconditions: 'unsupported' }))).toEqual({ kind: 'request' })
    expect(veleroBackupDeletePath(backup('New', undefined, { deletionTimestamp: '2026-11-19T00:00:00Z' }), input())).toEqual({ kind: 'request' })
    expect(veleroBackupDeletePath(backup('New', undefined, { finalizers: ['example.io/hold'] }), input())).toEqual({ kind: 'request' })
  })

  it('falls back to the object on a Radar without Delete backup, but not for a running backup, and decides nothing before it loads', () => {
    expect(veleroBackupDeletePath(backup('Completed', 'default'), input({ deleteBackup: 'unsupported' }))).toEqual({ kind: 'object', reason: 'olderRadar' })
    expect(veleroBackupDeletePath(backup('InProgress', 'default'), input({ deleteBackup: 'unsupported' }))).toEqual({ kind: 'pending' })
    expect(veleroBackupDeletePath(backup('New'), input({ deleteBackup: 'unknown' }))).toEqual({ kind: 'pending' })
    expect(veleroBackupDeletePath(undefined, input())).toEqual({ kind: 'pending' })
    expect(veleroBackupDeletePath(undefined, input({ deleteBackup: 'unsupported' }))).toEqual({ kind: 'pending' })
  })

  it('knows a location is missing only where every location in its namespace was read', () => {
    const read = workspace({ locations: [location({ name: 'default' })] })
    expect(veleroLocationMissing(read, 'velero', 'default')).toBe(false)
    expect(veleroLocationMissing(read, 'velero', 'gone')).toBe(true)
    const partial = (allowedNamespaces?: string[]) => ({ ...read, coverage: { ...read.coverage, backupStorageLocations: { state: 'partial' as const, allowedNamespaces } } })
    expect(veleroLocationMissing(partial(), 'velero', 'gone')).toBeUndefined()
    expect(veleroLocationMissing(partial(['velero']), 'velero', 'gone')).toBe(true)
    expect(veleroLocationMissing(undefined, 'velero', 'gone')).toBeUndefined()
  })
})

describe('the Delete object confirmation', () => {
  it('says one thing for a backup Velero never stored: the object goes, nothing is in storage', () => {
    expect(veleroObjectDeleteDetails('unstored')).toBe('Deletes the Backup object. Nothing of it is in storage.')
    expect(veleroObjectDeleteDetails('rejectedNoLocation')).toBe('Deletes the Backup object. Nothing of it is in storage.')
    const unstored = renderToStaticMarkup(<VeleroObjectDeleteNotice reason="unstored" backup={backup('New')} />)
    expect(unstored).toContain('Nothing of this backup is in storage')
    expect(unstored).toContain('never started this backup')
    const rejected = renderToStaticMarkup(<VeleroObjectDeleteNotice reason="rejectedNoLocation" backup={backup('FailedValidation', 'gone')} />)
    expect(rejected).toContain('so it wrote nothing')
    expect(rejected).toContain('its storage location gone doesn’t exist')
  })

  it('says a schedule’s queued run holds it, and deleting it removes the hold', () => {
    const queued = backup('New', undefined, { labels: { 'velero.io/schedule-name': 'nightly' } })
    expect(renderToStaticMarkup(<VeleroObjectDeleteNotice reason="unstored" backup={queued} />)).toContain('While it is queued, Velero holds schedule nightly; deleting it removes that hold.')
    expect(renderToStaticMarkup(<VeleroObjectDeleteNotice reason="unstored" backup={backup('New')} />)).not.toContain('holds schedule')
  })

  it('says only the object goes when this Radar can’t ask Velero, never "permanently"', () => {
    const details = veleroObjectDeleteDetails('olderRadar')
    expect(details).toContain('Removes only the Backup object')
    expect(details).toContain('volume snapshots and uploaded volume data')
    expect(details).not.toContain('permanently')
    expect(renderToStaticMarkup(<VeleroObjectDeleteNotice reason="olderRadar" backup={backup('Completed', 'default')} />)).toContain('velero backup delete')
  })
})
