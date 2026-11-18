import { useEffect, useState, type ReactNode } from 'react'
import type { UseQueryResult } from '@tanstack/react-query'
import { Archive } from 'lucide-react'
import {
  PaneLoader,
  RadarUpgradeAction,
  Tooltip,
  getRadarUpgradeRequirement,
  issueTitle,
  radarUpgradeDetail,
  radarUpgradeHeadline,
  veleroPlainReason,
  veleroReasonNextStep,
  type VeleroIssue,
  type VeleroWorkspaceKey,
  type VeleroWorkspaceResponse,
  type WorkspaceProblem,
} from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { useConnection } from '../../context/ConnectionContext'
import { refToSelectedResource } from '../../utils/navigation'
import { Notice, RefreshFailedNotice, ScreenEmptyState, Segments } from '../workspace/layout'
import { INCOMPLETE_COVERAGE_STATES, coverageLabel } from '../workspace/coverage'
import { VELERO_GROUP } from './routes'

export interface VeleroScreenProps {
  data: VeleroWorkspaceResponse
  /** The installation namespace the screen is narrowed to; null for all of them. */
  installation: string | null
  onInstallation: (namespace: string | null) => void
  onInspect: (resource: SelectedResource) => void
  inspected: SelectedResource | null
  /** Create one of the screen's kind, where the screen is a kind's list. */
  onCreate?: () => void
}

const KIND_LABEL: Record<VeleroWorkspaceKey, string> = {
  schedules: 'Schedule',
  backups: 'Backup',
  backupStorageLocations: 'BackupStorageLocation',
  restores: 'Restore',
  backupRepositories: 'BackupRepository',
}

export function list(values: unknown): string[] {
  return Array.isArray(values) ? values.filter((v): v is string => typeof v === 'string') : []
}

// Exclusions Velero writes into every spec it processes (v1.18 backup and
// restore controllers), so they say nothing about what the user chose. A
// backup gets the CSI snapshot kinds: both in excludedResources with the old
// filters, else split across the scoped lists. A restore gets the kinds
// Velero never restores.
const VELERO_SNAPSHOT = 'volumesnapshots.snapshot.storage.k8s.io'
const VELERO_SNAPSHOT_CONTENT = 'volumesnapshotcontents.snapshot.storage.k8s.io'
const VELERO_BACKUP_EXCLUSIONS: Record<string, readonly string[]> = {
  excludedResources: [VELERO_SNAPSHOT, VELERO_SNAPSHOT_CONTENT],
  excludedClusterScopedResources: [VELERO_SNAPSHOT_CONTENT],
  excludedNamespaceScopedResources: [VELERO_SNAPSHOT],
}
const VELERO_NON_RESTORABLE = [
  'nodes',
  'events',
  'events.events.k8s.io',
  'backups.velero.io',
  'restores.velero.io',
  'resticrepositories.velero.io',
  'csinodes.storage.k8s.io',
  'volumeattachments.storage.k8s.io',
  'backuprepositories.velero.io',
]

const RESOURCE_FILTERS = [
  'includedResources',
  'excludedResources',
  'includedClusterScopedResources',
  'excludedClusterScopedResources',
  'includedNamespaceScopedResources',
  'excludedNamespaceScopedResources',
] as const

/**
 * A run's or template's namespace selection as declared, and whether resource
 * or label filters narrow it. `kind` says which exclusions Velero adds on its
 * own (a schedule's template becomes a backup's spec), so they are not counted.
 */
export function selectionText(spec: any, emptyMeans: string, kind: 'backup' | 'restore' = 'backup'): { text: string; narrowed: boolean } {
  const included = list(spec?.includedNamespaces)
  const excluded = list(spec?.excludedNamespaces)
  const base = included.length === 0 || included.includes('*') ? emptyMeans : included.join(', ')
  const text = excluded.length ? `${base}, except ${excluded.join(', ')}` : base
  const injected = (field: string): readonly string[] =>
    kind === 'restore' ? (field === 'excludedResources' ? VELERO_NON_RESTORABLE : []) : (VELERO_BACKUP_EXCLUSIONS[field] ?? [])
  // `*` in an include list is everything; in an exclude list it drops the whole scope, which narrows.
  const narrowed =
    RESOURCE_FILTERS.some((field) => list(spec?.[field]).some((v) => (field.startsWith('included') ? v !== '*' : !injected(field).includes(v)))) ||
    !!spec?.labelSelector ||
    list(spec?.orLabelSelectors).length > 0
  return { text, narrowed }
}

/** A restore's namespace selection: its spec, or a run row's copy of it. */
export interface RestoreSelection {
  includedNamespaces?: unknown
  excludedNamespaces?: unknown
  namespaceMapping?: Record<string, string>
}

/**
 * Where a restore puts things: every namespace in the backup or the ones it
 * includes, each mapped one with its destination, and what it excludes. A
 * mapping for an excluded namespace does nothing, since Velero restores
 * nothing from it.
 */
export function restoreTargets(spec: RestoreSelection | undefined): { every: boolean; entries: { from: string; to?: string }[]; excluded: string[] } {
  const included = list(spec?.includedNamespaces)
  const excluded = list(spec?.excludedNamespaces)
  const mapping = spec?.namespaceMapping ?? {}
  const every = included.length === 0 || included.includes('*')
  const entries = every
    ? Object.entries(mapping).filter(([from]) => !excluded.includes(from)).map(([from, to]) => ({ from, to }))
    : included.map((from) => ({ from, to: mapping[from] }))
  return { every, entries, excluded }
}

/** "Every namespace in the backup, with a → b, except c", or "a → b, shop". */
export function restoredIntoText(spec: RestoreSelection | undefined): string {
  const { every, entries, excluded } = restoreTargets(spec)
  const named = entries.map((e) => (e.to ? `${e.from} → ${e.to}` : e.from)).join(', ')
  const base = every ? `Every namespace in the backup${entries.length ? `, with ${named}` : ''}` : named
  return excluded.length ? `${base}, except ${excluded.join(', ')}` : base
}

// Characters a run's name keeps after the cut: the end of its timestamp, which tells runs apart.
const RUN_NAME_TAIL = 6

/**
 * An object's name in a table: mono, one line, cut with an ellipsis and whole
 * on hover. `middle` cuts a run's name in the middle, keeping its end
 * (`nightly-2026…41210`), since runs of one schedule differ only there.
 */
export function VeleroName({ name, middle }: { name: string; middle?: boolean }) {
  if (middle && name.length > RUN_NAME_TAIL * 2) {
    return (
      <Tooltip content={name} wrapperClassName="min-w-0">
        <span className="flex min-w-0 font-mono text-sm">
          <span className="min-w-0 truncate">{name.slice(0, -RUN_NAME_TAIL)}</span>
          <span className="shrink-0">{name.slice(-RUN_NAME_TAIL)}</span>
        </span>
      </Tooltip>
    )
  }
  return (
    <Tooltip content={name}>
      <span className="block min-w-0 truncate font-mono text-sm">{name}</span>
    </Tooltip>
  )
}

/**
 * Text that names objects, wrapped only between words: a browser would
 * otherwise break a name at its hyphen ("dr-" / "replica"). A name wider than
 * its column is cut with an ellipsis; callers keep the whole text in a tooltip.
 */
export function WholeNames({ text }: { text: string }) {
  return (
    <>
      {text.split(/(\s+)/).map((part, i) =>
        part.includes('-') ? (
          <span key={i} className="inline-block max-w-full truncate align-bottom">
            {part}
          </span>
        ) : (
          part
        ),
      )}
    </>
  )
}

/**
 * The width the Velero tables show every column at. Below it each table folds
 * its secondary columns into the cell they qualify, so nothing is clipped at
 * the right edge.
 */
export const VELERO_WIDE = '(min-width: 1280px)'

/** Opens a Velero object in the drawer. */
export function veleroResource(kind: string, namespace: string, name: string): SelectedResource {
  return refToSelectedResource({ kind, group: VELERO_GROUP, namespace, name })
}

/** Narrows rows to the selected installation. */
export function inInstallation<T extends { namespace: string }>(rows: T[], installation: string | null): T[] {
  return installation === null ? rows : rows.filter((r) => r.namespace === installation)
}

/**
 * The title line, a note when Radar is limited to one namespace and, with
 * more than one installation, the installation picker. The namespace picker
 * says the filter does not apply (VELERO_NAMESPACE_FILTER_TOOLTIP). The picker narrows what was already read; it never
 * changes what the server reads.
 */
export function VeleroWorkspaceHeader({ title, data, installation, onInstallation }: { title: string } & Pick<VeleroScreenProps, 'data' | 'installation' | 'onInstallation'>) {
  const namespaces = data.installations.map((i) => i.namespace)
  return (
    <div className="px-5 pt-4 xl:px-7">
      <div className="flex flex-wrap items-center gap-x-2">
        <span className="flex items-center gap-1.5 text-sm text-theme-text-tertiary">
          <Archive className="h-3.5 w-3.5" />
          Velero
          <span>/</span>
        </span>
        <h1 className="text-lg font-semibold text-theme-text-primary">{title}</h1>
      </div>
      {data.forcedNamespace && (
        <div className="mt-0.5 text-sm text-theme-text-secondary">
          Radar is limited to namespace {data.forcedNamespace}, so only a Velero installation there is shown.
        </div>
      )}
      {namespaces.length > 1 && (
        <div className="mt-2.5">
          <Segments
            label="Velero installation"
            value={installation ?? ''}
            options={[{ id: '', label: 'All installations' }, ...namespaces.map((ns) => ({ id: ns, label: ns }))]}
            onChange={(id) => onInstallation(id === '' ? null : id)}
          />
        </div>
      )}
    </div>
  )
}

/** Says which kinds this screen reads were not read in full, and why. */
export function VeleroCoverageNotice({ data, kinds }: { data: VeleroWorkspaceResponse; kinds: readonly VeleroWorkspaceKey[] }) {
  const byLabel = new Map<string, string[]>()
  for (const k of kinds) {
    const cov = data.coverage[k]
    if (cov && !INCOMPLETE_COVERAGE_STATES.has(cov.state)) continue
    const label = coverageLabel(cov)
    byLabel.set(label, [...(byLabel.get(label) ?? []), KIND_LABEL[k]])
  }
  if (byLabel.size === 0) return null
  const text = [...byLabel].map(([label, names]) => `${names.join(', ')} (${label})`).join('; ')
  return (
    <Notice>Radar couldn’t read some of the Velero data on this screen: {text}. What it couldn’t read shows as unknown.</Notice>
  )
}

/** "Name, name and 3 more": a few names, then a count. */
export function namesAndMore(names: string[], shown = 3): string {
  const head = names.slice(0, shown)
  const rest = names.length - head.length
  if (rest > 0) return `${head.join(', ')} and ${rest} more`
  return head.length > 1 ? `${head.slice(0, -1).join(', ')} and ${head.at(-1)}` : (head[0] ?? '')
}

// Velero's own messages often end without a full stop; a sentence after one needs it.
function sentence(text: string | undefined): string {
  const trimmed = (text ?? '').replace(/[\s:;,]+$/, '')
  return trimmed && !/[.!?…)]$/.test(trimmed) ? `${trimmed}.` : trimmed
}

/** The runs Velero rejected because of the issue's fault, each a link. */
function rejectedRuns(issue: VeleroIssue): WorkspaceProblem['related'] {
  const runs = issue.affected ?? []
  if (runs.length === 0) return undefined
  return {
    lead: `Velero rejected ${runs.length === 1 ? '1 run' : `${runs.length} runs`} because of it:`,
    refs: runs.map((r) => ({ kind: r.kind, group: VELERO_GROUP, namespace: r.namespace ?? issue.namespace ?? '', name: r.name })),
  }
}

export const VELERO_START_SERVER = 'Start the Velero server Deployment, or find out why its pods don’t run'
export const VELERO_CHECK_SERVER = 'Check that the Velero server is running, and read its logs'
export const VELERO_SHORTEN_NAME = 'Recreate the schedule with a name of 63 characters or fewer'

// The next step for each Velero Issue whose message Radar doesn't recognise.
const ISSUE_NEXT_STEP: Record<string, string> = {
  BackupFailed: 'Show its errors, fix the cause, and run the backup again',
  BackupPartiallyFailed: 'Show its errors to see what was not backed up, and run the backup again once fixed',
  BackupValidationFailed: 'Fix what Velero rejected, and create the backup again',
  RestoreFailed: 'Show its errors, fix the cause, and run the restore again',
  RestorePartiallyFailed: 'Show its errors to see what was not restored',
  RestoreValidationFailed: 'Fix what Velero rejected, and create the restore again',
  ScheduleValidationFailed: 'Fix what Velero rejected in the schedule; Velero validates it again when it changes',
  BackupStorageLocationUnavailable: 'Check the storage location’s bucket, credentials and endpoint',
  BackupRepositoryNotReady: 'Read the repository’s message and the Velero server’s logs',
  BackupRepositoryMaintenanceFailed: 'Read the maintenance job’s logs',
  VeleroRunStalled: VELERO_CHECK_SERVER,
  VeleroServerNotRunning: VELERO_START_SERVER,
  VeleroScheduleLate: 'Check that the Velero server is running with its schedule controller, and read its logs',
  VeleroScheduleHeld: 'Find out why its backup hasn’t started, or delete that backup',
  VeleroScheduleNotValidated: VELERO_CHECK_SERVER,
  VeleroLocationValidationOverdue: VELERO_CHECK_SERVER,
}

/**
 * What to do about a Velero Issue, in one plain line: the Issue's own action,
 * else what its message means, else the step for its kind of fault.
 */
export function veleroIssueNextStep(issue: Pick<VeleroIssue, 'action' | 'message' | 'reason'>): string | undefined {
  const action = issue.action?.trim()
  if (action) return action
  const fromMessage = veleroReasonNextStep(issue.message)
  if (fromMessage) return fromMessage
  if (issue.reason === 'VeleroScheduleCannotRun') {
    return /63 characters/.test(issue.message ?? '') ? VELERO_SHORTEN_NAME : 'Change the cron expression to one with a future time'
  }
  return issue.reason ? ISSUE_NEXT_STEP[issue.reason] : undefined
}

/**
 * An Issues row as a workspace problem. A message Radar recognises reads as
 * one plain line, with Velero's own text folded under it. `withNextStep` adds
 * what to do, for a banner with room for it.
 */
export function veleroIssueProblem(issue: VeleroIssue, opts?: { withNextStep?: boolean }): WorkspaceProblem {
  const message = issue.message?.trim()
  const plain = veleroPlainReason(message)
  return {
    id: issue.id,
    category: issue.category,
    // The title the Issues page gives the same row.
    title: issueTitle(issue),
    detail: [plain ?? message, issue.cause?.trim()].filter(Boolean).map(sentence).join(' ') || undefined,
    related: rejectedRuns(issue),
    rawDetail: plain ? message : undefined,
    rawDetailLabel: plain ? VELERO_MESSAGE_LABEL : undefined,
    nextStep: opts?.withNextStep ? veleroIssueNextStep(issue) : undefined,
    subject: { kind: issue.kind, group: issue.group ?? VELERO_GROUP, namespace: issue.namespace ?? '', name: issue.name },
    source: 'issue',
    tone: issue.severity === 'critical' ? 'unhealthy' : 'degraded',
  }
}

/** The label of a raw Velero message folded under its plain reading. */
export const VELERO_MESSAGE_LABEL = 'Velero’s message'

/**
 * Loading, error, needs-newer-Radar and not-installed states both Velero
 * screens share. Renders the screen only once there is workspace data.
 */
export function VeleroScreenGate({
  query,
  discoveryPending,
  discovered,
  children,
}: {
  query: Pick<UseQueryResult<VeleroWorkspaceResponse>, 'data' | 'error' | 'isLoading' | 'isFetching' | 'refetch' | 'isRefetchError' | 'dataUpdatedAt'>
  /** API discovery has not answered yet, so whether Velero is served is not known. */
  discoveryPending: boolean
  /** Discovery answered and serves velero.io, or could not be read (the server is asked then). */
  discovered: boolean
  children: (data: VeleroWorkspaceResponse) => ReactNode
}) {
  const { connection } = useConnection()
  const data = query.data
  const notInstalled = (
    <ScreenEmptyState
      icon={Archive}
      title="Velero is not installed"
      detail={`No velero.io resources are served in ${connection.context || 'this cluster'}.`}
    />
  )
  if (!data && (query.isLoading || discoveryPending)) return <PaneLoader label="Loading Velero…" className="flex-1" />
  if (!data && !discovered) return notInstalled
  if (!data) {
    const upgrade = getRadarUpgradeRequirement(query.error)
    if (upgrade) {
      return (
        <ScreenEmptyState
          icon={Archive}
          title={radarUpgradeHeadline('Velero')}
          detail={radarUpgradeDetail(upgrade)}
          action={
            <div className="mt-3 text-sm">
              <RadarUpgradeAction requirement={upgrade} />
            </div>
          }
        />
      )
    }
    return (
      <ScreenEmptyState
        icon={Archive}
        title="Velero data unavailable"
        detail={query.error instanceof Error ? query.error.message : 'Radar could not load the Velero data.'}
        action={
          <button type="button" onClick={() => void query.refetch()} disabled={query.isFetching} className="btn-secondary mt-3 px-3 py-1.5 text-sm">
            {query.isFetching ? 'Retrying…' : 'Retry'}
          </button>
        }
      />
    )
  }
  if (!data.installed) return notInstalled
  return <><RefreshFailedNotice queries={[query]} className="px-5 pt-3 xl:px-7" />{children(data)}</>
}

/**
 * The installation chips' selection. They narrow what was read; a namespace
 * that is no longer read drops the narrowing rather than showing an empty screen.
 */
export function useVeleroInstallation(installations: VeleroWorkspaceResponse['installations'] | undefined) {
  const [installation, setInstallation] = useState<string | null>(null)
  useEffect(() => {
    if (installation !== null && installations && !installations.some((i) => i.namespace === installation)) setInstallation(null)
  }, [installation, installations])
  return [installation, setInstallation] as const
}
