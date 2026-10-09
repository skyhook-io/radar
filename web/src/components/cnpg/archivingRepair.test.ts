import { describe, expect, it } from 'vitest'
import { cnpgArchiveDestination, cnpgRecoveryBase, resumeBoundary, walAfter, type CNPGWALArchiver } from './archivingRepair'

describe('cnpgArchiveDestination', () => {
  it('names each credential Secret and key, never reading them', () => {
    const d = cnpgArchiveDestination(
      {
        destinationPath: 's3://radar-cnpg-demo/',
        endpointURL: 'http://192.0.2.1:9000',
        s3Credentials: { accessKeyId: { name: 'creds', key: 'ACCESS_KEY_ID' }, secretAccessKey: { name: 'creds', key: 'ACCESS_SECRET_KEY' } },
        endpointCA: { name: 'minio-ca', key: 'ca.crt' },
      },
      'ObjectStore pg-store',
    )
    expect(d).toMatchObject({ path: 's3://radar-cnpg-demo/', endpointURL: 'http://192.0.2.1:9000', declaredIn: 'ObjectStore pg-store' })
    expect(d.secrets).toEqual([
      { secret: 'creds', key: 'ACCESS_KEY_ID', what: 'S3 access key ID' },
      { secret: 'creds', key: 'ACCESS_SECRET_KEY', what: 'S3 secret access key' },
      { secret: 'minio-ca', key: 'ca.crt', what: 'endpoint CA bundle' },
    ])
    expect(d.identity).toBeUndefined()
  })
  it('says when credentials come from the workload identity instead', () => {
    expect(cnpgArchiveDestination({ destinationPath: 's3://b/', s3Credentials: { inheritFromIAMRole: true } }, 'x').identity).toContain('IAM role')
    expect(cnpgArchiveDestination({ destinationPath: 'gs://b/', googleCredentials: { gkeEnvironment: true } }, 'x').identity).toContain('GKE')
  })
})

describe('a base backup after archiving resumed', () => {
  const t = (iso: string) => Date.parse(iso)
  const cluster = (since?: string) => ({ status: { conditions: since ? [{ type: 'ContinuousArchiving', status: 'True', lastTransitionTime: since }] : [] } })
  const backup = (name: string, startedAt: string, extra: any = {}) => ({
    apiVersion: 'postgresql.cnpg.io/v1',
    kind: 'Backup',
    metadata: { name, namespace: 'db' },
    spec: { cluster: { name: 'pg' }, method: 'plugin', pluginConfiguration: { name: 'barman-cloud.cloudnative-pg.io' } },
    status: { phase: 'completed', startedAt, ...extra },
  })
  const pg = { namespace: 'db', name: 'pg' }
  const newest = (backups: any[] | null, boundary: number, archiver: CNPGWALArchiver) => {
    const r = cnpgRecoveryBase(pg, backups, boundary, archiver)
    return r.state === 'unread' ? 'unread' : 'backup' in r ? r.backup.metadata.name : undefined
  }
  const plugin: CNPGWALArchiver = { method: 'plugin', plugin: 'barman-cloud.cloudnative-pg.io' }

  it('resumes no earlier than the condition turning True after the last failure', () => {
    expect(resumeBoundary(cluster('2026-10-04T10:10:00Z'), t('2026-10-04T10:00:00Z'))).toBe(t('2026-10-04T10:10:00Z'))
    // A True transition from before the failure says nothing about the resume.
    expect(resumeBoundary(cluster('2026-10-01T00:00:00Z'), t('2026-10-04T10:00:00Z'))).toBe(t('2026-10-04T10:00:00Z'))
  })

  it('does not accept a backup that started between the failure and the resume', () => {
    const boundary = resumeBoundary(cluster('2026-10-04T10:10:00Z'), t('2026-10-04T10:00:00Z'))
    expect(newest([backup('gap', '2026-10-04T10:05:00Z')], boundary, plugin)).toBeUndefined()
    expect(newest([backup('gap', '2026-10-04T10:05:00Z'), backup('after', '2026-10-04T10:20:00Z')], boundary, plugin)).toBe('after')
  })

  it('ignores other clusters, unfinished runs, and says when Backups could not be read', () => {
    const boundary = t('2026-10-04T10:00:00Z')
    const other = { ...backup('other', '2026-10-04T11:00:00Z'), spec: { cluster: { name: 'pg-b' } } }
    const running = backup('running', '2026-10-04T11:00:00Z', { phase: 'running' })
    expect(newest([other, running], boundary, plugin)).toBeUndefined()
    expect(newest(null, boundary, plugin)).toBe('unread')
  })

  it('accepts only a Backup a restore can start from with this cluster\'s WAL archive', () => {
    const boundary = t('2026-10-04T10:00:00Z')
    const otherPlugin = { ...backup('other-plugin', '2026-10-04T11:00:00Z'), spec: { cluster: { name: 'pg' }, method: 'plugin', pluginConfiguration: { name: 'other.example.io' } } }
    const inTree = { ...backup('in-tree', '2026-10-04T11:00:00Z'), spec: { cluster: { name: 'pg' } } }
    const snapshot = { ...backup('snap', '2026-10-04T11:00:00Z'), spec: { cluster: { name: 'pg' }, method: 'volumeSnapshot' } }
    expect(newest([otherPlugin, inTree], boundary, plugin)).toBeUndefined()
    expect(newest([otherPlugin, inTree], boundary, { method: 'barmanObjectStore' })).toBe('in-tree')
    expect(newest([otherPlugin, snapshot], boundary, plugin)).toBe('snap')
  })

  it('takes the resume from the condition when the instance manager no longer reports the failure', () => {
    expect(resumeBoundary(cluster('2026-10-04T10:10:00Z'), NaN)).toBe(t('2026-10-04T10:10:00Z'))
    expect(resumeBoundary(cluster(), NaN)).toBeNaN()
  })

  it('counts a backup only when it begins after the last WAL that failed to archive', () => {
    const boundary = t('2026-10-04T10:00:00Z')
    const failed = '000000010000000000000004'
    // Started after the resume, but taken from a standby that had replayed only to segment 3.
    const lagging = backup('lagging', '2026-10-04T11:00:00Z', { beginWal: '000000010000000000000003' })
    const after = backup('after', '2026-10-04T10:30:00Z', { beginWal: '000000010000000000000006' })
    expect(cnpgRecoveryBase(pg, [lagging], boundary, plugin, failed)).toMatchObject({ state: 'beginsBefore', failedWal: failed })
    expect(cnpgRecoveryBase(pg, [lagging, after], boundary, plugin, failed)).toMatchObject({ state: 'verified', backup: { metadata: { name: 'after' } } })
    // Without the failed WAL (the primary's stats restarted) nothing can be verified.
    expect(cnpgRecoveryBase(pg, [after], boundary, plugin)).toMatchObject({ state: 'unverifiable', missing: 'failedWal' })
    // A backup whose status does not say where it begins is not diagnosed as beginning before.
    const unreported = backup('unreported', '2026-10-04T11:30:00Z')
    const malformed = backup('malformed', '2026-10-04T11:30:00Z', { beginWal: 'n/a' })
    expect(cnpgRecoveryBase(pg, [unreported], boundary, plugin, failed)).toMatchObject({ state: 'unverifiable', missing: 'beginWal' })
    expect(cnpgRecoveryBase(pg, [malformed, lagging], boundary, plugin, failed)).toMatchObject({ state: 'unverifiable', missing: 'beginWal' })
    expect(cnpgRecoveryBase(pg, [], boundary, plugin, failed)).toEqual({ state: 'none' })
  })

  it('orders WAL files by position across timelines', () => {
    expect(walAfter('000000020000000000000005', '000000010000000000000004')).toBe(true)
    expect(walAfter('000000020000000000000003', '000000010000000000000004')).toBe(false)
    expect(walAfter(undefined, '000000010000000000000004')).toBeNull()
  })
})
