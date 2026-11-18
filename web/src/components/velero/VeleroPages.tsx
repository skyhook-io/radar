import { Fragment, createContext, useContext, useState, type ReactNode } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import {
  Disclosure,
  FactGrid,
  FactRow,
  OpenIssueContext,
  SectionHeading,
  StatusDot,
  Tooltip,
  WorkspaceProblemCallout,
  formatRelativeAgeTime,
  getBackupErrors,
  getBackupStatus,
  getBackupValidationErrors,
  getBackupWarnings,
  getRestoreBackupName,
  getRestoreErrors,
  getRestoreNamespaceMapping,
  getRestoreStatus,
  getRestoreValidationErrors,
  getRestoreWarnings,
  refToSelectedResource,
  toneTextClass,
  VeleroZoneValue,
  veleroCadenceText,
  veleroDefaultText,
  veleroFrequencyText,
  veleroLocalTime,
  veleroLocationReasonText,
  veleroMomentText,
  veleroNextRunText,
  veleroNextRunTitle,
  veleroPlainReason,
  veleroDeclaredRetention,
  VELERO_SERVER_DEFAULT_RETENTION,
  veleroScheduleBadge,
  veleroScheduleNeverRan,
  veleroScheduleReasonText,
  VELERO_NONE_KEPT_SHORT,
  VELERO_RUNS_UNKNOWN,
  type NavigateToResource,
  type StatusTone,
  type VeleroLocationReading,
  type VeleroRun,
  type VeleroScheduleReading,
  type VeleroWorkspaceResponse,
} from '@skyhook-io/k8s-ui'
import { LookupFailureNote } from '@skyhook-io/k8s-ui/components/resources/renderers/LookupFailureNote'
import { useRadarFeature } from '../../api/client'
import { useVeleroRuns, useVeleroScheduleActivity, useVeleroWorkspace, type VeleroRestoreRow, type VeleroRunRow, type VeleroRunsResponse } from '../../api/velero'
import { useVeleroStoredBackups } from '../../api/policy'
import { useNavCustomization } from '../../context/NavCustomization'
import { currentPageLabel, issuesPathForSubject } from '../../utils/page-links'
import { Notice, RefreshFailedNotice, Segments, SectionTable } from '../workspace/layout'
import { INCOMPLETE_COVERAGE_STATES, coverageLabel } from '../workspace/coverage'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { RunMessages } from './VeleroRunMessagesHost'
import { VeleroBackupDeletionRow } from './VeleroDeleteBackup'
import { Expires, FAILED, Outcome, RunCounts, RunOutcome, RunsReadNotice, When, durationText, expired, runViewOptions, runsEmpty, useRunView } from './runs'
import { VELERO_DETAIL_KINDS, veleroDetailPath } from './routes'
import { VELERO_MESSAGE_LABEL, VELERO_WIDE, VeleroName, restoreTargets, selectionText, veleroResource, type RestoreSelection } from './shared'
import { OutcomeCell, RestoreIntoCell, StartedCell } from './VeleroRunLists'
import { useMediaQuery } from '../../hooks/useMediaQuery'
import { veleroObjectProblems } from './attention'

// The pages' own sections, beside Radar's detail view: a composed Overview for
// each Velero kind, and the tabs that list what the object relates to. Every
// value says where it comes from or reads unknown. Declared settings are named
// as declared, and nothing here says a backup would restore: Velero never
// tests a restore.

const CARD = 'rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm'

/** Where an Overview is shown: on the object's page, or in the drawer over a list. */
interface SummaryPlace {
  drawer: boolean
  plural: string
  namespace: string
  name: string
}
const SummaryPlaceContext = createContext<SummaryPlace | null>(null)

// The drawer is already a panel, so its sections are not framed again.
function Card({ title, children }: { title?: string; children: ReactNode }) {
  const drawer = !!useContext(SummaryPlaceContext)?.drawer
  return (
    <section className={drawer ? undefined : CARD}>
      {title && <SectionHeading>{title}</SectionHeading>}
      {children}
    </section>
  )
}

function Note({ children }: { children: ReactNode }) {
  return <div className="text-xs text-theme-text-tertiary">{children}</div>
}

function Link({ children, onClick }: { children: ReactNode; onClick?: () => void }) {
  if (!onClick) return <span>{children}</span>
  return (
    <button type="button" onClick={onClick} className="text-left text-accent-text hover:underline">
      {children}
    </button>
  )
}

/**
 * "Restores →": on the page, opens another of its tabs as a tab click does;
 * in the drawer, opens that tab on the object's page.
 */
function TabLink({ tab, children }: { tab: string; children: ReactNode }) {
  const [params, setParams] = useSearchParams()
  const location = useLocation()
  const place = useContext(SummaryPlaceContext)
  const navigate = useWorkspaceNavigate()
  const open = () => {
    // The page pins its context itself.
    if (place?.drawer) {
      navigate(veleroDetailPath({ plural: place.plural, namespace: place.namespace, name: place.name }, undefined, tab), { state: { returnLabel: currentPageLabel() } })
      return
    }
    const next = new URLSearchParams(params)
    next.set('tab', tab)
    setParams(next, { state: location.state })
  }
  return (
    <button type="button" onClick={open} className="whitespace-nowrap text-xs text-accent-text hover:underline">
      {children} →
    </button>
  )
}

/** Velero's own text, folded under Radar's plain reading of it. */
function RawMessage({ message }: { message: string }) {
  return (
    <Disclosure summary={VELERO_MESSAGE_LABEL} className="mt-1" summaryClassName="text-xs text-theme-text-tertiary hover:text-theme-text-secondary">
      <div className="mt-1 whitespace-pre-wrap break-words font-mono text-xs text-theme-text-secondary">{message}</div>
    </Disclosure>
  )
}

/** "as declared", and that filters narrow it when they do. */
function declaredNote(narrowed: boolean): string {
  return narrowed ? 'as declared · narrowed by resource or label filters' : 'as declared'
}

const TONE_TEXT = { ok: 'text-success-text', warn: 'text-warning-text', bad: 'text-error-text', unknown: 'text-theme-text-secondary' } as const
type Tone = keyof typeof TONE_TEXT

/**
 * A storage location's state as a page reads it. Missing is said only when the
 * namespace's locations were read in full; otherwise it is unread.
 */
export function locationState(name: string, namespace: string, workspace: VeleroWorkspaceResponse | undefined): { loc?: VeleroLocationReading; text: string; tone: Tone; missing?: boolean } {
  const loc = workspace?.locations.find((l) => l.namespace === namespace && l.name === name)
  if (loc) {
    if (loc.phase === 'Available') return { loc, text: 'available', tone: 'ok' }
    if (!loc.phase) return { loc, text: 'not validated yet', tone: 'unknown' }
    return { loc, text: loc.phase.toLowerCase(), tone: 'bad' }
  }
  const cov = workspace?.coverage.backupStorageLocations
  if (workspace && cov?.state === 'full') return { text: 'not found: Velero has no such location', tone: 'bad', missing: true }
  return { text: 'not read', tone: 'unknown' }
}

function LocationValue({ name, namespace, workspace, onNavigate }: { name: string; namespace: string; workspace: VeleroWorkspaceResponse | undefined; onNavigate?: NavigateToResource }) {
  const state = locationState(name, namespace, workspace)
  return (
    <>
      {state.loc ? <Link onClick={() => onNavigate?.(veleroResource('BackupStorageLocation', namespace, name))}>{name}</Link> : <span>{name}</span>}
      <span className={`ml-2 text-xs ${TONE_TEXT[state.tone]}`}>{state.text}</span>
    </>
  )
}

function useWorkspace() {
  const { support } = useRadarFeature('veleroWorkspace')
  return useVeleroWorkspace({ enabled: support === 'supported' })
}

/** "finished 2h ago · took 3s", or how far it got. */
function whenText(status: any): string | undefined {
  const took = durationText(status.startTimestamp, status.completionTimestamp)
  if (status.completionTimestamp) return `finished ${veleroMomentText(status.completionTimestamp)}${took ? ` · took ${took}` : ''}`
  if (status.startTimestamp) return `started ${veleroMomentText(status.startTimestamp)}`
  return undefined
}

function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? '' : 's'}`
}

/** "2 errors · 1 warning": each red or amber when there are some, dimmed at zero. */
function CountsText({ errors, warnings }: { errors: number; warnings: number }) {
  return (
    <>
      <span className={errors > 0 ? toneTextClass('unhealthy') : 'text-theme-text-tertiary'}>{plural(errors, 'error')}</span>
      <span className="text-theme-text-tertiary"> · </span>
      <span className={warnings > 0 ? toneTextClass('degraded') : 'text-theme-text-tertiary'}>{plural(warnings, 'warning')}</span>
    </>
  )
}

// ---- Issues -------------------------------------------------------------

/**
 * The page's lead: what Radar has for this object, its Velero reading when that
 * needs attention and Velero's issues about it, as plain banners, each with its
 * next step (and, for an issue, a way to the Issues page). The operational
 * issues the detail view found stand in when the Velero workspace isn't read
 * or holds nothing for it (it keeps the newest 50 issues per installation).
 */
function PageLead({ kind, namespace, name, leadContent, onNavigate }: { kind: string; namespace: string; name: string; leadContent?: ReactNode; onNavigate?: NavigateToResource }) {
  const workspace = useWorkspace()
  const navigate = useWorkspaceNavigate()
  const [params] = useSearchParams()
  const issuesTakenOver = !!useNavCustomization().fleetTakeoverHref?.('issues')
  const problems = workspace.data ? veleroObjectProblems(workspace.data, kind, namespace, name) : []
  if (problems.length === 0) return <>{leadContent}</>
  return (
    <OpenIssueContext.Provider value={issuesTakenOver ? undefined : (p) => navigate(issuesPathForSubject(p.subject, params.get('namespaces')))}>
      <div className="space-y-3">
        {problems.map((problem) => (
          <WorkspaceProblemCallout
            key={problem.id}
            problem={problem}
            rootKind={kind}
            subjectIsSelf={problem.subject.kind === kind && problem.subject.name === name}
            onNavigate={(ref) => onNavigate?.(refToSelectedResource(ref))}
          />
        ))}
      </div>
    </OpenIssueContext.Provider>
  )
}

// ---- Backup -------------------------------------------------------------

// The phases Velero restores from (its restore controller refuses any other).
const RESTORABLE_PHASES = new Set(['Completed', 'PartiallyFailed'])

/** Whether a restore from this backup can work: "No" with the reason where certain, else not verified. */
function restoreAnswer(resource: any, location: ReturnType<typeof locationState> | null): string | null {
  const phase: string = resource?.status?.phase ?? ''
  if (phase === 'Deleting' || resource?.metadata?.deletionTimestamp) return 'No: it is being deleted'
  if (phase === 'Failed') return 'No: it did not complete'
  if (!RESTORABLE_PHASES.has(phase)) return 'No: it hasn’t finished'
  if (location?.missing) return 'No: its storage location doesn’t exist'
  if (location?.loc?.phase === 'Unavailable') return 'No: its storage location is unavailable, and Velero refuses a restore from an unavailable location'
  return null
}

function VeleroBackupOverview({ resource, onNavigate }: { resource: any; onNavigate?: NavigateToResource }) {
  const namespace: string = resource?.metadata?.namespace ?? ''
  const workspace = useWorkspace()
  const status = resource?.status ?? {}
  const spec = resource?.spec ?? {}
  const phase: string = status.phase ?? ''
  const locationName: string = spec.storageLocation ?? ''
  const scheduleName: string | undefined = resource?.metadata?.labels?.['velero.io/schedule-name']
  const sched = scheduleName ? workspace.data?.schedules.find((s) => s.namespace === namespace && s.name === scheduleName) : undefined
  const selection = selectionText(spec, 'All namespaces')
  // Rejected, or failed before it started: nothing was backed up, so there is nothing to keep or restore.
  const rejected = phase === 'FailedValidation'
  const nothing = rejected || (phase === 'Failed' && !status.startTimestamp)
  const rawReason: string | undefined = rejected ? getBackupValidationErrors(resource).join('; ') || undefined : status.failureReason
  const reason = veleroPlainReason(rawReason) ?? rawReason
  const items = status.progress?.totalItems ? `${status.progress.itemsBackedUp ?? 0} of ${status.progress.totalItems} items backed up` : undefined
  const volumes = [
    status.volumeSnapshotsAttempted ? `${status.volumeSnapshotsCompleted ?? 0} of ${status.volumeSnapshotsAttempted} volume snapshots` : '',
    status.csiVolumeSnapshotsAttempted ? `${status.csiVolumeSnapshotsCompleted ?? 0} of ${status.csiVolumeSnapshotsAttempted} CSI snapshots` : '',
    status.backupItemOperationsAttempted ? `${status.backupItemOperationsCompleted ?? 0} of ${status.backupItemOperationsAttempted} plugin operations${status.backupItemOperationsFailed ? `, ${status.backupItemOperationsFailed} failed` : ''}` : '',
  ].filter(Boolean)
  const when = whenText(status)
  const location = locationName ? locationState(locationName, namespace, workspace.data) : null
  const restore = restoreAnswer(resource, location)
  const errors = getBackupErrors(resource)
  const warnings = getBackupWarnings(resource)
  const restores = useVeleroRuns('restores', namespace, { backup: resource?.metadata?.name ?? '' })
  // Runs are newest first: the newest restore from this backup that Velero finished.
  const restoredBefore = restore ? undefined : (restores.data?.runs ?? []).find((r) => RESTORABLE_PHASES.has(r.phase ?? ''))
  return (
    <>
      <RefreshFailedNotice queries={[workspace]} />
      <Card title="At a glance">
        <FactGrid>
          <FactRow label="Outcome">
            <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
              <Outcome badge={getBackupStatus(resource)} />
              {when && <span className="text-xs text-theme-text-secondary">{when}</span>}
            </span>
            {!nothing && (
              <div className="mt-0.5 text-xs text-theme-text-secondary">
                {items && <>{items} · </>}
                <CountsText errors={errors} warnings={warnings} />
              </div>
            )}
            <RunMessages kind="backups" resource={resource} />
          </FactRow>
          <VeleroBackupDeletionRow
            backup={resource}
            renderName={(n) => {
              const mono = <span className="whitespace-nowrap font-mono text-[0.95em]">{n}</span>
              const exists = workspace.data?.locations.some((l) => l.namespace === namespace && l.name === n)
              return exists ? <Link onClick={() => onNavigate?.(veleroResource('BackupStorageLocation', namespace, n))}>{mono}</Link> : mono
            }}
          />
          {nothing && (
            <FactRow label="Backed up">
              <span className={toneTextClass('unhealthy')}>Nothing</span>
              {' — '}
              {rejected ? 'Velero rejected it' : 'it failed before it started'}
              {reason ? `: ${reason}` : ''}
              {reason && rawReason && reason !== rawReason && <RawMessage message={rawReason} />}
            </FactRow>
          )}
          <FactRow label="Namespaces">
            {selection.text}
            <Note>{declaredNote(selection.narrowed)}</Note>
          </FactRow>
          {volumes.length > 0 && <FactRow label="Volumes">{volumes.join(' · ')}</FactRow>}
          <FactRow label="Stored in">
            {locationName ? <LocationValue name={locationName} namespace={namespace} workspace={workspace.data} onNavigate={onNavigate} /> : <span className="text-theme-text-secondary">Not assigned yet</span>}
          </FactRow>
          {!nothing && (
            <FactRow label="Kept until">
              {status.expiration ? (
                <>
                  {veleroLocalTime(status.expiration)}
                  <Note>
                    {expired(status.expiration) ? 'Past its retention: Velero deletes it at its next garbage collection' : veleroMomentText(status.expiration)}
                    {veleroDeclaredRetention(spec.ttl) ? ` · retention ${veleroDeclaredRetention(spec.ttl)}` : ''}
                  </Note>
                </>
              ) : (
                <span className="text-theme-text-secondary">Not reported yet</span>
              )}
            </FactRow>
          )}
          <FactRow label="Made by">
            {scheduleName ? (
              <>
                <Link onClick={() => onNavigate?.(veleroResource('Schedule', namespace, scheduleName))}>Schedule {scheduleName}</Link>
                {sched && <span className="ml-2 text-xs text-theme-text-secondary">{veleroScheduleBadge(sched).label}</span>}
              </>
            ) : (
              'On demand, not by a schedule'
            )}
          </FactRow>
          {!nothing && (
            <FactRow label="Restore">
              <span className="inline-flex flex-wrap items-baseline gap-x-2">
                {restore ? (
                  <span>{restore}</span>
                ) : restoredBefore ? (
                  <span>
                    Restored before:{' '}
                    <Link onClick={() => onNavigate?.(veleroResource('Restore', restoredBefore.namespace, restoredBefore.name))}>{restoredBefore.name}</Link>
                    <span className="text-theme-text-secondary">
                      {' · '}
                      {getRestoreStatus({ status: { phase: restoredBefore.phase } }).text.toLowerCase()}
                      {' · '}
                      <When iso={restoredBefore.completed ?? restoredBefore.started} fallback="time unknown" />
                    </span>
                  </span>
                ) : (
                  <span className="text-theme-text-secondary">Not verified — Velero never tests a restore</span>
                )}
                <TabLink tab="restores">Restores</TabLink>
              </span>
            </FactRow>
          )}
        </FactGrid>
      </Card>
    </>
  )
}

/** The Restores tab's count: restores made from this backup. */
export function VeleroBackupRestoresMark({ namespace, name }: { namespace: string; name: string }) {
  const query = useVeleroRuns('restores', namespace, { backup: name })
  const n = query.data?.runs.length ?? 0
  return n > 0 ? <span className="ml-1 badge-sm bg-theme-elevated">{n}</span> : null
}

/** Restores made from one backup (spec.backupName). */
export function VeleroBackupRestores({ namespace, name, onNavigate }: { namespace: string; name: string; onNavigate?: NavigateToResource }) {
  const query = useVeleroRuns('restores', namespace, { backup: name })
  const rows = query.data?.runs ?? []
  const wide = useMediaQuery(VELERO_WIDE)
  return (
    <div className="space-y-3 p-4">
      <RunsReadNotice query={query} noun="restores" />
      <SectionTable
        title="Restores from this backup"
        subtitle="Restores made from this backup that Velero still keeps"
        columns={[
          { header: 'Restore', cell: (r: VeleroRunRow) => <VeleroName name={r.name} middle /> },
          { header: 'Outcome', width: '170px', cell: (r: VeleroRunRow) => <OutcomeCell kind="Restore" run={r} counts={!wide} /> },
          { header: 'Into', width: wide ? '200px' : '160px', cell: (r: VeleroRestoreRow) => <RestoreIntoCell run={r} /> },
          { header: 'Started', width: '120px', cell: (r: VeleroRunRow) => <StartedCell run={r} folded={!wide} /> },
          ...(wide
            ? [
                { header: 'Took', width: '90px', cell: (r: VeleroRunRow) => durationText(r.started, r.completed) ?? '—' },
                { header: 'Errors · warnings', width: '140px', cell: (r: VeleroRunRow) => <RunCounts errors={r.errors} warnings={r.warnings} /> },
              ]
            : []),
        ]}
        minWidth={wide ? 760 : 560}
        rows={rows}
        rowKey={(r) => r.uid || r.name}
        rowResource={(r) => veleroResource('Restore', r.namespace, r.name)}
        onInspect={onNavigate}
        empty={runsEmpty(query, 'restores', 'No restores from this backup')}
      />
    </div>
  )
}

// ---- Restore ------------------------------------------------------------

/** Where a restore puts things, each destination namespace a link. */
function RestoredIntoValue({ spec, onNavigate }: { spec: RestoreSelection; onNavigate?: NavigateToResource }) {
  const { every, entries, excluded } = restoreTargets(spec)
  const ns = (name: string) => <Link onClick={() => onNavigate?.(refToSelectedResource({ kind: 'Namespace', group: '', namespace: '', name }))}>{name}</Link>
  const named = entries.map((e, i) => (
    <Fragment key={e.from}>
      {i > 0 && ', '}
      {e.to ? (
        <>
          {e.from} → {ns(e.to)}
        </>
      ) : (
        ns(e.from)
      )}
    </Fragment>
  ))
  return (
    <>
      {every ? (
        <>
          Every namespace in the backup
          {entries.length > 0 && <>, with {named}</>}
        </>
      ) : (
        named
      )}
      {excluded.length > 0 && `, except ${excluded.join(', ')}`}
    </>
  )
}

function VeleroRestoreOverview({ resource, onNavigate }: { resource: any; onNavigate?: NavigateToResource }) {
  const namespace: string = resource?.metadata?.namespace ?? ''
  const status = resource?.status ?? {}
  const spec = resource?.spec ?? {}
  const backupName = getRestoreBackupName(resource)
  const mapping = getRestoreNamespaceMapping(resource)
  const narrowed = selectionText(spec, '', 'restore').narrowed
  const rejected = status.phase === 'FailedValidation'
  const rawReason: string | undefined = rejected ? getRestoreValidationErrors(resource).join('; ') || undefined : undefined
  const reason = veleroPlainReason(rawReason) ?? rawReason
  // Velero counts every item it went through, including those it skipped or failed.
  const progress = status.progress?.totalItems ? `${status.progress.itemsRestored ?? 0} of ${status.progress.totalItems} items processed` : undefined
  const when = whenText(status)
  const errors = getRestoreErrors(resource)
  const warnings = getRestoreWarnings(resource)
  return (
    <Card title="At a glance">
      <FactGrid>
        {rejected ? (
          <FactRow label="Restored">
            <span className={toneTextClass('unhealthy')}>Nothing</span>
            {' — Velero rejected it'}
            {reason ? `: ${reason}` : ''}
            {reason && rawReason && reason !== rawReason && <RawMessage message={rawReason} />}
          </FactRow>
        ) : (
          <FactRow label="Restored into">
            <RestoredIntoValue spec={spec} onNavigate={onNavigate} />
            <Note>
              {declaredNote(narrowed)}
              {Object.keys(mapping).length > 0 ? ' · unmapped namespaces keep their names' : ''}
            </Note>
          </FactRow>
        )}
        <FactRow label="Outcome">
          <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
            <Outcome badge={getRestoreStatus(resource)} />
            {warnings > 0 && <span className={`text-xs ${toneTextClass('degraded')}`}>{plural(warnings, 'warning')}</span>}
            {when && <span className="text-xs text-theme-text-secondary">{when}</span>}
          </span>
          {(progress || errors > 0) && (
            <div className="mt-0.5 text-xs text-theme-text-secondary">
              {progress}
              {progress && errors > 0 && ' · '}
              {errors > 0 && <span className={toneTextClass('unhealthy')}>{plural(errors, 'error')}</span>}
            </div>
          )}
          <RunMessages kind="restores" resource={resource} />
        </FactRow>
        <FactRow label="From backup">
          {backupName ? <Link onClick={() => onNavigate?.(veleroResource('Backup', namespace, backupName))}>{backupName}</Link> : spec.scheduleName ? `The newest backup of schedule ${spec.scheduleName}` : '—'}
        </FactRow>
        <FactRow label="Existing objects">
          {spec.existingResourcePolicy === 'update' ? 'Update requested' : 'Left as they are (Velero’s default)'}
          {spec.existingResourcePolicy === 'update' && <Note>Velero updates an existing object where it can, and reports a warning where it can’t.</Note>}
        </FactRow>
      </FactGrid>
    </Card>
  )
}

// ---- Schedule -----------------------------------------------------------

// Recent runs shown as dots; the activity endpoint returns ten.
const RECENT_DOTS = 10

function runTone(phase: string | undefined): StatusTone {
  return getBackupStatus({ status: { phase: phase ?? '' } }).level as StatusTone
}

/** The newest successful run, with a dot saying whether the newest run was it. */
function LastSuccess({ reading }: { reading: VeleroScheduleReading }) {
  const runs = reading.runs
  if (runs.read === 'unknown') return <span className="text-theme-text-tertiary">{VELERO_RUNS_UNKNOWN}</span>
  const success = runs.newestCompleted
  if (!success) {
    if (veleroScheduleNeverRan(reading)) return <span className="text-theme-text-secondary">Never</span>
    return (
      <span className="inline-flex items-center gap-2">
        <StatusDot tone="unknown" />
        <span className="text-theme-text-secondary">{VELERO_NONE_KEPT_SHORT}</span>
      </span>
    )
  }
  const at = success.completed ?? success.ranAt
  const newestFailed = !!runs.newest && runs.newest.name !== success.name && FAILED.has(runs.newest.phase ?? '')
  return (
    <span className="inline-flex flex-wrap items-center gap-x-2">
      <StatusDot tone={newestFailed ? 'alert' : 'healthy'} />
      <Tooltip content={`${success.name} · ${veleroLocalTime(at)}`}>
        <span>{formatRelativeAgeTime(at)}</span>
      </Tooltip>
      {newestFailed && <span className={`text-xs ${toneTextClass('alert')}`}>the newest run since then failed</span>}
    </span>
  )
}

function RecentRunDots({ runs, namespace, onNavigate }: { runs: VeleroRun[]; namespace: string; onNavigate?: NavigateToResource }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-0.5">
      {runs.slice(0, RECENT_DOTS).map((run) => (
        <Tooltip key={run.name} content={`${run.name} · ${getBackupStatus({ status: { phase: run.phase ?? '' } }).text} · ${formatRelativeAgeTime(run.ranAt)}`}>
          <button type="button" onClick={() => onNavigate?.(veleroResource('Backup', namespace, run.name))} aria-label={`Open backup ${run.name}`} className="rounded-full p-1 hover:bg-theme-hover">
            <StatusDot tone={runTone(run.phase)} />
          </button>
        </Tooltip>
      ))}
    </span>
  )
}

/** Counts by outcome: "all completed", or "40 completed · 6 failed or partially failed". */
function outcomeCounts(runs: VeleroRunRow[]): string {
  const completed = runs.filter((r) => r.phase === 'Completed').length
  const failed = runs.filter((r) => FAILED.has(r.phase ?? '')).length
  const other = runs.length - completed - failed
  if (runs.length > 0 && completed === runs.length) return 'all completed'
  return [`${completed} completed`, failed ? `${failed} failed or partially failed` : '', other ? `${other} other` : ''].filter(Boolean).join(' · ')
}

function readInPart(query: { data?: VeleroRunsResponse }): boolean {
  return !!query.data && (INCOMPLETE_COVERAGE_STATES.has(query.data.coverage.state) || !!query.data.truncated)
}

/**
 * A read that returned no inventory at all (no access, not cached, still
 * loading, failed): its empty list says nothing, so no count or lower bound
 * is drawn from it. A partial read still bounds the count from below.
 */
function readNothing(query: { data?: VeleroRunsResponse }): boolean {
  const state = query.data?.coverage.state
  return !!state && state !== 'full' && state !== 'partial'
}

function VeleroScheduleOverview({ resource, onNavigate }: { resource: any; onNavigate?: NavigateToResource }) {
  const namespace: string = resource?.metadata?.namespace ?? ''
  const name: string = resource?.metadata?.name ?? ''
  const { support } = useRadarFeature('veleroWorkspace')
  const activity = useVeleroScheduleActivity(namespace, name, { enabled: support === 'supported' })
  const workspace = useWorkspace()
  const query = useVeleroRuns('backups', namespace, { schedule: name })
  const runs = query.data?.runs ?? []
  const reading: VeleroScheduleReading | undefined = activity.data?.reading
  const template = resource?.spec?.template ?? {}
  const locationName: string = template.storageLocation || reading?.location?.name || ''
  const selection = selectionText(template, 'All namespaces')
  const partial = readInPart(query)
  const kept = reading && reading.runs.read !== 'unknown' ? reading.runs.kept : runs.length
  const dueNow = reading?.state === 'due' && reading.reason !== 'serverDown'
  const cron: string = reading?.cron ?? resource?.spec?.schedule ?? ''
  const cadence = veleroCadenceText(cron)
  // The banner above already says why it needs attention; the rows don't repeat it.
  const inBanner = !!workspace.data?.schedules.find((s) => s.namespace === namespace && s.name === name)?.attention
  return (
    <>
      <RefreshFailedNotice queries={[activity, query]} />
      {activity.error && !reading && <LookupFailureNote errors={[activity.error]} what="this schedule’s activity" />}
      <Card title="At a glance">
        <FactGrid>
          <FactRow label="Runs">
            {cadence ?? <span className="font-mono text-sm">{cron || '—'}</span>}
            {reading && (
              <>
                <span className="text-theme-text-tertiary"> · </span>
                <VeleroZoneValue zone={reading.zone} />
              </>
            )}
            {cadence && <div className="font-mono text-xs text-theme-text-tertiary">{cron}</div>}
          </FactRow>
          {reading && (
            <>
              <FactRow label="Last success">
                <LastSuccess reading={reading} />
              </FactRow>
              <FactRow label="Next run">
                {reading.nextRun ? (
                  <>
                    <Tooltip content={veleroNextRunTitle(reading)}>
                      <span>{dueNow ? 'due now' : veleroNextRunText(reading)}</span>
                    </Tooltip>
                    <span className="ml-2 text-xs text-theme-text-tertiary">{veleroLocalTime(reading.nextRun)}</span>
                  </>
                ) : (
                  <span className="text-theme-text-tertiary">None</span>
                )}
                {!dueNow && reading.state !== 'onSchedule' && !inBanner && <Note>{veleroScheduleReasonText(reading, { created: resource?.metadata?.creationTimestamp })}</Note>}
              </FactRow>
              <FactRow label="Recent runs">
                {reading.runs.read === 'unknown' ? (
                  <span className="text-theme-text-tertiary">{VELERO_RUNS_UNKNOWN}</span>
                ) : (reading.runs.recent ?? []).length === 0 ? (
                  <span className="text-theme-text-secondary">{veleroScheduleNeverRan(reading) ? 'None' : VELERO_NONE_KEPT_SHORT}</span>
                ) : (
                  <span className="inline-flex flex-wrap items-center gap-x-3">
                    <RecentRunDots runs={reading.runs.recent ?? []} namespace={namespace} onNavigate={onNavigate} />
                    <TabLink tab="runs">All {kept} runs</TabLink>
                  </span>
                )}
              </FactRow>
            </>
          )}
          <FactRow label="Covers">
            {selection.text}
            <Note>{declaredNote(selection.narrowed)}</Note>
          </FactRow>
          <FactRow label="Stored in">
            {locationName ? (
              <>
                <LocationValue name={locationName} namespace={namespace} workspace={workspace.data} onNavigate={onNavigate} />
                {!template.storageLocation && <Note>The installation’s default; the schedule names none</Note>}
              </>
            ) : (
              <span className="text-theme-text-secondary">The default storage location, which could not be read</span>
            )}
          </FactRow>
          <FactRow label="Each run kept for">{veleroDeclaredRetention(template.ttl) ?? VELERO_SERVER_DEFAULT_RETENTION}</FactRow>
          <FactRow label="Runs Velero keeps">
            {query.isPending ? (
              'Loading…'
            ) : !query.data ? (
              <span className="text-theme-text-secondary">Not read: {(query.error as Error | null)?.message}</span>
            ) : readNothing(query) ? (
              <span className="text-theme-text-tertiary">Unknown — {coverageLabel(query.data.coverage)}</span>
            ) : kept === 0 && runs.length === 0 && !partial ? (
              'None'
            ) : (
              <>
                {partial ? `At least ${Math.max(kept, runs.length)}` : kept} kept · {outcomeCounts(runs)}
                {partial && ` among the ${runs.length} Radar read`}
                <Note>Velero deletes each run after its retention, so this is not a full history.</Note>
              </>
            )}
          </FactRow>
        </FactGrid>
      </Card>
    </>
  )
}

/** A red dot on a tab whose list holds failed runs. */
function FailedMark({ count }: { count: number }) {
  if (count === 0) return null
  const label = `${count} failed`
  return (
    <Tooltip content={label} position="bottom">
      <span role="img" aria-label={label} className="ml-1 inline-flex shrink-0">
        <StatusDot tone="unhealthy" />
      </span>
    </Tooltip>
  )
}

/** The Runs tab's mark: some of the schedule's runs failed. */
export function VeleroScheduleRunsMark({ namespace, name }: { namespace: string; name: string }) {
  const query = useVeleroRuns('backups', namespace, { schedule: name })
  return <FailedMark count={(query.data?.runs ?? []).filter((r) => FAILED.has(r.phase ?? '')).length} />
}

/** Every run of a schedule Velero still keeps, newest first, with the failed ones a segment away. */
export function VeleroScheduleRuns({ namespace, name, onNavigate }: { namespace: string; name: string; onNavigate?: NavigateToResource }) {
  const query = useVeleroRuns('backups', namespace, { schedule: name })
  const runs = query.data?.runs ?? []
  const { failed, view, setView, shown } = useRunView(runs)
  const wide = useMediaQuery(VELERO_WIDE)
  return (
    <div className="space-y-3 p-4">
      <Segments label="Runs" value={view} onChange={setView} options={runViewOptions(failed.length, runs.length, query)} />
      <RunsReadNotice query={query} noun="backups" />
      <SectionTable
        columns={[
          { header: 'Run', cell: (r: VeleroRunRow) => <VeleroName name={r.name} middle /> },
          { header: 'Outcome', width: '170px', cell: (r: VeleroRunRow) => <OutcomeCell kind="Backup" run={r} counts={!wide} /> },
          { header: 'Started', width: wide ? '110px' : '170px', cell: (r: VeleroRunRow) => <StartedCell run={r} folded={!wide} items expires /> },
          ...(wide
            ? [
                { header: 'Took', width: '80px', cell: (r: VeleroRunRow) => durationText(r.started, r.completed) ?? '—' },
                { header: 'Items', width: '90px', cell: (r: VeleroRunRow) => (r.itemsTotal ? `${r.itemsDone ?? 0} / ${r.itemsTotal}` : '—') },
                { header: 'Errors · warnings', width: '130px', cell: (r: VeleroRunRow) => <RunCounts errors={r.errors} warnings={r.warnings} /> },
                { header: 'Expires', width: '100px', cell: (r: VeleroRunRow) => <Expires iso={r.expiration} /> },
              ]
            : []),
        ]}
        rows={shown}
        rowKey={(r) => r.uid || r.name}
        rowResource={(r) => veleroResource('Backup', r.namespace, r.name)}
        onInspect={onNavigate}
        minWidth={wide ? 900 : 500}
        empty={runsEmpty(query, 'backups', view === 'failed' ? 'No failed runs among those Velero keeps' : 'No runs kept; Velero deletes each run after its retention')}
        footer="This schedule’s backups that Velero still keeps (by the velero.io/schedule-name label)."
      />
    </div>
  )
}

// ---- Storage location ---------------------------------------------------

const PROVIDER_SCHEME: Record<string, string> = { aws: 's3://', gcp: 'gs://' }

/** "s3://bucket/prefix" for the providers with a URL scheme; otherwise "bucket/prefix". */
function bucketText(provider: string | undefined, bucket: string | undefined, prefix: string | undefined): string {
  if (!bucket) return ''
  const scheme = PROVIDER_SCHEME[(provider ?? '').replace(/^velero\.io\//, '')] ?? ''
  return `${scheme}${bucket}${prefix ? `/${prefix.replace(/^\/+/, '')}` : ''}`
}

/** The failed backups among those naming a location, when the list Radar holds is all of them. */
function useLocationBackups(namespace: string, name: string) {
  const stored = useVeleroStoredBackups(namespace, name)
  const failed = (stored.data?.backups ?? []).filter((b) => FAILED.has(b.phase ?? '')).length
  return { stored, failed }
}

/** "48 backups · 30 completed · 16 expired · 2 failed". */
function LocationBackupCounts({ loc, namespace, name, unavailable }: { loc?: VeleroLocationReading; namespace: string; name: string; unavailable: boolean }) {
  const { stored, failed } = useLocationBackups(namespace, name)
  const d = stored.data
  const counts = d
    ? { total: d.stored ?? d.backups.length, usable: d.restorable, expired: d.expired ?? 0, exact: !d.truncated }
    : loc?.backups
      ? { total: loc.backups.targeting, usable: loc.backups.completedNotExpired, expired: loc.backups.expired, exact: false }
      : null
  if (!counts) return <span className="text-theme-text-secondary">Backups not read</span>
  const rest = Math.max(0, counts.total - counts.usable - counts.expired)
  const tail = counts.exact
    ? [failed ? <span key="f" className={toneTextClass('unhealthy')}>{failed} failed</span> : null, rest - failed > 0 ? `${rest - failed} not finished` : null]
    : [rest ? `${rest} failed or not finished` : null]
  const parts: ReactNode[] = [plural(counts.total, 'backup'), `${counts.usable} completed`, `${counts.expired} expired`, ...tail].filter(Boolean)
  return (
    <>
      <span className="inline-flex flex-wrap items-baseline gap-x-3">
        <span>
          {parts.map((p, i) => (
            <span key={i}>
              {i > 0 && ' · '}
              {p}
            </span>
          ))}
        </span>
        {counts.total > 0 && <TabLink tab="backups">Backups</TabLink>}
      </span>
      {unavailable && counts.usable > 0 && (
        <div className={`text-xs ${toneTextClass('unhealthy')}`}>None of them can be restored while the location is unavailable: Velero refuses a restore from it.</div>
      )}
      <Note>Counted from the Backup objects that name this location, not from what is in the bucket. Completed: completed and inside their retention.</Note>
    </>
  )
}

function VeleroLocationOverview({ resource, onNavigate }: { resource: any; onNavigate?: NavigateToResource }) {
  const namespace: string = resource?.metadata?.namespace ?? ''
  const name: string = resource?.metadata?.name ?? ''
  const workspace = useWorkspace()
  const loc = workspace.data?.locations.find((l) => l.namespace === namespace && l.name === name)
  const installation = workspace.data?.installations.find((i) => i.namespace === namespace)
  const phase: string | undefined = loc?.phase ?? resource?.status?.phase
  const tone: StatusTone = phase === 'Available' ? 'healthy' : phase === 'Unavailable' ? 'unhealthy' : 'unknown'
  const message: string | undefined = loc?.message ?? resource?.status?.message
  const plain = phase === 'Unavailable' ? veleroPlainReason(message) : undefined
  const lastSynced: string | undefined = resource?.status?.lastSyncedTime
  const spec = resource?.spec ?? {}
  const objectStorage = spec.objectStorage ?? {}
  const def = loc ? veleroDefaultText(loc) : null
  const otherDefault = installation?.defaultLocation && installation.defaultLocation !== name ? installation.defaultLocation : undefined
  return (
    <>
      <RefreshFailedNotice queries={[workspace]} />
      <Card title="At a glance">
        <FactGrid>
          <FactRow label="Availability">
            <span className="inline-flex items-center gap-2">
              <StatusDot tone={tone} />
              <span className={toneTextClass(tone)}>{phase ?? 'Not validated yet'}</span>
            </span>
            {plain && <div className={`text-sm ${toneTextClass('unhealthy')}`}>{plain}</div>}
            {message && (plain ? <RawMessage message={message} /> : <Note>{message}</Note>)}
          </FactRow>
          <FactRow label="Validation">
            {loc ? (
              <>
                {/* Velero stamps the time of each attempt, whether or not the location passed. */}
                {!loc.lastValidation ? (
                  'Never validated'
                ) : phase === 'Unavailable' ? (
                  <>
                    Last checked {veleroMomentText(loc.lastValidation)} — <span className={toneTextClass('unhealthy')}>failed</span>
                  </>
                ) : (
                  `Last validated ${veleroMomentText(loc.lastValidation)}`
                )}
                <Note>
                  {!loc.frequency ? 'Validation interval not known' : loc.frequency.disabled ? 'Recurring validation is off' : `Every ${veleroFrequencyText(loc.frequency.seconds)}`}
                  {lastSynced && (
                    <>
                      {' · '}
                      <Tooltip content={`${veleroLocalTime(lastSynced)} · when Velero last tried to sync the backups in the bucket into the cluster`}>
                        <span>last sync attempt {formatRelativeAgeTime(lastSynced)}</span>
                      </Tooltip>
                    </>
                  )}
                  {(loc.validationOverdue || loc.neverValidated) && ` · ${veleroLocationReasonText(loc)}`}
                </Note>
              </>
            ) : (
              <span className="text-theme-text-secondary">Not read</span>
            )}
          </FactRow>
          <FactRow label="Default">
            {!def ? (
              <span className="text-theme-text-secondary">Not read</span>
            ) : def.text === 'Yes' ? (
              <Tooltip content={def.detail}>
                <span>Yes · {loc?.defaultSource === 'spec' ? 'set on this location' : 'named by the Velero server'}</span>
              </Tooltip>
            ) : def.text === 'No' && otherDefault ? (
              <span>
                No —{' '}
                <Link onClick={() => onNavigate?.(veleroResource('BackupStorageLocation', namespace, otherDefault))}>{otherDefault}</Link>{' '}
                <Tooltip content={def.detail}>
                  <span>is the default location</span>
                </Tooltip>
              </span>
            ) : (
              <>
                {def.text}
                <Note>{def.detail}</Note>
              </>
            )}
          </FactRow>
          <FactRow label="Access">{spec.accessMode === 'ReadOnly' ? 'Read-only: Velero writes no backups here and deletes none' : 'Read-write'}</FactRow>
          <FactRow label="Bucket">{[spec.provider, bucketText(spec.provider, objectStorage.bucket, objectStorage.prefix)].filter(Boolean).join(' · ') || '—'}</FactRow>
          <FactRow label="Backups targeting it">
            <LocationBackupCounts loc={loc} namespace={namespace} name={name} unavailable={phase === 'Unavailable'} />
          </FactRow>
          <FactRow label="Written to by">
            {loc?.schedules == null ? (
              <span className="text-theme-text-secondary">Schedules not read</span>
            ) : loc.schedules.length === 0 ? (
              'No schedule'
            ) : (
              <span className="flex flex-wrap gap-x-3">
                {loc.schedules.map((s) => (
                  <Link key={s.name} onClick={() => onNavigate?.(veleroResource('Schedule', namespace, s.name))}>
                    {s.name}
                    {s.viaDefault ? ' (as the default)' : ''}
                  </Link>
                ))}
              </span>
            )}
          </FactRow>
        </FactGrid>
      </Card>
    </>
  )
}

/** The Backups tab's mark: some backups naming the location failed. */
export function VeleroLocationBackupsMark({ namespace, name }: { namespace: string; name: string }) {
  return <FailedMark count={useLocationBackups(namespace, name).failed} />
}

/** The backups that name a location, newest first, with the failed ones a segment away. */
export function VeleroLocationBackups({ namespace, name, onNavigate }: { namespace: string; name: string; onNavigate?: NavigateToResource }) {
  const stored = useVeleroStoredBackups(namespace, name)
  const rows = stored.data?.backups ?? []
  const failed = rows.filter((b) => FAILED.has(b.phase ?? ''))
  const [view, setView] = useState<'all' | 'failed'>('all')
  const shown = view === 'failed' ? failed : rows
  const partial = !!stored.data?.truncated
  const count = (n: number) => (partial && n > 0 ? `≥${n}` : String(n))
  return (
    <div className="space-y-3 p-4">
      <Segments
        label="Backups"
        value={view}
        onChange={setView}
        options={[
          { id: 'all' as const, label: 'All', count: String(stored.data?.stored ?? rows.length) },
          { id: 'failed' as const, label: 'Failed', count: failed.length > 0 ? <span className={toneTextClass('unhealthy')}>{count(failed.length)}</span> : count(0) },
        ]}
      />
      {stored.error && stored.data && <Notice>The last refresh failed ({(stored.error as Error).message}); what is shown is from before it.</Notice>}
      <SectionTable
        title="Backups targeting this location"
        subtitle={
          stored.data
            ? `${stored.data.stored ?? rows.length} · ${stored.data.restorable} completed · ${stored.data.expired ?? 0} expired · ${count(failed.length)} failed`
            : undefined
        }
        columns={[
          { header: 'Backup', cell: (b) => <VeleroName name={b.name} middle /> },
          { header: 'Outcome', width: '160px', cell: (b) => <RunOutcome kind="Backup" phase={b.phase} /> },
          { header: 'Finished', width: '140px', cell: (b) => <When iso={b.completed} fallback="—" /> },
          { header: 'Expires', width: '130px', cell: (b) => <Expires iso={b.expiration} /> },
        ]}
        rows={shown}
        rowKey={(b) => b.name}
        rowResource={(b) => veleroResource('Backup', b.namespace, b.name)}
        onInspect={onNavigate}
        minWidth={560}
        empty={
          stored.isPending
            ? 'Loading backups…'
            : stored.error && !stored.data
              ? `Backups could not be read: ${(stored.error as Error).message}`
              : view === 'failed'
                ? 'No failed backups name this location.'
                : 'No backups name this location.'
        }
        footer={stored.data?.truncated ? `The newest ${rows.length} of ${stored.data.stored}.` : 'Counted from the Backup objects that name this location, not from what is in the bucket.'}
      />
    </div>
  )
}

/**
 * The composed Overview of a Velero object, led by Velero's issues for it: on
 * its page, and in the drawer beside Spec & status, as CloudNativePG's is.
 */
export function renderVeleroSummary({ apiKind, resource, context, onNavigate, leadContent }: { apiKind: string; resource: any; context: 'drawer' | 'expanded'; onNavigate?: NavigateToResource; leadContent?: ReactNode }) {
  if (!resource) return null
  const body = (() => {
    switch (apiKind) {
      case 'backups':
        return <VeleroBackupOverview resource={resource} onNavigate={onNavigate} />
      case 'restores':
        return <VeleroRestoreOverview resource={resource} onNavigate={onNavigate} />
      case 'schedules':
        return <VeleroScheduleOverview resource={resource} onNavigate={onNavigate} />
      case 'backupstoragelocations':
        return <VeleroLocationOverview resource={resource} onNavigate={onNavigate} />
      default:
        return null
    }
  })()
  if (!body) return null
  const namespace: string = resource?.metadata?.namespace ?? ''
  const name: string = resource?.metadata?.name ?? ''
  return (
    <SummaryPlaceContext.Provider value={{ drawer: context === 'drawer', plural: apiKind, namespace, name }}>
      <div className="space-y-4 p-4">
        <PageLead kind={VELERO_DETAIL_KINDS[apiKind].kind} namespace={namespace} name={name} leadContent={leadContent} onNavigate={onNavigate} />
        {body}
      </div>
    </SummaryPlaceContext.Provider>
  )
}
