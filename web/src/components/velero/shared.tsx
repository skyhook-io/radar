import type { ReactNode } from 'react'
import type { UseQueryResult } from '@tanstack/react-query'
import { Archive } from 'lucide-react'
import {
  FoldSection,
  PaneLoader,
  RadarUpgradeAction,
  getRadarUpgradeRequirement,
  issueReasonTitle,
  radarUpgradeDetail,
  radarUpgradeHeadline,
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
}

const KIND_LABEL: Record<VeleroWorkspaceKey, string> = {
  schedules: 'Schedule',
  backups: 'Backup',
  backupStorageLocations: 'BackupStorageLocation',
  restores: 'Restore',
  backupRepositories: 'BackupRepository',
}

export const VELERO_SCOPE_LINE = 'All Velero installations you can read. The namespace filter does not apply here.'

/** Opens a Velero object in the drawer. */
export function veleroResource(kind: string, namespace: string, name: string): SelectedResource {
  return refToSelectedResource({ kind, group: VELERO_GROUP, namespace, name })
}

/** Narrows rows to the selected installation. */
export function inInstallation<T extends { namespace: string }>(rows: T[], installation: string | null): T[] {
  return installation === null ? rows : rows.filter((r) => r.namespace === installation)
}

/**
 * The title line, the scope line and, with more than one installation, the
 * installation picker. The picker narrows what was already read; it never
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
      <div className="mt-0.5 text-sm text-theme-text-secondary">
        {data.forcedNamespace
          ? `Radar is limited to namespace ${data.forcedNamespace}, so only a Velero installation there is shown. The namespace filter does not apply here.`
          : VELERO_SCOPE_LINE}
      </div>
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
    <Notice>
      Some Velero data used on this screen was not read. Readings in namespaces that were read in full stay exact; elsewhere a value Radar could not read is shown as unknown.
      <FoldSection title="Coverage details" summary="Kinds and reasons" attention={false}>
        {text}.
      </FoldSection>
    </Notice>
  )
}

function reasonWords(reason: string): string {
  const words = reason.replace(/([a-z])([A-Z])/g, '$1 $2').toLowerCase()
  return words[0] ? words[0].toUpperCase() + words.slice(1) : reason
}

/** An Issues row as a workspace problem, with the canonical tone for its severity. */
export function veleroIssueProblem(issue: VeleroIssue): WorkspaceProblem {
  const message = issue.message?.trim()
  const title = issueReasonTitle(issue.reason) ?? reasonWords(issue.reason)
  return {
    id: issue.id,
    category: issue.category,
    title,
    detail: [message, issue.cause?.trim()].filter(Boolean).join(' ') || undefined,
    subject: { kind: issue.kind, group: issue.group ?? VELERO_GROUP, namespace: issue.namespace ?? '', name: issue.name },
    source: 'issue',
    tone: issue.severity === 'critical' ? 'unhealthy' : 'degraded',
  }
}

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
