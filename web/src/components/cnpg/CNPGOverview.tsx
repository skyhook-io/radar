import { useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
import { clsx } from 'clsx'
import { ArrowRight, Database, FileText, Search } from 'lucide-react'
import {
  CNPG_PROBLEM_CATEGORIES,
  cnpgReadyInstances,
  FactValue,
  StatusDot,
  Tooltip,
  toneTextClass,
  type CNPGFleetRow,
  type CNPGProblemCategory,
} from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { useConnection } from '../../context/ConnectionContext'
import { EmptyState, ROW_HOVER, TABLE_HEAD, TABLE_WRAP, TBODY, TD, TH } from '../capacity/shared'
import { CNPGWorkspaceHeader, CoverageNotice, FilterChips, type CNPGScreenProps } from './shared'
import { cnpgClusterFullPath, currentPageLabel } from './paths'
import { sameResource } from './routes'
import { CNPGOperatorBanner } from './CNPGOperatorBanner'
import { cnpgInstancePillLabel, cnpgRowStatus } from './fleetStatus'

type Filter = 'attention' | 'all'

function InstancePills({ row }: { row: CNPGFleetRow }) {
  if (row.pods.length === 0) return null
  return (
    <div className="mt-1 flex flex-wrap gap-1 font-sans">
      {row.pods.map((p) => {
        const tone = p.ready === true ? 'healthy' : p.ready === false ? 'unhealthy' : 'unknown'
        return (
          <Tooltip key={p.name} content={cnpgInstancePillLabel(p)}>
            <span
              aria-label={cnpgInstancePillLabel(p)}
              className="inline-flex items-center gap-1 rounded border border-theme-border bg-theme-base px-1 font-mono text-[10.5px] text-theme-text-secondary"
            >
              <StatusDot tone={tone} size="xs" />
              {p.role === 'primary' ? 'P' : p.role === 'replica' ? 'R' : '?'}
            </span>
          </Tooltip>
        )
      })}
    </div>
  )
}

function ReadyCell({ row }: { row: CNPGFleetRow }) {
  const r = cnpgReadyInstances(row)
  return (
    <>
      {r.note ? (
        <Tooltip content={r.note}>
          <span className={clsx('underline decoration-dotted underline-offset-2', toneTextClass(r.tone ?? 'unknown'))}>{r.text} Pods</span>
        </Tooltip>
      ) : (
        r.text
      )}
      <InstancePills row={row} />
    </>
  )
}

function RowStatusDot({ row }: { row: CNPGFleetRow }) {
  const status = cnpgRowStatus(row)
  return (
    <Tooltip content={status.label}>
      <span role="img" aria-label={status.label} className="inline-flex">
        <StatusDot tone={status.tone} />
      </span>
    </Tooltip>
  )
}

function AttentionCell({ row }: { row: CNPGFleetRow }) {
  const top = row.problems.find((p) => p.severity !== 'posture') ?? row.problems[0]
  if (!top) return <span className="text-theme-text-tertiary">—</span>
  const tone = top.severity === 'critical' ? 'unhealthy' : top.severity === 'warning' ? 'degraded' : 'neutral'
  const more = row.problems.length - 1
  return (
    <div className="min-w-0">
      <Tooltip content={top.title} wrapperClassName="w-full">
        <div className={clsx('line-clamp-2 break-words', toneTextClass(tone))}>{top.title}</div>
      </Tooltip>
      {more > 0 && <div className="text-xs text-theme-text-tertiary">+{more} more</div>}
    </div>
  )
}

export function CNPGOverview({
  data,
  fleet,
  namespaces,
  searchParams,
  onSetParams,
  onInspect,
  inspected,
  onClearNamespaces,
}: CNPGScreenProps) {
  const navigate = useNavigate()
  const { connection } = useConnection()
  const q = searchParams.get('q') ?? ''
  const cat = (searchParams.get('cat') as CNPGProblemCategory | null) ?? null
  const rawFilter = searchParams.get('filter') as Filter | null
  const filter: Filter = rawFilter ?? (fleet.attentionCount > 0 ? 'attention' : 'all')

  const rows = useMemo(() => {
    let list = fleet.rows
    if (filter === 'attention') list = list.filter((r) => r.attention)
    if (cat) list = list.filter((r) => r.categories.has(cat))
    if (q) {
      const needle = q.toLowerCase()
      list = list.filter((r) => r.name.toLowerCase().includes(needle) || r.namespace.toLowerCase().includes(needle))
    }
    return list
  }, [fleet, filter, cat, q])

  const clustersCov = data.coverage.clusters
  const total = fleet.rows.length
  const context = connection.context || data.context

  if (total === 0) {
    const state = clustersCov?.state ?? 'notInstalled'
    const empty =
      state === 'denied'
        ? { title: 'No access to PostgreSQL clusters', detail: 'Your identity cannot list CloudNativePG Clusters. Other CloudNativePG kinds may still be browsable under Resource kinds.' }
        : state === 'syncing'
          ? { title: 'Loading PostgreSQL clusters', detail: 'Radar is still syncing CloudNativePG Clusters from the API server.' }
          : state === 'error'
            ? { title: 'PostgreSQL clusters could not be read', detail: 'Reading CloudNativePG Clusters failed; see the Radar server log.' }
            : state === 'partial'
              ? { title: 'No visible PostgreSQL clusters', detail: 'None in the namespaces you can read. Clusters in namespaces you cannot list are not shown.' }
              : namespaces.length > 0
                ? { title: `No PostgreSQL clusters in ${context}`, detail: `None in namespace ${namespaces.join(', ')}. Clear the namespace filter to see the whole cluster.` }
                : { title: `No PostgreSQL clusters in ${context}`, detail: 'The CloudNativePG CRDs are installed. Clusters, backups and declarations appear here once they exist.' }
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <CNPGWorkspaceHeader title="Overview" />
        <EmptyState
          icon={Database}
          title={empty.title}
          detail={empty.detail}
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
          <CNPGOperatorBanner namespaces={fleet.rows.map((r) => r.namespace)} className="" />

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

          <FilterChips chips={chips} />

          <div className="overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
            <div className={TABLE_WRAP}>
              <table className="w-full min-w-[1140px] table-fixed">
                <colgroup>
                  <col className="w-[14%]" />
                  <col className="w-[9%]" />
                  <col className="w-[8%]" />
                  <col className="w-[13%]" />
                  <col className="w-[12%]" />
                  <col className="w-[8%]" />
                  <col className="w-[9%]" />
                  <col className="w-[5%]" />
                  <col className="w-[13%]" />
                  <col className="w-[9.5rem]" />
                </colgroup>
                <thead className={TABLE_HEAD}>
                  <tr>
                    <th className={TH}>Cluster</th>
                    <th className={TH}>Namespace</th>
                    <th className={TH}>Ready</th>
                    <th className={TH}>Replication</th>
                    <th className={TH}>Protection</th>
                    <th className={TH}>Disk</th>
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
                            <RowStatusDot row={row} />
                            <Tooltip content={row.name} wrapperClassName="min-w-0"><span className="block truncate font-medium">{row.name}</span></Tooltip>
                          </div>
                        </td>
                        <td className={clsx(TD, 'text-theme-text-secondary')}>
                          <Tooltip content={row.namespace} wrapperClassName="min-w-0"><span className="block truncate">{row.namespace}</span></Tooltip>
                        </td>
                        <td className={clsx(TD, 'font-mono')}>
                          <ReadyCell row={row} />
                        </td>
                        <td className={TD}><FactValue fact={row.replication} /></td>
                        <td className={TD}><FactValue fact={row.protection.summary} /></td>
                        <td className={TD}>
                          <FactValue fact={row.disk ?? { text: 'Reading…', tone: 'unknown' }} />
                          {row.diskGrowth && <div className="text-xs"><FactValue fact={row.diskGrowth} className="text-theme-text-tertiary" /></div>}
                        </td>
                        <td className={TD}><FactValue fact={row.declarations.summary} /></td>
                        <td className={clsx(TD, 'font-mono')}>{row.pgVersion ?? '—'}</td>
                        <td className={clsx(TD, 'overflow-hidden')}><AttentionCell row={row} /></td>
                        <td className={clsx(TD, 'text-right')}>
                          <div className="flex items-center justify-end gap-1">
                            <button
                              type="button"
                              aria-label={`Logs from every instance of ${row.name}`}
                              onClick={(e) => {
                                e.stopPropagation()
                                const podProblem = row.problems.find((p) => p.subject.kind === 'Pod')
                                const path = cnpgClusterFullPath(row.namespace, row.name, connection.context || undefined, 'logs')
                                navigate(podProblem ? `${path}&pod=${encodeURIComponent(podProblem.subject.name)}` : path, {
                                  state: { returnLabel: currentPageLabel(), returnCtx: connection.context },
                                })
                              }}
                              className="inline-flex items-center gap-1 whitespace-nowrap rounded-md px-2 py-1 text-xs font-medium text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
                            >
                              <FileText className="h-3 w-3" /> Logs
                            </button>
                            <button
                              type="button"
                              aria-label={`Open ${row.name}`}
                              onClick={(e) => {
                                e.stopPropagation()
                                navigate(cnpgClusterFullPath(row.namespace, row.name, connection.context || undefined), {
                                  state: { returnLabel: currentPageLabel(), returnCtx: connection.context },
                                })
                              }}
                              className="inline-flex items-center gap-1 whitespace-nowrap rounded-md px-2 py-1 text-xs font-medium text-accent-text hover:bg-theme-hover"
                            >
                              Open <ArrowRight className="h-3 w-3" />
                            </button>
                          </div>
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
