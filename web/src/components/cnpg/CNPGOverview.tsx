import { useMemo } from 'react'
import { clsx } from 'clsx'
import { ArrowRight, Database, FileText, Plus, Search } from 'lucide-react'
import {
  CNPG_PROBLEM_CATEGORIES,
  PROBLEM_TONE,
  cnpgDimensions,
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
import { CNPGWorkspaceHeader, CoverageNotice, coverageEmpty, type CNPGScreenProps } from './shared'
import { cnpgClusterFullPath, cnpgClusterProblemsPath } from './paths'
import { currentPageLabel } from '../../utils/page-links'
import { CNPGOperatorBanner } from './CNPGOperatorBanner'
import { cnpgInstancePillLabel, cnpgPillsToShow, cnpgRowStatus } from './fleetStatus'
import { BreakText, FilterChips, ScreenEmptyState } from '../workspace/layout'
import { ROW_HOVER, TABLE_HEAD, TABLE_WRAP, TBODY, TD, TH } from '../workspace/table'
import { sameSelectedResource } from '../../utils/drawer-trail'
import { useCNPGNavigate } from './useCNPGNavigate'

type Filter = 'attention' | 'all'

// Headers may wrap: at a 1280px window the columns are narrower than their labels.
const TH_WRAP = TH.replace('whitespace-nowrap', '')

// Five pills fit the Ready column; the rest are counted, listed on hover.
const MAX_PILLS = 5

function InstancePills({ row }: { row: CNPGFleetRow }) {
  if (row.pods.length === 0) return null
  const { shown, hidden } = cnpgPillsToShow(row.pods, MAX_PILLS)
  return (
    <div className="mt-1 flex flex-nowrap items-center gap-0.5 font-sans">
      {shown.map((p) => {
        const tone = p.ready === true ? 'healthy' : p.ready === false ? 'unhealthy' : 'unknown'
        return (
          <Tooltip key={p.name} content={cnpgInstancePillLabel(p)}>
            <span
              aria-label={cnpgInstancePillLabel(p)}
              className="inline-flex items-center gap-0.5 rounded border border-theme-border bg-theme-base px-0.5 font-mono text-[10.5px] text-theme-text-secondary"
            >
              <StatusDot tone={tone} size="xs" />
              {p.role === 'primary' ? 'P' : p.role === 'replica' ? 'R' : '?'}
            </span>
          </Tooltip>
        )
      })}
      {hidden.length > 0 && (
        <Tooltip
          content={
            <ul className="space-y-0.5">
              {hidden.map((p) => (
                <li key={p.name}>{cnpgInstancePillLabel(p)}</li>
              ))}
            </ul>
          }
        >
          <span aria-label={`${hidden.length} more instances`} className="px-0.5 text-[10.5px] text-theme-text-tertiary">
            +{hidden.length}
          </span>
        </Tooltip>
      )}
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

// The Storage cell is the same assessment as the Cluster page's Storage chip:
// measured usage, or the WAL an inactive slot holds, or why neither is known.
function storageFact(row: CNPGFleetRow) {
  const d = cnpgDimensions({ row }).find((x) => x.id === 'storage')!
  return { text: d.text, tone: d.tone, source: d.source }
}

/** The one reason no cluster's volume usage is measured, when that is so; the column then says it once. */
function cnpgStorageUnmeasured(rows: CNPGFleetRow[]): string | null {
  if (rows.length === 0 || rows.some((r) => !r.disk || r.disk.tone !== 'unknown')) return null
  const reasons = new Set(rows.map((r) => (r.disk?.source ?? r.disk?.text ?? '').replace("this cluster's claims", 'their claims')))
  return reasons.size === 1 ? [...reasons][0] || 'no measurement' : 'no measurement'
}

function StorageCell({ row, quiet }: { row: CNPGFleetRow; quiet: boolean }) {
  const fact = storageFact(row)
  if (quiet && fact.tone === 'unknown') {
    return (
      <Tooltip content={fact.source ?? 'Volume usage not measured'}>
        <span className="text-theme-text-tertiary">—</span>
      </Tooltip>
    )
  }
  return (
    <>
      <FactValue fact={fact} />
      {row.diskGrowth && <div className="text-xs"><FactValue fact={row.diskGrowth} className="text-theme-text-tertiary" /></div>}
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

// A token this long cannot wrap at a word boundary within the cell.
const UNBREAKABLE_TOKEN = 24

// A measured problem names what measured it, so a qualified match stays qualified in the fleet.
function problemTip(p: CNPGFleetRow['problems'][number]): string {
  return p.source === 'measurement' && p.measuredBy ? `${p.title} (measured by ${p.measuredBy})` : p.title
}

function AttentionCell({ row, onOpenAll }: { row: CNPGFleetRow; onOpenAll: () => void }) {
  const top = row.problems.find((p) => p.severity !== 'posture') ?? row.problems[0]
  if (!top) return <span className="text-theme-text-tertiary">—</span>
  const others = row.problems.filter((p) => p !== top)
  const headline = top.shortTitle ?? top.title
  const unbreakable = headline.split(/\s+/).some((w) => w.length > UNBREAKABLE_TOKEN)
  return (
    <div className="min-w-0">
      <Tooltip content={problemTip(top)} wrapperClassName="w-full">
        <div className={clsx('[overflow-wrap:normal]', unbreakable ? 'truncate' : 'line-clamp-3', toneTextClass(PROBLEM_TONE[top.severity]))}>{headline}</div>
      </Tooltip>
      {top.unverifiedMatch && (
        <div className="text-[11px] text-theme-text-tertiary">measured by {top.measuredBy}</div>
      )}
      {others.length > 0 && (
        <Tooltip
          content={
            <ul className="space-y-1">
              {others.map((p) => (
                <li key={p.id} className="flex items-start gap-1.5">
                  <span className="mt-1 shrink-0">
                    <StatusDot tone={PROBLEM_TONE[p.severity]} size="xs" />
                  </span>
                  <span>{problemTip(p)}</span>
                </li>
              ))}
            </ul>
          }
        >
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation()
              onOpenAll()
            }}
            aria-label={`${others.length} more problems: open ${row.name} with every problem listed`}
            className="rounded text-xs text-accent-text hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
          >
            +{others.length} more
          </button>
        </Tooltip>
      )}
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
  onCreate,
}: CNPGScreenProps) {
  const navigate = useCNPGNavigate()
  const { connection } = useConnection()
  const q = searchParams.get('q') ?? ''
  const cat = (searchParams.get('cat') as CNPGProblemCategory | null) ?? null
  const rawFilter = searchParams.get('filter') as Filter | null
  // The view is the list of clusters: all of them unless asked for less.
  const filter: Filter = rawFilter === 'attention' ? 'attention' : 'all'

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
  // Some namespaces were not read: the count is a floor, not the cluster's total.
  const totalText = `${clustersCov?.state === 'partial' ? '≥' : ''}${total}`
  const createAction = onCreate ? (
    <button type="button" onClick={onCreate} className="btn-secondary inline-flex items-center gap-1.5 px-3 py-1.5 text-sm">
      <Plus className="h-3.5 w-3.5" />
      Create
    </button>
  ) : undefined

  if (total === 0) {
    const state = clustersCov?.state ?? 'notInstalled'
    const empty =
      state === 'denied'
        ? { title: 'No access to PostgreSQL clusters', detail: 'Your identity cannot list CloudNativePG Clusters. Other CloudNativePG kinds may still be browsable under Resource kinds.' }
        : state === 'syncing'
          ? { title: 'Loading PostgreSQL clusters', detail: 'Radar is still syncing CloudNativePG Clusters from the API server.' }
          : state === 'error'
            ? { title: 'PostgreSQL clusters could not be read', detail: 'Reading CloudNativePG Clusters failed; see the Radar server log.' }
            : state === 'partial' || state === 'uncached'
              ? { title: 'No visible PostgreSQL clusters', detail: coverageEmpty(clustersCov, 'PostgreSQL clusters') }
              : namespaces.length > 0
                ? { title: `No PostgreSQL clusters in ${context}`, detail: `None in namespace ${namespaces.join(', ')}. Clear the namespace filter to see the whole cluster.` }
                : { title: `No PostgreSQL clusters in ${context}`, detail: 'The CloudNativePG CRDs are installed. Clusters, backups and declarations appear here once they exist.' }
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <CNPGWorkspaceHeader title="Clusters" actions={createAction} />
        <ScreenEmptyState
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

  const segment = (id: Filter, label: string, n: number | string) => {
    const on = filter === id
    return (
      <button
        key={id}
        type="button"
        role="tab"
        aria-selected={on}
        onClick={() => onSetParams({ filter: id === 'all' ? null : id })}
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
  const storageUnmeasured = cnpgStorageUnmeasured(fleet.rows)

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <CNPGWorkspaceHeader
        title="Clusters"
        subtitle={
          <>
            {totalText} PostgreSQL {total === 1 ? 'cluster' : 'clusters'} · {fleet.attentionCount}
            {lowerBound ? '+' : ''} need attention
            <span className="text-theme-text-tertiary"> · {context}</span>
          </>
        }
        actions={createAction}
      />
      <div className="min-h-0 flex-1 overflow-y-auto [scrollbar-gutter:stable]">
        <div className="space-y-3 px-5 pb-6 pt-3 xl:px-7">
          <CoverageNotice fleet={fleet} data={data} />
          <CNPGOperatorBanner namespaces={fleet.rows.map((r) => r.namespace)} className="" />

          <div className="flex flex-wrap items-center gap-2">
            <div role="tablist" aria-label="Clusters" className="inline-flex rounded-lg bg-theme-elevated p-0.5">
              {segment('all', 'All clusters', totalText)}
              {segment('attention', 'Needs attention', fleet.attentionCount)}
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
          {storageUnmeasured && (
            <div className="text-xs text-theme-text-tertiary">
              Storage: volume usage is not measured for any cluster here ({storageUnmeasured}). Retained WAL and resize state are on each cluster’s Storage tab.
            </div>
          )}

          <div className="overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
            <div className={TABLE_WRAP}>
              {/* Fits an ~850px content area (a 1280px window) without scrolling: the
                  two fixed columns take 16rem and the percentages stay under the rest. */}
              <table className="w-full min-w-[820px] table-fixed">
                <colgroup>
                  <col className="w-[22%]" />
                  <col className="w-[18%]" />
                  <col className="w-[13%]" />
                  <col className="w-[15%]" />
                  <col />
                  <col className="w-[7.5rem]" />
                </colgroup>
                <thead className={TABLE_HEAD}>
                  <tr>
                    <th className={TH_WRAP}>Cluster</th>
                    <th className={TH_WRAP}>Replication</th>
                    <th className={TH_WRAP}>Storage</th>
                    <th className={TH_WRAP}>Backups</th>
                    <th className={TH_WRAP}>Needs attention</th>
                    <th className={TH_WRAP}><span className="sr-only">Actions</span></th>
                  </tr>
                </thead>
                <tbody className={TBODY}>
                  {rows.map((row) => {
                    const ref: SelectedResource = { kind: 'clusters', group: 'postgresql.cnpg.io', namespace: row.namespace, name: row.name }
                    const active = sameSelectedResource(inspected, ref)
                    return (
                      <tr
                        key={row.key}
                        onClick={() => onInspect(ref)}
                        className={clsx('cursor-pointer', ROW_HOVER, active && 'selection')}
                        aria-selected={active}
                      >
                        <td className={TD}>
                          <div className="flex min-w-0 items-start gap-2">
                            <span className="mt-1.5 shrink-0"><RowStatusDot row={row} /></span>
                            <div className="min-w-0">
                              <BreakText value={row.name} after="-" className="font-medium" />
                              <div className="flex min-w-0 items-baseline text-xs text-theme-text-tertiary">
                                <Tooltip content={`Namespace ${row.namespace}`} wrapperClassName="min-w-0">
                                  <span className="block truncate">{row.namespace}</span>
                                </Tooltip>
                                {row.pgVersion && (
                                  <span className="shrink-0 whitespace-pre">
                                    {' · '}
                                    <Tooltip content="PostgreSQL version"><span>PG {row.pgVersion}</span></Tooltip>
                                  </span>
                                )}
                              </div>
                              <div className="mt-0.5 font-mono text-xs">
                                <ReadyCell row={row} />
                              </div>
                            </div>
                          </div>
                        </td>
                        <td className={TD}><FactValue fact={row.replication} /></td>
                        <td className={TD}><StorageCell row={row} quiet={!!storageUnmeasured} /></td>
                        <td className={TD}><FactValue fact={row.protection.summary} /></td>
                        <td className={clsx(TD, 'overflow-hidden')}>
                          <AttentionCell
                            row={row}
                            onOpenAll={() =>
                              navigate(cnpgClusterProblemsPath(row.namespace, row.name, connection.context || undefined), {
                                state: { returnLabel: currentPageLabel(), returnCtx: connection.context },
                              })
                            }
                          />
                        </td>
                        <td className={clsx(TD, 'text-right')}>
                          <div className="flex items-center justify-end gap-0.5">
                            <Tooltip content="Logs from every instance">
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
                              className="inline-flex items-center rounded-md p-1.5 text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
                            >
                              <FileText className="h-3.5 w-3.5" />
                            </button>
                            </Tooltip>
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
