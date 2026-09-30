import { describe, expect, it } from 'vitest'
import { cnpgCompareProblems, cnpgIssueText, cnpgIssueTitle, type CNPGProblem } from './workspace'

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
    const crit = problem('pg-1 restarting (CrashLoopBackOff)', 'critical', 'Pod')
    const warn = problem('Backup failed', 'warning', 'Backup', 'postgresql.cnpg.io')
    expect([warn, crit].sort(cnpgCompareProblems)[0]).toBe(crit)
  })
})

describe('cnpgIssueTitle', () => {
  it('turns a bare reason into a sentence about the subject', () => {
    expect(cnpgIssueTitle({ kind: 'Pod', name: 'pg-wal-failing-1', reason: 'ReadinessProbeFailed', message: 'ReadinessProbeFailed' })).toBe(
      'pg-wal-failing-1 not ready (readiness probe failing)',
    )
    expect(cnpgIssueTitle({ kind: 'Pod', name: 'pg-runtime-5', reason: 'CrashLoopBackOff' })).toBe('pg-runtime-5 restarting (CrashLoopBackOff)')
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
