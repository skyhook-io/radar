import { useCallback, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, Ban, ArrowDown, ArrowUp, ArrowUpDown, ListChecks, RefreshCw, Search, Trash2, Workflow } from 'lucide-react'
import { clsx } from 'clsx'
import yamlLib from 'yaml'
import {
  SummaryTile,
  Facet,
  Input,
  PageHeader,
  FreshnessControl,
  RowActionMenu,
  ConfirmDialog,
  Tooltip,
  buildPipelineTaskGraph,
  getTektonPipelineRunStatus,
  tektonRefName,
  formatAge,
  formatDuration,
  type SummaryTone,
  type RowActionItem,
} from '@skyhook-io/k8s-ui'
import { fetchJSON, isForbiddenError, useBulkDeleteResources, useDeleteResource, useUpdateResource } from '../../api/client'

interface CicdViewProps {
  namespaces: string[]
  onOpenPipelineRun: (ref: { namespace: string; name: string }) => void
  onOpenPipeline: (ref: { namespace: string; name: string }) => void
}

// Resolves a PipelineRun's pipelineRef to a real, navigable Pipeline object —
// null when there's nothing to navigate to. A direct name ref lives in the
// PipelineRun's own namespace; a cluster resolver ref carries its own
// namespace param (platform-cicd's catalog pipelines live in a shared
// namespace, not the run's). Other resolvers (git/http/bundle) fetch the
// spec from outside the cluster — there's no live Pipeline object for them.
function resolvePipelineTarget(run: any): { namespace: string; name: string } | null {
  const ref = run?.spec?.pipelineRef
  if (!ref) return null
  if (ref.name) return { namespace: run?.metadata?.namespace ?? '', name: ref.name }
  if (ref.resolver === 'cluster') {
    const params = ref.params ?? []
    const name = params.find((p: any) => p?.name === 'name')?.value
    const namespace = params.find((p: any) => p?.name === 'namespace')?.value
    if (name && namespace) return { namespace, name }
  }
  return null
}

type StatusFacet = 'running' | 'succeeded' | 'failed' | 'cancelled' | 'pending'

// Same bucketing as getTektonPipelineRunStatus's `level`, but named for the
// filter UI rather than the badge — cancelled reads as `degraded` there,
// which is correct as a health signal but a confusing filter label.
function statusFacet(run: any): StatusFacet {
  const cond = (run?.status?.conditions ?? []).find((c: any) => c?.type === 'Succeeded')
  if (!cond) return 'pending'
  if (cond.status === 'True') return 'succeeded'
  if (cond.status === 'False') {
    const reason = cond.reason || ''
    return reason === 'Cancelled' || reason.endsWith('Cancelled') ? 'cancelled' : 'failed'
  }
  return 'running'
}

const STATUS_LABEL: Record<StatusFacet, string> = {
  running: 'Running',
  succeeded: 'Succeeded',
  failed: 'Failed',
  cancelled: 'Cancelled',
  pending: 'Pending',
}
const STATUS_TONE: Record<StatusFacet, 'success' | 'error' | 'info' | 'warning' | 'neutral'> = {
  running: 'info',
  succeeded: 'success',
  failed: 'error',
  cancelled: 'warning',
  pending: 'neutral',
}
// Sort rank for the Status column — most-actionable states first (running,
// failed) rather than alphabetical, so ascending reads as "needs attention"
// first.
const STATUS_SORT_RANK: Record<StatusFacet, number> = {
  running: 0,
  failed: 1,
  pending: 2,
  cancelled: 3,
  succeeded: 4,
}

function runStartTime(run: any): number {
  const t = run?.status?.startTime
  return t ? new Date(t).getTime() : 0
}

function runDurationMs(run: any): number | null {
  const start = run?.status?.startTime
  if (!start) return null
  const end = run?.status?.completionTime
  return (end ? new Date(end).getTime() : Date.now()) - new Date(start).getTime()
}

// Fraction of declared tasks that have at least started (status.childReferences
// only tells us "a TaskRun was created for this task," not its outcome — an
// honest "how far did it get" proxy, not "how much succeeded").
function taskProgress(run: any): { started: number; total: number } {
  // buildPipelineTaskGraph, not a bare pipelineSpec.tasks.length, so finally
  // tasks count toward the total — Tekton runs them and includes their
  // outcome in the PipelineRun result same as any regular task.
  const total = buildPipelineTaskGraph(run?.status?.pipelineSpec ?? {}).length
  const started = (run?.status?.childReferences ?? []).length
  return { started, total }
}

// A `matrix`-strategy task expands into several childReferences from one
// declared pipeline task, so `started` can legitimately exceed `total` —
// display/sort against whichever is larger so the fraction never reads
// backwards (e.g. "12/7"), without pretending the declared count was
// actually 12.
function progressFraction(run: any, facet: StatusFacet): number {
  const { started, total } = taskProgress(run)
  const displayTotal = Math.max(total, started)
  return displayTotal > 0 ? Math.min(1, started / displayTotal) : facet === 'succeeded' ? 1 : 0
}

function ProgressBar({ run, facet }: { run: any; facet: StatusFacet }) {
  const { started, total } = taskProgress(run)
  const displayTotal = Math.max(total, started)
  const fraction = progressFraction(run, facet)
  const fillClass = {
    running: 'bg-sky-500',
    succeeded: 'bg-emerald-500',
    failed: 'bg-red-500',
    cancelled: 'bg-amber-500',
    pending: 'bg-theme-text-tertiary',
  }[facet]
  return (
    <div className="flex items-center gap-2">
      <div className="h-1.5 w-full min-w-[80px] overflow-hidden rounded-full bg-theme-hover">
        <div
          className={clsx('h-full rounded-full transition-all', fillClass, facet === 'running' && 'animate-pulse')}
          style={{ width: `${Math.max(fraction * 100, displayTotal > 0 ? 4 : 0)}%` }}
        />
      </div>
      {displayTotal > 0 && (
        <span
          className="shrink-0 text-[11px] tabular-nums text-theme-text-tertiary"
          title={
            started > total
              ? `${total} declared task${total === 1 ? '' : 's'} expanded into ${started} runs (matrix strategy) — tasks started, not necessarily succeeded`
              : 'Tasks started, not necessarily succeeded'
          }
        >
          {started}/{displayTotal}
        </span>
      )}
    </div>
  )
}

type SortKey = 'name' | 'status' | 'pipeline' | 'progress' | 'duration' | 'started'
type SortDir = 'asc' | 'desc'

const SORT_COLUMNS: { key: SortKey; label: string; className: string }[] = [
  { key: 'name', label: 'Name', className: 'w-64 min-w-0 shrink-0' },
  { key: 'status', label: 'Status', className: 'w-40 shrink-0' },
  { key: 'pipeline', label: 'Pipeline', className: 'w-40 shrink-0' },
  { key: 'progress', label: 'Progress', className: 'min-w-[140px] flex-1' },
  { key: 'duration', label: 'Duration', className: 'w-28 shrink-0 text-right' },
  { key: 'started', label: 'Started', className: 'w-24 shrink-0 text-right' },
]

function sortValue(run: any, key: SortKey): number | string {
  const facet = statusFacet(run)
  switch (key) {
    case 'name':
      return (run?.metadata?.name ?? '').toLowerCase()
    case 'status':
      return STATUS_SORT_RANK[facet]
    case 'pipeline':
      return tektonRefName(run?.spec?.pipelineRef).toLowerCase()
    case 'progress':
      return progressFraction(run, facet)
    case 'duration':
      // Still-running/unstarted rows sort as smallest — reads as "shortest so
      // far" ascending, "longest-running" descending, without a separate
      // null bucket.
      return runDurationMs(run) ?? -1
    case 'started':
      return runStartTime(run)
  }
}

function SortHeaderCell({
  column,
  sort,
  onSort,
}: {
  column: (typeof SORT_COLUMNS)[number]
  sort: { key: SortKey; dir: SortDir }
  onSort: (key: SortKey) => void
}) {
  const active = sort.key === column.key
  const Icon = active ? (sort.dir === 'asc' ? ArrowUp : ArrowDown) : ArrowUpDown
  return (
    <button
      type="button"
      onClick={() => onSort(column.key)}
      className={clsx(
        column.className,
        'group flex items-center gap-1 text-xs font-medium uppercase tracking-wide',
        column.key === 'duration' || column.key === 'started' ? 'justify-end' : '',
        active ? 'text-theme-text-primary' : 'text-theme-text-tertiary hover:text-theme-text-secondary',
      )}
    >
      {column.label}
      <Icon className={clsx('h-3 w-3', !active && 'opacity-0 group-hover:opacity-100')} />
    </button>
  )
}

export function CicdView({ namespaces, onOpenPipelineRun, onOpenPipeline }: CicdViewProps) {
  const namespacesParam = namespaces.join(',')
  const [search, setSearch] = useState('')
  const [statusFilter, setStatusFilter] = useState<Set<StatusFacet>>(new Set())
  const [pipelineFilter, setPipelineFilter] = useState<Set<string>>(new Set())
  const [sort, setSort] = useState<{ key: SortKey; dir: SortDir }>({ key: 'started', dir: 'desc' })
  const handleSort = (key: SortKey) =>
    setSort((prev) =>
      prev.key === key ? { key, dir: prev.dir === 'asc' ? 'desc' : 'asc' } : { key, dir: key === 'name' || key === 'pipeline' ? 'asc' : 'desc' },
    )
  const [deleteTarget, setDeleteTarget] = useState<{ namespace: string; name: string } | null>(null)
  const cancelRun = useUpdateResource()
  const deleteRun = useDeleteResource()

  const [bulkMode, setBulkMode] = useState(false)
  const [checkedKeys, setCheckedKeys] = useState<Set<string>>(new Set())
  const [showBulkDeleteConfirm, setShowBulkDeleteConfirm] = useState(false)
  const bulkDeleteRuns = useBulkDeleteResources()
  const exitBulkMode = useCallback(() => {
    setBulkMode(false)
    setCheckedKeys(new Set())
  }, [])
  const toggleChecked = useCallback((key: string) => {
    setCheckedKeys((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }, [])

  const runsQuery = useQuery({
    queryKey: ['cicd-pipelineruns', namespacesParam],
    queryFn: async () => {
      const params = new URLSearchParams({ group: 'tekton.dev' })
      if (namespacesParam) params.set('namespaces', namespacesParam)
      return fetchJSON<any[]>(`/resources/pipelineruns?${params}`)
    },
    staleTime: 10_000,
    refetchInterval: 15_000,
  })

  const tasksRunningQuery = useQuery({
    queryKey: ['cicd-taskruns-running', namespacesParam],
    queryFn: async () => {
      const params = new URLSearchParams({ group: 'tekton.dev' })
      if (namespacesParam) params.set('namespaces', namespacesParam)
      const all = await fetchJSON<any[]>(`/resources/taskruns?${params}`)
      return all.filter((t) => {
        const cond = (t?.status?.conditions ?? []).find((c: any) => c?.type === 'Succeeded')
        return cond?.status === 'Unknown'
      }).length
    },
    staleTime: 10_000,
    refetchInterval: 15_000,
  })

  const runs = runsQuery.data ?? []

  const stats = useMemo(() => {
    const counts: Record<StatusFacet, number> = { running: 0, succeeded: 0, failed: 0, cancelled: 0, pending: 0 }
    for (const r of runs) counts[statusFacet(r)]++
    const finished = counts.succeeded + counts.failed
    const successRate = finished > 0 ? Math.round((counts.succeeded / finished) * 100) : null
    return { counts, total: runs.length, successRate }
  }, [runs])

  const pipelineCounts = useMemo(() => {
    const m = new Map<string, number>()
    for (const r of runs) {
      const name = tektonRefName(r?.spec?.pipelineRef)
      m.set(name, (m.get(name) ?? 0) + 1)
    }
    return m
  }, [runs])

  const filteredRuns = useMemo(() => {
    const q = search.trim().toLowerCase()
    const rows = runs.filter((r) => {
      if (statusFilter.size > 0 && !statusFilter.has(statusFacet(r))) return false
      const pipelineName = tektonRefName(r?.spec?.pipelineRef)
      if (pipelineFilter.size > 0 && !pipelineFilter.has(pipelineName)) return false
      if (q) {
        const haystack = `${r?.metadata?.name ?? ''} ${r?.metadata?.namespace ?? ''} ${pipelineName}`.toLowerCase()
        if (!haystack.includes(q)) return false
      }
      return true
    })
    const dirMul = sort.dir === 'asc' ? 1 : -1
    return [...rows].sort((a, b) => {
      const av = sortValue(a, sort.key)
      const bv = sortValue(b, sort.key)
      if (av < bv) return -1 * dirMul
      if (av > bv) return 1 * dirMul
      return 0
    })
  }, [runs, search, statusFilter, pipelineFilter, sort])

  const allVisibleChecked = filteredRuns.length > 0 && filteredRuns.every((r) => checkedKeys.has(`${r.metadata?.namespace ?? ''}/${r.metadata?.name ?? ''}`))
  const toggleCheckAll = useCallback(() => {
    setCheckedKeys(
      allVisibleChecked
        ? new Set()
        : new Set(filteredRuns.map((r) => `${r.metadata?.namespace ?? ''}/${r.metadata?.name ?? ''}`)),
    )
  }, [allVisibleChecked, filteredRuns])
  const checkedRuns = useMemo(
    () => filteredRuns.filter((r) => checkedKeys.has(`${r.metadata?.namespace ?? ''}/${r.metadata?.name ?? ''}`)),
    [filteredRuns, checkedKeys],
  )

  const toggleStatus = (v: StatusFacet) => setStatusFilter((prev) => {
    const next = new Set(prev)
    if (next.has(v)) next.delete(v)
    else next.add(v)
    return next
  })
  const togglePipeline = (v: string) => setPipelineFilter((prev) => {
    const next = new Set(prev)
    if (next.has(v)) next.delete(v)
    else next.add(v)
    return next
  })
  const hasFacetFilters = statusFilter.size > 0 || pipelineFilter.size > 0
  const clearFacetFilters = () => {
    setStatusFilter(new Set())
    setPipelineFilter(new Set())
  }

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col bg-theme-base">
      <div className="shrink-0 border-b border-theme-border bg-theme-base px-4 py-4">
        <PageHeader
          icon={Workflow}
          title="CI/CD"
          description="Tekton PipelineRuns and TaskRuns across your pipelines, with live per-task progress."
          actions={
            <>
              <FreshnessControl
                mode="auto"
                dataUpdatedAt={runsQuery.dataUpdatedAt}
                isFetching={runsQuery.isFetching}
                onRefresh={() => runsQuery.refetch()}
              />
              <SummaryTile label="Total Runs" value={stats.total} loading={runsQuery.isLoading} />
              <SummaryTile
                label="Running"
                value={stats.counts.running}
                tone="info"
                loading={runsQuery.isLoading}
                active={statusFilter.has('running')}
                onClick={() => toggleStatus('running')}
              />
              <SummaryTile
                label="Succeeded"
                value={stats.counts.succeeded}
                tone="success"
                loading={runsQuery.isLoading}
                active={statusFilter.has('succeeded')}
                onClick={() => toggleStatus('succeeded')}
              />
              <SummaryTile
                label="Failed"
                value={stats.counts.failed}
                tone="error"
                loading={runsQuery.isLoading}
                active={statusFilter.has('failed')}
                onClick={() => toggleStatus('failed')}
              />
              <SummaryTile
                label="Success Rate"
                value={stats.successRate ?? 0}
                tone={(stats.successRate == null ? 'neutral' : stats.successRate >= 90 ? 'success' : stats.successRate >= 70 ? 'warning' : 'error') as SummaryTone}
                loading={runsQuery.isLoading}
              />
              <SummaryTile label="Tasks Running" value={tasksRunningQuery.data ?? 0} tone="info" loading={tasksRunningQuery.isLoading || tasksRunningQuery.isError} />
            </>
          }
        />
      </div>

      <div className="flex min-h-0 min-w-0 flex-1">
        <aside className="flex w-56 shrink-0 flex-col overflow-hidden border-r border-theme-border bg-theme-surface/90">
          <div className="flex items-center justify-between border-b border-theme-border px-3 py-2">
            <span className="text-sm font-medium text-theme-text-secondary">Filters</span>
            <button
              type="button"
              onClick={clearFacetFilters}
              disabled={!hasFacetFilters}
              className="text-[10px] font-medium text-blue-500 hover:text-blue-400 disabled:opacity-40 disabled:hover:text-blue-500"
            >
              Clear
            </button>
          </div>
          <div className="flex-1 overflow-y-auto">
            <Facet
              title="Status"
              options={(Object.keys(STATUS_LABEL) as StatusFacet[]).map((v) => ({
                value: v,
                label: STATUS_LABEL[v],
                count: stats.counts[v],
                tone: STATUS_TONE[v],
              }))}
              selected={statusFilter}
              onToggle={toggleStatus}
            />
            <Facet
              title="Pipeline"
              options={[...pipelineCounts.entries()].map(([name, count]) => ({ value: name, label: name, count }))}
              selected={pipelineFilter}
              onToggle={togglePipeline}
            />
          </div>
        </aside>

        <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
          <div className="flex shrink-0 items-center gap-2 border-b border-theme-border px-4 py-2">
            <div className="relative max-w-md flex-1">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-theme-text-tertiary" />
              <Input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search PipelineRuns, namespaces, pipelines..."
                className="h-8 w-full rounded-md border border-theme-border bg-theme-base pl-8 pr-3 text-sm text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:ring-1 focus:ring-blue-500/50"
              />
            </div>
            <Tooltip content={bulkMode ? 'Exit bulk select mode' : 'Select multiple resources'}>
              <button
                type="button"
                onClick={() => (bulkMode ? exitBulkMode() : setBulkMode(true))}
                aria-pressed={bulkMode}
                className={clsx(
                  'rounded-lg p-2 transition-colors',
                  bulkMode
                    ? 'border border-skyhook-400/50 bg-skyhook-500/15 text-skyhook-300'
                    : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary',
                )}
              >
                <ListChecks className="h-4 w-4" />
              </button>
            </Tooltip>
          </div>

          {bulkMode && (
            <div className="flex shrink-0 items-center gap-3 border-b border-skyhook-400/20 bg-skyhook-500/10 px-4 py-2">
              <span className="text-sm font-medium text-theme-text-primary">{checkedRuns.length} selected</span>
              <button
                type="button"
                onClick={() => setShowBulkDeleteConfirm(true)}
                disabled={checkedRuns.length === 0 || bulkDeleteRuns.isPending}
                className="flex items-center gap-1.5 rounded-lg bg-red-600 px-3 py-1.5 text-xs font-medium text-white transition-colors hover:bg-red-700 disabled:pointer-events-none disabled:opacity-50"
              >
                <Trash2 className="h-3.5 w-3.5" />
                {bulkDeleteRuns.isPending ? 'Deleting…' : 'Delete'}
              </button>
              <button
                type="button"
                onClick={exitBulkMode}
                className="rounded-lg px-3 py-1.5 text-xs text-theme-text-secondary transition-colors hover:bg-theme-elevated hover:text-theme-text-primary"
              >
                Cancel
              </button>
            </div>
          )}

          {filteredRuns.length > 0 && (
            <div className="flex shrink-0 items-center gap-4 border-b border-theme-border bg-theme-surface/40 px-4 py-2">
              {bulkMode && (
                <span className="flex w-5 shrink-0 items-center justify-center">
                  <input
                    type="checkbox"
                    checked={allVisibleChecked}
                    ref={(el) => { if (el) el.indeterminate = checkedRuns.length > 0 && !allVisibleChecked }}
                    onChange={toggleCheckAll}
                    className="h-3.5 w-3.5 cursor-pointer rounded border-theme-border accent-skyhook-500"
                    title={allVisibleChecked ? 'Deselect all' : 'Select all'}
                  />
                </span>
              )}
              {SORT_COLUMNS.map((column) => (
                <SortHeaderCell key={column.key} column={column} sort={sort} onSort={handleSort} />
              ))}
              <span className="w-8 shrink-0" aria-hidden />
            </div>
          )}

          <div className="min-h-0 flex-1 overflow-y-auto">
            {runsQuery.isLoading ? (
              <div className="p-4 text-sm text-theme-text-tertiary">Loading PipelineRuns…</div>
            ) : runsQuery.isError ? (
              <div className="flex h-full flex-col items-center justify-center gap-2 p-4 text-center text-sm">
                <AlertTriangle className="h-5 w-5 text-red-500" />
                <span className="text-theme-text-secondary">
                  {isForbiddenError(runsQuery.error)
                    ? "You don't have permission to list PipelineRuns."
                    : `Failed to load PipelineRuns: ${runsQuery.error instanceof Error ? runsQuery.error.message : 'unknown error'}`}
                </span>
                <button
                  type="button"
                  onClick={() => runsQuery.refetch()}
                  className="mt-1 inline-flex items-center gap-1.5 rounded border border-theme-border bg-theme-base px-2.5 py-1 text-xs text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
                >
                  <RefreshCw className="h-3 w-3" />
                  Retry
                </button>
              </div>
            ) : filteredRuns.length === 0 ? (
              <div className="flex h-full items-center justify-center text-sm text-theme-text-tertiary">
                {runs.length === 0 ? 'No PipelineRuns found.' : 'No PipelineRuns match the current filters.'}
              </div>
            ) : (
              <div className="divide-y divide-theme-border">
                {filteredRuns.map((run) => {
                  const facet = statusFacet(run)
                  const status = getTektonPipelineRunStatus(run)
                  const durationMs = runDurationMs(run)
                  const name = run.metadata?.name ?? ''
                  const namespace = run.metadata?.namespace ?? ''
                  const rowKey = `${namespace}/${name}`
                  const checked = checkedKeys.has(rowKey)
                  const pipelineTarget = resolvePipelineTarget(run)
                  const pipelineLabel = tektonRefName(run?.spec?.pipelineRef)
                  const actionItems: RowActionItem[] = [
                    {
                      key: 'cancel',
                      label: 'Cancel run',
                      icon: Ban,
                      disabled: facet !== 'running',
                      disabledReason: facet !== 'running' ? 'Only running PipelineRuns can be cancelled' : undefined,
                      pending: cancelRun.isPending && cancelRun.variables?.name === name,
                      onClick: () => {
                        const patched = { ...run, spec: { ...run.spec, status: 'Cancelled' } }
                        cancelRun.mutate(
                          { kind: 'pipelineruns', namespace, name, yaml: yamlLib.stringify(patched) },
                          { onSuccess: () => runsQuery.refetch() },
                        )
                      },
                    },
                    {
                      key: 'delete',
                      label: 'Delete PipelineRun',
                      icon: Trash2,
                      danger: true,
                      divider: true,
                      onClick: () => setDeleteTarget({ namespace, name }),
                    },
                  ]
                  return (
                    // A real <button> can't host the nested RowActionMenu button
                    // (invalid nested-interactive HTML), so the row is a
                    // keyboard-accessible div instead.
                    <div
                      key={rowKey}
                      role="button"
                      tabIndex={0}
                      onClick={() => (bulkMode ? toggleChecked(rowKey) : onOpenPipelineRun({ namespace, name }))}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter' || e.key === ' ') {
                          e.preventDefault()
                          if (bulkMode) toggleChecked(rowKey)
                          else onOpenPipelineRun({ namespace, name })
                        }
                      }}
                      className={clsx(
                        'flex w-full cursor-pointer items-center gap-4 px-4 py-3 text-left hover:bg-theme-hover',
                        checked && 'bg-skyhook-500/5',
                      )}
                    >
                      {bulkMode && (
                        <span className="flex w-5 shrink-0 items-center justify-center">
                          <input
                            type="checkbox"
                            checked={checked}
                            onChange={() => toggleChecked(rowKey)}
                            onClick={(e) => e.stopPropagation()}
                            className="h-3.5 w-3.5 cursor-pointer rounded border-theme-border accent-skyhook-500"
                          />
                        </span>
                      )}
                      <div className="w-64 min-w-0 shrink-0">
                        <div className="truncate text-sm font-medium text-theme-text-primary">{name}</div>
                        <div className="truncate text-xs text-theme-text-tertiary">{namespace}</div>
                      </div>
                      <div className="w-40 shrink-0">
                        <span className={clsx('badge', status.color)}>{status.text}</span>
                      </div>
                      <div className="w-40 shrink-0 truncate text-xs">
                        {pipelineTarget ? (
                          <button
                            type="button"
                            title={`Open Pipeline ${pipelineLabel}`}
                            onClick={(e) => {
                              e.stopPropagation()
                              onOpenPipeline(pipelineTarget)
                            }}
                            className="truncate text-theme-text-secondary hover:text-skyhook-400 hover:underline"
                          >
                            {pipelineLabel}
                          </button>
                        ) : (
                          <span className="truncate text-theme-text-secondary" title={pipelineLabel}>
                            {pipelineLabel}
                          </span>
                        )}
                      </div>
                      <div className="min-w-[140px] flex-1">
                        <ProgressBar run={run} facet={facet} />
                      </div>
                      <div className="w-28 shrink-0 text-right text-xs text-theme-text-tertiary tabular-nums">
                        {durationMs !== null ? formatDuration(durationMs, true) : '—'}
                      </div>
                      <div className="w-24 shrink-0 text-right text-xs text-theme-text-tertiary tabular-nums">
                        {run?.status?.startTime ? formatAge(run.status.startTime) : '—'}
                      </div>
                      <div className="w-8 shrink-0 text-right">
                        {!bulkMode && <RowActionMenu items={actionItems} ariaLabel={`Actions for ${name}`} />}
                      </div>
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        </div>
      </div>

      <ConfirmDialog
        open={!!deleteTarget}
        onClose={() => setDeleteTarget(null)}
        onConfirm={() => {
          if (!deleteTarget) return
          deleteRun.mutate(
            { kind: 'pipelineruns', group: 'tekton.dev', namespace: deleteTarget.namespace, name: deleteTarget.name },
            { onSuccess: () => runsQuery.refetch() },
          )
          setDeleteTarget(null)
        }}
        title="Delete PipelineRun"
        message={deleteTarget ? `Delete "${deleteTarget.name}" in ${deleteTarget.namespace}?` : ''}
        confirmLabel="Delete"
        variant="danger"
      />

      <ConfirmDialog
        open={showBulkDeleteConfirm}
        onClose={() => setShowBulkDeleteConfirm(false)}
        onConfirm={() => {
          bulkDeleteRuns.mutate(
            {
              items: checkedRuns.map((r) => ({
                kind: 'pipelineruns',
                group: 'tekton.dev',
                namespace: r.metadata?.namespace ?? '',
                name: r.metadata?.name ?? '',
              })),
            },
            {
              onSettled: () => {
                runsQuery.refetch()
                exitBulkMode()
                setShowBulkDeleteConfirm(false)
              },
            },
          )
        }}
        title={`Delete ${checkedRuns.length} PipelineRun${checkedRuns.length === 1 ? '' : 's'}?`}
        message={`You are about to delete ${checkedRuns.length} PipelineRun${checkedRuns.length === 1 ? '' : 's'}. This action cannot be undone.`}
        details={checkedRuns.map((r) => `${r.metadata?.namespace}/${r.metadata?.name}`).join('\n')}
        confirmLabel={`Delete ${checkedRuns.length} PipelineRun${checkedRuns.length === 1 ? '' : 's'}`}
        variant="danger"
        isLoading={bulkDeleteRuns.isPending}
        isClosable
      />
    </div>
  )
}
