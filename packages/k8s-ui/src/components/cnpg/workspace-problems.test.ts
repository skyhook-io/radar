import { describe, expect, it } from 'vitest'
import { cnpgCollapseBackupFailures, cnpgCompareProblems, cnpgIssueText, cnpgIssueTitle, type CNPGProblem } from './workspace'

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
    expect(t.title).toBe('WAL archiving is failing')
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
  const issue = (name: string, message: string, first_seen: string) => ({ kind: 'Backup', name, reason: 'CNPGBackupFailed', message, first_seen })
  it('does not repeat the title at the start of the detail', () => {
    expect(cnpgIssueText({ kind: 'Backup', name: 'b', reason: 'CNPGBackupFailed', message: 'Backup failed: cannot proceed with the backup' })).toEqual({
      title: 'Backup failed',
      detail: 'Cannot proceed with the backup',
    })
  })
  it('collapses Backups that failed the same way into one problem about the latest', () => {
    const problems = ['b-1', 'b-3', 'b-2'].map((n, i) => {
      const t = cnpgIssueText(issue(n, 'Backup failed: cannot proceed as the cluster has no plugin configured', `2026-09-2${i}T00:00:00Z`))
      return {
        id: n,
        severity: 'warning' as const,
        category: 'protection' as const,
        ...t,
        subject: { kind: 'Backup', group: 'postgresql.cnpg.io', namespace: 'pg', name: n },
        source: 'issue' as const,
        reason: 'CNPGBackupFailed',
        firstSeen: `2026-09-2${i}T00:00:00Z`,
      }
    })
    const out = cnpgCollapseBackupFailures(problems)
    expect(out).toHaveLength(1)
    expect(out[0]).toMatchObject({ title: '3 backups failed: cannot proceed as the cluster has no plugin configured', subject: { name: 'b-2' } })
    expect(out[0].alsoAbout?.map((o) => o.name)).toEqual(['b-3', 'b-1'])
    expect('reason' in out[0]).toBe(false)
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
