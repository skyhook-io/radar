import { describe, expect, it } from 'vitest'
import { cnpgCollapseBackupFailures, cnpgCompareProblems, cnpgFoldLastBackupFailed, cnpgFormatLag, cnpgIssueOrigin, cnpgIssueText, cnpgIssueTitle, type CNPGProblem } from './workspace'

const problem = (title: string, severity: CNPGProblem['severity'], kind: string, group = ''): CNPGProblem => ({
  id: title,
  severity,
  category: kind === 'Pod' ? 'availability' : 'protection',
  title,
  subject: { kind, group, namespace: 'pg', name: kind === 'Pod' ? 'pg-wal-failing-1' : 'pg-wal-failing' },
  source: 'issue',
})

describe('cnpgCompareProblems', () => {
  it('puts the cluster-level cause ahead of a Pod symptom at the same severity (pg-wal-failing)', () => {
    const probe = problem('pg-wal-failing-1 not ready (readiness probe failing)', 'critical', 'Pod')
    const wal = problem('WAL archiving failing', 'critical', 'Cluster', 'postgresql.cnpg.io')
    expect([probe, wal].sort(cnpgCompareProblems).map((p) => p.title)).toEqual(['WAL archiving failing', 'pg-wal-failing-1 not ready (readiness probe failing)'])
  })
  it('keeps severity first', () => {
    const crit = problem('pg-1 restarted recently (CrashLoopBackOff)', 'critical', 'Pod')
    const warn = problem('Backup failed', 'warning', 'Backup', 'postgresql.cnpg.io')
    expect([warn, crit].sort(cnpgCompareProblems)[0]).toBe(crit)
  })
})

describe('cnpgIssueTitle', () => {
  it('turns a bare reason into a sentence about the subject', () => {
    expect(cnpgIssueTitle({ kind: 'Pod', name: 'pg-wal-failing-1', reason: 'ReadinessProbeFailed', message: 'ReadinessProbeFailed' })).toBe(
      'pg-wal-failing-1 not ready (readiness probe failing)',
    )
    expect(cnpgIssueTitle({ kind: 'Pod', name: 'pg-runtime-5', reason: 'CrashLoopBackOff' })).toBe('pg-runtime-5 restarted recently (CrashLoopBackOff)')
    expect(cnpgIssueTitle({ kind: 'Backup', name: 'b-1', reason: 'BackupStuck' })).toBe('Backup b-1: backup stuck')
  })
  it('keeps a real message', () => {
    expect(cnpgIssueTitle({ kind: 'Cluster', name: 'pg', reason: 'ContinuousArchivingFailing', message: 'WAL archiving failing' })).toBe('WAL archiving failing')
  })
})

describe('cnpgIssueText', () => {
  it('heads a CNPG condition issue with a plain title and keeps the operator message beneath', () => {
    const t = cnpgIssueText({
      kind: 'Cluster',
      name: 'pg-wal-failing',
      reason: 'CNPGWALArchivingFailing',
      message:
        'The last WAL archival did not complete; recovery-point advancement is uncertain: unexpected failure invoking barman-cloud-wal-archive: exit status 4',
    })
    expect(t.title).toBe('WAL archiving failing')
    expect(t.detail).toContain('exit status 4')
  })
})

describe('cnpgIssueText for certificates and schedules', () => {
  it('words expired and expiring certificates apart, and does not claim no backup was produced', () => {
    expect(cnpgIssueText({ kind: 'Cluster', name: 'pg', reason: 'CNPGCertificateExpired', message: 'The certificate in Secret pg-ca expired 2026-09-30T11:00:00Z' }).title).toBe(
      'A certificate has expired',
    )
    expect(cnpgIssueText({ kind: 'Cluster', name: 'pg', reason: 'CNPGCertificateExpiring', message: 'The certificate in Secret x expires in 3 days' }).title).toBe(
      'A certificate expires soon',
    )
    expect(cnpgIssueText({ kind: 'ScheduledBackup', name: 's', reason: 'CNPGScheduledRunNoBackup', message: 'x y' }).title).toBe('No successful backup since a scheduled run')
  })
})

describe('backup failures', () => {
  it('does not repeat the title at the start of the detail', () => {
    expect(cnpgIssueText({ kind: 'Backup', name: 'b', reason: 'CNPGBackupFailed', message: 'Backup failed: cannot proceed with the backup' })).toEqual({
      title: 'Backup failed',
      detail: 'Cannot proceed with the backup',
    })
  })
  it('collapses Backups that failed the same way into one problem about the latest, by the Backups\' own times', () => {
    // detectCNPGBackupIssues emits no first_seen: the latest comes from the Backup objects.
    const problems = ['b-1', 'b-3', 'b-2'].map((n) => {
      const t = cnpgIssueText({ kind: 'Backup', name: n, reason: 'CNPGBackupFailed', message: 'Backup failed: cannot proceed as the cluster has no plugin configured' })
      return {
        id: n,
        severity: 'warning' as const,
        category: 'protection' as const,
        ...t,
        subject: { kind: 'Backup', group: 'postgresql.cnpg.io', namespace: 'pg', name: n },
        source: 'issue' as const,
        reason: 'CNPGBackupFailed',
      }
    })
    const times = new Map([
      ['b-1', Date.parse('2026-09-20T00:00:00Z')],
      ['b-3', Date.parse('2026-09-21T00:00:00Z')],
      ['b-2', Date.parse('2026-09-22T00:00:00Z')],
    ])
    const out = cnpgCollapseBackupFailures(problems, times)
    expect(out).toHaveLength(1)
    expect(out[0]).toMatchObject({ title: '3 backups failed: cannot proceed as the cluster has no plugin configured', subject: { name: 'b-2' } })
    expect(out[0].alsoAbout?.map((o) => o.name)).toEqual(['b-3', 'b-1'])
  })
})

describe('scheduled run without a backup', () => {
  it('words the schedule and dates the run as an age from first_seen', () => {
    const t = cnpgIssueText({
      kind: 'Cluster',
      name: 'pg',
      reason: 'CNPGScheduledRunNoBackup',
      message: 'ScheduledBackup pg-nightly (every day at 02:00 UTC) has had no successful backup since its run',
      first_seen: new Date(Date.now() - 2 * 24 * 3600 * 1000 - 60_000).toISOString(),
    })
    expect(t.title).toBe('No successful backup since a scheduled run')
    expect(t.detail).toBe('ScheduledBackup pg-nightly (every day at 02:00 UTC) has had no successful backup since its run 2d ago')
  })
})

describe('cnpgFormatLag', () => {
  it('reads every replay lag the same way, rounded down', () => {
    expect(cnpgFormatLag(0)).toBe('0 s')
    expect(cnpgFormatLag(0.25)).toBe('250 ms')
    expect(cnpgFormatLag(0.2509)).toBe('250 ms')
    expect(cnpgFormatLag(8.27)).toBe('8.2 s')
    expect(cnpgFormatLag(55.9)).toBe('55 s')
    expect(cnpgFormatLag(1500.4)).toBe('25 min')
    expect(cnpgFormatLag(1721.3)).toBe('28 min')
    expect(cnpgFormatLag(3600)).toBe('1 h')
    expect(cnpgFormatLag(4000)).toBe('1 h 6 min')
  })
})

describe('cnpgFoldLastBackupFailed', () => {
  const p = (id: string, kind: string, name: string, reason: string, alsoAbout?: { kind: string; name: string }[]) => ({
    id,
    severity: 'warning' as const,
    category: 'protection' as const,
    title: id,
    subject: { kind, group: 'postgresql.cnpg.io', namespace: 'pg', name },
    source: 'issue' as const,
    reason,
    alsoAbout,
  })
  const last = p('last', 'Cluster', 'pg', 'CNPGLastBackupFailed')
  it('drops "the last backup failed" when a failed-Backup problem already covers the newest Backup', () => {
    const group = p('3 backups failed', 'Backup', 'b-3', 'CNPGBackupFailed', [{ kind: 'Backup', name: 'b-2' }])
    const out = cnpgFoldLastBackupFailed([group, last], 'b-3')
    expect(out.map((x) => x.id)).toEqual(['3 backups failed'])
    expect('reason' in out[0]).toBe(false)
  })
  it('counts only a failed-Backup problem as covering the newest Backup', () => {
    const other = p('stuck', 'Backup', 'b-3', 'CNPGBackupStuck')
    expect(cnpgFoldLastBackupFailed([other, last], 'b-3').map((x) => x.id)).toEqual(['stuck', 'last'])
  })
  it('keeps it when the newest Backup is not among the failures, or is unknown', () => {
    const old = p('old failure', 'Backup', 'b-1', 'CNPGBackupFailed')
    expect(cnpgFoldLastBackupFailed([old, last], 'b-9')).toHaveLength(2)
    expect(cnpgFoldLastBackupFailed([old, last], undefined)).toHaveLength(2)
  })
})

describe('cnpgIssueOrigin', () => {
  it('names where the evidence comes from', () => {
    expect(cnpgIssueOrigin({ kind: 'Cluster', reason: 'CNPGWALArchivingFailing' })).toEqual({ label: 'Reported by CNPG', detail: 'Cluster ContinuousArchiving condition' })
    expect(cnpgIssueOrigin({ kind: 'Backup', reason: 'CNPGWALArchivingFailing' }).label).toBe('Backup status')
    expect(cnpgIssueOrigin({ kind: 'Cluster', reason: 'CNPGClusterDegraded' }).label).toBe('Radar check of ready instances')
    expect(cnpgIssueOrigin({ kind: 'Cluster', reason: 'CNPGLastBackupFailed' }).label).toBe('Reported by CNPG')
    expect(cnpgIssueOrigin({ kind: 'Database', reason: 'CNPGDeclarativeNotApplied' }).label).toBe('Reported by CNPG')
    expect(cnpgIssueOrigin({ kind: 'ScheduledBackup', reason: 'CNPGScheduledBackupMissed' }).label).toBe('Radar check of the backup schedule')
    expect(cnpgIssueOrigin({ kind: 'Pod', reason: 'HighRestartCount' }).label).toBe('Radar check of restarts')
    expect(cnpgIssueOrigin({ kind: 'Pod', reason: 'ReadinessProbeInvalid' }).label).toBe('Radar check of the probe')
    expect(cnpgIssueOrigin({ kind: 'Cluster', reason: 'CNPGClusterFailingOver' }).label).toBe('Reported by CNPG')
    expect(cnpgIssueOrigin({ kind: 'Backup', reason: 'CNPGBackupFailed' }).label).toBe('Backup status')
    expect(cnpgIssueOrigin({ kind: 'Cluster', reason: 'CNPGScheduledRunNoBackup' }).label).toBe('Radar check of the backup schedule')
    expect(cnpgIssueOrigin({ kind: 'Cluster', reason: 'CNPGCertificateExpired' }).label).toBe('Certificate expiry (from Cluster status)')
    expect(cnpgIssueOrigin({ kind: 'Pod', reason: 'ReadinessProbeFailed' }).label).toBe('Pod readiness probe')
    expect(cnpgIssueOrigin({ kind: 'Pod', reason: 'CrashLoopBackOff' }).label).toBe('Pod status')
  })
  it('falls back to "Detected by Radar" without inventing a source', () => {
    expect(cnpgIssueOrigin({ kind: 'Pooler', reason: 'SomethingNew' })).toEqual({ label: 'Detected by Radar' })
  })
})
