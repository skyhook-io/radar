import { useMemo, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import {
  FactGrid,
  FactRow,
  SectionHeading,
  Tooltip,
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
} from '@skyhook-io/k8s-ui'
import { useRadarFeature, useResources } from '../../api/client'
import { useVeleroScheduleActivity, useVeleroWorkspace } from '../../api/velero'
import { useVeleroStoredBackups } from '../../api/policy'
import { RefreshFailedNotice, Segments, SectionTable } from '../workspace/layout'
import { RunMessages } from './VeleroRunMessagesHost'
import { VELERO_GROUP } from './routes'
import { veleroResource } from './shared'

// The pages' own sections, beside Radar's detail view: a composed Overview for
// each Velero kind, and the tabs that list what the object relates to. Every
// value says where it comes from or reads unknown; nothing here claims a
// backup is restorable, since Velero never tests a restore.

const CARD = 'mb-4 last:mb-0 rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm'

function Card({ title, hint, children }: { title: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <section className={CARD}>
      <SectionHeading hint={hint}>{title}</SectionHeading>
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

function namespacesText(included: string[], excluded: string[]): string {
  const all = included.length === 0 || included.includes('*')
  const base = all ? 'All namespaces' : included.join(', ')
  return excluded.length ? `${base}, except ${excluded.join(', ')}` : base
}

export function expired(expiration: string | undefined): boolean {
  return !!expiration && new Date(expiration).getTime() < Date.now()
}

/** How long until a time ahead, e.g. "21h left". */
export function leftText(iso: string): string {
  const s = Math.max(0, Math.round((new Date(iso).getTime() - Date.now()) / 1000))
  if (s < 3600) return `${Math.max(1, Math.round(s / 60))}m left`
  if (s < 2 * 86400) return `${Math.round(s / 3600)}h left`
  return `${Math.round(s / 86400)} days left`
}

export function durationText(start?: string, end?: string): string | undefined {
  if (!start || !end) return undefined
  const s = Math.max(0, Math.round((new Date(end).getTime() - new Date(start).getTime()) / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${s % 60}s`
  return `${Math.floor(m / 60)}h ${m % 60}m`
}

export function Outcome({ badge }: { badge: { text: string; color: string } }) {
  return <span className={`badge whitespace-nowrap ${badge.color}`}>{badge.text}</span>
}

function locationAvailability(loc: VeleroLocationReading | undefined): { text: string; tone: 'ok' | 'bad' | 'unknown' } {
  if (!loc) return { text: 'not read', tone: 'unknown' }
  if (loc.phase === 'Available') return { text: 'available', tone: 'ok' }
  if (!loc.phase) return { text: 'not validated yet', tone: 'unknown' }
  return { text: loc.phase.toLowerCase(), tone: 'bad' }
}

/** What a run's outcome means for restoring from it, in Velero's terms. */
export function restoreVerdict(backup: any, loc: VeleroLocationReading | undefined): { text: string; detail: string; tone: 'ok' | 'warn' | 'bad' | 'unknown' } {
  const phase: string = backup?.status?.phase ?? ''
  const expiration: string | undefined = backup?.status?.expiration
  if (phase === 'Deleting') return { text: 'No: Velero is deleting it', detail: '', tone: 'bad' }
  if (phase !== 'Completed' && phase !== 'PartiallyFailed') {
    return { text: 'No: it did not complete', detail: phase ? `Velero reports ${phase}.` : 'Velero has not reported an outcome.', tone: 'bad' }
  }
  if (expired(expiration)) return { text: 'No: past its retention', detail: 'Velero deletes expired backups on its next garbage collection.', tone: 'bad' }
  const availability = locationAvailability(loc)
  if (availability.tone === 'bad') return { text: `No: its storage location is ${availability.text}`, detail: 'A restore reads the backup from its location.', tone: 'bad' }
  const partial = phase === 'PartiallyFailed'
  const base = partial ? 'Partly: some items were not backed up' : 'Yes, as far as Velero reports'
  const detail = [
    'Velero reports the run finished and keeps it',
    availability.tone === 'unknown' ? 'its location’s availability could not be read' : 'its location is available',
    'Velero never tests a restore',
  ].join('; ') + '.'
  return { text: base, detail, tone: availability.tone === 'unknown' ? 'unknown' : partial ? 'warn' : 'ok' }
}

const TONE_TEXT: Record<string, string> = { ok: 'text-success-text', warn: 'text-warning-text', bad: 'text-error-text', unknown: 'text-theme-text-secondary' }

function useReadings(namespace: string) {
  const { support } = useRadarFeature('veleroWorkspace')
  const workspace = useVeleroWorkspace({ enabled: support === 'supported' })
  return {
    workspace,
    location: (name: string) => workspace.data?.locations.find((l) => l.namespace === namespace && l.name === name),
    schedule: (name: string) => workspace.data?.schedules.find((s) => s.namespace === namespace && s.name === name),
  }
}

// ---- Backup -------------------------------------------------------------

export function VeleroBackupOverview({ resource, onNavigate }: { resource: any; onNavigate?: NavigateToResource }) {
  const namespace: string = resource?.metadata?.namespace ?? ''
  const { location, schedule, workspace } = useReadings(namespace)
  const status = resource?.status ?? {}
  const spec = resource?.spec ?? {}
  const locationName: string = spec.storageLocation ?? ''
  const loc = locationName ? location(locationName) : undefined
  const scheduleName: string | undefined = resource?.metadata?.labels?.['velero.io/schedule-name']
  const sched = scheduleName ? schedule(scheduleName) : undefined
  const verdict = restoreVerdict(resource, loc)
  const items = status.progress?.totalItems ? `${status.progress.itemsBackedUp ?? 0} of ${status.progress.totalItems}` : undefined
  const snapshots = [
    status.volumeSnapshotsAttempted ? `${status.volumeSnapshotsCompleted ?? 0} of ${status.volumeSnapshotsAttempted} volume snapshots` : '',
    status.csiVolumeSnapshotsAttempted ? `${status.csiVolumeSnapshotsCompleted ?? 0} of ${status.csiVolumeSnapshotsAttempted} CSI snapshots` : '',
    status.backupItemOperationsAttempted ? `${status.backupItemOperationsCompleted ?? 0} of ${status.backupItemOperationsAttempted} plugin operations${status.backupItemOperationsFailed ? `, ${status.backupItemOperationsFailed} failed` : ''}` : '',
  ].filter(Boolean)
  const open = (kind: string, name: string) => onNavigate?.(veleroResource(kind, namespace, name))
  const availability = locationAvailability(loc)
  return (
    <div>
      <RefreshFailedNotice queries={[workspace]} className="mb-3" />
      <Card title="At a glance">
        <FactGrid>
          <FactRow label="Outcome">
            <span className="inline-flex items-center gap-2"><Outcome badge={getBackupStatus(resource)} />{durationText(status.startTimestamp, status.completionTimestamp) && <Note>ran {durationText(status.startTimestamp, status.completionTimestamp)}, {veleroMomentText(status.completionTimestamp ?? status.startTimestamp)}</Note>}</span>
          </FactRow>
          <FactRow label="Restore from it">
            <span className={TONE_TEXT[verdict.tone]}>{verdict.text}</span>
            {verdict.detail && <Note>{verdict.detail}</Note>}
          </FactRow>
          <FactRow label="Covers">
            {namespacesText(list(spec.includedNamespaces), list(spec.excludedNamespaces))}
            {items && <Note>{items} items backed up</Note>}
          </FactRow>
          {snapshots.length > 0 && <FactRow label="Volumes">{snapshots.join(' · ')}</FactRow>}
          <FactRow label="Stored in">
            {locationName ? (
              <>
                <Link onClick={() => open('BackupStorageLocation', locationName)}>{locationName}</Link>
                <span className={`ml-2 text-xs ${TONE_TEXT[availability.tone === 'ok' ? 'ok' : availability.tone === 'bad' ? 'bad' : 'unknown']}`}>{availability.text}</span>
              </>
            ) : (
              <span className="text-theme-text-secondary">Not assigned yet</span>
            )}
          </FactRow>
          <FactRow label="Made by">
            {scheduleName ? (
              <>
                <Link onClick={() => open('Schedule', scheduleName)}>Schedule {scheduleName}</Link>
                {sched && <span className="ml-2 text-xs text-theme-text-secondary">{veleroScheduleStateLabel(sched.state)}</span>}
              </>
            ) : (
              'Created directly, not by a schedule'
            )}
          </FactRow>
          <FactRow label="Kept until">
            {status.expiration ? (
              <>
                {veleroLocalTime(status.expiration)}
                <Note>{expired(status.expiration) ? 'Past its retention' : leftText(status.expiration)}{spec.ttl ? ` · retention ${veleroRetentionText(spec.ttl)}` : ''}</Note>
              </>
            ) : (
              <span className="text-theme-text-secondary">Not reported yet</span>
            )}
          </FactRow>
        </FactGrid>
      </Card>
      {getBackupErrors(resource) + getBackupWarnings(resource) > 0 && (
        <Card title="What Velero reported">
          <RunMessages kind="backups" resource={resource} />
        </Card>
      )}
    </div>
  )
}

/** Restores made from one backup (spec.backupName). */
export function VeleroBackupRestores({ namespace, name, onNavigate }: { namespace: string; name: string; onNavigate?: NavigateToResource }) {
  const restores = useResources<any>('restores', namespace, VELERO_GROUP)
  const rows = useMemo(() => (restores.data ?? []).filter((r) => getRestoreBackupName(r) === name), [restores.data, name])
  return (
    <div className="p-4">
      <SectionTable
        title="Restores from this backup"
        subtitle="every Restore Velero keeps whose backup is this one"
        columns={[
          { header: 'Restore', cell: (r: any) => <span className="font-medium">{r.metadata.name}</span> },
          { header: 'Outcome', width: '170px', cell: (r: any) => <Outcome badge={getRestoreStatus(r)} /> },
          { header: 'Started', width: '140px', cell: (r: any) => veleroMomentText(r.status?.startTimestamp) },
          { header: 'Took', width: '110px', cell: (r: any) => durationText(r.status?.startTimestamp, r.status?.completionTimestamp) ?? '—' },
          { header: 'Errors · warnings', width: '150px', cell: (r: any) => `${getRestoreErrors(r)} · ${getRestoreWarnings(r)}` },
        ]}
        rows={rows}
        rowKey={(r: any) => r.metadata.uid ?? r.metadata.name}
        rowResource={(r: any) => veleroResource('Restore', namespace, r.metadata.name)}
        onInspect={onNavigate}
        empty={restores.isPending ? 'Loading restores…' : restores.error ? `Restores could not be read: ${(restores.error as Error).message}` : 'No restores from this backup.'}
      />
    </div>
  )
}

// ---- Restore ------------------------------------------------------------

export function VeleroRestoreOverview({ resource, onNavigate }: { resource: any; onNavigate?: NavigateToResource }) {
  const namespace: string = resource?.metadata?.namespace ?? ''
  const status = resource?.status ?? {}
  const spec = resource?.spec ?? {}
  const backupName = getRestoreBackupName(resource)
  const mapping = getRestoreNamespaceMapping(resource)
  const progress = status.progress?.totalItems ? `${status.progress.itemsRestored ?? 0} of ${status.progress.totalItems}` : undefined
  return (
    <div>
      <Card title="At a glance">
        <FactGrid>
          <FactRow label="Outcome">
            <span className="inline-flex items-center gap-2"><Outcome badge={getRestoreStatus(resource)} />{durationText(status.startTimestamp, status.completionTimestamp) && <Note>took {durationText(status.startTimestamp, status.completionTimestamp)}, {veleroMomentText(status.completionTimestamp ?? status.startTimestamp)}</Note>}</span>
          </FactRow>
          <FactRow label="From backup">
            {backupName ? <Link onClick={() => onNavigate?.(veleroResource('Backup', namespace, backupName))}>{backupName}</Link> : spec.scheduleName ? `The newest backup of schedule ${spec.scheduleName}` : '—'}
          </FactRow>
          <FactRow label="Restores">
            {namespacesText(list(spec.includedNamespaces), list(spec.excludedNamespaces))}
            {Object.keys(mapping).length > 0 && <Note>Into: {Object.entries(mapping).map(([from, to]) => `${from} → ${to}`).join(', ')}; other namespaces keep their names</Note>}
          </FactRow>
          {progress && <FactRow label="Items">{progress} restored</FactRow>}
          <FactRow label="Existing objects">{spec.existingResourcePolicy === 'update' ? 'Updated to match the backup' : 'Left as they are'}</FactRow>
        </FactGrid>
      </Card>
      {getRestoreErrors(resource) + getRestoreWarnings(resource) > 0 && (
        <Card title="What Velero reported">
          <RunMessages kind="restores" resource={resource} />
        </Card>
      )}
    </div>
  )
}

// ---- Schedule -----------------------------------------------------------

function useScheduleRuns(namespace: string, name: string) {
  const backups = useResources<any>('backups', namespace, VELERO_GROUP)
  const runs = useMemo(
    () =>
      (backups.data ?? [])
        .filter((b: any) => b.metadata?.labels?.['velero.io/schedule-name'] === name)
        .sort((a: any, b: any) => (b.status?.startTimestamp ?? b.metadata?.creationTimestamp ?? '').localeCompare(a.status?.startTimestamp ?? a.metadata?.creationTimestamp ?? '')),
    [backups.data, name],
  )
  return { backups, runs }
}

export const FAILED = new Set(['Failed', 'PartiallyFailed', 'FailedValidation', 'FinalizingPartiallyFailed', 'WaitingForPluginOperationsPartiallyFailed'])

export function VeleroScheduleOverview({ resource, onNavigate }: { resource: any; onNavigate?: NavigateToResource }) {
  const namespace: string = resource?.metadata?.namespace ?? ''
  const name: string = resource?.metadata?.name ?? ''
  const { support } = useRadarFeature('veleroWorkspace')
  const activity = useVeleroScheduleActivity(namespace, name, { enabled: support === 'supported' })
  const { location } = useReadings(namespace)
  const { runs, backups } = useScheduleRuns(namespace, name)
  const reading: VeleroScheduleReading | undefined = activity.data?.reading
  const [params, setParams] = useSearchParams()
  // Every run is on this page's Runs tab.
  const showRuns = () => {
    const next = new URLSearchParams(params)
    next.set('tab', 'runs')
    setParams(next)
  }
  const template = resource?.spec?.template ?? {}
  const locationName: string = template.storageLocation || reading?.location?.name || ''
  const loc = locationName ? location(locationName) : undefined
  const availability = locationAvailability(loc)
  const counts = runs.reduce((acc: { completed: number; failed: number; other: number }, r: any) => {
    const p: string = r.status?.phase ?? ''
    if (p === 'Completed') acc.completed++
    else if (FAILED.has(p)) acc.failed++
    else acc.other++
    return acc
  }, { completed: 0, failed: 0, other: 0 })
  return (
    <div>
      <RefreshFailedNotice queries={[activity]} className="mb-3" />
      {reading && (
        <section className={CARD}>
          <VeleroScheduleActivity reading={reading} created={resource?.metadata?.creationTimestamp} onNavigate={(ref) => onNavigate?.(veleroResource(ref.kind, ref.namespace, ref.name))} onShowAllRuns={showRuns} />
        </section>
      )}
      <Card title="At a glance">
        <FactGrid>
          <FactRow label="Backs up">{namespacesText(list(template.includedNamespaces), list(template.excludedNamespaces))}</FactRow>
          <FactRow label="Stored in">
            {locationName ? (
              <>
                <Link onClick={() => onNavigate?.(veleroResource('BackupStorageLocation', namespace, locationName))}>{locationName}</Link>
                <span className={`ml-2 text-xs ${TONE_TEXT[availability.tone === 'ok' ? 'ok' : availability.tone === 'bad' ? 'bad' : 'unknown']}`}>{availability.text}</span>
                {!template.storageLocation && <Note>The installation’s default; the schedule names none</Note>}
              </>
            ) : (
              <span className="text-theme-text-secondary">The default storage location, which could not be read</span>
            )}
          </FactRow>
          <FactRow label="Each run kept for">{template.ttl ? veleroRetentionText(template.ttl) : 'The Velero server’s default (30 days unless it was changed)'}</FactRow>
          <FactRow label="Runs Velero keeps">
            {backups.isPending ? 'Loading…' : backups.error ? <span className="text-theme-text-secondary">Not read: {(backups.error as Error).message}</span> : runs.length === 0 ? 'None' : (
              <>
                {runs.length}: {counts.completed} completed{counts.failed ? `, ${counts.failed} failed or partly failed` : ''}{counts.other ? `, ${counts.other} other` : ''}
                <Note>Older runs are gone once their retention passes, so this is not a full history.</Note>
              </>
            )}
          </FactRow>
        </FactGrid>
      </Card>
    </div>
  )
}

/** Every run of a schedule Velero still keeps, failed first when any failed. */
export function VeleroScheduleRuns({ namespace, name, onNavigate }: { namespace: string; name: string; onNavigate?: NavigateToResource }) {
  const { runs, backups } = useScheduleRuns(namespace, name)
  const failed = runs.filter((r: any) => FAILED.has(r.status?.phase ?? ''))
  const [view, setView] = useState<'failed' | 'all' | null>(null)
  const shown = (view ?? (failed.length > 0 ? 'failed' : 'all')) === 'failed' ? failed : runs
  return (
    <div className="space-y-3 p-4">
      <Segments
        label="Runs"
        value={view ?? (failed.length > 0 ? 'failed' : 'all')}
        onChange={setView}
        options={[{ id: 'failed', label: 'Failed', count: failed.length }, { id: 'all', label: 'All', count: runs.length }]}
      />
      <SectionTable
        columns={[
          { header: 'Run', cell: (r: any) => <span className="font-mono text-xs">{r.metadata.name}</span> },
          { header: 'Outcome', width: '170px', cell: (r: any) => <Outcome badge={getBackupStatus(r)} /> },
          { header: 'Started', width: '130px', cell: (r: any) => (
            <Tooltip content={r.status?.startTimestamp ? veleroLocalTime(r.status.startTimestamp) : 'Not started'}>
              <span>{veleroMomentText(r.status?.startTimestamp, 'not started')}</span>
            </Tooltip>
          ) },
          { header: 'Took', width: '100px', cell: (r: any) => durationText(r.status?.startTimestamp, r.status?.completionTimestamp) ?? '—' },
          { header: 'Items', width: '110px', cell: (r: any) => (r.status?.progress?.totalItems ? `${r.status.progress.itemsBackedUp ?? 0} / ${r.status.progress.totalItems}` : '—') },
          { header: 'Errors · warnings', width: '140px', cell: (r: any) => `${getBackupErrors(r)} · ${getBackupWarnings(r)}` },
          { header: 'Kept until', width: '150px', cell: (r: any) => (r.status?.expiration ? (expired(r.status.expiration) ? 'past retention' : veleroLocalTime(r.status.expiration)) : '—') },
        ]}
        rows={shown}
        rowKey={(r: any) => r.metadata.uid ?? r.metadata.name}
        rowResource={(r: any) => veleroResource('Backup', namespace, r.metadata.name)}
        onInspect={onNavigate}
        minWidth={900}
        empty={backups.isPending ? 'Loading runs…' : backups.error ? `Backups could not be read: ${(backups.error as Error).message}` : view === 'failed' || failed.length > 0 ? 'No failed runs among those Velero keeps.' : 'No runs kept. Velero deletes each run once its retention passes.'}
        footer="Runs are this schedule’s backups that Velero still keeps (by the velero.io/schedule-name label); older ones are gone once their retention passes."
      />
    </div>
  )
}

// ---- Storage location ---------------------------------------------------

export function VeleroLocationOverview({ resource, onNavigate }: { resource: any; onNavigate?: NavigateToResource }) {
  const namespace: string = resource?.metadata?.namespace ?? ''
  const name: string = resource?.metadata?.name ?? ''
  const { location, workspace } = useReadings(namespace)
  const loc = location(name)
  const availability = locationAvailability(loc ?? { namespace, name, phase: resource?.status?.phase, default: false, defaultSource: 'unknown', schedules: null, attention: false })
  const spec = resource?.spec ?? {}
  const objectStorage = spec.objectStorage ?? {}
  const def = loc ? veleroDefaultText(loc) : null
  return (
    <div>
      <RefreshFailedNotice queries={[workspace]} className="mb-3" />
      <Card title="At a glance">
        <FactGrid>
          <FactRow label="Availability">
            <span className={TONE_TEXT[availability.tone === 'ok' ? 'ok' : availability.tone === 'bad' ? 'bad' : 'unknown']}>{availability.text[0].toUpperCase() + availability.text.slice(1)}</span>
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
          <FactRow label="Default">
            {def ? <>{def.text}{def.detail && <Note>{def.detail}</Note>}</> : '—'}
          </FactRow>
          <FactRow label="Access">{spec.accessMode === 'ReadOnly' ? 'Read-only: Velero writes no backups here and deletes none' : 'Read-write'}</FactRow>
          <FactRow label="Where">
            {[spec.provider, objectStorage.bucket, objectStorage.prefix].filter(Boolean).join(' · ') || '—'}
          </FactRow>
          <FactRow label="Holds">
            {loc?.backups ? (
              <>
                {loc.backups.targeting} backups: {loc.backups.completedNotExpired} completed and inside their retention, {loc.backups.expired} past it
                <Note>Velero never tests a restore; completed is what it reports.</Note>
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
                  <Link key={s.name} onClick={() => onNavigate?.(veleroResource('Schedule', namespace, s.name))}>{s.name}{s.viaDefault ? ' (as the default)' : ''}</Link>
                ))}
              </span>
            )}
          </FactRow>
        </FactGrid>
      </Card>
    </div>
  )
}

/** The backups a location holds, newest first. */
export function VeleroLocationBackups({ namespace, name, onNavigate }: { namespace: string; name: string; onNavigate?: NavigateToResource }) {
  const stored = useVeleroStoredBackups(namespace, name)
  const rows = stored.data?.backups ?? []
  return (
    <div className="p-4">
      <SectionTable
        title="Backups stored here"
        subtitle={stored.data ? `${stored.data.stored} · ${stored.data.restorable} completed and inside retention · ${stored.data.expired} past it` : undefined}
        columns={[
          { header: 'Backup', cell: (b) => <span className="font-mono text-xs">{b.name}</span> },
          { header: 'Outcome', width: '170px', cell: (b) => <Outcome badge={getBackupStatus({ status: { phase: b.phase } })} /> },
          { header: 'Completed', width: '150px', cell: (b) => veleroMomentText(b.completed) },
          { header: 'Kept until', width: '170px', cell: (b) => (b.expiration ? (expired(b.expiration) ? 'past retention' : veleroLocalTime(b.expiration)) : '—') },
        ]}
        rows={rows}
        rowKey={(b) => b.name}
        rowResource={(b) => veleroResource('Backup', b.namespace, b.name)}
        onInspect={onNavigate}
        empty={stored.isPending ? 'Loading backups…' : stored.error ? `Backups could not be read: ${(stored.error as Error).message}` : 'No backups stored here.'}
        footer={stored.data?.truncated ? `The newest ${rows.length} of ${stored.data.stored}.` : undefined}
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
  return (
    <div className="space-y-4 p-4">
      {leadContent}
      {body}
    </div>
  )
}
