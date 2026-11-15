import { useMemo, useState, type ReactNode } from 'react'
import {
  Collapse,
  CollapseChevron,
  CronValue,
  ProblemList,
  StatusDot,
  Tooltip,
  VELERO_COMPLETED_NOTE,
  VELERO_NONE_KEPT,
  VELERO_NONE_KEPT_SHORT,
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
  veleroLocalTime,
  veleroServerDownText,
  type ResourceRef,
  type StatusTone,
  type VeleroKindCoverage,
  type VeleroLocationReading,
  type VeleroScheduleReading,
  type WorkspaceProblem,
} from '@skyhook-io/k8s-ui'
import { refToSelectedResource } from '../../utils/navigation'
import { ScreenBody, SectionTable, Sub, type TableColumn } from '../workspace/layout'
import { INCOMPLETE_COVERAGE_STATES, coverageEmpty } from '../workspace/coverage'
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

// Each group shows this many rows before folding, so a burst of failed runs
// cannot push the schedules this view exists for off the screen.
const GROUP_FOLD = 4

type AttentionGroupId = 'server' | 'schedules' | 'storage' | 'runs'

const ATTENTION_GROUPS: { id: AttentionGroupId; title: string }[] = [
  { id: 'server', title: 'Velero server' },
  { id: 'schedules', title: 'Schedules' },
  { id: 'storage', title: 'Storage' },
  { id: 'runs', title: 'Backups and restores' },
]

const STORAGE_KINDS = new Set(['BackupStorageLocation', 'VolumeSnapshotLocation', 'BackupRepository'])

/** Which part of Velero a problem is about, the way an operator sorts them: the server, a schedule, storage, or a run. */
function attentionGroup(p: WorkspaceProblem): AttentionGroupId {
  if (p.category === 'server') return 'server'
  if (p.category === 'schedule' || p.subject.kind === 'Schedule') return 'schedules'
  if (p.category === 'location' || STORAGE_KINDS.has(p.subject.kind)) return 'storage'
  return 'runs'
}

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

function serverProblem(namespace: string, deployment?: string): WorkspaceProblem {
  return {
    id: `server:${namespace}`,
    category: 'server',
    title: 'Velero server not running',
    detail: veleroServerDownText(namespace, deployment),
    subject: { kind: 'Deployment', group: 'apps', namespace, name: deployment ?? 'velero' },
    source: 'reading',
    origin: { label: 'Radar’s reading of the Velero server', detail: 'The server Deployment’s running replicas; a pod still terminating is not counted.' },
    tone: 'unhealthy',
  }
}

function locationProblem(l: VeleroLocationReading): WorkspaceProblem {
  return {
    id: `location:${l.namespace}/${l.name}`,
    category: 'location',
    title: l.neverValidated ? 'Never validated' : 'Validation overdue',
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
  if (!run) return <NoneKept />
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
      <span className="text-xs text-theme-text-tertiary">{formatRelativeAgeTime(run.ranAt)}</span>
    </button>
  )
}

function NoneKept() {
  return (
    <Tooltip content={`${VELERO_NONE_KEPT}. Velero deletes backups past their TTL, so this is not “never”.`}>
      <span className="text-theme-text-tertiary">{VELERO_NONE_KEPT_SHORT}</span>
    </Tooltip>
  )
}

function LocationCell({ s }: { s: VeleroScheduleReading }) {
  if (!s.location.viaDefault) return <span className="break-all">{s.location.name}</span>
  return (
    <Tooltip content="The schedule names no location, so its backups go to the installation’s default.">
      <span>
        <span className="break-all">{s.location.name || 'Unresolved'}</span>
        <Sub>via default</Sub>
      </span>
    </Tooltip>
  )
}

interface SummaryFact {
  label: string
  value: ReactNode
  tone: StatusTone
}

function serverFact(installations: VeleroScreenProps['data']['installations']): SummaryFact {
  const read = installations.filter((i) => i.server.replicas !== undefined)
  const down = read.filter((i) => i.server.replicas === 0)
  if (installations.length === 0 || read.length === 0) return { label: 'Velero server', value: 'unknown', tone: 'unknown' }
  if (down.length > 0) {
    const value = installations.length > 1 ? `not running in ${down.map((i) => i.namespace).join(', ')}` : 'not running'
    return { label: 'Velero server', value, tone: 'unhealthy' }
  }
  return { label: 'Velero server', value: read.length < installations.length ? 'running where Radar can read it' : 'running', tone: 'healthy' }
}

/** Whether a kind was read only in part, so a count of it is a lower bound and an empty list proves nothing. */
function readInPart(cov: VeleroKindCoverage | undefined): boolean {
  return !cov || INCOMPLETE_COVERAGE_STATES.has(cov.state)
}

function schedulesFact(schedules: VeleroScheduleReading[], partial: boolean): SummaryFact {
  if (schedules.length === 0) {
    return partial ? { label: 'Schedules', value: 'unknown — not read', tone: 'unknown' } : { label: 'Schedules', value: 'none', tone: 'neutral' }
  }
  const of = partial ? `${schedules.length} Radar can read` : `${schedules.length}`
  const needing = schedules.filter((s) => s.attention || s.state === 'rejected')
  if (needing.length === 0) {
    // A cron Radar cannot read is not known to be fine.
    const unreadable = schedules.filter((s) => s.state === 'unknown').length
    if (unreadable > 0) return { label: 'Schedules', value: `${of}, ${unreadable} with a cron Radar can’t read`, tone: 'unknown' }
    return { label: 'Schedules', value: `${of}, none need attention`, tone: 'healthy' }
  }
  const worst = needing.some((s) => veleroScheduleTone(s) === 'unhealthy') ? 'unhealthy' : 'alert'
  return { label: 'Schedules', value: `${needing.length} of ${of} need attention`, tone: worst }
}

function newestBackupFact(schedules: VeleroScheduleReading[], onInspect: VeleroScreenProps['onInspect']): SummaryFact {
  const label = 'Newest successful scheduled backup'
  let newest: { at: string; namespace: string; name: string } | undefined
  for (const s of schedules) {
    const run = s.runs.newestCompleted
    if (!run) continue
    const at = run.completed ?? run.ranAt
    if (!newest || Date.parse(at) > Date.parse(newest.at)) newest = { at, namespace: s.namespace, name: run.name }
  }
  const unread = schedules.some((s) => s.runs.read === 'unknown')
  if (!newest) {
    if (unread) return { label, value: 'unknown — no access to backups', tone: 'unknown' }
    return { label, value: schedules.length === 0 ? 'no schedules' : 'none kept', tone: schedules.length === 0 ? 'neutral' : 'alert' }
  }
  const run = newest
  return {
    label,
    // An age is not a verdict: whether it is late is each schedule's state.
    tone: 'neutral',
    value: (
      <Tooltip content={`${run.name} · ${veleroLocalTime(run.at)}${unread ? ' · some schedules’ backups were not read' : ''}`}>
        <button type="button" onClick={() => onInspect(veleroResource('Backup', run.namespace, run.name))} className="text-accent-text hover:underline">
          {formatRelativeAgeTime(run.at)}
        </button>
      </Tooltip>
    ),
  }
}

function storageFact(locations: VeleroLocationReading[], partial: boolean): SummaryFact {
  if (locations.length === 0) {
    return partial ? { label: 'Storage', value: 'unknown — not read', tone: 'unknown' } : { label: 'Storage', value: 'no locations', tone: 'unhealthy' }
  }
  const available = locations.filter((l) => l.phase === 'Available').length
  const unavailable = locations.filter((l) => l.phase === 'Unavailable').length
  const tone: StatusTone = unavailable > 0 ? 'unhealthy' : available === locations.length ? 'healthy' : 'unknown'
  const of = `${locations.length}${partial ? ' Radar can read' : ''}`
  return { label: 'Storage', value: `${available} of ${of} ${locations.length === 1 && !partial ? 'location' : 'locations'} available`, tone }
}

/** One line answering "is Velero working?" before the detail below it. */
function VeleroSummary({ facts }: { facts: SummaryFact[] }) {
  return (
    <dl className="flex flex-wrap gap-x-7 gap-y-2 text-sm" aria-label="Velero at a glance">
      {facts.map((f) => (
        <div key={f.label} className="flex items-center gap-1.5">
          <StatusDot tone={f.tone} />
          <dt className="text-theme-text-tertiary">{f.label}:</dt>
          <dd className="font-medium text-theme-text-primary">{f.value}</dd>
        </div>
      ))}
    </dl>
  )
}

function AttentionGroup({ id, title, problems, onNavigate }: { id: string; title: string; problems: WorkspaceProblem[]; onNavigate: (ref: ResourceRef) => void }) {
  const [showAll, setShowAll] = useState(false)
  const shown = problems.slice(0, GROUP_FOLD)
  const folded = problems.slice(GROUP_FOLD)
  const foldId = `velero-attention-${id}-more`
  return (
    <div>
      <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
        {title} <span className="font-normal">· {problems.length}</span>
      </h3>
      <ProblemList problems={shown} rootKind="" onNavigate={onNavigate} />
      {folded.length > 0 && (
        <>
          <Collapse open={showAll} id={foldId}>
            <div className="pt-2">
              <ProblemList problems={folded} rootKind="" onNavigate={onNavigate} />
            </div>
          </Collapse>
          <button
            type="button"
            aria-expanded={showAll}
            aria-controls={foldId}
            onClick={() => setShowAll((v) => !v)}
            className="mt-2 inline-flex items-center gap-1 text-xs font-medium text-accent-text hover:underline"
          >
            <CollapseChevron open={showAll} />
            {showAll ? 'Show fewer' : `Show ${folded.length} more`}
          </button>
        </>
      )}
    </div>
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
      ...data.installations
        .filter((i) => (installation === null || i.namespace === installation) && i.server.replicas === 0)
        .map((i) => serverProblem(i.namespace, i.server.deployment)),
      ...schedules.filter((s) => s.attention).map(scheduleProblem),
      ...locations.filter((l) => l.attention).map(locationProblem),
      ...issues.map(veleroIssueProblem),
    ],
    [issues, schedules, locations, data.installations, installation],
  )
  const capped = cappedIssueNotes(data, issues, installation)
  const groups = useMemo(
    () => ATTENTION_GROUPS.map((g) => ({ ...g, problems: attention.filter((p) => attentionGroup(p) === g.id) })).filter((g) => g.problems.length > 0),
    [attention],
  )
  const facts = [
    serverFact(inInstallation(data.installations, installation)),
    schedulesFact(schedules, readInPart(data.coverage.schedules)),
    newestBackupFact(schedules, onInspect),
    storageFact(locations, readInPart(data.coverage.backupStorageLocations)),
  ]
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
            <Sub>{veleroLocalTime(s.nextRun)}</Sub>
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
          <Tooltip content={veleroLocalTime(s.lastSubmitted)}>
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
        if (newest.run) {
          return (
            <Tooltip content={veleroLocalTime(newest.run.completed ?? newest.run.ranAt)}>
              <span>{newest.text}</span>
            </Tooltip>
          )
        }
        return s.runs.read === 'unknown' ? <span className="text-theme-text-tertiary">{newest.text}</span> : <NoneKept />
      },
    },
    { header: 'Location', width: '11%', cell: (s) => <LocationCell s={s} /> },
  ]

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <VeleroWorkspaceHeader title="Overview" data={data} installation={installation} onInstallation={onInstallation} />
      <ScreenBody>
        <VeleroCoverageNotice data={data} kinds={['schedules', 'backups', 'backupStorageLocations', 'restores', 'backupRepositories']} />
        <VeleroSummary facts={facts} />
        <section aria-labelledby="velero-attention">
          <h2 id="velero-attention" className="mb-2 text-sm font-semibold text-theme-text-primary">Needs attention</h2>
          <div className="rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
            {attention.length === 0 ? (
              <p className="text-sm text-theme-text-tertiary">{VELERO_NOTHING_NEEDS_ATTENTION}</p>
            ) : (
              <div className="space-y-4">
                {groups.map((g) => (
                  <AttentionGroup key={g.id} id={g.id} title={g.title} problems={g.problems} onNavigate={navigate} />
                ))}
              </div>
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
