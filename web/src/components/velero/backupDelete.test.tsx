import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { VeleroObjectDeleteNotice, genericDeleteOffered, veleroBackupDeletePath, veleroLocationMissing } from './backupDelete'
import { location, workspace } from './velero.fixtures'

const backup = (phase: string, storageLocation?: string) => ({
  metadata: { namespace: 'velero', name: 'nightly-1' },
  spec: storageLocation ? { storageLocation } : {},
  status: phase ? { phase } : {},
})
const known = (missing: boolean | undefined) => () => missing

describe('how a Velero Backup is deleted', () => {
  it('deletes the object of a backup Velero never started and gave no location', () => {
    for (const phase of ['', 'New']) {
      expect(veleroBackupDeletePath(backup(phase), 'supported', known(undefined))).toEqual({ kind: 'object', reason: 'unstored' })
    }
    // A schedule's template can name the location before Velero processes the run: Velero acts on a request for it.
    expect(veleroBackupDeletePath(backup('New', 'default'), 'supported', known(false))).toEqual({ kind: 'request' })
  })

  it('deletes the object of a rejected backup only when its location is known not to exist', () => {
    expect(veleroBackupDeletePath(backup('FailedValidation', 'gone'), 'supported', known(true))).toEqual({ kind: 'object', reason: 'rejectedNoLocation' })
    expect(veleroBackupDeletePath(backup('FailedValidation'), 'supported', known(undefined))).toEqual({ kind: 'object', reason: 'rejectedNoLocation' })
    expect(veleroBackupDeletePath(backup('FailedValidation', 'gone'), 'supported', known(undefined))).toEqual({ kind: 'request' })
    expect(veleroBackupDeletePath(backup('FailedValidation', 'default'), 'supported', known(false))).toEqual({ kind: 'request' })
  })

  it('asks Velero for every other backup', () => {
    for (const phase of ['Completed', 'PartiallyFailed', 'Failed', 'InProgress', 'Deleting']) {
      expect(veleroBackupDeletePath(backup(phase, 'default'), 'supported', known(false)), phase).toEqual({ kind: 'request' })
    }
  })

  it('falls back to the object on a Radar without Delete backup, and decides nothing while capabilities load', () => {
    expect(veleroBackupDeletePath(backup('Completed', 'default'), 'unsupported', known(false))).toEqual({ kind: 'object', reason: 'olderRadar' })
    expect(veleroBackupDeletePath(backup('New'), 'unknown', known(undefined))).toEqual({ kind: 'pending' })
    expect(veleroBackupDeletePath(undefined, 'supported', known(undefined))).toEqual({ kind: 'pending' })
    expect(veleroBackupDeletePath(undefined, 'unsupported', known(undefined))).toEqual({ kind: 'pending' })
    expect(veleroBackupDeletePath(undefined, 'supported', known(undefined))).toEqual({ kind: 'pending' })
    expect(veleroBackupDeletePath(undefined, 'unsupported', known(undefined))).toEqual({ kind: 'pending' })
  })

  it('offers the generic Delete for a Velero Backup only on the object path', () => {
    expect(genericDeleteOffered('backups', 'velero.io', { kind: 'request' })).toBe(false)
    expect(genericDeleteOffered('backups', 'velero.io', { kind: 'pending' })).toBe(false)
    expect(genericDeleteOffered('backups', 'velero.io', { kind: 'object', reason: 'unstored' })).toBe(true)
    expect(genericDeleteOffered('deployments', 'apps', { kind: 'request' })).toBe(true)
  })

  it('knows a location is missing only from a full read', () => {
    const read = workspace({ locations: [location({ name: 'default' })] })
    expect(veleroLocationMissing(read, 'velero', 'default')).toBe(false)
    expect(veleroLocationMissing(read, 'velero', 'gone')).toBe(true)
    expect(veleroLocationMissing({ ...read, coverage: { ...read.coverage, backupStorageLocations: { state: 'partial' } } }, 'velero', 'gone')).toBeUndefined()
    expect(veleroLocationMissing(undefined, 'velero', 'gone')).toBeUndefined()
  })
})

describe('the generic delete confirmation for a Velero Backup', () => {
  it('says nothing is in storage when Velero never stored the backup', () => {
    const unstored = renderToStaticMarkup(<VeleroObjectDeleteNotice reason="unstored" backup={backup('New')} />)
    expect(unstored).toContain('Nothing of this backup is in storage')
    expect(unstored).toContain('never started this backup')
    const rejected = renderToStaticMarkup(<VeleroObjectDeleteNotice reason="rejectedNoLocation" backup={backup('FailedValidation', 'gone')} />)
    expect(rejected).toContain('its storage location gone doesn’t exist')
  })

  it('says what stays behind when this Radar can’t ask Velero', () => {
    const html = renderToStaticMarkup(<VeleroObjectDeleteNotice reason="olderRadar" backup={backup('Completed', 'default')} />)
    expect(html).toContain('This deletes only the Backup object')
    expect(html).toContain('brings the object back')
  })
})
