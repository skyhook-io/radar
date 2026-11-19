import { describe, expect, it } from 'vitest'
import { VELERO_FAILED_PHASES, veleroCadenceText, veleroDeclaredRetention, veleroDeclaredTtl, veleroPlainReason, veleroReasonNextStep, veleroScheduleBadge } from './reading'

describe('veleroPlainReason', () => {
  it('says what the common object-store failures mean', () => {
    expect(veleroPlainReason('BackupStorageLocation "dr" is unavailable: rpc error: code = Unknown desc = NoSuchBucket: The specified bucket does not exist\n\tstatus code: 404')).toBe('The bucket doesn’t exist')
    expect(veleroPlainReason('rpc error: code = Unknown desc = AccessDenied: Access Denied\n\tstatus code: 403, request id: X')).toBe('Velero’s credentials can’t access the bucket')
    expect(veleroPlainReason('operation error S3: ListObjectsV2, https response error StatusCode: 403, api error Forbidden')).toBe('Velero’s credentials can’t access the bucket')
    expect(veleroPlainReason('InvalidAccessKeyId: The AWS Access Key Id you provided does not exist in our records.\n\tstatus code: 403')).toBe('Velero’s credentials are wrong')
    expect(veleroPlainReason('SignatureDoesNotMatch: The request signature we calculated does not match')).toBe('Velero’s credentials are wrong')
    expect(veleroPlainReason('dial tcp: lookup minio.velero.svc on 10.0.0.10:53: no such host')).toBe('Velero can’t reach the object store')
    expect(veleroPlainReason('Get "https://s3.example.com/bucket": dial tcp 10.1.2.3:443: i/o timeout')).toBe('Velero can’t reach the object store')
  })

  it('names a run Velero failed because it restarted', () => {
    expect(veleroPlainReason('found a backup with status "InProgress" during the server starting, mark it as "Failed"')).toBe('Velero restarted while this backup ran, so it was marked Failed')
    expect(veleroPlainReason('found a restore with status "InProgress" during the server starting, mark it as "Failed"')).toBe('Velero restarted while this restore ran, so it was marked Failed')
    expect(veleroReasonNextStep('found a restore with status "InProgress" during the server starting, mark it as "Failed"')).toBe('Run the restore again')
  })

  it('names the storage location Velero refused a run or a deletion over, without its kind name', () => {
    expect(veleroPlainReason('backup can\'t be created because BackupStorageLocation dr-replica is in Unavailable status.')).toBe('Its storage location dr-replica is unavailable')
    expect(veleroPlainReason('The BSL dr-replica is unavailable, cannot retrieve the backup')).toBe('Its storage location dr-replica is unavailable')
    expect(veleroPlainReason('cannot delete backup because backup storage location dr is currently in Unavailable state')).toBe('Its storage location dr is unavailable')
    expect(veleroPlainReason('cannot delete backup because backup storage location dr is currently in read-only mode')).toBe('Its storage location dr is read-only')
    expect(veleroPlainReason('backup can\'t be created because backup storage location dr is currently in read-only mode')).toBe('Its storage location dr is read-only')
    expect(veleroPlainReason('backup storage location nowhere not found')).toBe('Its storage location nowhere doesn’t exist')
    expect(veleroPlainReason('an existing backup storage location was not specified at backup creation time and the default default was not found. Please address this issue')).toBe('It names no storage location, and the default, default, doesn’t exist')
    expect(veleroPlainReason('backup is still in progress')).toBe('The backup was still running')
    expect(veleroPlainReason('backup not found')).toBe('Velero couldn’t find the backup')
    // Radar's own Issue message for an unavailable location keeps the store's reason.
    expect(veleroPlainReason('BackupStorageLocation "dr" is unavailable: some error Radar does not know')).toBeUndefined()
  })

  it('gives each recognised reason a next step', () => {
    expect(veleroReasonNextStep('NoSuchBucket: The specified bucket does not exist')).toBe('Create the bucket, or point the storage location at an existing one')
    expect(veleroReasonNextStep('InvalidAccessKeyId: nope')).toBe('Fix the credentials Secret Velero uses for this storage location')
    expect(veleroReasonNextStep('backup can\'t be created because BackupStorageLocation dr is in Unavailable status.')).toBe('Make storage location dr available, then try again')
    expect(veleroReasonNextStep('something else')).toBeUndefined()
  })

  it('leaves a message it does not recognise alone', () => {
    expect(veleroPlainReason(undefined)).toBeUndefined()
    expect(veleroPlainReason('')).toBeUndefined()
    expect(veleroPlainReason('Invalid included/excluded namespace lists: excludes list cannot contain an item in the includes list: demo')).toBeUndefined()
  })
})

describe('veleroScheduleBadge', () => {
  const run = (phase: string) => ({ name: 'nightly-1', phase, created: '2026-11-16T00:00:00Z', ranAt: '2026-11-16T00:00:00Z' })
  const base = { attention: false, zone: { name: 'UTC', source: 'serverDefault' as const }, runs: { read: 'full' as const, kept: 1, newest: run('Completed') } }

  it('reads a schedule due within its grace as on schedule while the server runs', () => {
    expect(veleroScheduleBadge({ ...base, state: 'due' })).toEqual({ label: 'On schedule', tone: 'healthy' })
    expect(veleroScheduleBadge({ ...base, state: 'due', reason: 'serverDown' }).label).toBe('Due')
  })

  it('says the last run failed instead of a healthy state', () => {
    const b = veleroScheduleBadge({ ...base, state: 'onSchedule', runs: { ...base.runs, newest: run('PartiallyFailed') } })
    expect(b.label).toBe('Last run failed')
    expect(b.tone).toBe('alert')
    expect(b.detail).toContain('partially failed')
    // A worse state keeps its own name.
    expect(veleroScheduleBadge({ ...base, state: 'late', attention: true, runs: { ...base.runs, newest: run('Failed') } }).label).toBe('Late')
  })

  it.each([...VELERO_FAILED_PHASES])('reads %s as the last run failing, on schedule or due', (phase) => {
    for (const state of ['onSchedule', 'due'] as const) {
      const b = veleroScheduleBadge({ ...base, state, runs: { ...base.runs, newest: run(phase) } })
      expect(b).toMatchObject({ label: 'Last run failed', tone: 'alert' })
      expect(b.detail).toContain(phase === 'Failed' ? 'failed' : phase === 'FailedValidation' ? 'was rejected' : 'partially failed')
    }
  })

  it('counts every partial phase as failed', () => {
    expect([...VELERO_FAILED_PHASES]).toEqual(expect.arrayContaining(['FinalizingPartiallyFailed', 'WaitingForPluginOperationsPartiallyFailed']))
  })

  it('keeps a healthy state out of green when the zone is not confirmed', () => {
    expect(veleroScheduleBadge({ ...base, state: 'onSchedule', zone: { name: 'UTC+02:00', source: 'inferred' } }).tone).toBe('unknown')
  })
})

describe('veleroCadenceText', () => {
  it.each([
    ['*/2 * * * *', 'Every 2 minutes'],
    ['15 1 * * *', 'Daily at 1:15'],
    ['15 * * * *', 'Every hour at :15'],
    ['0 2 * * 0', 'Weekly on Sunday at 2:00'],
    ['CRON_TZ=Europe/Berlin 0 2 * * *', 'Daily at 2:00'],
    ['@every 6h', 'Every 6h'],
    ['@daily', 'Daily at midnight'],
  ])('reads %s as %s', (cron, text) => {
    expect(veleroCadenceText(cron)).toBe(text)
  })

  it('leaves a shape it does not describe to the cron', () => {
    expect(veleroCadenceText('15 14 1 * *')).toBeUndefined()
    expect(veleroCadenceText('')).toBeUndefined()
  })
})

describe('a declared retention', () => {
  it('is the server default when absent or zero, as Velero applies it', () => {
    for (const ttl of [undefined, '', '0', '0s', '0h0m0s', '0.0s']) expect(veleroDeclaredTtl(ttl)).toBeUndefined()
    expect(veleroDeclaredRetention('0h0m0s')).toBeUndefined()
    expect(veleroDeclaredRetention('720h0m0s')).toBe('30 days')
    expect(veleroDeclaredTtl('1h30m')).toBe('1h30m')
    expect(veleroDeclaredTtl('soon')).toBe('soon')
    expect(veleroDeclaredTtl('0h1ms')).toBe('0h1ms')
    expect(veleroDeclaredTtl('0h0')).toBe('0h0')
  })

  // A crafted ttl is read in linear time.
  it('reads a long run of zero terms without backtracking', () => {
    const long = '0h'.repeat(50_000) + '!'
    const started = performance.now()
    expect(veleroDeclaredTtl(long)).toBe(long)
    expect(performance.now() - started).toBeLessThan(1000)
  })
})
