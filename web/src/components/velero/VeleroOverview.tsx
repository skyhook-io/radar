import { useMemo } from 'react'
import {
  CronValue,
  ProblemList,
  Tooltip,
  VELERO_COMPLETED_NOTE,
  VELERO_NONE_KEPT,
  VELERO_RUNS_UNKNOWN,
  VeleroRunPhase,
  VeleroScheduleStateBadge,
  VeleroZoneValue,
  formatRelativeAgeTime,
  veleroLocationReasonText,
  veleroNewestSuccessfulText,
  veleroNextRunText,
  veleroNextRunTitle,
  veleroScheduleReasonText,
  veleroScheduleStateLabel,
  veleroScheduleTone,
  type ResourceRef,
  type VeleroLocationReading,
  type VeleroScheduleReading,
  type WorkspaceProblem,
} from '@skyhook-io/k8s-ui'
import { refToSelectedResource } from '../../utils/navigation'
import { ScreenBody, SectionTable, Sub, type TableColumn } from '../workspace/layout'
import { coverageEmpty } from '../workspace/coverage'
import { VELERO_GROUP } from './routes'
import {
  VeleroCoverageNotice,
  VeleroWorkspaceHeader,
  inInstallation,
  veleroIssueProblem,
  veleroResource,
  type VeleroScreenProps,
} from './shared'

export const VELERO_NOTHING_NEEDS_ATTENTION = 'Nothing in what Radar can read needs attention.'

function scheduleProblem(s: VeleroScheduleReading): WorkspaceProblem {
  const tone = veleroScheduleTone(s)
  return {
    id: `schedule:${s.namespace}/${s.name}`,
    category: 'schedule',
    title: veleroScheduleStateLabel(s.state),
    detail: veleroScheduleReasonText(s),
    subject: { kind: 'Schedule', group: VELERO_GROUP, namespace: s.namespace, name: s.name },
    source: 'reading',
    origin: { label: 'Radar’s reading of the schedule', detail: 'The Schedule and its Backups, read against the Velero controller’s own rules.' },
    tone: tone === 'healthy' ? 'neutral' : tone,
  }
}

function locationProblem(l: VeleroLocationReading): WorkspaceProblem {
  return {
    id: `location:${l.namespace}/${l.name}`,
    category: 'location',
    title: 'Validation overdue',
    detail: veleroLocationReasonText(l) || undefined,
    subject: { kind: 'BackupStorageLocation', group: VELERO_GROUP, namespace: l.namespace, name: l.name },
    source: 'reading',
    origin: { label: 'Radar’s reading of the location', detail: 'The location’s last validation time against its validation frequency.' },
    tone: 'degraded',
  }
}

/** "Showing 50 of 120 issues in velero", per installation the server capped. */
function cappedIssueNotes(data: VeleroScreenProps['data'], shown: VeleroScreenProps['data']['issues'], installation: string | null): string[] {
  const notes: string[] = []
  for (const [ns, total] of Object.entries(data.issueTotals ?? {})) {
    if (installation !== null && ns !== installation) continue
    const count = shown.filter((i) => (i.namespace ?? '') === ns).length
    if (total > count) notes.push(`Showing ${count} of ${total} issues in ${ns}, the newest.`)
  }
  return notes
}

function NewestRunCell({ s, onInspect }: { s: VeleroScheduleReading; onInspect: VeleroScreenProps['onInspect'] }) {
  if (s.runs.read === 'unknown') return <span className="text-theme-text-tertiary">{VELERO_RUNS_UNKNOWN}</span>
  const run = s.runs.newest
  if (!run) return <span className="text-theme-text-tertiary">{VELERO_NONE_KEPT}</span>
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation()
        onInspect(veleroResource('Backup', s.namespace, run.name))
      }}
      aria-label={`Open backup ${run.name}`}
      className="flex flex-col items-start gap-0.5 text-left"
    >
      <VeleroRunPhase phase={run.phase} />
      <span className="text-xs text-theme-text-tertiary">{formatRelativeAgeTime(run.created)}</span>
    </button>
  )
}

function LocationCell({ s }: { s: VeleroScheduleReading }) {
  if (!s.location.viaDefault) return <span className="break-all">{s.location.name}</span>
  return (
    <Tooltip content="The schedule names no location, so its backups go to the installation’s default.">
      <span className="text-theme-text-secondary">default ({s.location.name || 'unresolved'})</span>
    </Tooltip>
  )
}

export function VeleroOverview({ data, installation, onInstallation, onInspect, inspected }: VeleroScreenProps) {
  const multi = data.installations.length > 1
  const schedules = useMemo(() => inInstallation(data.schedules, installation), [data.schedules, installation])
  const locations = useMemo(() => inInstallation(data.locations, installation), [data.locations, installation])
  const issues = useMemo(
    () => (installation === null ? data.issues : data.issues.filter((i) => (i.namespace ?? '') === installation)),
    [data.issues, installation],
  )
  const attention = useMemo<WorkspaceProblem[]>(
    () => [
      ...issues.map(veleroIssueProblem),
      ...schedules.filter((s) => s.attention).map(scheduleProblem),
      ...locations.filter((l) => l.attention).map(locationProblem),
    ],
    [issues, schedules, locations],
  )
  const capped = cappedIssueNotes(data, issues, installation)
  const navigate = (ref: ResourceRef) => onInspect(refToSelectedResource(ref))

  const columns: TableColumn<VeleroScheduleReading>[] = [
    {
      header: 'Schedule',
      width: '15%',
      cell: (s) => (
        <>
          <span className="break-all font-medium">{s.name}</span>
          {multi && <Sub>{s.namespace}</Sub>}
        </>
      ),
    },
    {
      header: 'Cron',
      width: '15%',
      cell: (s) => (
        <>
          <CronValue cron={s.cron} />
          <Sub><VeleroZoneValue zone={s.zone} /></Sub>
        </>
      ),
    },
    {
      header: 'State',
      width: '12%',
      cell: (s) => (
        <Tooltip content={veleroScheduleReasonText(s)}>
          <VeleroScheduleStateBadge reading={s} />
        </Tooltip>
      ),
    },
    {
      header: 'Next run',
      width: '12%',
      cell: (s) =>
        s.nextRun ? (
          <>
            <Tooltip content={veleroNextRunTitle(s)}>
              <span>{veleroNextRunText(s)}</span>
            </Tooltip>
            <Sub>{new Date(s.nextRun).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })}</Sub>
          </>
        ) : (
          <span className="text-theme-text-tertiary">—</span>
        ),
    },
    {
      header: 'Last submission',
      width: '11%',
      cell: (s) =>
        s.lastSubmitted ? (
          <Tooltip content={new Date(s.lastSubmitted).toLocaleString()}>
            <span>{formatRelativeAgeTime(s.lastSubmitted)}</span>
          </Tooltip>
        ) : (
          <span className="text-theme-text-tertiary">Never</span>
        ),
    },
    { header: 'Newest run', width: '12%', cell: (s) => <NewestRunCell s={s} onInspect={onInspect} /> },
    {
      header: 'Newest successful',
      width: '12%',
      cell: (s) => {
        const newest = veleroNewestSuccessfulText(s.runs)
        return newest.run ? (
          <Tooltip content={new Date(newest.run.created).toLocaleString()}>
            <span>{newest.text}</span>
          </Tooltip>
        ) : (
          <span className="text-theme-text-tertiary">{newest.text}</span>
        )
      },
    },
    { header: 'Location', width: '11%', cell: (s) => <LocationCell s={s} /> },
  ]

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <VeleroWorkspaceHeader title="Overview" data={data} installation={installation} onInstallation={onInstallation} />
      <ScreenBody>
        <VeleroCoverageNotice data={data} kinds={['schedules', 'backups', 'backupStorageLocations', 'restores', 'backupRepositories']} />
        <section aria-labelledby="velero-attention">
          <h2 id="velero-attention" className="mb-2 text-sm font-semibold text-theme-text-primary">Needs attention</h2>
          <div className="rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
            {attention.length === 0 ? (
              <p className="text-sm text-theme-text-tertiary">{VELERO_NOTHING_NEEDS_ATTENTION}</p>
            ) : (
              <ProblemList problems={attention} rootKind="" onNavigate={navigate} />
            )}
            {capped.map((note) => (
              <p key={note} className="mt-2 text-xs text-theme-text-tertiary">{note}</p>
            ))}
          </div>
        </section>
        <SectionTable
          title="Schedules"
          subtitle={`${schedules.length} ${schedules.length === 1 ? 'schedule' : 'schedules'}`}
          columns={columns}
          rows={schedules}
          rowKey={(s) => `${s.namespace}/${s.name}`}
          rowResource={(s) => veleroResource('Schedule', s.namespace, s.name)}
          onInspect={onInspect}
          inspected={inspected}
          empty={coverageEmpty(data.coverage.schedules, 'Velero schedules')}
          minWidth={1040}
          footer={VELERO_COMPLETED_NOTE}
        />
      </ScreenBody>
    </div>
  )
}
