import { useMemo, useState } from 'react'
import {
  Badge,
  CNPGClusterBackupFacts,
  backupsForScheduledBackup,
  refToSelectedResource,
  FactValue,
  formatAge,
  formatDuration,
  getCNPGBackupStatus,
  getCNPGClusterBarmanPlugin,
  getCNPGObjectStoreDestination,
  getCNPGScheduledBackupStatus,
  inferredObjectStoreHealth,
  cnpgBackupRunsInWindow,
  cnpgBackupRunInFlight,
  CNPG_BACKUP_RUN_WINDOW_MS,
  coverageReadable,
  cnpgCoverageGap,
  getCNPGScheduledBackupNextSchedule,
  toneTextClass,
  usersOfObjectStore,
  type CNPGFleetRow,
  type HealthLevel,
  Tooltip,
} from '@skyhook-io/k8s-ui'
import { CNPGWorkspaceHeader, CoverageNotice, clusterResource, coverageEmpty, cnpgResource, type CNPGScreenProps } from './shared'
import { FilterChips, Mono, namespaceChip, PathText, ScreenBody, SectionTable, Segments, Sub } from '../workspace/layout'

const SEVERITY: Record<HealthLevel, 'success' | 'warning' | 'alert' | 'error' | 'neutral'> = {
  healthy: 'success',
  degraded: 'warning',
  alert: 'alert',
  unhealthy: 'error',
  unknown: 'neutral',
  neutral: 'neutral',
}

function backupStart(b: any): string | undefined {
  return b?.status?.startedAt || b?.metadata?.creationTimestamp
}

function ageText(ts?: string): string {
  return ts ? `${formatAge(ts)} ago` : '—'
}

interface StoreRow {
  key: string
  namespace: string
  name: string
  destination: string
  users: CNPGFleetRow[]
  health: { text: string; tone: HealthLevel; evidence: string }
}

// The same inference the ObjectStore summary shows, so the two never disagree:
// only the recovery windows of clusters that use the store now count.
function storeHealth(store: any, users: CNPGFleetRow[]): StoreRow['health'] {
  const { summary, evidence } = inferredObjectStoreHealth(store, usersOfObjectStore(store, users.map((u) => u.cluster)))
  const names = (list: typeof evidence) => list.map((e) => e.cluster.name).join(', ')
  if (evidence.length === 0) return { text: 'Unknown', tone: 'unknown', evidence: summary.text }
  if (summary.tone === 'unhealthy') {
    const archiving = evidence.filter((e) => e.archiving.tone === 'unhealthy')
    const backups = evidence.filter((e) => e.window?.failingSinceLastSuccess)
    const parts = [
      archiving.length > 0 ? `WAL archiving failing on ${names(archiving)}` : null,
      backups.length > 0 ? `a backup failed after the last success for ${names(backups)}` : null,
    ].filter(Boolean)
    return { text: summary.text, tone: summary.tone, evidence: `Inferred: ${parts.join('; ')}` }
  }
  const archiving = evidence.filter((e) => e.archiving.tone === 'healthy')
  if (summary.tone === 'healthy') {
    return { text: summary.text, tone: summary.tone, evidence: `Inferred from WAL archiving on ${names(archiving)}` }
  }
  return {
    text: summary.text,
    tone: summary.tone,
    evidence: archiving.length > 0
      ? `Archiving on ${names(archiving)}; no archiving result from the others`
      : 'Its clusters report no archiving result yet',
  }
}

// Enough to see the pattern of recent runs without pushing schedules and
// destinations off the page.
const RUNS_SHOWN = 10

function isFailedRun(b: any): boolean {
  const level = getCNPGBackupStatus(b).level
  return level === 'unhealthy' || level === 'alert'
}

function runDetail(b: any): string {
  const started = Date.parse(backupStart(b) ?? '')
  const stopped = Date.parse(b?.status?.stoppedAt ?? '')
  const method = b?.status?.method || b?.spec?.method
  const took = Number.isFinite(started) && Number.isFinite(stopped) && stopped >= started ? `took ${formatDuration(stopped - started)}` : null
  return [method, took].filter(Boolean).join(' · ') || '—'
}

// The newest Backup the schedule created and how it ended; the schedule's own
// lastScheduleTime says only that it fired.
function LastRun({ schedule, backups, readable }: { schedule: any; backups: any[]; readable: boolean }) {
  const fired = schedule?.status?.lastScheduleTime
  const last = backupsForScheduledBackup(schedule, backups)[0]
  if (!fired && !last) return <span className="text-theme-text-tertiary">Not yet</span>
  if (!last) {
    return (
      <>
        {ageText(fired)}
        <Sub>{readable ? 'no Backup found for it' : 'outcome needs list backups'}</Sub>
      </>
    )
  }
  // Settled Backups older than 7 days are not loaded, so the newest one here
  // can be from an earlier run than the schedule's last firing.
  const firedAt = Date.parse(fired ?? '')
  const startedAt = Date.parse(backupStart(last) ?? '')
  if (Number.isFinite(firedAt) && Number.isFinite(startedAt) && startedAt < firedAt - 2 * 60_000) {
    return (
      <>
        {ageText(fired)}
        <Sub>its Backup is not loaded</Sub>
      </>
    )
  }
  const st = getCNPGBackupStatus(last)
  return (
    <>
      {ageText(backupStart(last) ?? fired)}
      <Sub>
        <span className={st.level === 'healthy' ? undefined : toneTextClass(st.level)}>{st.text.toLowerCase()}</span>
      </Sub>
    </>
  )
}

export function CNPGProtection({
  data,
  fleet,
  namespaces,
  searchParams,
  onSetParams,
  onInspect,
  inspected,
  onClearNamespaces,
  scopeCluster,
}: CNPGScreenProps & { scopeCluster?: { namespace: string; name: string } }) {
  const clusterFilter = scopeCluster ? `${scopeCluster.namespace}/${scopeCluster.name}` : searchParams.get('cluster')
  const rows = useMemo(
    () => fleet.rows.filter((r) => !clusterFilter || `${r.namespace}/${r.name}` === clusterFilter),
    [fleet.rows, clusterFilter],
  )

  const runs = useMemo(
    () => cnpgBackupRunsInWindow(data.objects.backups ?? [])
      .filter((b) => !clusterFilter || `${b.metadata?.namespace}/${b.spec?.cluster?.name}` === clusterFilter),
    [data.objects.backups, clusterFilter],
  )
  const failedRuns = useMemo(() => runs.filter((b) => isFailedRun(b)), [runs])
  const [runFilter, setRunFilter] = useState<'failed' | 'all' | null>(null)
  const showRuns = runFilter ?? (scopeCluster || failedRuns.length === 0 ? 'all' : 'failed')
  const [allRuns, setAllRuns] = useState(false)
  const shownRuns = showRuns === 'failed' ? failedRuns : runs

  const stores = useMemo<StoreRow[]>(() => {
    return (data.objects.objectStores ?? []).map((s) => {
      const ns = s.metadata?.namespace ?? ''
      const name = s.metadata?.name ?? ''
      const users = fleet.rows.filter(
        (r) => r.namespace === ns && getCNPGClusterBarmanPlugin(r.cluster)?.barmanObjectName === name,
      )
      return { key: `${ns}/${name}`, namespace: ns, name, destination: getCNPGObjectStoreDestination(s), users, health: storeHealth(s, users) }
    }).filter((s) => !clusterFilter || s.users.some((u) => `${u.namespace}/${u.name}` === clusterFilter))
  }, [data.objects.objectStores, fleet.rows, clusterFilter])

  const schedules = useMemo(
    () =>
      (data.objects.scheduledBackups ?? []).filter(
        (s) => !clusterFilter || `${s.metadata?.namespace}/${s.spec?.cluster?.name}` === clusterFilter,
      ),
    [data.objects.scheduledBackups, clusterFilter],
  )

  const chips = scopeCluster ? [] : [
    ...(clusterFilter ? [{ label: `Cluster: ${clusterFilter}`, onClear: () => onSetParams({ cluster: null }) }] : []),
    ...namespaceChip(namespaces, onClearNamespaces),
  ]
  const backupsCoverage = data.coverage.backups!
  const scopedNamespace = scopeCluster?.namespace ?? clusterFilter?.split('/')[0]
  const backupsReadable = coverageReadable(backupsCoverage, scopedNamespace)
  const countText = (count: number) => backupsReadable ? String(count) : count > 0 ? `≥${count}` : '?'
  const backupsGap = backupsReadable ? undefined : cnpgCoverageGap(backupsCoverage, 'Backups', scopedNamespace)

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {!scopeCluster && (
        <CNPGWorkspaceHeader
          title="Backups"
          subtitle="Recovery evidence, backup runs, schedules and destinations for every cluster. Configured, backed up and restore-tested are separate facts."
        />
      )}
      <ScreenBody>
        <CoverageNotice fleet={fleet} data={data} />
        <FilterChips chips={chips} />

        {scopeCluster && rows[0] && (
          <section className="rounded-xl border border-theme-border bg-theme-surface px-4 pb-3 shadow-theme-sm">
            <h3 className="pt-3 text-sm font-semibold text-theme-text-primary">Recovery evidence</h3>
            <CNPGClusterBackupFacts row={rows[0]} onNavigate={(ref) => onInspect(refToSelectedResource(ref))} />
            <p className="mt-2 text-xs text-theme-text-tertiary">Kubernetes records no restore tests, so restore validation is never shown as passed. Recovery windows come from ObjectStore status.</p>
          </section>
        )}
        {!scopeCluster && <SectionTable
          title="Recovery evidence by cluster"
          columns={[
            {
              header: 'Cluster',
              width: '14%',
              cell: (r: CNPGFleetRow) => (
                <>
                  <div className="truncate font-medium">{r.name}</div>
                  <Sub>{r.namespace}</Sub>
                </>
              ),
            },
            { header: 'Schedule', width: '13%', cell: (r) => <FactValue fact={r.protection.schedule} /> },
            {
              header: 'Last successful backup',
              width: '16%',
              cell: (r) => (
                <>
                  <FactValue fact={r.protection.lastSuccessfulBackup} />
                  {r.protection.lastSuccessfulBackup.source && <Sub>{r.protection.lastSuccessfulBackup.source}</Sub>}
                </>
              ),
            },
            { header: 'WAL archiving', width: '16%', cell: (r) => <FactValue fact={r.protection.walArchiving} className="line-clamp-2 break-words" /> },
            {
              header: 'Recovery window',
              width: '12%',
              cell: (r) =>
                r.protection.recoveryWindow.from ? (
                  <>
                    <span className={toneTextClass(r.protection.recoveryWindow.tone)}>
                      from {ageText(r.protection.recoveryWindow.from)}
                    </span>
                    <Sub>
                      {r.protection.recoveryWindow.tone === 'degraded' ? 'not advancing: archiving failing' : 'to the newest archived WAL'}
                    </Sub>
                  </>
                ) : (
                  <FactValue fact={r.protection.recoveryWindow} />
                ),
            },
            {
              header: 'Restore validation',
              width: '12%',
              cell: (r) => (
                <FactValue fact={r.protection.restoreValidation} />
              ),
            },
            {
              header: 'Destination',
              width: '17%',
              cell: (r) => {
                const d = r.protection.destination
                if (d.method === 'barmanObjectStore') return <PathText value={d.text} />
                // A resource name stays whole on its own line; only a name too long for the cell truncates.
                if (d.method === 'plugin' && d.objectStore) {
                  return (
                    <>
                      <div className="text-theme-text-secondary">ObjectStore</div>
                      <Tooltip content={d.objectStore} wrapperClassName="max-w-full">
                        <span className="block truncate">{d.objectStore}</span>
                      </Tooltip>
                    </>
                  )
                }
                return <FactValue fact={d} />
              },
            },
          ]}
          rows={rows}
          rowKey={(r) => r.key}
          rowResource={(r) => clusterResource(r.namespace, r.name)}
          onInspect={onInspect}
          inspected={inspected}
          minWidth={1000}
          empty={coverageEmpty(data.coverage.clusters, 'PostgreSQL clusters')}
          footer="Kubernetes records no restore tests, so restore validation is never shown as passed. Recovery windows come from ObjectStore status."
        />}

        <SectionTable
          title="Backup runs"
          subtitle={
            <span className="inline-flex flex-wrap items-center gap-2">
              last 7 days · all in-flight runs
              <Segments
                label="Backup runs shown"
                value={showRuns}
                onChange={(v) => setRunFilter(v)}
                options={[
                  { id: 'failed', label: `Failed ${countText(failedRuns.length)}` },
                  { id: 'all', label: `All ${countText(runs.length)}` },
                ]}
              />
              {backupsGap && <span className="text-theme-text-tertiary">{backupsGap}</span>}
            </span>
          }
          columns={[
            { header: 'Backup', width: scopeCluster ? '32%' : '26%', cell: (b: any) => <Mono>{b.metadata?.name}</Mono> },
            ...(scopeCluster ? [] : [{ header: 'Cluster', width: '14%', cell: (b: any) => <>{b.spec?.cluster?.name ?? '—'}<Sub>{b.metadata?.namespace}</Sub></> }]),
            { header: 'Started', width: '12%', cell: (b: any) => <>{ageText(backupStart(b))}{cnpgBackupRunInFlight(b) && Date.now() - Date.parse(backupStart(b) ?? '') > CNPG_BACKUP_RUN_WINDOW_MS && <Sub>started more than a week ago</Sub>}</> },
            {
              header: 'Outcome',
              width: '12%',
              cell: (b: any) => {
                const st = getCNPGBackupStatus(b)
                return <Badge severity={SEVERITY[st.level]} size="sm">{st.text}</Badge>
              },
            },
            {
              header: 'Detail',
              width: scopeCluster ? '44%' : '36%',
              cell: (b: any) =>
                isFailedRun(b) ? (
                  <span className={toneTextClass('unhealthy')}>{b.status?.error || getCNPGBackupStatus(b).text}</span>
                ) : (
                  <span className="text-theme-text-secondary">{runDetail(b)}</span>
                ),
            },
          ]}
          rows={allRuns ? shownRuns : shownRuns.slice(0, RUNS_SHOWN)}
          rowKey={(b) => `${b.metadata?.namespace}/${b.metadata?.name}`}
          rowResource={(b) => cnpgResource('backups', b.metadata?.namespace, b.metadata?.name)}
          onInspect={onInspect}
          inspected={inspected}
          empty={
            backupsReadable
              ? showRuns === 'failed'
                ? 'No failed backups in the last 7 days.'
                : 'No backups in the last 7 days.'
              : coverageEmpty(data.coverage.backups, 'backups')
          }
          footer={
            shownRuns.length > RUNS_SHOWN ? (
              <button type="button" onClick={() => setAllRuns((v) => !v)} className="text-accent-text hover:underline">
                {allRuns ? `Show the newest ${RUNS_SHOWN}` : `Show all ${shownRuns.length} runs`}
              </button>
            ) : undefined
          }
        />

        <SectionTable
          title="Destinations"
          subtitle="ObjectStores (barman-cloud plugin)"
          columns={[
            {
              header: 'ObjectStore',
              width: '18%',
              cell: (s: StoreRow) => (
                <>
                  <Tooltip content={s.name} wrapperClassName="max-w-full">
                    <span className="block truncate">{s.name}</span>
                  </Tooltip>
                  <Sub>{s.namespace}</Sub>
                </>
              ),
            },
            { header: 'Destination', width: '30%', cell: (s) => (s.destination ? <PathText value={s.destination} /> : '—') },
            { header: 'Used by', width: '20%', cell: (s) => (s.users.length ? s.users.map((u) => u.name).join(', ') : <span className="text-theme-text-tertiary">None visible</span>) },
            {
              header: 'Upload health (inferred)',
              width: '32%',
              cell: (s) => (
                <>
                  <Badge severity={SEVERITY[s.health.tone]} size="sm">{s.health.text}</Badge>
                  <Sub>{s.health.evidence}</Sub>
                </>
              ),
            },
          ]}
          rows={stores}
          rowKey={(s) => s.key}
          rowResource={(s) => cnpgResource('objectstores', s.namespace, s.name, 'barmancloud.cnpg.io')}
          onInspect={onInspect}
          inspected={inspected}
          empty={data.coverage.objectStores?.state === 'notInstalled' ? 'The barman-cloud plugin’s ObjectStore kind is not installed.' : coverageEmpty(data.coverage.objectStores, 'ObjectStores')}
          footer="ObjectStore has no health status of its own; upload health is inferred from its clusters’ WAL archiving and backup results."
        />

        <SectionTable
          title="Schedules"
          columns={[
            { header: 'ScheduledBackup', width: scopeCluster ? '30%' : '24%', cell: (s: any) => <>{s.metadata?.name}<Sub>{s.metadata?.namespace}</Sub></> },
            ...(scopeCluster ? [] : [{ header: 'Cluster', width: '16%', cell: (s: any) => s.spec?.cluster?.name ?? '—' }]),
            {
              header: 'Schedule',
              width: '24%',
              cell: (s) => {
                const reading = data.scheduleReadings?.[`${s.metadata?.namespace}/${s.metadata?.name}`]
                return reading ? (
                  <>
                    {reading}
                    <Sub>
                      <Mono>{s.spec?.schedule}</Mono>
                    </Sub>
                  </>
                ) : (
                  <>
                    <Mono>{s.spec?.schedule ?? '—'}</Mono>
                    <Sub>CNPG cron, seconds first</Sub>
                  </>
                )
              },
            },
            {
              header: 'State',
              width: '10%',
              cell: (s) => {
                const st = getCNPGScheduledBackupStatus(s)
                // Enabled says nothing about whether its runs succeed; the last run says that.
                return st.text === 'Active' ? <span className="text-theme-text-secondary">Enabled</span> : <Badge severity={SEVERITY[st.level]} size="sm">{st.text}</Badge>
              },
            },
            { header: 'Last run', width: '14%', cell: (s) => <LastRun schedule={s} backups={data.objects.backups ?? []} readable={backupsReadable} /> },
            {
              header: 'Next run',
              width: '12%',
              cell: (s) => getCNPGScheduledBackupNextSchedule(s),
            },
          ]}
          rows={schedules}
          rowKey={(s) => `${s.metadata?.namespace}/${s.metadata?.name}`}
          rowResource={(s) => cnpgResource('scheduledbackups', s.metadata?.namespace, s.metadata?.name)}
          onInspect={onInspect}
          inspected={inspected}
          empty={coverageEmpty(data.coverage.scheduledBackups, 'ScheduledBackups')}
          footer={data.backupsOmitted > 0 ? `${data.backupsOmitted} settled backups older than 7 days are not listed.` : undefined}
        />
      </ScreenBody>
    </div>
  )
}
