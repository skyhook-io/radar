import type { ReactNode } from 'react'
import type { UseQueryResult } from '@tanstack/react-query'
import { clsx } from 'clsx'
import { AlertTriangle, Database, X } from 'lucide-react'
import {
  CNPG_KIND_BY_KEY,
  PaneLoader,
  Tooltip,
  formatUpdatedAgo,
  toneTextClass,
  type CNPGFleet,
  type CNPGKindCoverage,
  type CNPGWorkspaceResponse,
} from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { useConnection } from '../../context/ConnectionContext'
import { EmptyState, Notice, ROW_HOVER, TABLE_HEAD, TABLE_WRAP, TBODY, TD, TH } from '../capacity/shared'
import { sameResource } from './routes'

export interface CNPGScreenProps {
  data: CNPGWorkspaceResponse
  fleet: CNPGFleet
  namespaces: string[]
  searchParams: URLSearchParams
  onSetParams: (update: Record<string, string | null>) => void
  onInspect: (resource: SelectedResource) => void
  inspected: SelectedResource | null
  onClearNamespaces: () => void
}

const COVERAGE_LABEL: Record<string, string> = {
  denied: 'no access',
  partial: 'no access in some namespaces',
  syncing: 'still loading',
  error: 'could not be read',
}

export function CNPGWorkspaceHeader({ title, subtitle, actions }: { title: string; subtitle?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="px-5 pt-4 xl:px-7">
      <div className="flex items-center gap-2 text-xs text-theme-text-tertiary">
        <Database className="h-3.5 w-3.5" />
        <span>CloudNativePG</span>
      </div>
      <div className="mt-1 flex flex-wrap items-end justify-between gap-x-4 gap-y-2">
        <div className="min-w-0">
          <h1 className="text-lg font-semibold text-theme-text-primary">{title}</h1>
          {subtitle && <div className="mt-0.5 text-sm text-theme-text-secondary">{subtitle}</div>}
        </div>
        {actions}
      </div>
    </div>
  )
}

export function CoverageNotice({ fleet, data }: { fleet: CNPGFleet; data: CNPGWorkspaceResponse }) {
  if (fleet.incompleteKinds.length === 0) return null
  const parts = fleet.incompleteKinds.map((k) => {
    const cov = data.coverage[k]
    const label = COVERAGE_LABEL[cov?.state ?? ''] ?? cov?.state
    return `${CNPG_KIND_BY_KEY[k].kind} (${label})`
  })
  return (
    <Notice>
      Some CloudNativePG data is not readable: {parts.join(', ')}. Facts built on it read “No access” or “unknown” rather than none, and counts are lower bounds.
    </Notice>
  )
}

/**
 * Loading, error and not-installed states every workspace screen shares.
 * Renders the screen only once there is workspace data to render.
 */
export function CNPGScreenGate({
  query,
  fleet,
  children,
}: {
  query: UseQueryResult<CNPGWorkspaceResponse>
  fleet: CNPGFleet | null
  children: (data: CNPGWorkspaceResponse, fleet: CNPGFleet) => ReactNode
}) {
  const { connection } = useConnection()
  const data = query.data
  if (!data && query.isLoading) return <PaneLoader label="Loading CloudNativePG…" className="flex-1" />
  if (!data) {
    return (
      <EmptyState
        icon={Database}
        title="CloudNativePG workspace unavailable"
        detail={query.error instanceof Error ? query.error.message : 'The workspace could not be loaded.'}
      />
    )
  }
  if (!data.installed || !fleet) {
    return (
      <EmptyState
        icon={Database}
        title="CloudNativePG is not installed"
        detail={`No postgresql.cnpg.io resources are served in ${connection.context || 'this cluster'}.`}
      />
    )
  }
  return <>{children(data, fleet)}</>
}

export function ScreenBody({ children }: { children: ReactNode }) {
  return (
    <div className="min-h-0 flex-1 overflow-y-auto [scrollbar-gutter:stable]">
      <div className="space-y-5 px-5 pb-6 pt-3 xl:px-7">{children}</div>
    </div>
  )
}

export function FilterChips({ chips }: { chips: { label: string; onClear: () => void }[] }) {
  if (chips.length === 0) return null
  return (
    <div className="flex flex-wrap gap-1.5">
      {chips.map((c) => (
        <span key={c.label} className="inline-flex items-center gap-1 rounded-full bg-theme-elevated px-2.5 py-0.5 text-xs text-theme-text-secondary">
          {c.label}
          <button type="button" onClick={c.onClear} aria-label={`Remove ${c.label}`} className="rounded-full p-0.5 hover:bg-theme-hover">
            <X className="h-3 w-3" />
          </button>
        </span>
      ))}
    </div>
  )
}

export function namespaceChip(namespaces: string[], onClear: () => void) {
  return namespaces.length > 0 ? [{ label: `Namespace: ${namespaces.join(', ')}`, onClear }] : []
}

export function Segments<T extends string>({
  value,
  options,
  onChange,
  label,
}: {
  value: T
  options: { id: T; label: string; count?: number }[]
  onChange: (id: T) => void
  label: string
}) {
  return (
    <div role="tablist" aria-label={label} className="inline-flex rounded-lg bg-theme-elevated p-0.5">
      {options.map((o) => {
        const on = o.id === value
        return (
          <button
            key={o.id}
            type="button"
            role="tab"
            aria-selected={on}
            onClick={() => onChange(o.id)}
            className={clsx(
              'inline-flex items-center gap-1.5 rounded-md px-3 py-1 text-sm font-medium transition-colors',
              on ? 'bg-theme-surface text-theme-text-primary shadow-theme-sm' : 'text-theme-text-secondary hover:text-theme-text-primary',
            )}
          >
            {o.label}
            {o.count !== undefined && <span className="font-mono text-xs text-theme-text-tertiary">{o.count}</span>}
          </button>
        )
      })}
    </div>
  )
}

export interface TableColumn<T> {
  header: ReactNode
  width?: string
  cell: (row: T) => ReactNode
  className?: string
}

/** A workspace table. Rows inspect in the drawer; the inspected row is highlighted. */
export function SectionTable<T>({
  title,
  subtitle,
  columns,
  rows,
  rowKey,
  rowResource,
  onInspect,
  inspected,
  empty,
  minWidth = 760,
  footer,
}: {
  title: ReactNode
  subtitle?: ReactNode
  columns: TableColumn<T>[]
  rows: T[]
  rowKey: (row: T) => string
  rowResource?: (row: T) => SelectedResource | null
  onInspect?: (resource: SelectedResource) => void
  inspected?: SelectedResource | null
  empty: ReactNode
  minWidth?: number
  footer?: ReactNode
}) {
  return (
    <section>
      <div className="mb-2 flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
        <h2 className="text-sm font-semibold text-theme-text-primary">{title}</h2>
        {subtitle && <span className="text-xs text-theme-text-tertiary">{subtitle}</span>}
      </div>
      <div className="overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
        {rows.length === 0 ? (
          <div className="px-4 py-5 text-sm text-theme-text-tertiary">{empty}</div>
        ) : (
          <div className={TABLE_WRAP}>
            <table className="w-full table-fixed" style={{ minWidth }}>
              <colgroup>
                {columns.map((c, i) => (
                  <col key={i} style={c.width ? { width: c.width } : undefined} />
                ))}
              </colgroup>
              <thead className={TABLE_HEAD}>
                <tr>
                  {columns.map((c, i) => (
                    <th key={i} className={TH}>{c.header}</th>
                  ))}
                </tr>
              </thead>
              <tbody className={TBODY}>
                {rows.map((row) => {
                  const res = rowResource?.(row) ?? null
                  const active = !!res && sameResource(inspected, res)
                  return (
                    <tr
                      key={rowKey(row)}
                      onClick={res && onInspect ? () => onInspect(res) : undefined}
                      className={clsx(res && onInspect && 'cursor-pointer', ROW_HOVER, active && 'selection')}
                      aria-selected={res ? active : undefined}
                    >
                      {columns.map((c, i) => (
                        <td key={i} className={clsx(TD, c.className)}>{c.cell(row)}</td>
                      ))}
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
      {footer && <div className="mt-1.5 text-xs text-theme-text-tertiary">{footer}</div>}
    </section>
  )
}

/** Empty-state text for a collection, derived from how much of it was readable. */
export function coverageEmpty(cov: CNPGKindCoverage | undefined, noun: string): string {
  switch (cov?.state) {
    case 'full':
      return `No ${noun} in this scope.`
    case 'partial':
      return `No ${noun} visible. Some namespaces are not readable with your access.`
    case 'denied':
      return `No access to ${noun}.`
    case 'syncing':
      return `${noun[0].toUpperCase()}${noun.slice(1)} are still loading.`
    case 'error':
      return `${noun[0].toUpperCase()}${noun.slice(1)} could not be read.`
    default:
      return `This kind is not installed.`
  }
}

/** The less complete of two coverages, for collections built from several kinds. */
export function worstCoverage(...covs: (CNPGKindCoverage | undefined)[]): CNPGKindCoverage | undefined {
  const rank: Record<string, number> = { error: 0, denied: 1, syncing: 2, partial: 3, full: 4, notInstalled: 5 }
  return covs.filter(Boolean).sort((a, b) => (rank[a!.state] ?? 9) - (rank[b!.state] ?? 9))[0]
}

/**
 * A URL or path that wraps only after "/" (never mid-word), with the whole
 * value on hover; a single segment too long for its cell is cut with an
 * ellipsis.
 */
export function PathText({ value, className }: { value: string; className?: string }) {
  const parts = value.split(/(?<=\/)/)
  return (
    <Tooltip content={value} wrapperClassName="max-w-full">
      <span className={clsx('block max-w-full overflow-hidden text-ellipsis font-mono text-[12.5px] [overflow-wrap:normal]', className)}>
        {parts.map((p, i) => (
          <span key={i}>
            {p}
            {i < parts.length - 1 && <wbr />}
          </span>
        ))}
      </span>
    </Tooltip>
  )
}

export function Mono({ children }: { children: ReactNode }) {
  return <span className="font-mono text-[12.5px] break-all">{children}</span>
}

export function Sub({ children }: { children: ReactNode }) {
  return <div className="mt-0.5 text-xs text-theme-text-tertiary break-words">{children}</div>
}

export function clusterResource(namespace: string, name: string): SelectedResource {
  return { kind: 'clusters', group: 'postgresql.cnpg.io', namespace, name }
}

export function cnpgResource(plural: string, namespace: string, name: string, group = 'postgresql.cnpg.io'): SelectedResource {
  return { kind: plural, group, namespace, name }
}

type RefreshableQuery = Pick<UseQueryResult<unknown>, 'isRefetchError' | 'error' | 'dataUpdatedAt'>

/**
 * A refetch failed while the last good answer stays on screen: say so, why,
 * and how old that answer is, so cached values are not read as current.
 */
export function CNPGRefreshFailedNotice({ queries, className }: { queries: RefreshableQuery[]; className?: string }) {
  const failed = queries.filter((q) => q.isRefetchError)
  if (failed.length === 0) return null
  const oldest = failed.reduce((a, b) => (b.dataUpdatedAt < a.dataUpdatedAt ? b : a))
  const reason = oldest.error instanceof Error ? oldest.error.message : 'unknown error'
  return (
    <div role="status" className={clsx('flex items-start gap-1.5 text-xs text-theme-text-secondary', className)}>
      <AlertTriangle className={clsx('mt-px h-3.5 w-3.5 shrink-0', toneTextClass('degraded'))} />
      <span>
        Last refresh failed: {reason.length > 160 ? `${reason.slice(0, 160)}…` : reason} · showing data from {formatUpdatedAgo(Date.now() - oldest.dataUpdatedAt)}
      </span>
    </div>
  )
}
