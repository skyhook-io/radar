import { useMemo, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import {
  FactGrid,
  FactRow,
  SectionHeading,
  VeleroScheduleActivity,
  getBackupErrors,
  getBackupStatus,
  getBackupWarnings,
  getRestoreBackupName,
  getRestoreErrors,
  getRestoreNamespaceMapping,
  getRestoreStatus,
  getRestoreWarnings,
  veleroDefaultText,
  veleroFrequencyText,
  veleroLocalTime,
  veleroLocationReasonText,
  veleroMomentText,
  veleroRetentionText,
  veleroScheduleStateLabel,
  type NavigateToResource,
  type VeleroLocationReading,
  type VeleroScheduleReading,
  type VeleroWorkspaceResponse,
} from '@skyhook-io/k8s-ui'
import { LookupFailureNote } from '@skyhook-io/k8s-ui/components/resources/renderers/LookupFailureNote'
import { useRadarFeature } from '../../api/client'
import { useVeleroRuns, useVeleroScheduleActivity, useVeleroWorkspace, type VeleroRunRow } from '../../api/velero'
import { useVeleroStoredBackups } from '../../api/policy'
import { Notice, RefreshFailedNotice, Segments, SectionTable } from '../workspace/layout'
import { INCOMPLETE_COVERAGE_STATES } from '../workspace/coverage'
import { RunMessages, veleroMessagesInIssue } from './VeleroRunMessagesHost'
import { FAILED, KeptUntil, Outcome, RunOutcome, RunsReadNotice, When, boundedCount, durationText, expired, leftText, runsEmpty } from './runs'
import { veleroResource } from './shared'

// The pages' own sections, beside Radar's detail view: a composed Overview for
// each Velero kind, and the tabs that list what the object relates to. Every
// value says where it comes from or reads unknown. Declared settings are named
// as declared, and nothing here says a backup would restore: Velero never
// tests a restore.

const CARD = 'rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm'

function Card({ title, children }: { title?: string; children: ReactNode }) {
  return (
    <section className={CARD}>
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
    <button type="button" onClick={onClick} className="text-left text-accent hover:underline">
      {children}
    </button>
  )
}

function list(values: unknown): string[] {
  return Array.isArray(values) ? values.filter((v): v is string => typeof v === 'string') : []
}

/** A run's namespace selection as declared, and whether other filters narrow it. */
function selectionText(spec: any, emptyMeans: string): { text: string; narrowed: boolean } {
  const included = list(spec?.includedNamespaces)
  const excluded = list(spec?.excludedNamespaces)
  const base = included.length === 0 || included.includes('*') ? emptyMeans : included.join(', ')
  const text = excluded.length ? `${base}, except ${excluded.join(', ')}` : base
  const narrowed =
    list(spec?.includedResources).length > 0 ||
    list(spec?.excludedResources).length > 0 ||
    !!spec?.labelSelector ||
    list(spec?.orLabelSelectors).length > 0
  return { text, narrowed }
}

const TONE_TEXT = { ok: 'text-success-text', warn: 'text-warning-text', bad: 'text-error-text', unknown: 'text-theme-text-secondary' } as const
type Tone = keyof typeof TONE_TEXT

/**
 * A storage location's state as a page reads it. Missing is said only when the
 * namespace's locations were read in full; otherwise it is unread.
 */
export function locationState(name: string, namespace: string, workspace: VeleroWorkspaceResponse | undefined): { loc?: VeleroLocationReading; text: string; tone: Tone } {
  const loc = workspace?.locations.find((l) => l.namespace === namespace && l.name === name)
  if (loc) {
    if (loc.phase === 'Available') return { loc, text: 'available', tone: 'ok' }
    if (!loc.phase) return { loc, text: 'not validated yet', tone: 'unknown' }
    return { loc, text: loc.phase.toLowerCase(), tone: 'bad' }
  }
  const cov = workspace?.coverage.backupStorageLocations
  if (workspace && cov?.state === 'full') return { text: 'not found: Velero has no such location', tone: 'bad' }
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

// ---- Backup -------------------------------------------------------------

export function VeleroBackupOverview({ resource, onNavigate, issuesShown }: { resource: any; onNavigate?: NavigateToResource; issuesShown: boolean }) {
  const namespace: string = resource?.metadata?.namespace ?? ''
  const workspace = useWorkspace()
  const status = resource?.status ?? {}
  const spec = resource?.spec ?? {}
  const locationName: string = spec.storageLocation ?? ''
  const scheduleName: string | undefined = resource?.metadata?.labels?.['velero.io/schedule-name']
  const sched = scheduleName ? workspace.data?.schedules.find((s) => s.namespace === namespace && s.name === scheduleName) : undefined
  const selection = selectionText(spec, 'All namespaces')
  const items = status.progress?.totalItems ? `${status.progress.itemsBackedUp ?? 0} of ${status.progress.totalItems} items backed up` : undefined
  const volumes = [
    status.volumeSnapshotsAttempted ? `${status.volumeSnapshotsCompleted ?? 0} of ${status.volumeSnapshotsAttempted} volume snapshots` : '',
    status.csiVolumeSnapshotsAttempted ? `${status.csiVolumeSnapshotsCompleted ?? 0} of ${status.csiVolumeSnapshotsAttempted} CSI snapshots` : '',
    status.backupItemOperationsAttempted ? `${status.backupItemOperationsCompleted ?? 0} of ${status.backupItemOperationsAttempted} plugin operations${status.backupItemOperationsFailed ? `, ${status.backupItemOperationsFailed} failed` : ''}` : '',
  ].filter(Boolean)
  const ran = durationText(status.startTimestamp, status.completionTimestamp)
  // A failed run's messages already sit in its issue above.
  const messagesHere = getBackupErrors(resource) + getBackupWarnings(resource) > 0 && !veleroMessagesInIssue(resource, issuesShown)
  return (
    <>
      <RefreshFailedNotice queries={[workspace]} />
      <Card title="At a glance">
        <FactGrid>
          <FactRow label="Outcome">
            <span className="inline-flex flex-wrap items-center gap-2">
              <Outcome badge={getBackupStatus(resource)} />
              {ran && <Note>ran {ran}, {veleroMomentText(status.completionTimestamp ?? status.startTimestamp)}</Note>}
            </span>
            {items && <Note>{items}</Note>}
          </FactRow>
          <FactRow label="Selected">
            {selection.text}
            <Note>As declared on the backup{selection.narrowed ? ', narrowed by resource or label filters' : ''}; Velero counts what it actually backed up above.</Note>
          </FactRow>
          {volumes.length > 0 && <FactRow label="Volumes">{volumes.join(' · ')}</FactRow>}
          <FactRow label="Stored in">
            {locationName ? <LocationValue name={locationName} namespace={namespace} workspace={workspace.data} onNavigate={onNavigate} /> : <span className="text-theme-text-secondary">Not assigned yet</span>}
          </FactRow>
          <FactRow label="Kept until">
            {status.expiration ? (
              <>
                {veleroLocalTime(status.expiration)}
                <Note>
                  {expired(status.expiration) ? 'Past its retention: Velero deletes it at its next garbage collection' : leftText(status.expiration)}
                  {spec.ttl ? ` · retention ${veleroRetentionText(spec.ttl)}` : ''}
                </Note>
              </>
            ) : (
              <span className="text-theme-text-secondary">Not reported yet</span>
            )}
          </FactRow>
          <FactRow label="Made by">
            {scheduleName ? (
              <>
                <Link onClick={() => onNavigate?.(veleroResource('Schedule', namespace, scheduleName))}>Schedule {scheduleName}</Link>
                {sched && <span className="ml-2 text-xs text-theme-text-secondary">{veleroScheduleStateLabel(sched.state)}</span>}
              </>
            ) : (
              'Created directly, not by a schedule'
            )}
          </FactRow>
          <FactRow label="Restore">
            <span className="text-theme-text-secondary">Not verified</span>
            <Note>Velero never tests a restore; a completed backup is what it reports. The Restores tab lists restores made from it.</Note>
          </FactRow>
        </FactGrid>
      </Card>
      {messagesHere && (
        <Card title="What Velero reported">
          <RunMessages kind="backups" resource={resource} />
        </Card>
      )}
    </>
  )
}

/** Restores made from one backup (spec.backupName). */
export function VeleroBackupRestores({ namespace, name, onNavigate }: { namespace: string; name: string; onNavigate?: NavigateToResource }) {
  const query = useVeleroRuns('restores', namespace)
  const rows = useMemo(() => (query.data?.runs ?? []).filter((r) => r.backupName === name), [query.data, name])
  return (
    <div className="space-y-3 p-4">
      <RunsReadNotice query={query} noun="restores" />
      <SectionTable
        title="Restores from this backup"
        subtitle="every Restore Velero keeps whose backup is this one"
        columns={[
          { header: 'Restore', cell: (r: VeleroRunRow) => <span className="font-medium">{r.name}</span> },
          { header: 'Outcome', width: '160px', cell: (r: VeleroRunRow) => <RunOutcome kind="Restore" phase={r.phase} /> },
          { header: 'Started', width: '120px', cell: (r: VeleroRunRow) => <When iso={r.started} fallback="not started" /> },
          { header: 'Took', width: '90px', cell: (r: VeleroRunRow) => durationText(r.started, r.completed) ?? '—' },
          { header: 'Errors · warnings', width: '140px', cell: (r: VeleroRunRow) => `${r.errors} · ${r.warnings}` },
        ]}
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

export function VeleroRestoreOverview({ resource, onNavigate, issuesShown }: { resource: any; onNavigate?: NavigateToResource; issuesShown: boolean }) {
  const namespace: string = resource?.metadata?.namespace ?? ''
  const status = resource?.status ?? {}
  const spec = resource?.spec ?? {}
  const backupName = getRestoreBackupName(resource)
  const mapping = getRestoreNamespaceMapping(resource)
  const selection = selectionText(spec, 'Every namespace in the backup')
  const progress = status.progress?.totalItems ? `${status.progress.itemsRestored ?? 0} of ${status.progress.totalItems} items restored` : undefined
  const took = durationText(status.startTimestamp, status.completionTimestamp)
  const messagesHere = getRestoreErrors(resource) + getRestoreWarnings(resource) > 0 && !veleroMessagesInIssue(resource, issuesShown)
  return (
    <>
      <Card title="At a glance">
        <FactGrid>
          <FactRow label="Outcome">
            <span className="inline-flex flex-wrap items-center gap-2">
              <Outcome badge={getRestoreStatus(resource)} />
              {took && <Note>took {took}, {veleroMomentText(status.completionTimestamp ?? status.startTimestamp)}</Note>}
            </span>
            {progress && <Note>{progress}</Note>}
          </FactRow>
          <FactRow label="From backup">
            {backupName ? <Link onClick={() => onNavigate?.(veleroResource('Backup', namespace, backupName))}>{backupName}</Link> : spec.scheduleName ? `The newest backup of schedule ${spec.scheduleName}` : '—'}
          </FactRow>
          <FactRow label="Selected">
            {selection.text}
            {Object.keys(mapping).length > 0 && <Note>Mapped: {Object.entries(mapping).map(([from, to]) => `${from} → ${to}`).join(', ')}; other namespaces keep their names</Note>}
            <Note>As declared on the restore{selection.narrowed ? ', narrowed by resource or label filters' : ''}.</Note>
          </FactRow>
          <FactRow label="Existing objects">
            {spec.existingResourcePolicy === 'update' ? 'Update requested' : 'Left as they are (Velero’s default)'}
            {spec.existingResourcePolicy === 'update' && <Note>Velero updates an existing object where it can, and reports a warning where it can’t.</Note>}
          </FactRow>
        </FactGrid>
      </Card>
      {messagesHere && (
        <Card title="What Velero reported">
          <RunMessages kind="restores" resource={resource} />
        </Card>
      )}
    </>
  )
}

// ---- Schedule -----------------------------------------------------------

function useScheduleRuns(namespace: string, name: string) {
  const query = useVeleroRuns('backups', namespace)
  const runs = useMemo(() => (query.data?.runs ?? []).filter((r) => r.schedule === name), [query.data, name])
  return { query, runs }
}

export function VeleroScheduleOverview({ resource, onNavigate }: { resource: any; onNavigate?: NavigateToResource }) {
  const namespace: string = resource?.metadata?.namespace ?? ''
  const name: string = resource?.metadata?.name ?? ''
  const { support } = useRadarFeature('veleroWorkspace')
  const activity = useVeleroScheduleActivity(namespace, name, { enabled: support === 'supported' })
  const workspace = useWorkspace()
  const { runs, query } = useScheduleRuns(namespace, name)
  const reading: VeleroScheduleReading | undefined = activity.data?.reading
  const [params, setParams] = useSearchParams()
  // "All N runs" opens this page's Runs tab, on all of them.
  const showRuns = () => {
    const next = new URLSearchParams(params)
    next.set('tab', 'runs')
    next.set('runs', 'all')
    setParams(next)
  }
  const template = resource?.spec?.template ?? {}
  const locationName: string = template.storageLocation || reading?.location?.name || ''
  const selection = selectionText(template, 'All namespaces')
  const counts = runs.reduce((acc, r) => {
    if (r.phase === 'Completed') acc.completed++
    else if (FAILED.has(r.phase ?? '')) acc.failed++
    else acc.other++
    return acc
  }, { completed: 0, failed: 0, other: 0 })
  const partial = !!query.data && INCOMPLETE_COVERAGE_STATES.has(query.data.coverage.state)
  return (
    <>
      <RefreshFailedNotice queries={[activity]} />
      {reading ? (
        <Card>
          <VeleroScheduleActivity reading={reading} created={resource?.metadata?.creationTimestamp} onNavigate={(ref) => onNavigate?.(veleroResource(ref.kind, ref.namespace, ref.name))} onShowAllRuns={showRuns} />
        </Card>
      ) : activity.error ? (
        <LookupFailureNote errors={[activity.error]} what="this schedule’s activity" />
      ) : null}
      <Card title="At a glance">
        <FactGrid>
          <FactRow label="Selects">
            {selection.text}
            <Note>As declared in its template{selection.narrowed ? ', narrowed by resource or label filters' : ''}.</Note>
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
          <FactRow label="Each run kept for">{template.ttl ? veleroRetentionText(template.ttl) : 'The Velero server’s default (30 days unless it was changed)'}</FactRow>
          <FactRow label="Runs Velero keeps">
            {query.isPending ? 'Loading…' : !query.data ? <span className="text-theme-text-secondary">Not read: {(query.error as Error | null)?.message}</span> : runs.length === 0 && !partial ? 'None' : (
              <>
                {partial ? `At least ${runs.length}` : runs.length}: {counts.completed} completed{counts.failed ? `, ${counts.failed} failed or partly failed` : ''}{counts.other ? `, ${counts.other} other` : ''}
                <Note>Velero deletes each run after its retention, so this is not a full history.</Note>
              </>
            )}
          </FactRow>
        </FactGrid>
      </Card>
    </>
  )
}

/** Every run of a schedule Velero still keeps; failed first when any failed, unless asked for all. */
export function VeleroScheduleRuns({ namespace, name, onNavigate }: { namespace: string; name: string; onNavigate?: NavigateToResource }) {
  const { runs, query } = useScheduleRuns(namespace, name)
  const [params] = useSearchParams()
  const failed = runs.filter((r) => FAILED.has(r.phase ?? ''))
  const [view, setView] = useState<'failed' | 'all' | null>(params.get('runs') === 'all' ? 'all' : null)
  const current = view ?? (failed.length > 0 ? 'failed' : 'all')
  const shown = current === 'failed' ? failed : runs
  return (
    <div className="space-y-3 p-4">
      <Segments
        label="Runs"
        value={current}
        onChange={setView}
        options={[{ id: 'failed', label: 'Failed', count: boundedCount(failed.length, query) }, { id: 'all', label: 'All', count: boundedCount(runs.length, query) }]}
      />
      <RunsReadNotice query={query} noun="backups" />
      <SectionTable
        columns={[
          { header: 'Run', cell: (r: VeleroRunRow) => <span className="font-mono text-xs">{r.name}</span> },
          { header: 'Outcome', width: '160px', cell: (r: VeleroRunRow) => <RunOutcome kind="Backup" phase={r.phase} /> },
          { header: 'Started', width: '120px', cell: (r: VeleroRunRow) => <When iso={r.started} fallback="not started" /> },
          { header: 'Took', width: '90px', cell: (r: VeleroRunRow) => durationText(r.started, r.completed) ?? '—' },
          { header: 'Items', width: '100px', cell: (r: VeleroRunRow) => (r.itemsTotal ? `${r.itemsDone ?? 0} / ${r.itemsTotal}` : '—') },
          { header: 'Errors · warnings', width: '140px', cell: (r: VeleroRunRow) => `${r.errors} · ${r.warnings}` },
          { header: 'Kept until', width: '120px', cell: (r: VeleroRunRow) => <KeptUntil iso={r.expiration} /> },
        ]}
        rows={shown}
        rowKey={(r) => r.uid || r.name}
        rowResource={(r) => veleroResource('Backup', r.namespace, r.name)}
        onInspect={onNavigate}
        minWidth={900}
        empty={runsEmpty(query, 'backups', current === 'failed' ? 'No failed runs among those Velero keeps' : 'No runs kept; Velero deletes each run after its retention')}
        footer="This schedule’s backups that Velero still keeps (by the velero.io/schedule-name label)."
      />
    </div>
  )
}

// ---- Storage location ---------------------------------------------------

export function VeleroLocationOverview({ resource, onNavigate }: { resource: any; onNavigate?: NavigateToResource }) {
  const namespace: string = resource?.metadata?.namespace ?? ''
  const name: string = resource?.metadata?.name ?? ''
  const workspace = useWorkspace()
  const loc = workspace.data?.locations.find((l) => l.namespace === namespace && l.name === name)
  const phase: string | undefined = loc?.phase ?? resource?.status?.phase
  const tone: Tone = phase === 'Available' ? 'ok' : phase ? 'bad' : 'unknown'
  const spec = resource?.spec ?? {}
  const objectStorage = spec.objectStorage ?? {}
  const def = loc ? veleroDefaultText(loc) : null
  return (
    <>
      <RefreshFailedNotice queries={[workspace]} />
      <Card title="At a glance">
        <FactGrid>
          <FactRow label="Availability">
            <span className={TONE_TEXT[tone]}>{phase ?? 'Not validated yet'}</span>
            {(loc?.message ?? resource?.status?.message) && <Note>{loc?.message ?? resource?.status?.message}</Note>}
          </FactRow>
          <FactRow label="Validation">
            {loc ? (
              <>
                {loc.lastValidation ? `Last validated ${veleroMomentText(loc.lastValidation)}` : 'Never validated'}
                <Note>
                  {!loc.frequency ? 'Validation interval not known' : loc.frequency.disabled ? 'Recurring validation is off' : `Every ${veleroFrequencyText(loc.frequency.seconds)}`}
                  {(loc.validationOverdue || loc.neverValidated) && ` · ${veleroLocationReasonText(loc)}`}
                </Note>
              </>
            ) : (
              <span className="text-theme-text-secondary">Not read</span>
            )}
          </FactRow>
          <FactRow label="Default">{def ? <>{def.text}{def.detail && <Note>{def.detail}</Note>}</> : <span className="text-theme-text-secondary">Not read</span>}</FactRow>
          <FactRow label="Access">{spec.accessMode === 'ReadOnly' ? 'Read-only: Velero writes no backups here and deletes none' : 'Read-write'}</FactRow>
          <FactRow label="Where">{[spec.provider, objectStorage.bucket, objectStorage.prefix].filter(Boolean).join(' · ') || '—'}</FactRow>
          <FactRow label="Backups targeting it">
            {loc?.backups ? (
              <>
                {loc.backups.targeting}: {loc.backups.completedNotExpired} completed and inside their retention, {loc.backups.expired} past it, the rest failed or not finished
                <Note>Counted from the Backup objects that name this location; Velero never tests a restore.</Note>
              </>
            ) : (
              <span className="text-theme-text-secondary">Backups not read</span>
            )}
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

/** The backups that name a location, newest first. */
export function VeleroLocationBackups({ namespace, name, onNavigate }: { namespace: string; name: string; onNavigate?: NavigateToResource }) {
  const stored = useVeleroStoredBackups(namespace, name)
  const rows = stored.data?.backups ?? []
  return (
    <div className="space-y-3 p-4">
      {stored.error && stored.data && <Notice>The last refresh failed ({(stored.error as Error).message}); what is shown is from before it.</Notice>}
      <SectionTable
        title="Backups targeting this location"
        subtitle={stored.data ? `${stored.data.stored} · ${stored.data.restorable} completed and inside retention · ${stored.data.expired} past it` : undefined}
        columns={[
          { header: 'Backup', cell: (b) => <span className="font-mono text-xs">{b.name}</span> },
          { header: 'Outcome', width: '160px', cell: (b) => <RunOutcome kind="Backup" phase={b.phase} /> },
          { header: 'Completed', width: '140px', cell: (b) => <When iso={b.completed} fallback="—" /> },
          { header: 'Kept until', width: '130px', cell: (b) => <KeptUntil iso={b.expiration} /> },
        ]}
        rows={rows}
        rowKey={(b) => b.name}
        rowResource={(b) => veleroResource('Backup', b.namespace, b.name)}
        onInspect={onNavigate}
        empty={stored.isPending ? 'Loading backups…' : stored.error ? `Backups could not be read: ${(stored.error as Error).message}` : 'No backups name this location.'}
        footer={stored.data?.truncated ? `The newest ${rows.length} of ${stored.data.stored}.` : 'Counted from the Backup objects that name this location, not from what is in the bucket.'}
      />
    </div>
  )
}

/**
 * The composed Overview of a Velero object's page, led by its operational
 * issues; null in the drawer, which keeps the renderer.
 */
export function renderVeleroSummary({ apiKind, resource, context, onNavigate, leadContent }: { apiKind: string; resource: any; context: 'drawer' | 'expanded'; onNavigate?: NavigateToResource; leadContent?: ReactNode }) {
  if (context !== 'expanded' || !resource) return null
  const issuesShown = !!leadContent
  const body = (() => {
    switch (apiKind) {
      case 'backups':
        return <VeleroBackupOverview resource={resource} onNavigate={onNavigate} issuesShown={issuesShown} />
      case 'restores':
        return <VeleroRestoreOverview resource={resource} onNavigate={onNavigate} issuesShown={issuesShown} />
      case 'schedules':
        return <VeleroScheduleOverview resource={resource} onNavigate={onNavigate} />
      case 'backupstoragelocations':
        return <VeleroLocationOverview resource={resource} onNavigate={onNavigate} />
      default:
        return null
    }
  })()
  if (!body) return null
  return (
    <div className="space-y-4 p-4">
      {leadContent}
      {body}
    </div>
  )
}
