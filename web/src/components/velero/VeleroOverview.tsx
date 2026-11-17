import { useMemo, useState, type ReactNode } from 'react'
import {
  Collapse,
  CollapseChevron,
  ProblemList,
  StatusDot,
  Tooltip,
  VELERO_NONE_KEPT_SHORT,
  VELERO_RUNS_UNKNOWN,
  VeleroScheduleStateBadge,
  formatRelativeAgeTime,
  veleroScheduleNeverRan,
  veleroScheduleTone,
  veleroLocalTime,
  type ResourceRef,
  type StatusTone,
  type VeleroLocationReading,
  type VeleroScheduleReading,
  type WorkspaceProblem,
} from '@skyhook-io/k8s-ui'
import { refToSelectedResource } from '../../utils/navigation'
import { ScreenBody, SectionTable, Sub, type TableColumn } from '../workspace/layout'
import { coverageEmpty } from '../workspace/coverage'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { readInPart, veleroAttention } from './attention'
import { VELERO_LOCATIONS_PATH, VELERO_SCHEDULES_PATH } from './routes'
import { KEPT_FOR_HEADER, KeptForCell, KeptForLine, NextRunCell } from './VeleroSchedules'
import { useMediaQuery } from '../../hooks/useMediaQuery'
import { namespacesNoScheduleIncludes } from './coverage'
import {
  VELERO_WIDE,
  VeleroCoverageNotice,
  VeleroName,
  VeleroWorkspaceHeader,
  WholeNames,
  inInstallation,
  namesAndMore,
  veleroResource,
  type VeleroScreenProps,
} from './shared'

export const VELERO_NOTHING_NEEDS_ATTENTION = 'Nothing needs attention.'
/** The same, when part of Velero could not be read: an empty list there proves less. */
export const VELERO_NOTHING_IN_WHAT_WAS_READ = 'Nothing in what Radar can read needs attention.'

const ATTENTION_KINDS = ['schedules', 'backups', 'restores', 'backupStorageLocations', 'backupRepositories'] as const

// Rows shown across all groups before folding, so a burst of failed runs cannot
// push the server, schedule and storage groups off the screen. Every group
// shows at least its first row.
const ATTENTION_BUDGET = 8

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

interface SummaryFact {
  label: string
  value: ReactNode
  tone: StatusTone
  /** The kind list this fact summarises, opened from its label. */
  list?: { path: string; title: string }
}

function serverFact(installations: VeleroScreenProps['data']['installations']): SummaryFact {
  const read = installations.filter((i) => i.server.replicas !== undefined)
  const down = read.filter((i) => i.server.replicas === 0)
  // Kubernetes leaves readyReplicas out at zero, so absent reads as none ready.
  const notReady = read.filter((i) => (i.server.replicas ?? 0) > 0 && !i.server.readyReplicas)
  const where = (list: typeof installations) => (installations.length > 1 ? ` in ${list.map((i) => i.namespace).join(', ')}` : '')
  if (installations.length === 0 || read.length === 0) return { label: 'Velero server', value: 'unknown', tone: 'unknown' }
  if (down.length > 0) return { label: 'Velero server', value: `not running${where(down)}`, tone: 'unhealthy' }
  if (notReady.length > 0) return { label: 'Velero server', value: `not ready${where(notReady)}`, tone: 'degraded' }
  return { label: 'Velero server', value: read.length < installations.length ? 'running where Radar can read it' : 'running', tone: 'healthy' }
}

/** Whether a kind was read only in part, so a count of it is a lower bound and an empty list proves nothing. */
function schedulesFact(schedules: VeleroScheduleReading[], partial: boolean): SummaryFact {
  if (schedules.length === 0) {
    return partial ? { label: 'Schedules', value: 'unknown — not read', tone: 'unknown' } : { label: 'Schedules', value: 'none', tone: 'neutral' }
  }
  const of = partial ? `${schedules.length} Radar can read` : `${schedules.length}`
  const needing = schedules.filter((s) => s.attention || s.state === 'rejected')
  const paused = schedules.filter((s) => s.state === 'paused').length
  const ofPaused = paused > 0 ? `${of}, ${paused} paused` : of
  if (needing.length === 0) {
    // A cron Radar cannot read is not known to be fine.
    const unreadable = schedules.filter((s) => s.state === 'unknown').length
    if (unreadable > 0) return { label: 'Schedules', value: `${ofPaused}, ${unreadable} with a cron Radar can’t read`, tone: 'unknown' }
    return { label: 'Schedules', value: `${ofPaused}, none need attention`, tone: 'healthy' }
  }
  const worst = needing.some((s) => veleroScheduleTone(s) === 'unhealthy') ? 'unhealthy' : 'alert'
  return { label: 'Schedules', value: `${needing.length} of ${of} need attention${paused > 0 ? `, ${paused} paused` : ''}`, tone: worst }
}

function newestBackupFact(schedules: VeleroScheduleReading[], schedulesPartial: boolean, onInspect: VeleroScreenProps['onInspect']): SummaryFact {
  const label = 'Newest successful scheduled backup'
  let newest: { at: string; namespace: string; name: string } | undefined
  for (const s of schedules) {
    const run = s.runs.newestCompleted
    if (!run) continue
    const at = run.completed ?? run.ranAt
    if (!newest || Date.parse(at) > Date.parse(newest.at)) newest = { at, namespace: s.namespace, name: run.name }
  }
  // Schedules or their backups Radar could not read may hold a newer one.
  const unread = schedulesPartial || schedules.some((s) => s.runs.read === 'unknown')
  if (!newest) {
    if (unread) return { label, value: schedulesPartial && schedules.length === 0 ? 'unknown — not read' : 'unknown — no access to backups', tone: 'unknown' }
    return { label, value: schedules.length === 0 ? 'no schedules' : 'none kept', tone: schedules.length === 0 ? 'neutral' : 'alert' }
  }
  const run = newest
  return {
    label,
    // An age is not a verdict: whether it is late is each schedule's state.
    tone: 'neutral',
    value: (
      <Tooltip content={`${run.name} · ${veleroLocalTime(run.at)}${unread ? ' · some schedules or their backups were not read' : ''}`}>
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
function VeleroSummary({ facts, onOpenList }: { facts: SummaryFact[]; onOpenList: (path: string) => void }) {
  return (
    <dl className="flex flex-wrap gap-x-7 gap-y-2 text-sm" aria-label="Velero at a glance">
      {facts.map((f) => (
        <div key={f.label} className="flex items-center gap-1.5">
          <StatusDot tone={f.tone} />
          <dt className="text-theme-text-tertiary">
            {f.list ? (
              <Tooltip content={f.list.title}>
                <button type="button" onClick={() => onOpenList(f.list!.path)} className="text-accent-text hover:underline">
                  {f.label}
                </button>
              </Tooltip>
            ) : (
              f.label
            )}
            :
          </dt>
          <dd className="font-medium text-theme-text-primary">
            {f.list ? (
              <button type="button" onClick={() => onOpenList(f.list!.path)} className="text-left hover:underline">
                {f.value}
              </button>
            ) : (
              f.value
            )}
          </dd>
        </div>
      ))}
    </dl>
  )
}

function AttentionGroup({ id, title, problems, visible, onNavigate }: { id: string; title: string; problems: WorkspaceProblem[]; visible: number; onNavigate: (ref: ResourceRef) => void }) {
  const [showAll, setShowAll] = useState(false)
  const shown = problems.slice(0, visible)
  const folded = problems.slice(visible)
  const foldId = `velero-attention-${id}-more`
  return (
    <div>
      <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
        {title} <span className="font-normal">· {problems.length}</span>
      </h3>
      <ProblemList problems={shown} rootKind="" onNavigate={onNavigate} hideOrigin subjectFirst />
      {folded.length > 0 && (
        <>
          <Collapse open={showAll} id={foldId}>
            <div className="pt-2">
              <ProblemList problems={folded} rootKind="" onNavigate={onNavigate} hideOrigin subjectFirst />
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

const NAMES_SHOWN = 8

function CoverageRow({ tone, label, title, children }: { tone: StatusTone; label: string; title?: string; children: ReactNode }) {
  return (
    <div className="flex items-baseline gap-2 text-sm">
      <span className="translate-y-[1px]"><StatusDot tone={tone} /></span>
      <div className="min-w-0">
        <span className="text-theme-text-tertiary">{title ? <Tooltip content={title}><span className="underline decoration-dotted underline-offset-2">{label}</span></Tooltip> : label}:</span>{' '}
        <span className="text-theme-text-primary">{children}</span>
      </div>
    </div>
  )
}

/**
 * What the schedules leave out, as information: namespaces no schedule's
 * template selects, and schedules paused on purpose. Neither counts toward the
 * attention badge.
 */
function ScheduleCoverage({ data, schedules, namespaces, onInspect }: { data: VeleroScreenProps['data']; schedules: VeleroScheduleReading[]; namespaces: VeleroNamespaceList; onInspect: VeleroScreenProps['onInspect'] }) {
  const namespaceList = Array.isArray(namespaces) ? namespaces : undefined
  const isError = namespaces === 'error'
  const paused = schedules.filter((s) => s.state === 'paused')
  const coverage = useMemo(() => {
    // Every schedule Radar can see counts, whichever installation is picked:
    // a namespace is backed up if any installation's schedule selects it.
    if (!namespaceList || data.forcedNamespace || readInPart(data.coverage.schedules)) return undefined
    return namespacesNoScheduleIncludes(namespaceList, data.schedules, data.installations.map((i) => i.namespace))
  }, [namespaceList, data])
  const unknown = !!data.forcedNamespace || readInPart(data.coverage.schedules)
  if (!coverage && !unknown && !isError && paused.length === 0) return null
  const leftOut = coverage && coverage.leftOut > 0 ? ' Kubernetes’ own namespaces and Velero’s are not listed.' : ''
  const title = `Namespaces you can see that no schedule's template selects. A schedule counts even when it is paused or failing, or selects only some resources by label or kind.${leftOut}`
  return (
    <section aria-labelledby="velero-coverage">
      <h2 id="velero-coverage" className="mb-2 text-sm font-semibold text-theme-text-primary">Coverage</h2>
      <div className="space-y-1.5 rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
        {unknown ? (
          <CoverageRow tone="unknown" label="Namespaces not in any schedule" title={title}>
            unknown, Radar can’t read every schedule
          </CoverageRow>
        ) : isError ? (
          <CoverageRow tone="unknown" label="Namespaces not in any schedule" title={title}>
            unknown, Radar couldn’t list namespaces
          </CoverageRow>
        ) : coverage && coverage.uncovered.length === 0 ? (
          <CoverageRow tone="healthy" label="Namespaces not in any schedule" title={title}>
            none
          </CoverageRow>
        ) : coverage ? (
          <CoverageRow tone="degraded" label="Namespaces not in any schedule" title={title}>
            {coverage.uncovered.length > NAMES_SHOWN ? (
              <Tooltip content={coverage.uncovered.join(', ')}>
                <span><WholeNames text={namesAndMore(coverage.uncovered, NAMES_SHOWN)} /></span>
              </Tooltip>
            ) : (
              <WholeNames text={namesAndMore(coverage.uncovered, NAMES_SHOWN)} />
            )}
          </CoverageRow>
        ) : null}
        {paused.length > 0 && (
          <CoverageRow tone="neutral" label="Paused">
            {paused.map((s, i) => (
              <span key={`${s.namespace}/${s.name}`}>
                {i > 0 && ', '}
                <button type="button" onClick={() => onInspect(veleroResource('Schedule', s.namespace, s.name))} className="font-mono text-accent-text hover:underline">
                  {s.name}
                </button>
              </span>
            ))}
            {' '}— no backups until {paused.length === 1 ? 'it is' : 'they are'} resumed
          </CoverageRow>
        )}
      </div>
    </section>
  )
}

function LastSuccessCell({ s }: { s: VeleroScheduleReading }) {
  if (s.runs.read === 'unknown') return <span className="text-theme-text-tertiary">{VELERO_RUNS_UNKNOWN}</span>
  const run = s.runs.newestCompleted
  if (!run) return <span className="text-theme-text-tertiary">{veleroScheduleNeverRan(s) ? 'Never' : VELERO_NONE_KEPT_SHORT}</span>
  const at = run.completed ?? run.ranAt
  return (
    <Tooltip content={`${run.name} · ${veleroLocalTime(at)}`}>
      <span>{formatRelativeAgeTime(at)}</span>
    </Tooltip>
  )
}

function StoredInCell({ s, locations }: { s: VeleroScheduleReading; locations: VeleroLocationReading[] }) {
  if (!s.location.name) return <span className="text-theme-text-tertiary">Unresolved</span>
  const loc = locations.find((l) => l.namespace === s.namespace && l.name === s.location.name)
  const tone: StatusTone = !loc ? 'unknown' : loc.phase === 'Available' ? 'healthy' : loc.phase === 'Unavailable' ? 'unhealthy' : 'unknown'
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      <StatusDot tone={tone} />
      <VeleroName name={s.location.name} />
      {loc && loc.phase !== 'Available' && <span className="shrink-0 text-xs text-theme-text-tertiary">{loc.phase ? loc.phase.toLowerCase() : 'not validated'}</span>}
    </span>
  )
}

/**
 * Each schedule's protection in one row: its state, newest success, where it
 * writes and for how long, so a healthy installation shows its evidence rather
 * than only the absence of problems.
 */
function ScheduleProtection({ schedules, locations, multi, onInspect, inspected, empty }: { schedules: VeleroScheduleReading[]; locations: VeleroLocationReading[]; multi: boolean; onInspect: VeleroScreenProps['onInspect']; inspected: VeleroScreenProps['inspected']; empty: string }) {
  // Narrow: the retention goes under where the runs are stored.
  const wide = useMediaQuery(VELERO_WIDE)
  const columns: TableColumn<VeleroScheduleReading>[] = [
    {
      header: 'Schedule',
      cell: (s) => (
        <>
          <VeleroName name={s.name} />
          {multi && <Sub><WholeNames text={s.namespace} /></Sub>}
        </>
      ),
    },
    { header: 'State', width: wide ? '130px' : '120px', cell: (s) => <VeleroScheduleStateBadge reading={s} /> },
    { header: 'Last success', width: wide ? '110px' : '100px', cell: (s) => <LastSuccessCell s={s} /> },
    { header: 'Next run', width: wide ? '120px' : '110px', cell: (s) => <NextRunCell s={s} /> },
    {
      header: 'Stored in',
      width: wide ? '170px' : '140px',
      cell: (s) => (
        <>
          <StoredInCell s={s} locations={locations} />
          {!wide && <KeptForLine s={s} />}
        </>
      ),
    },
    ...(wide ? [{ header: KEPT_FOR_HEADER, width: '110px', cell: (s: VeleroScheduleReading) => <KeptForCell s={s} /> }] : []),
  ]
  return (
    <SectionTable
      title="Schedules"
      columns={columns}
      rows={schedules}
      rowKey={(s) => `${s.namespace}/${s.name}`}
      rowResource={(s) => veleroResource('Schedule', s.namespace, s.name)}
      onInspect={onInspect}
      inspected={inspected}
      empty={empty}
      minWidth={wide ? 860 : 580}
    />
  )
}

/** The namespaces the caller can see, from the namespace list; undefined while it loads. */
export type VeleroNamespaceList = string[] | 'error' | undefined

export function VeleroOverview({ data, installation, onInstallation, onInspect, inspected, namespaces }: VeleroScreenProps & { namespaces?: VeleroNamespaceList }) {
  const navigateTo = useWorkspaceNavigate()
  const schedules = useMemo(() => inInstallation(data.schedules, installation), [data.schedules, installation])
  const locations = useMemo(() => inInstallation(data.locations, installation), [data.locations, installation])
  const issues = useMemo(
    () => (installation === null ? data.issues : data.issues.filter((i) => (i.namespace ?? '') === installation)),
    [data.issues, installation],
  )
  const attention = useMemo(() => veleroAttention(data, installation), [data, installation])
  const capped = cappedIssueNotes(data, issues, installation)
  const groups = useMemo(() => {
    let budget = ATTENTION_BUDGET
    return ATTENTION_GROUPS.map((g) => ({ ...g, problems: attention.filter((p) => attentionGroup(p) === g.id) }))
      .filter((g) => g.problems.length > 0)
      .map((g) => {
        const visible = Math.max(1, Math.min(g.problems.length, budget))
        budget = Math.max(0, budget - visible)
        return { ...g, visible }
      })
  }, [attention])
  const facts: SummaryFact[] = [
    serverFact(inInstallation(data.installations, installation)),
    { ...schedulesFact(schedules, readInPart(data.coverage.schedules)), list: { path: VELERO_SCHEDULES_PATH, title: 'Every schedule, its next run and its newest runs' } },
    newestBackupFact(schedules, readInPart(data.coverage.schedules), onInspect),
    { ...storageFact(locations, readInPart(data.coverage.backupStorageLocations)), list: { path: VELERO_LOCATIONS_PATH, title: 'Every storage location, the backups in it and the schedules writing to it' } },
  ]
  const navigate = (ref: ResourceRef) => onInspect(refToSelectedResource(ref))

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <VeleroWorkspaceHeader title="Overview" data={data} installation={installation} onInstallation={onInstallation} />
      <ScreenBody>
        <VeleroCoverageNotice data={data} kinds={['schedules', 'backups', 'backupStorageLocations', 'restores', 'backupRepositories']} />
        <VeleroSummary facts={facts} onOpenList={(path) => navigateTo(path)} />
        <section aria-labelledby="velero-attention">
          <h2 id="velero-attention" className="mb-2 text-sm font-semibold text-theme-text-primary">Needs attention</h2>
          <div className="rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
            {attention.length === 0 ? (
              <p className="text-sm text-theme-text-tertiary">
                {ATTENTION_KINDS.some((k) => readInPart(data.coverage[k])) ? VELERO_NOTHING_IN_WHAT_WAS_READ : VELERO_NOTHING_NEEDS_ATTENTION}
              </p>
            ) : (
              <div className="space-y-4">
                {groups.map((g) => (
                  <AttentionGroup key={g.id} id={g.id} title={g.title} problems={g.problems} visible={g.visible} onNavigate={navigate} />
                ))}
              </div>
            )}
            {capped.map((note) => (
              <p key={note} className="mt-2 text-xs text-theme-text-tertiary">{note}</p>
            ))}
          </div>
        </section>
        <ScheduleCoverage data={data} schedules={schedules} namespaces={namespaces} onInspect={onInspect} />
        <ScheduleProtection
          schedules={schedules}
          locations={locations}
          multi={data.installations.length > 1}
          onInspect={onInspect}
          inspected={inspected}
          empty={coverageEmpty(data.coverage.schedules, 'Velero schedules')}
        />
      </ScreenBody>
    </div>
  )
}
