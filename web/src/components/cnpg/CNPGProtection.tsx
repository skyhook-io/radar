import { useMemo } from 'react'
import {
  Badge,
  FactValue,
  cronToHuman,
  formatAge,
  formatDuration,
  getCNPGBackupStatus,
  getCNPGClusterBarmanPlugin,
  getCNPGObjectStoreDestination,
  getCNPGObjectStoreRecoveryWindows,
  getCNPGScheduledBackupStatus,
  isApiGroup,
  toneTextClass,
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

function inferStoreHealth(store: any, users: CNPGFleetRow[]): StoreRow['health'] {
  if (users.length === 0) {
    return { text: 'Unknown', tone: 'unknown', evidence: 'No visible cluster uses this store' }
  }
  const failingArchiving = users.filter((u) => u.protection.walArchiving.tone === 'unhealthy')
  const windows = getCNPGObjectStoreRecoveryWindows(store)
  const failingBackups = windows.filter((w) => w.failingSinceLastSuccess)
  if (failingArchiving.length > 0 || failingBackups.length > 0) {
    const parts = [
      failingArchiving.length > 0 ? `WAL archiving failing on ${failingArchiving.map((u) => u.name).join(', ')}` : null,
      failingBackups.length > 0 ? `a backup failed after the last success for ${failingBackups.map((w) => w.server).join(', ')}` : null,
    ].filter(Boolean)
    return { text: 'Uploads failing', tone: 'unhealthy', evidence: `Inferred: ${parts.join('; ')}` }
  }
  const archiving = users.filter((u) => u.protection.walArchiving.tone === 'healthy')
  if (archiving.length > 0) {
    return { text: 'Accepting uploads', tone: 'healthy', evidence: `Inferred from WAL archiving on ${archiving.map((u) => u.name).join(', ')}` }
  }
  return { text: 'Unknown', tone: 'unknown', evidence: 'Its clusters report no archiving result yet' }
}

export function CNPGProtection({ data, fleet, namespaces, searchParams, onSetParams, onInspect, inspected, onClearNamespaces }: CNPGScreenProps) {
  const clusterFilter = searchParams.get('cluster')
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
      return { key: `${ns}/${name}`, namespace: ns, name, destination: getCNPGObjectStoreDestination(s), users, health: inferStoreHealth(s, users) }
    }).filter((s) => !clusterFilter || s.users.some((u) => `${u.namespace}/${u.name}` === clusterFilter))
  }, [data.objects.objectStores, fleet.rows, clusterFilter])

  const schedules = useMemo(
    () =>
      (data.objects.scheduledBackups ?? []).filter(
        (s) => !clusterFilter || `${s.metadata?.namespace}/${s.spec?.cluster?.name}` === clusterFilter,
      ),
    [data.objects.scheduledBackups, clusterFilter],
  )

  const chips = [
    ...(clusterFilter ? [{ label: `Cluster: ${clusterFilter}`, onClear: () => onSetParams({ cluster: null }) }] : []),
    ...namespaceChip(namespaces, onClearNamespaces),
  ]
  const backupsReadable = data.coverage.backups?.state === 'full' || data.coverage.backups?.state === 'partial'

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <CNPGWorkspaceHeader
        title="Protection"
        subtitle="Backup outcomes, schedules, destinations and recovery evidence for every cluster. Configured, backed up and restore-tested are separate facts."
      />
      <ScreenBody>
        <CoverageNotice fleet={fleet} data={data} />
        <FilterChips chips={chips} />

        <SectionTable
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
              width: '13%',
              cell: (r) =>
                r.protection.recoveryWindow.from ? (
                  <>
                    <span className={toneTextClass(r.protection.recoveryWindow.tone)}>
                      from {ageText(r.protection.recoveryWindow.from)}
                    </span>
                    <Sub>
                      to {r.protection.recoveryWindow.to ? ageText(r.protection.recoveryWindow.to) : 'unknown'}
                      {r.protection.recoveryWindow.tone === 'degraded' ? ' · not advancing' : ''}
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
                <span title={r.protection.restoreValidation.source}>
                  <FactValue fact={r.protection.restoreValidation} />
                </span>
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
          empty="No PostgreSQL clusters in this scope."
          footer="Kubernetes records no restore tests, so restore validation is never shown as passed. Recovery windows come from ObjectStore status."
        />

        <SectionTable
          title="Failed backups"
          subtitle="last 7 days"
          columns={[
            { header: 'Backup', width: '28%', cell: (b: any) => <Mono>{b.metadata?.name}</Mono> },
            { header: 'Cluster', width: '16%', cell: (b) => <>{b.spec?.cluster?.name ?? '—'}<Sub>{b.metadata?.namespace}</Sub></> },
            { header: 'Started', width: '12%', cell: (b) => ageText(backupTime(b)) },
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
          empty={backupsReadable ? 'No failed backups in the last 7 days.' : 'Backups are not readable with your access.'}
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
          empty={data.coverage.objectStores?.state === 'notInstalled' ? 'The barman-cloud plugin’s ObjectStore kind is not installed.' : 'No ObjectStores in this scope.'}
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
                  {s.spec?.schedule && <Sub>{cronToHuman(s.spec.schedule)}</Sub>}
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
          empty={data.coverage.scheduledBackups?.state === 'full' ? 'No ScheduledBackups in this scope.' : 'ScheduledBackups are not fully readable with your access.'}
          footer={data.backupsOmitted > 0 ? `${data.backupsOmitted} settled backups older than 7 days are not listed.` : undefined}
        />
      </ScreenBody>
    </div>
  )
}
