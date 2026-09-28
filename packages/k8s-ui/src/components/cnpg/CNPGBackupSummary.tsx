import { cronToHuman, formatDuration } from '../resources/resource-utils'
import {
  CNPG_BARMAN_OBJECTSTORE_GROUP,
  CNPG_GROUP,
  getCNPGBackupStatus,
  getCNPGScheduledBackupNextSchedule,
  getCNPGScheduledBackupStatus,
} from '../resources/resource-utils-cnpg'
import type { CNPGWorkspaceResponse } from './workspace'
import { FactGrid, FactRow, RefLink, SummaryHeading, toneTextClass, type CNPGNavigate } from './primitives'
import { ClusterLink, NotReported, Note, ObjectProblems, PhaseBadge, SummaryShell, TimeAgo } from './CNPGSharedSummary'
import {
  backupDestination,
  backupsForScheduledBackup,
  clustersIn,
  refOf,
  relationUnavailable,
  scheduledBackupOf,
  workspaceList,
} from './relations'

interface SummaryProps {
  resource: any
  workspace: CNPGWorkspaceResponse | null
  onNavigate?: CNPGNavigate
}

const RECENT_RUNS = 5

function methodText(resource: any): string | null {
  const m = resource?.status?.method || resource?.spec?.method
  if (!m) return null
  switch (m) {
    case 'plugin':
      return resource?.spec?.pluginConfiguration?.name ? `Plugin · ${resource.spec.pluginConfiguration.name}` : 'Plugin'
    case 'barmanObjectStore':
      return 'Barman object store (in-tree)'
    case 'volumeSnapshot':
      return 'Volume snapshot'
    default:
      return m
  }
}

function Timing({ resource }: { resource: any }) {
  const started = resource?.status?.startedAt
  const stopped = resource?.status?.stoppedAt
  const startMs = started ? Date.parse(started) : NaN
  const stopMs = stopped ? Date.parse(stopped) : NaN
  const tookMs = Number.isFinite(startMs) && Number.isFinite(stopMs) && stopMs >= startMs ? stopMs - startMs : null
  return (
    <>
      <FactRow label="Started">
        <TimeAgo at={started} />
      </FactRow>
      <FactRow label="Stopped">
        {stopped ? (
          <span>
            <TimeAgo at={stopped} />
            {tookMs !== null && <span className="text-theme-text-secondary"> · took {formatDuration(tookMs, true)}</span>}
          </span>
        ) : Number.isFinite(startMs) ? (
          <span className="text-theme-text-secondary">Not stopped · running for {formatDuration(Date.now() - startMs, true)}</span>
        ) : (
          <NotReported />
        )}
      </FactRow>
    </>
  )
}

export function CNPGBackupSummary({ resource, workspace, onNavigate }: SummaryProps) {
  const ns = resource?.metadata?.namespace ?? ''
  const status = getCNPGBackupStatus(resource)
  const pod = resource?.status?.instanceID?.podName
  const error = resource?.status?.error
  const trigger = scheduledBackupOf(resource)
  const dest = backupDestination(resource, clustersIn(workspace))
  const method = methodText(resource)

  return (
    <SummaryShell>
      <ObjectProblems issues={workspace?.issues} subject={refOf(resource, 'Backup')} onNavigate={onNavigate} />

      <SummaryHeading>Outcome</SummaryHeading>
      <FactGrid>
        <FactRow label="Phase">
          <PhaseBadge status={status} />
        </FactRow>
        <Timing resource={resource} />
        <FactRow label="Taken on">
          {pod ? <RefLink refTo={{ kind: 'Pod', group: '', namespace: ns, name: pod }} onNavigate={onNavigate} mono /> : <NotReported />}
        </FactRow>
        <FactRow label="Backup ID">
          {resource?.status?.backupId ? <span className="font-mono">{resource.status.backupId}</span> : <NotReported />}
        </FactRow>
        <FactRow label="Target">
          {resource?.spec?.target ? resource.spec.target : <span className="text-theme-text-secondary">Not set · the Cluster's backup target applies</span>}
        </FactRow>
        {error && (
          <FactRow label="Error">
            <span className={toneTextClass('unhealthy')}>{error}</span>
          </FactRow>
        )}
      </FactGrid>

      <SummaryHeading>Relationships</SummaryHeading>
      <FactGrid>
        <FactRow label="Cluster">
          <ClusterLink resource={resource} workspace={workspace} onNavigate={onNavigate} />
        </FactRow>
        <FactRow label="Trigger">
          {trigger ? (
            <span>
              ScheduledBackup{' '}
              <RefLink refTo={{ kind: 'ScheduledBackup', group: CNPG_GROUP, namespace: ns, name: trigger }} onNavigate={onNavigate} mono />
            </span>
          ) : (
            'On demand'
          )}
        </FactRow>
        <FactRow label="Destination">
          {dest.type === 'objectStore' ? (
            <span>
              ObjectStore{' '}
              <RefLink refTo={{ kind: 'ObjectStore', group: CNPG_BARMAN_OBJECTSTORE_GROUP, namespace: ns, name: dest.name }} onNavigate={onNavigate} mono />
            </span>
          ) : dest.type === 'path' ? (
            <span className="font-mono">{dest.path}</span>
          ) : dest.type === 'volumeSnapshot' ? (
            'Volume snapshot'
          ) : (
            <NotReported />
          )}
        </FactRow>
        <FactRow label="Method">{method ?? <NotReported />}</FactRow>
      </FactGrid>
    </SummaryShell>
  )
}

export function CNPGScheduledBackupSummary({ resource, workspace, onNavigate }: SummaryProps) {
  const ns = resource?.metadata?.namespace ?? ''
  const cron = resource?.spec?.schedule
  const human = cron ? cronToHuman(cron) : ''
  const next = getCNPGScheduledBackupNextSchedule(resource)
  const runsUnavailable = relationUnavailable(workspace, 'backups', ns, 'Backups')
  const runs = runsUnavailable ? [] : backupsForScheduledBackup(resource, workspaceList(workspace, 'backups'))
  const shown = runs.slice(0, RECENT_RUNS)

  return (
    <SummaryShell>
      <ObjectProblems issues={workspace?.issues} subject={refOf(resource, 'ScheduledBackup')} onNavigate={onNavigate} />

      <SummaryHeading>Schedule</SummaryHeading>
      <FactGrid>
        <FactRow label="Status">
          <PhaseBadge status={getCNPGScheduledBackupStatus(resource)} />
        </FactRow>
        <FactRow label="Cluster">
          <ClusterLink resource={resource} workspace={workspace} onNavigate={onNavigate} />
        </FactRow>
        <FactRow label="Schedule">
          {cron ? (
            <span>
              <span className="font-mono">{cron}</span>
              {human && human !== cron && <span className="text-theme-text-secondary"> · {human}</span>}
            </span>
          ) : (
            <NotReported text="Not set" />
          )}
        </FactRow>
        <FactRow label="Last scheduled">
          <TimeAgo at={resource?.status?.lastScheduleTime} missing="Never" />
        </FactRow>
        <FactRow label="Next scheduled">{next === '-' ? <NotReported /> : next}</FactRow>
        <FactRow label="Method">{methodText(resource) ?? 'Barman object store (in-tree) · default'}</FactRow>
      </FactGrid>

      <SummaryHeading hint={runs.length > shown.length ? `${shown.length} of ${runs.length}` : undefined}>Recent runs</SummaryHeading>
      {runsUnavailable ? (
        <div className="text-sm">
          <NotReported text={runsUnavailable} />
        </div>
      ) : shown.length === 0 ? (
        <div className="text-sm text-theme-text-secondary">No Backups from this schedule are visible</div>
      ) : (
        <FactGrid>
          {shown.map((b) => (
            <FactRow
              key={b.metadata?.name}
              label={<RefLink refTo={{ kind: 'Backup', group: CNPG_GROUP, namespace: ns, name: b.metadata?.name }} onNavigate={onNavigate} mono />}
            >
              <span className="inline-flex flex-wrap items-center gap-2">
                <PhaseBadge status={getCNPGBackupStatus(b)} />
                {b.status?.startedAt ? (
                  <span className="text-theme-text-secondary">
                    started <TimeAgo at={b.status.startedAt} />
                  </span>
                ) : (
                  <NotReported text="Not started" />
                )}
              </span>
            </FactRow>
          ))}
        </FactGrid>
      )}
      {!runsUnavailable && (workspace?.backupsOmitted ?? 0) > 0 && <Note>Backups older than 7 days are not listed.</Note>}
    </SummaryShell>
  )
}
