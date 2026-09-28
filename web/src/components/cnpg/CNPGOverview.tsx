import { useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
import type { UseQueryResult } from '@tanstack/react-query'
import { clsx } from 'clsx'
import { ArrowRight, Database, Search, X } from 'lucide-react'
import {
  CNPG_KIND_BY_KEY,
  CNPG_PROBLEM_CATEGORIES,
  FactValue,
  PaneLoader,
  StatusDot,
  Tooltip,
  toneTextClass,
  type CNPGFleet,
  type CNPGFleetRow,
  type CNPGProblemCategory,
  type CNPGWorkspaceResponse,
} from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { useConnection } from '../../context/ConnectionContext'
import { EmptyState, Notice, ROW_HOVER, TABLE_HEAD, TABLE_WRAP, TBODY, TD, TH } from '../capacity/shared'
import { cnpgClusterFullPath } from './paths'
import { sameResource } from './routes'

const COVERAGE_LABEL: Record<string, string> = {
  denied: 'no access',
  partial: 'no access in some namespaces',
  syncing: 'still loading',
  error: 'could not be read',
}

type Filter = 'attention' | 'all'

export function CNPGWorkspaceHeader({
  title,
  subtitle,
  actions,
}: {
  title: string
  subtitle?: React.ReactNode
  actions?: React.ReactNode
}) {
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

function InstancePills({ row }: { row: CNPGFleetRow }) {
  if (row.pods.length === 0) return null
  return (
    <div className="mt-1 flex flex-wrap gap-1">
      {row.pods.map((p) => {
        const tone = p.ready === true ? 'healthy' : p.ready === false ? 'unhealthy' : 'unknown'
        return (
          <Tooltip key={p.name} content={`${p.name} · ${p.role} · ${p.ready === true ? 'ready' : p.ready === false ? 'not ready' : 'readiness unknown'}`}>
            <span className="inline-flex items-center gap-1 rounded border border-theme-border bg-theme-base px-1 text-[10.5px] font-mono text-theme-text-secondary">
              <StatusDot tone={tone} size="xs" />
              {p.role === 'primary' ? 'P' : p.role === 'replica' ? 'R' : '?'}
            </span>
          </Tooltip>
        )
      })}
    </div>
  )
}

function AttentionCell({ row }: { row: CNPGFleetRow }) {
  const top = row.problems.find((p) => p.severity !== 'posture') ?? row.problems[0]
  if (!top) return <span className="text-theme-text-tertiary">—</span>
  const tone = top.severity === 'critical' ? 'unhealthy' : top.severity === 'warning' ? 'degraded' : 'neutral'
  const more = row.problems.length - 1
  return (
    <div className="min-w-0">
      <div className={clsx('line-clamp-2 break-words', toneTextClass(tone))} title={top.title}>
        {top.title}
      </div>
      {more > 0 && <div className="text-xs text-theme-text-tertiary">+{more} more</div>}
    </div>
  )
}

export function CNPGOverview({
  query,
  fleet,
  namespaces,
  searchParams,
  onSetParams,
  onInspect,
  inspected,
  onClearNamespaces,
}: {
  query: UseQueryResult<CNPGWorkspaceResponse>
  fleet: CNPGFleet | null
  namespaces: string[]
  searchParams: URLSearchParams
  onSetParams: (update: Record<string, string | null>) => void
  onInspect: (resource: SelectedResource) => void
  inspected: SelectedResource | null
  onClearNamespaces: () => void
}) {
  const navigate = useNavigate()
  const { connection } = useConnection()
  const data = query.data
  const q = searchParams.get('q') ?? ''
  const cat = (searchParams.get('cat') as CNPGProblemCategory | null) ?? null
  const rawFilter = searchParams.get('filter') as Filter | null
  const filter: Filter = rawFilter ?? (fleet && fleet.attentionCount > 0 ? 'attention' : 'all')

  const rows = useMemo(() => {
    if (!fleet) return []
    let list = fleet.rows
    if (filter === 'attention') list = list.filter((r) => r.attention)
    if (cat) list = list.filter((r) => r.categories.has(cat))
    if (q) {
      const needle = q.toLowerCase()
      list = list.filter((r) => r.name.toLowerCase().includes(needle) || r.namespace.toLowerCase().includes(needle))
    }
    return list
  }, [fleet, filter, cat, q])

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

  const clustersCov = data.coverage.clusters
  const total = fleet.rows.length
  const context = connection.context || data.context

  if (total === 0) {
    const denied = clustersCov?.state === 'denied'
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <CNPGWorkspaceHeader title="Overview" />
        <EmptyState
          icon={Database}
          title={denied ? 'No access to PostgreSQL clusters' : `No PostgreSQL clusters in ${context}`}
          detail={
            denied
              ? 'Your identity cannot list CloudNativePG Clusters. Other CloudNativePG kinds may still be browsable under Resource kinds.'
              : namespaces.length > 0
                ? `None in namespace ${namespaces.join(', ')}. Clear the namespace filter to see the whole cluster.`
                : 'The CloudNativePG CRDs are installed. Clusters, backups and declarations appear here once they exist.'
          }
          action={
            namespaces.length > 0 ? (
              <button type="button" onClick={onClearNamespaces} className="mt-3 text-sm font-medium text-accent-text hover:underline">
                Clear namespace filter
              </button>
            ) : undefined
          }
        />
      </div>
    )
  }

  const chips: { label: string; onClear: () => void }[] = []
  if (cat) chips.push({ label: `Problem: ${CNPG_PROBLEM_CATEGORIES.find((c) => c.id === cat)?.label ?? cat}`, onClear: () => onSetParams({ cat: null }) })
  if (q) chips.push({ label: `Search: ${q}`, onClear: () => onSetParams({ q: null }) })
  if (namespaces.length > 0) chips.push({ label: `Namespace: ${namespaces.join(', ')}`, onClear: onClearNamespaces })

  const segment = (id: Filter, label: string, n: number) => {
    const on = filter === id
    return (
      <button
        key={id}
        type="button"
        role="tab"
        aria-selected={on}
        onClick={() => onSetParams({ filter: id })}
        className={clsx(
          'inline-flex items-center gap-1.5 rounded-md px-3 py-1 text-sm font-medium transition-colors',
          on ? 'bg-theme-surface text-theme-text-primary shadow-theme-sm' : 'text-theme-text-secondary hover:text-theme-text-primary',
        )}
      >
        {label}
        <span className="font-mono text-xs text-theme-text-tertiary">{n}</span>
      </button>
    )
  }

  const lowerBound = fleet.incompleteKinds.length > 0

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <CNPGWorkspaceHeader
        title="Overview"
        subtitle={
          <>
            {total} PostgreSQL {total === 1 ? 'cluster' : 'clusters'} · {fleet.attentionCount}
            {lowerBound ? '+' : ''} need attention
            <span className="text-theme-text-tertiary"> · {context}</span>
          </>
        }
      />
      <div className="min-h-0 flex-1 overflow-y-auto [scrollbar-gutter:stable]">
        <div className="space-y-3 px-5 pb-6 pt-3 xl:px-7">
          <CoverageNotice fleet={fleet} data={data} />

          <div className="flex flex-wrap items-center gap-2">
            <div role="tablist" aria-label="Clusters" className="inline-flex rounded-lg bg-theme-elevated p-0.5">
              {segment('attention', 'Needs attention', fleet.attentionCount)}
              {segment('all', 'All clusters', total)}
            </div>
            {CNPG_PROBLEM_CATEGORIES.filter((c) => fleet.categoryCounts[c.id] > 0).map((c) => {
              const on = cat === c.id
              return (
                <button
                  key={c.id}
                  type="button"
                  aria-pressed={on}
                  onClick={() => onSetParams({ cat: on ? null : c.id })}
                  className={clsx(
                    'inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs transition-colors',
                    on ? 'border-accent bg-accent-muted text-accent-text' : 'border-theme-border text-theme-text-secondary hover:bg-theme-hover',
                  )}
                >
                  {c.label}
                  <span className="font-mono">{fleet.categoryCounts[c.id]}</span>
                </button>
              )
            })}
            <div className="ml-auto flex h-8 w-56 items-center gap-2 rounded-lg border border-theme-border bg-theme-surface px-2.5">
              <Search className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
              <input
                value={q}
                onChange={(e) => onSetParams({ q: e.target.value })}
                placeholder="Filter clusters…"
                aria-label="Filter clusters"
                className="min-w-0 flex-1 bg-transparent text-sm text-theme-text-primary placeholder-theme-text-disabled focus:outline-none"
              />
            </div>
          </div>

          {chips.length > 0 && (
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
          )}

          <div className="overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
            <div className={TABLE_WRAP}>
              <table className="w-full min-w-[980px] table-fixed">
                <colgroup>
                  <col className="w-[22%]" />
                  <col className="w-[7%]" />
                  <col className="w-[15%]" />
                  <col className="w-[15%]" />
                  <col className="w-[12%]" />
                  <col className="w-[5%]" />
                  <col className="w-[16%]" />
                  <col className="w-[8%]" />
                </colgroup>
                <thead className={TABLE_HEAD}>
                  <tr>
                    <th className={TH}>Cluster</th>
                    <th className={TH}>Ready</th>
                    <th className={TH}>Replication</th>
                    <th className={TH}>Protection</th>
                    <th className={TH}>Declarations</th>
                    <th className={TH}>PG</th>
                    <th className={TH}>Needs attention</th>
                    <th className={TH}><span className="sr-only">Actions</span></th>
                  </tr>
                </thead>
                <tbody className={TBODY}>
                  {rows.map((row) => {
                    const ref: SelectedResource = { kind: 'clusters', group: 'postgresql.cnpg.io', namespace: row.namespace, name: row.name }
                    const active = sameResource(inspected, ref)
                    return (
                      <tr
                        key={row.key}
                        onClick={() => onInspect(ref)}
                        className={clsx('cursor-pointer', ROW_HOVER, active && 'selection')}
                        aria-selected={active}
                      >
                        <td className={TD}>
                          <div className="flex items-center gap-2 min-w-0">
                            <StatusDot tone={row.attention ? (row.problems.some((p) => p.severity === 'critical') ? 'unhealthy' : 'degraded') : row.controllerStatus.level} />
                            <span className="truncate font-medium" title={row.name}>{row.name}</span>
                          </div>
                          <div className="truncate pl-4 text-xs text-theme-text-tertiary">{row.namespace}</div>
                          <div className="pl-4"><InstancePills row={row} /></div>
                        </td>
                        <td className={clsx(TD, 'font-mono')}>
                          {row.instances.ready ?? '–'}/{row.instances.desired ?? '–'}
                        </td>
                        <td className={TD}><FactValue fact={row.replication} /></td>
                        <td className={TD}><FactValue fact={row.protection.summary} /></td>
                        <td className={TD}><FactValue fact={row.declarations.summary} /></td>
                        <td className={clsx(TD, 'font-mono')}>{row.pgVersion ?? '—'}</td>
                        <td className={TD}><AttentionCell row={row} /></td>
                        <td className={clsx(TD, 'text-right')}>
                          <button
                            type="button"
                            onClick={(e) => {
                              e.stopPropagation()
                              navigate(cnpgClusterFullPath(row.namespace, row.name), { state: { returnLabel: 'CloudNativePG Overview' } })
                            }}
                            className="inline-flex items-center gap-1 whitespace-nowrap rounded-md px-2 py-1 text-xs font-medium text-accent-text hover:bg-theme-hover"
                          >
                            Open <ArrowRight className="h-3 w-3" />
                          </button>
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
            {rows.length === 0 && (
              <div className="px-4 py-6 text-sm text-theme-text-tertiary">
                {filter === 'attention' && !cat && !q
                  ? 'No clusters need attention.'
                  : 'No clusters match these filters.'}{' '}
                <button type="button" onClick={() => onSetParams({ filter: 'all', cat: null, q: null })} className="font-medium text-accent-text hover:underline">
                  Show all clusters
                </button>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
