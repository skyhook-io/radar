import { useMemo } from 'react'
import {
  Badge,
  FactValue,
  formatAge,
  formatDuration,
  getCNPGBackupStatus,
  getCNPGClusterBarmanPlugin,
  getCNPGObjectStoreDestination,
  getCNPGScheduledBackupStatus,
  inferredObjectStoreHealth,
  isApiGroup,
  toneTextClass,
  usersOfObjectStore,
  type CNPGFleetRow,
  type HealthLevel,
} from '@skyhook-io/k8s-ui'
import {
  CNPGWorkspaceHeader,
  CoverageNotice,
  FilterChips,
  Mono,
  ScreenBody,
  SectionTable,
  Sub,
  clusterResource,
  coverageEmpty,
  cnpgResource,
  namespaceChip,
  type CNPGScreenProps,
} from './shared'

const SEVERITY: Record<HealthLevel, 'success' | 'warning' | 'alert' | 'error' | 'neutral'> = {
  healthy: 'success',
  degraded: 'warning',
  alert: 'alert',
  unhealthy: 'error',
  unknown: 'neutral',
  neutral: 'neutral',
}

const WEEK_MS = 7 * 24 * 60 * 60 * 1000

function backupTime(b: any): string | undefined {
  return b?.status?.stoppedAt || b?.status?.startedAt || b?.metadata?.creationTimestamp
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

  const failed = useMemo(() => {
    const now = Date.now()
    return (data.objects.backups ?? [])
      .filter((b) => isApiGroup(b.apiVersion, 'postgresql.cnpg.io'))
      .filter((b) => {
        const level = getCNPGBackupStatus(b).level
        if (level !== 'unhealthy' && level !== 'alert') return false
        const t = Date.parse(backupTime(b) ?? '')
        return Number.isFinite(t) && now - t <= WEEK_MS
      })
      .filter((b) => !clusterFilter || `${b.metadata?.namespace}/${b.spec?.cluster?.name}` === clusterFilter)
      .sort((a, b) => Date.parse(backupTime(b) ?? '') - Date.parse(backupTime(a) ?? ''))
  }, [data.objects.backups, clusterFilter])

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
  const backupsReadable = data.coverage.backups?.state === 'full'

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {!scopeCluster && (
        <CNPGWorkspaceHeader
          title="Protection"
          subtitle="Backup outcomes, schedules, destinations and recovery evidence for every cluster. Configured, backed up and restore-tested are separate facts."
        />
      )}
      <ScreenBody>
        <CoverageNotice fleet={fleet} data={data} />
        <FilterChips chips={chips} />

        <SectionTable
          title={scopeCluster ? 'Recovery evidence' : 'Recovery evidence by cluster'}
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
              width: '13%',
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
              width: '13%',
              cell: (r) => (
                <FactValue fact={r.protection.restoreValidation} />
              ),
            },
            {
              header: 'Destination',
              width: '15%',
              cell: (r) => <FactValue fact={r.protection.destination} className={r.protection.destination.method === 'barmanObjectStore' ? 'break-all font-mono text-[12.5px]' : 'break-words'} />,
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
        />

        <SectionTable
          title="Failed backups"
          subtitle="last 7 days"
          columns={[
            { header: 'Backup', width: '28%', cell: (b: any) => <Mono>{b.metadata?.name}</Mono> },
            { header: 'Cluster', width: '16%', cell: (b) => <>{b.spec?.cluster?.name ?? '—'}<Sub>{b.metadata?.namespace}</Sub></> },
            { header: 'Started', width: '12%', cell: (b) => ageText(backupStart(b)) },
            {
              header: 'Error',
              width: '44%',
              cell: (b) => <span className={toneTextClass('unhealthy')}>{b.status?.error || getCNPGBackupStatus(b).text}</span>,
            },
          ]}
          rows={failed}
          rowKey={(b) => `${b.metadata?.namespace}/${b.metadata?.name}`}
          rowResource={(b) => cnpgResource('backups', b.metadata?.namespace, b.metadata?.name)}
          onInspect={onInspect}
          inspected={inspected}
          empty={backupsReadable && data.coverage.backups?.state === 'full' ? 'No failed backups in the last 7 days.' : coverageEmpty(data.coverage.backups, 'failed backups')}
        />

        <SectionTable
          title="Destinations"
          subtitle="ObjectStores (barman-cloud plugin)"
          columns={[
            { header: 'ObjectStore', width: '18%', cell: (s: StoreRow) => <>{s.name}<Sub>{s.namespace}</Sub></> },
            { header: 'Destination', width: '30%', cell: (s) => <Mono>{s.destination}</Mono> },
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
            { header: 'ScheduledBackup', width: '24%', cell: (s: any) => <>{s.metadata?.name}<Sub>{s.metadata?.namespace}</Sub></> },
            { header: 'Cluster', width: '16%', cell: (s) => s.spec?.cluster?.name ?? '—' },
            {
              header: 'Schedule',
              width: '24%',
              cell: (s) => (
                <>
                  <Mono>{s.spec?.schedule ?? '—'}</Mono>
                  <Sub>CNPG cron, seconds first</Sub>
                </>
              ),
            },
            {
              header: 'Status',
              width: '12%',
              cell: (s) => {
                const st = getCNPGScheduledBackupStatus(s)
                return <Badge severity={SEVERITY[st.level]} size="sm">{st.text}</Badge>
              },
            },
            { header: 'Last run', width: '12%', cell: (s) => ageText(s.status?.lastScheduleTime) },
            {
              header: 'Next run',
              width: '12%',
              cell: (s) => {
                const next = s.status?.nextScheduleTime
                if (!next) return '—'
                const ms = Date.parse(next) - Date.now()
                return ms >= 0 ? `in ${formatDuration(ms)}` : `${formatDuration(-ms)} overdue`
              },
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
