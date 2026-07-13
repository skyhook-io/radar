import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { AlertTriangle, DollarSign, ExternalLink, Gauge, Loader2, RefreshCw } from 'lucide-react'
import {
  Collapse,
  CollapseChevron,
  EmptyState,
  PageHeader,
  SearchBox,
  SelectMenu,
} from '@skyhook-io/k8s-ui'
import { Badge } from '@skyhook-io/k8s-ui/components/ui/Badge'
import {
  useAutoPromConnect,
  useClusterInfo,
  usePrometheusStatus,
  useRightsizingScan,
  type RightsizingRow,
} from '../../api/client'
import { RIGHTSIZING_DOCS_URL } from '../resource/RightsizingStrip'
import { buildWorkloadPath } from '../../utils/navigation'
import { CostViewTabs } from '../cost/CostViewTabs'
import {
  flattenScanResults,
  isActionableClass,
  scanClassCounts,
  type RightsizingScanRow,
  type ScanClass,
} from './model'

export const RIGHTSIZING_SCAN_DESCRIPTION =
  'Find CPU and memory requests to increase, reduce, or review. Radar never changes them.'
export const RIGHTSIZING_SCAN_METHODOLOGY =
  'Based on 7 days of history: CPU P95 and memory maximum, plus 15% headroom. Memory reductions require verifiable restart history.'

export type RightsizingScanSurfaceState =
  | 'discovering'
  | 'prometheus_required'
  | 'first_run'
  | 'scanning'
  | 'fatal_error'
  | 'unavailable'
  | 'results'

export function getRightsizingScanSurfaceState(input: {
  statusLoading: boolean
  hasStatus: boolean
  connected: boolean
  pending: boolean
  hasResult: boolean
  hasError: boolean
  resultState?: string
}): RightsizingScanSurfaceState {
  if (input.statusLoading && !input.hasStatus) return 'discovering'
  if (!input.connected) return 'prometheus_required'
  if (input.pending && !input.hasResult) return 'scanning'
  if (input.hasError && !input.hasResult) return 'fatal_error'
  if (!input.hasResult) return 'first_run'
  if (input.resultState === 'unavailable') return 'unavailable'
  return 'results'
}

interface RightsizingScanViewProps {
  namespaces: string[]
}

type ScanResult = NonNullable<ReturnType<typeof useRightsizingScan>['data']>
type ClassFilter = ScanClass | 'actions'
const ROW_PAGE_SIZE = 50

const ACTION_META: Record<
  'increase' | 'reduction' | 'review',
  { label: string; severity: 'warning' | 'info' | 'neutral'; helper: string }
> = {
  reduction: {
    label: 'Reduce requests',
    severity: 'info',
    helper: 'Reclaim meaningful capacity',
  },
  increase: {
    label: 'Increase or add',
    severity: 'warning',
    helper: 'Reduce reliability risk',
  },
  review: {
    label: 'Review first',
    severity: 'neutral',
    helper: 'Check safety signals or workloads with no replicas',
  },
}

export function RightsizingScanView({ namespaces }: RightsizingScanViewProps) {
  useAutoPromConnect()
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()
  const { data: clusterInfo } = useClusterInfo()
  const {
    data: promStatus,
    isLoading: statusLoading,
    refetch: retryPrometheus,
  } = usePrometheusStatus()
  const scan = useRightsizingScan(namespaces)
  const [result, setResult] = useState<Awaited<ReturnType<typeof scan.mutateAsync>>>()
  const [openRow, setOpenRow] = useState<string | null>(null)
  const [visibleLimit, setVisibleLimit] = useState(ROW_PAGE_SIZE)
  const scopeKey = `${clusterInfo?.context ?? ''}\0${[...namespaces].sort().join(',')}`
  const scopeKeyRef = useRef(scopeKey)
  scopeKeyRef.current = scopeKey

  useEffect(() => {
    setResult(undefined)
    setOpenRow(null)
    scan.reset()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scopeKey])

  const runScan = async () => {
    const startedForScope = scopeKey
    try {
      const next = await scan.mutateAsync()
      if (scopeKeyRef.current === startedForScope) setResult(next)
    } catch {
      // A same-scope snapshot stays visible when refresh fails.
    }
  }

  const rows = useMemo(() => (result ? flattenScanResults(result) : []), [result])
  const includeSystem = params.get('rfScope') === 'all'
  const scopeRows = includeSystem ? rows : rows.filter((row) => !row.system)
  const counts = useMemo(() => scanClassCounts(scopeRows), [scopeRows])
  const classFilter = (params.get('rfClass') as ClassFilter | null) ?? 'actions'
  const search = params.get('rfQ') ?? ''
  const kindFilter = params.get('rfKind') ?? ''
  const namespaceFilter = params.get('rfNs') ?? ''
  const setFilter = (key: string, value?: string) => {
    const next = new URLSearchParams(params)
    if (value) next.set(key, value)
    else next.delete(key)
    setParams(next, { replace: true })
  }
  const clearFilters = () => {
    const next = new URLSearchParams(params)
    for (const key of ['rfClass', 'rfQ', 'rfKind', 'rfNs', 'rfScope']) next.delete(key)
    setParams(next, { replace: true })
  }
  const filteredRows = scopeRows.filter((row) => {
    if (
      classFilter === 'actions'
        ? !isActionableClass(row.classification)
        : row.classification !== classFilter
    )
      return false
    if (kindFilter && row.kind !== kindFilter) return false
    if (namespaceFilter && row.namespace !== namespaceFilter) return false
    if (
      search &&
      !`${row.namespace} ${row.name} ${row.container} ${row.kind}`
        .toLowerCase()
        .includes(search.toLowerCase())
    )
      return false
    return true
  })
  const visibleRows = filteredRows.slice(0, visibleLimit)

  useEffect(() => {
    setVisibleLimit(ROW_PAGE_SIZE)
  }, [classFilter, search, kindFilter, namespaceFilter, includeSystem])

  const kinds = [...new Set(scopeRows.map((row) => row.kind))].sort()
  const resultNamespaces = [...new Set(scopeRows.map((row) => row.namespace))].sort()
  const activeFilters =
    classFilter !== 'actions' || Boolean(search || kindFilter || namespaceFilter || includeSystem)
  const hiddenSystem = rows.length - rows.filter((row) => !row.system).length
  const onlySystemRowsAreHidden = !includeSystem && rows.length > 0 && scopeRows.length === 0
  const surfaceState = getRightsizingScanSurfaceState({
    statusLoading,
    hasStatus: Boolean(promStatus),
    connected: promStatus?.connected === true,
    pending: scan.isPending,
    hasResult: Boolean(result),
    hasError: Boolean(scan.error),
    resultState: result?.state,
  })

  const openWorkload = (row: RightsizingScanRow) => {
    navigate(
      `${buildWorkloadPath({ kind: row.kind, namespace: row.namespace, name: row.name })}?tab=cost`,
    )
  }

  return (
    <div className="flex-1 min-h-0 overflow-y-auto">
      <div className="mx-auto flex w-full max-w-[1920px] flex-col gap-4 px-6 py-6">
        <PageHeader
          icon={DollarSign}
          title="Cost Insights"
          description="Understand current allocation and find CPU and memory requests worth tuning."
        />
        <CostViewTabs />
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-theme-text-primary">Rightsizing</h2>
            <p className="mt-0.5 text-sm text-theme-text-secondary">
              {RIGHTSIZING_SCAN_DESCRIPTION}
            </p>
          </div>
          <div className="flex items-center gap-3">
            <a
              href={RIGHTSIZING_DOCS_URL}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-xs text-accent-text hover:underline"
            >
              Methodology <ExternalLink className="h-3 w-3" />
            </a>
            {result?.scannedAt && (
              <span className="text-xs text-theme-text-tertiary">
                Scanned {formatScanTime(result.scannedAt)}
              </span>
            )}
            {result && (
              <button
                type="button"
                onClick={runScan}
                disabled={scan.isPending}
                className="btn-brand inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium"
              >
                <RefreshCw className={`h-3.5 w-3.5 ${scan.isPending ? 'animate-spin' : ''}`} />
                {scan.isPending ? 'Scanning…' : 'Run again'}
              </button>
            )}
          </div>
        </div>

        {surfaceState === 'discovering' ? (
          <CenteredState
            loading
            title="Looking for Prometheus…"
            body="Radar is checking the current cluster for workload metrics."
          />
        ) : surfaceState === 'prometheus_required' ? (
          <CenteredState
            title="Prometheus is required"
            body="Rightsizing uses Prometheus history. Cost Overview can still use OpenCost independently."
            action={
              <button
                type="button"
                onClick={() => retryPrometheus()}
                className="btn-brand px-3 py-1.5 text-xs font-medium"
              >
                Check again
              </button>
            }
          />
        ) : surfaceState === 'first_run' ? (
          <FirstRunState namespaces={namespaces} onRun={runScan} />
        ) : surfaceState === 'scanning' ? (
          <CenteredState
            loading
            title="Analyzing CPU and memory requests…"
            body="Comparing 7 days of CPU and memory usage with configured requests. Larger clusters can take up to a minute."
          />
        ) : surfaceState === 'fatal_error' ? (
          <CenteredState
            title="Rightsizing scan failed"
            body={errorMessage(scan.error)}
            action={
              <button
                type="button"
                onClick={runScan}
                className="btn-brand px-3 py-1.5 text-xs font-medium"
              >
                Try again
              </button>
            }
          />
        ) : surfaceState === 'unavailable' && result ? (
          <CenteredState
            title="Rightsizing is unavailable"
            body={unavailableMessage(result.reason)}
            action={
              <button
                type="button"
                onClick={runScan}
                disabled={scan.isPending}
                className="btn-brand px-3 py-1.5 text-xs font-medium"
              >
                Try again
              </button>
            }
          />
        ) : result ? (
          <div
            className={`flex flex-col gap-4 transition-opacity ${scan.isPending ? 'opacity-70' : ''}`}
          >
            {scan.error && (
              <Notice
                tone="warning"
                text={`Refresh failed; showing results from ${formatScanTime(result.scannedAt)}. ${errorMessage(scan.error)}`}
              />
            )}
            {scan.isPending && (
              <Notice text="Scanning the current scope. Previous results remain visible until the scan completes." />
            )}
            <ScanSummary
              result={result}
              counts={counts}
              selected={classFilter}
              onSelect={(value) => setFilter('rfClass', value === 'actions' ? undefined : value)}
            />
            <ScanNotices result={result} rows={rows} />
            {result.coverage.workloadsDiscovered === 0 || rows.length === 0 ? (
              <EmptyState
                variant="card"
                headline="No supported workloads in this scope"
                body="No Deployment, StatefulSet, or DaemonSet containers are visible in the selected namespaces."
              />
            ) : (
              <section className="overflow-visible rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
                <ScanFilters
                  search={search}
                  onSearch={(value) => setFilter('rfQ', value || undefined)}
                  kind={kindFilter}
                  kinds={kinds}
                  onKind={(value) => setFilter('rfKind', value || undefined)}
                  namespace={namespaceFilter}
                  namespaces={resultNamespaces}
                  onNamespace={(value) => setFilter('rfNs', value || undefined)}
                  includeSystem={includeSystem}
                  hiddenSystem={hiddenSystem}
                  onScope={(value) => setFilter('rfScope', value === 'all' ? 'all' : undefined)}
                  shown={filteredRows.length}
                  total={scopeRows.length}
                  onClear={clearFilters}
                  active={activeFilters}
                />
                {filteredRows.length === 0 ? (
                  <EmptyState
                    tone="filtered"
                    headline={
                      onlySystemRowsAreHidden
                        ? 'System workloads are hidden'
                        : 'No results match the current filters'
                    }
                    body={
                      onlySystemRowsAreHidden
                        ? 'This scope contains only Kubernetes system workloads. Include them to review platform requests.'
                        : 'Choose another result type or clear a filter.'
                    }
                    action={
                      <button
                        type="button"
                        onClick={() =>
                          onlySystemRowsAreHidden ? setFilter('rfScope', 'all') : clearFilters()
                        }
                        className="badge badge-sm border border-theme-border bg-theme-elevated text-theme-text-primary"
                      >
                        {onlySystemRowsAreHidden
                          ? 'Include system workloads'
                          : 'Show recommended actions'}
                      </button>
                    }
                  />
                ) : (
                  <div>
                    <div className="grid grid-cols-[minmax(260px,1.1fr)_minmax(360px,1.6fr)_minmax(220px,.9fr)_28px] gap-4 border-b border-theme-border px-4 py-2 text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">
                      <span>Workload / container</span>
                      <span>Suggested next step</span>
                      <span>Across replicas</span>
                      <span />
                    </div>
                    <div className="table-divide-subtle">
                      {visibleRows.map((row) => (
                        <ScanResultRow
                          key={row.id}
                          row={row}
                          open={openRow === row.id}
                          onToggle={() => setOpenRow(openRow === row.id ? null : row.id)}
                          onOpen={() => openWorkload(row)}
                        />
                      ))}
                      {visibleRows.length < filteredRows.length && (
                        <div className="flex justify-center border-t border-theme-border px-4 py-3">
                          <button
                            type="button"
                            onClick={() => setVisibleLimit((value) => value + ROW_PAGE_SIZE)}
                            className="text-xs font-medium text-accent-text hover:underline"
                          >
                            Show {Math.min(ROW_PAGE_SIZE, filteredRows.length - visibleRows.length)}{' '}
                            more
                          </button>
                        </div>
                      )}
                    </div>
                  </div>
                )}
              </section>
            )}
          </div>
        ) : null}
      </div>
    </div>
  )
}

function FirstRunState({ namespaces, onRun }: { namespaces: string[]; onRun: () => void }) {
  const scope =
    namespaces.length === 0
      ? 'all visible namespaces'
      : namespaces.length === 1
        ? namespaces[0]
        : `${namespaces.length} selected namespaces`
  return (
    <div className="rounded-xl border border-theme-border bg-theme-surface p-8 text-center shadow-theme-sm">
      <Gauge className="mx-auto h-9 w-9 text-theme-text-tertiary" />
      <h2 className="mt-3 text-base font-semibold text-theme-text-primary">
        Review CPU and memory requests
      </h2>
      <p className="mx-auto mt-1 max-w-xl text-sm text-theme-text-secondary">
        Scan {scope} to find CPU and memory requests to increase, reduce, or review alongside
        autoscaling.
      </p>
      <p className="mt-3 text-xs text-theme-text-tertiary">{RIGHTSIZING_SCAN_METHODOLOGY}</p>
      <button
        type="button"
        onClick={onRun}
        className="btn-brand mt-5 px-4 py-2 text-sm font-medium"
      >
        Scan visible workloads
      </button>
    </div>
  )
}

function ScanSummary({
  result,
  counts,
  selected,
  onSelect,
}: {
  result: ScanResult
  counts: ReturnType<typeof scanClassCounts>
  selected: ClassFilter
  onSelect: (value: ClassFilter) => void
}) {
  const evaluated = result.coverage.workloadsEvaluated ?? result.workloads.length
  const discovered = result.coverage.workloadsDiscovered ?? result.workloads.length
  return (
    <section className="rounded-xl border border-theme-border bg-theme-surface p-4 shadow-theme-sm">
      <div className="grid gap-2 md:grid-cols-3">
        {(Object.keys(ACTION_META) as Array<keyof typeof ACTION_META>).map((key) => {
          const meta = ACTION_META[key]
          return (
            <button
              key={key}
              type="button"
              onClick={() => onSelect(selected === key ? 'actions' : key)}
              className={`rounded-lg border p-3 text-left transition-colors ${selected === key ? 'border-accent bg-accent-muted' : 'border-theme-border bg-theme-base/50 hover:bg-theme-hover'}`}
            >
              <div className="flex items-center justify-between gap-2">
                <span className="text-sm font-medium text-theme-text-primary">{meta.label}</span>
                <Badge severity={meta.severity} size="sm">
                  {counts[key]} containers
                </Badge>
              </div>
              <p className="mt-1 text-xs text-theme-text-tertiary">{meta.helper}</p>
            </button>
          )
        })}
      </div>
      <div className="mt-3 flex flex-wrap items-center justify-between gap-2 text-xs text-theme-text-tertiary">
        <span>
          {evaluated} of {discovered} visible workloads evaluated · {result.window || '7d'} history
        </span>
        <span className="flex items-center gap-3">
          <button
            type="button"
            onClick={() => onSelect('in_range')}
            className="hover:text-theme-text-primary"
          >
            {counts.in_range} containers · no meaningful change
          </button>
          <button
            type="button"
            onClick={() => onSelect('need_data')}
            className="hover:text-theme-text-primary"
          >
            {counts.need_data} containers · not analyzed
          </button>
          {selected !== 'actions' && (
            <button
              type="button"
              onClick={() => onSelect('actions')}
              className="font-medium text-accent-text hover:underline"
            >
              Recommended actions
            </button>
          )}
        </span>
      </div>
    </section>
  )
}

function ScanNotices({ result, rows }: { result: ScanResult; rows: RightsizingScanRow[] }) {
  const notices: string[] = []
  if (result.state === 'partial')
    notices.push('Some workloads could not be fully analyzed. Completed recommendations are shown.')
  if ((result.coverage.restrictedKinds?.length ?? 0) > 0)
    notices.push('Some workload kinds or namespaces were excluded by your Kubernetes access.')
  if ((result.coverage.unavailableKinds?.length ?? 0) > 0)
    notices.push('Some workload kinds could not be evaluated with the available ownership data.')
  for (const warning of result.warnings ?? []) notices.push(warningMessage(warning.code))
  if (rows.length > 0 && rows.every((row) => row.classification === 'need_data'))
    notices.push('There is not enough recent history to recommend request changes yet.')
  return notices.length > 0 ? (
    <div className="flex flex-col gap-2">
      {notices.map((text) => (
        <Notice key={text} text={text} />
      ))}
    </div>
  ) : null
}

function Notice({ text, tone }: { text: string; tone?: 'warning' }) {
  return (
    <div
      className={`flex items-start gap-2 rounded-lg border px-3 py-2 text-xs ${tone === 'warning' ? 'status-degraded' : 'border-theme-border bg-theme-surface text-theme-text-secondary'}`}
    >
      <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
      {text}
    </div>
  )
}

function ScanFilters(props: {
  search: string
  onSearch: (value: string) => void
  kind: string
  kinds: string[]
  onKind: (value: string) => void
  namespace: string
  namespaces: string[]
  onNamespace: (value: string) => void
  includeSystem: boolean
  hiddenSystem: number
  onScope: (value: string) => void
  shown: number
  total: number
  active: boolean
  onClear: () => void
}) {
  return (
    <div className="flex flex-wrap items-center gap-2 border-b border-theme-border p-3">
      <SearchBox
        value={props.search}
        onChange={props.onSearch}
        scope="global"
        shortcutId="rightsizing-search"
        placeholder="Search workloads…"
        className="mr-auto w-72"
      />
      <SelectMenu
        ariaLabel="Filter by namespace"
        value={props.namespace}
        onChange={props.onNamespace}
        className="w-48"
        options={[
          { value: '', label: 'All namespaces' },
          ...props.namespaces.map((value) => ({ value, label: value })),
        ]}
      />
      <SelectMenu
        ariaLabel="Filter by workload kind"
        value={props.kind}
        onChange={props.onKind}
        className="w-36"
        options={[
          { value: '', label: 'All kinds' },
          ...props.kinds.map((value) => ({ value, label: value })),
        ]}
      />
      {props.hiddenSystem > 0 && (
        <SelectMenu
          ariaLabel="Choose workload scope"
          value={props.includeSystem ? 'all' : 'applications'}
          onChange={props.onScope}
          className="w-48"
          options={[
            { value: 'applications', label: 'Hide system workloads' },
            {
              value: 'all',
              label: `Include system workloads (+${props.hiddenSystem})`,
            },
          ]}
        />
      )}
      <span className="text-xs tabular-nums text-theme-text-tertiary">
        {props.shown} of {props.total} containers
      </span>
      {props.active && (
        <button
          type="button"
          onClick={props.onClear}
          className="text-xs text-accent-text hover:underline"
        >
          Reset view
        </button>
      )}
    </div>
  )
}

function ScanResultRow({
  row,
  open,
  onToggle,
  onOpen,
}: {
  row: RightsizingScanRow
  open: boolean
  onToggle: () => void
  onOpen: () => void
}) {
  return (
    <div>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="grid w-full grid-cols-[minmax(260px,1.1fr)_minmax(360px,1.6fr)_minmax(220px,.9fr)_28px] items-center gap-4 px-4 py-3 text-left hover:bg-theme-hover/50"
      >
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <Badge kind={row.kind} size="sm">
              {row.kind}
            </Badge>
            <span className="truncate text-sm font-medium text-theme-text-primary">{row.name}</span>
          </div>
          <div className="mt-1 truncate text-xs text-theme-text-tertiary">
            {row.namespace} · {row.container}
          </div>
        </div>
        <RecommendationCell row={row} />
        <ImpactCell row={row} />
        <CollapseChevron open={open} className="h-4 w-4" />
      </button>
      <Collapse open={open} mountLazily>
        <div className="grid gap-4 border-t border-theme-border bg-theme-base/40 px-4 py-3 md:grid-cols-[1fr_1fr_auto]">
          <FitWhy label="CPU" row={row.cpu} />
          <FitWhy label="Memory" row={row.memory} />
          <button
            type="button"
            onClick={onOpen}
            className="self-start text-xs font-medium text-accent-text hover:underline"
          >
            Open workload
          </button>
        </div>
      </Collapse>
    </div>
  )
}

function RecommendationCell({ row }: { row: RightsizingScanRow }) {
  if (row.classification === 'in_range') {
    return <span className="text-xs text-theme-text-tertiary">No meaningful request change</span>
  }
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <ResourceAction
        label="CPU"
        row={row.cpu}
        reviewWithNoCurrentReplicas={row.scaledToZero && row.classification === 'review'}
      />
      <ResourceAction
        label="Memory"
        row={row.memory}
        reviewWithNoCurrentReplicas={row.scaledToZero && row.classification === 'review'}
      />
    </div>
  )
}

function ResourceAction({
  label,
  row,
  reviewWithNoCurrentReplicas = false,
}: {
  label: string
  row?: RightsizingRow
  reviewWithNoCurrentReplicas?: boolean
}) {
  if (!row) return <span className="text-xs text-theme-text-tertiary">{label}: no result</span>
  const reason = row.recommendationReason
  if (row.hpaManaged)
    return (
      <span className="text-xs font-medium text-theme-text-secondary">
        Review {label} with the HPA
      </span>
    )
  if (reason === 'hpa_evidence_unavailable')
    return (
      <span className="text-xs font-medium text-theme-text-secondary">
        Review {label}; HPA status is unavailable
      </span>
    )
  if (reason === 'oom_evidence')
    return (
      <span className="text-xs font-medium text-theme-text-secondary">
        Keep {label}; an out-of-memory restart was seen
      </span>
    )
  if (reason === 'oom_evidence_unavailable')
    return (
      <span className="text-xs font-medium text-theme-text-secondary">
        Review {label}; restart history is incomplete
      </span>
    )
  if (row.limitConflict)
    return (
      <span className="text-xs font-medium text-theme-text-secondary">
        Raise the {label} limit before its request
      </span>
    )
  if (row.queryError)
    return <span className="text-xs text-theme-text-tertiary">{label}: metrics query failed</span>
  if (row.fit === 'insufficient_history')
    return <span className="text-xs text-theme-text-tertiary">{label}: not enough history</span>
  if (reviewWithNoCurrentReplicas)
    return (
      <span className="text-xs font-medium text-theme-text-secondary">
        Review {label} before the workload runs again
      </span>
    )
  const isReduction =
    row.recommendedRequestValue != null &&
    row.currentRequestValue != null &&
    row.recommendedRequestValue < row.currentRequestValue
  if (row.resource === 'cpu' && isReduction && (row.bursty || (row.throttleRatio ?? 0) >= 0.1))
    return (
      <span className="text-xs font-medium text-theme-text-secondary">
        Review {label} before reducing
      </span>
    )
  if (row.recommendedRequest) {
    if (!row.currentRequest || row.currentRequestValue === 0)
      return (
        <span className="text-xs font-medium text-theme-text-primary">
          Add {label} request: {row.recommendedRequest}
        </span>
      )
    const verb =
      (row.recommendedRequestValue ?? 0) > (row.currentRequestValue ?? 0) ? 'Increase' : 'Reduce'
    return (
      <span className="text-xs font-medium text-theme-text-primary">
        {verb} {label}:{' '}
        <span className="tabular-nums">
          {row.currentRequest} → {row.recommendedRequest}
        </span>
      </span>
    )
  }
  return <span className="text-xs text-theme-text-tertiary">{label}: no meaningful change</span>
}

function ImpactCell({ row }: { row: RightsizingScanRow }) {
  if (row.classification === 'in_range') {
    return <div className="text-xs text-theme-text-tertiary">Below action threshold</div>
  }
  const changes: string[] = []
  if (row.impact.cpuChange !== 0) changes.push(formatCPUChange(row.impact.cpuChange))
  if (row.impact.memoryChange !== 0) changes.push(formatMemoryChange(row.impact.memoryChange))
  return (
    <div className="min-w-0 text-xs text-theme-text-secondary">
      <div>{row.replicas === 1 ? '1 replica' : `${row.replicas} replicas`}</div>
      {row.scaledToZero ? (
        <div className="mt-0.5 text-theme-text-tertiary">No current capacity impact</div>
      ) : changes.length > 0 ? (
        changes.map((change) => (
          <div key={change} className="mt-0.5 tabular-nums text-theme-text-tertiary">
            {change}
          </div>
        ))
      ) : (
        <div className="mt-0.5 text-theme-text-tertiary">No calculated capacity change</div>
      )}
      <div className="mt-1 flex flex-wrap gap-1">
        {[...row.signals].map((signal) => (
          <Badge
            key={signal}
            severity={
              signal === 'oom' || signal === 'query_error'
                ? 'error'
                : signal === 'throttling'
                  ? 'warning'
                  : 'neutral'
            }
            size="sm"
          >
            {signalLabel(signal)}
          </Badge>
        ))}
      </div>
    </div>
  )
}

function FitWhy({ label, row }: { label: string; row?: RightsizingRow }) {
  if (!row)
    return (
      <div>
        <div className="text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">
          {label}
        </div>
        <p className="mt-1 text-xs text-theme-text-secondary">No result returned.</p>
      </div>
    )
  const history =
    row.coverage >= 0.95
      ? 'Full 7-day history.'
      : `${Math.max(row.coverage * 7, 0).toFixed(1)} days of usable history.`
  const observation = row.observed
    ? row.observed.name === 'Max'
      ? `${label} peaked at ${row.observed.formatted} during the measured period. `
      : `${label} stayed below ${row.observed.formatted} for ${row.observed.name === 'P95' ? '95%' : '99%'} of the measured period. `
    : ''
  return (
    <div>
      <div className="text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">
        Why this {label.toLowerCase()} guidance
      </div>
      <p className="mt-1 text-xs text-theme-text-secondary">
        {observation}
        {history}
      </p>
      <EvidenceNote row={row} />
    </div>
  )
}

function EvidenceNote({ row }: { row: RightsizingRow }) {
  if (row.hpaManaged)
    return (
      <p className="mt-1 text-xs text-theme-text-tertiary">
        The HPA uses this request when calculating scale, so Radar does not suggest changing it
        directly.
      </p>
    )
  if (row.recommendationReason === 'oom_evidence')
    return (
      <p className="mt-1 text-xs text-theme-text-tertiary">
        A recent out-of-memory restart makes a lower memory request unsafe to suggest.
      </p>
    )
  if (row.recommendationReason === 'hpa_evidence_unavailable')
    return (
      <p className="mt-1 text-xs text-theme-text-tertiary">
        Radar could not verify whether autoscaling depends on this request.
      </p>
    )
  if (row.recommendationReason === 'oom_evidence_unavailable')
    return (
      <p className="mt-1 text-xs text-theme-text-tertiary">
        Radar could not verify restart history before suggesting a lower memory request.
      </p>
    )
  if (row.limitConflict)
    return (
      <p className="mt-1 text-xs text-theme-text-tertiary">
        The calculated request would exceed the current limit.
      </p>
    )
  if (row.fit === 'insufficient_history')
    return (
      <p className="mt-1 text-xs text-theme-text-tertiary">
        Wait for more runtime history before changing this request.
      </p>
    )
  const isReduction =
    row.recommendedRequestValue != null &&
    row.currentRequestValue != null &&
    row.recommendedRequestValue < row.currentRequestValue
  if (row.resource === 'cpu' && isReduction && (row.throttleRatio ?? 0) >= 0.1)
    return (
      <p className="mt-1 text-xs text-theme-text-tertiary">
        CPU limit throttling was observed. Review the limit and burst behavior before reducing this
        request.
      </p>
    )
  if (row.resource === 'cpu' && isReduction && row.bursty)
    return (
      <p className="mt-1 text-xs text-theme-text-tertiary">
        CPU usage was bursty during the measured period. Review the peak before reducing this
        request.
      </p>
    )
  if (row.reductionLimited && row.calculatedRequest && row.recommendedRequest) {
    return (
      <p className="mt-1 text-xs text-theme-text-tertiary">
        Demand-based target: {row.calculatedRequest}. Radar suggests {row.recommendedRequest} as a
        conservative next step; observe another full window before reducing further.
        {row.bursty && row.peak ? ` CPU P99 reached ${row.peak.formatted}.` : ''}
      </p>
    )
  }
  if (row.recommendedRequest)
    return (
      <p className="mt-1 text-xs text-theme-text-tertiary">
        The target includes 15% headroom, a practical minimum, and scale-aware rounding.
      </p>
    )
  return null
}

function CenteredState({
  loading,
  title,
  body,
  action,
}: {
  loading?: boolean
  title: string
  body: string
  action?: React.ReactNode
}) {
  return (
    <div className="flex min-h-48 items-center justify-center rounded-xl border border-theme-border bg-theme-surface">
      <div className="flex max-w-lg flex-col items-center px-6 text-center">
        {loading ? (
          <Loader2 className="h-8 w-8 animate-spin text-theme-text-tertiary" />
        ) : (
          <Gauge className="h-8 w-8 text-theme-text-tertiary" />
        )}
        <h2 className="mt-3 text-base font-semibold text-theme-text-primary">{title}</h2>
        <p className="mt-1 text-sm text-theme-text-secondary">{body}</p>
        {action && <div className="mt-4">{action}</div>}
      </div>
    </div>
  )
}

function formatCPUChange(value: number): string {
  const prefix = value > 0 ? '+' : '−'
  const absolute = Math.abs(value)
  return absolute >= 1
    ? `${prefix}${absolute.toFixed(1)} CPU requested`
    : `${prefix}${Math.round(absolute * 1000)}m CPU requested`
}

function formatMemoryChange(value: number): string {
  const prefix = value > 0 ? '+' : '−'
  const mib = Math.abs(value) / (1024 * 1024)
  return mib >= 1024
    ? `${prefix}${(mib / 1024).toFixed(1)}Gi memory requested`
    : `${prefix}${Math.round(mib)}Mi memory requested`
}

function signalLabel(
  signal: RightsizingScanRow['signals'] extends Set<infer T> ? T : never,
): string {
  return {
    hpa: 'HPA',
    oom: 'OOM',
    bursty: 'Bursty usage',
    throttling: 'Throttling',
    query_error: 'Query error',
    scaled_zero: 'No current replicas',
  }[signal]
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : 'An unexpected error occurred.'
}

function unavailableMessage(reason?: string): string {
  if (reason === 'workload_kinds_unavailable')
    return 'No supported workload kinds are available with your current Kubernetes access.'
  if (reason === 'owner_metrics_missing')
    return 'kube-state-metrics ownership metrics were not found. Rightsizing needs kube_pod_owner to map samples to workloads.'
  if (reason === 'deployment_owner_metrics_missing')
    return 'Deployment history cannot be mapped because kube_replicaset_owner is unavailable from kube-state-metrics.'
  if (reason === 'owner_metrics_query_failed')
    return 'Radar found Prometheus, but could not query kube-state-metrics ownership data.'
  if (reason === 'scan_incomplete')
    return 'The scan could not evaluate any workloads in the current scope.'
  return 'Prometheus or kube-state-metrics does not expose the metrics required for this scan.'
}

function warningMessage(code: string): string {
  if (code === 'scan_deadline_exceeded')
    return 'The scan reached its time limit. Results from completed batches are shown.'
  if (code === 'owner_metrics_query_failed') return 'Workload ownership data could not be queried.'
  if (code === 'oom_evidence_unavailable')
    return 'Radar could not verify restart history for some containers, so it withheld their memory reductions.'
  if (code.endsWith('_query_failed'))
    return `Some ${code.replace('_query_failed', '').replaceAll('_', ' ')} data could not be queried.`
  return 'Some rightsizing data was unavailable. Completed recommendations are shown.'
}

function formatScanTime(value: string): string {
  const timestamp = new Date(value)
  return Number.isNaN(timestamp.getTime())
    ? value
    : timestamp.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })
}
