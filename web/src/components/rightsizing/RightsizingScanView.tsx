import { useEffect, useLayoutEffect, useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { AlertTriangle, Coins, ExternalLink, Gauge, Loader2, RefreshCw } from 'lucide-react'
import { ClusterScopeNote } from '@skyhook-io/k8s-ui/components/ui/ClusterScopeNote'
import { isClusterScopeWithheld } from '@skyhook-io/k8s-ui/types/cluster-scope'
import {
  Collapse,
  CollapseChevron,
  EmptyState,
  PageHeader,
  pluralize,
  useDisclosure,
} from '@skyhook-io/k8s-ui'
import {
  useAutoPromConnect,
  useClusterInfo,
  usePrometheusStatus,
  useRightsizingScan,
} from '../../api/client'
import { RIGHTSIZING_DOCS_URL } from '../resource/RightsizingStrip'
import { buildWorkloadPath } from '../../utils/navigation'
import { CostViewTabs } from '../cost/CostViewTabs'
import {
  flattenScanResults,
  type RightsizingScanRow,
} from './model'
import { matchesResultClass, matchesResultIdentity, readResultFilters, resultFilterKeys, scanClassCounts, type ClassFilter, type ScanClass } from '@skyhook-io/k8s-ui/components/rightsizing/results-model'
import { RightsizingActionFilters, RightsizingResultFilters } from '@skyhook-io/k8s-ui/components/rightsizing/RightsizingFilters'
import { RightsizingResultsTable } from '@skyhook-io/k8s-ui/components/rightsizing/RightsizingResultsTable'
import { useNavCustomization } from '../../context/NavCustomization'
import { previousSettingsNote, usePreviousIntegrationSettings } from '../../hooks/usePreviousIntegrationSettings'

export const RIGHTSIZING_SCAN_DESCRIPTION =
  'Find CPU and memory requests to increase, reduce, or review. Radar never changes them.'
export const RIGHTSIZING_SCAN_METHODOLOGY =
  'Based on 7 days of history: CPU P95 and memory maximum, plus 15% headroom. Memory reductions require verifiable restart history.'
export const RIGHTSIZING_METRICS_REQUIRED_TITLE = 'Metrics history is required'
export const RIGHTSIZING_METRICS_REQUIRED_BODY =
  'Rightsizing needs 7 days of Kubernetes workload history from a PromQL-compatible metrics backend (Prometheus, VictoriaMetrics, Thanos, or Mimir).\nCost Overview remains available without it.'
export const RIGHTSIZING_EMBEDDED_METRICS_REQUIRED_BODY =
  `${RIGHTSIZING_METRICS_REQUIRED_BODY}\nConfigure metrics for this cluster in the host application or Radar deployment.`

export type RightsizingScanSurfaceState =
  | 'loading_scan'
  | 'discovering'
  | 'prometheus_required'
  | 'first_run'
  | 'scanning'
  | 'fatal_error'
  | 'unavailable'
  | 'results'

export function getRightsizingScanSurfaceState(input: {
  scanLoading?: boolean
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
  if (input.scanLoading && !input.hasResult) return 'loading_scan'
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
const ROW_PAGE_SIZE = 50

export function RightsizingScanView({ namespaces }: RightsizingScanViewProps) {
  useAutoPromConnect()
  const settingsAvailable = !useNavCustomization().embedded
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()
  const { data: clusterInfo } = useClusterInfo()
  const {
    data: promStatus,
    isLoading: statusLoading,
    refetch: retryPrometheus,
  } = usePrometheusStatus()
  const offers = usePreviousIntegrationSettings(!!promStatus && !promStatus.connected && !promStatus.discovering && !statusLoading)
  const previousNote = offers.metrics ? previousSettingsNote('metrics', offers.metrics) : undefined
  const scan = useRightsizingScan(namespaces, clusterInfo?.context)
  const result = scan.data
  const [openRow, setOpenRow] = useState<string | null>(null)
  const [visibleLimit, setVisibleLimit] = useState(ROW_PAGE_SIZE)
  const scopeKey = `${clusterInfo?.context ?? ''}\0${[...namespaces].sort().join(',')}`

  useLayoutEffect(() => {
    setOpenRow(null)
    scan.reset()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scopeKey])

  const runScan = async () => {
    try {
      await scan.mutateAsync()
    } catch {
      // A same-scope snapshot stays visible when refresh fails.
    }
  }

  const rows = useMemo(() => (result ? flattenScanResults(result) : []), [result])
  const filters = readResultFilters(params, 'actions')
  const { includeSystem, classFilter, search, kindFilter, namespaceFilter } = filters
  const scopeRows = includeSystem ? rows : rows.filter((row) => !row.system)
  const matching = scopeRows.filter((row) => matchesResultIdentity(row, filters))
  const counts = scanClassCounts(matching)
  const setFilter = (key: string, value?: string) => {
    const next = new URLSearchParams(params)
    if (value) next.set(key, value)
    else next.delete(key)
    setParams(next, { replace: true })
  }
  const clearFilters = () => {
    const next = new URLSearchParams(params)
    for (const key of resultFilterKeys) next.delete(key)
    setParams(next, { replace: true })
  }
  const filteredRows = matching.filter((row) => matchesResultClass(row, classFilter))
  const visibleRows = filteredRows.slice(0, visibleLimit)

  useEffect(() => {
    setVisibleLimit(ROW_PAGE_SIZE)
  }, [classFilter, search, kindFilter, namespaceFilter, includeSystem])

  const kinds = [...new Set(scopeRows.map((row) => row.kind))].sort()
  const resultNamespaces = [...new Set(scopeRows.map((row) => row.namespace))].sort()
  const activeFilters =
    classFilter !== 'actions' || Boolean(search || kindFilter || namespaceFilter || includeSystem)
  const hasSystem = rows.some((row) => row.system)
  const onlySystemRowsAreHidden = !includeSystem && rows.length > 0 && scopeRows.length === 0
  const surfaceState = getRightsizingScanSurfaceState({
    statusLoading,
    scanLoading: scan.isLoading,
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
          icon={Coins}
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
                onClick={scan.isPending ? scan.stop : runScan}
                disabled={scan.isStarting || scan.isStopping}
                className="btn-brand inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium"
              >
                <RefreshCw className={`h-3.5 w-3.5 ${scan.isPending ? 'animate-spin' : ''}`} />
                {scan.isStarting ? 'Starting…' : scan.isStopping ? 'Stopping…' : scan.isPending ? 'Stop scan' : 'Run again'}
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
            title={RIGHTSIZING_METRICS_REQUIRED_TITLE}
            body={settingsAvailable
              ? `${RIGHTSIZING_METRICS_REQUIRED_BODY}${previousNote ? `\n${previousNote}` : ''}`
              : RIGHTSIZING_EMBEDDED_METRICS_REQUIRED_BODY}
            action={
              <div className="flex flex-wrap items-center justify-center gap-3">
                {settingsAvailable && (
                  <button
                    type="button"
                    onClick={() =>
                      window.dispatchEvent(
                        new CustomEvent('radar:open-settings', { detail: { section: 'prometheus' } }),
                      )
                    }
                    className="btn-brand px-4 py-2 text-sm font-medium"
                  >
                    Configure metrics
                  </button>
                )}
                <button
                  type="button"
                  onClick={() => retryPrometheus()}
                  className="rounded-lg border border-theme-border px-4 py-2 text-sm font-medium text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
                >
                  Check again
                </button>
              </div>
            }
          />
        ) : surfaceState === 'loading_scan' ? (
          <CenteredState loading title="Loading scan…" body="Checking for an existing scan in this cluster and namespace scope." />
        ) : surfaceState === 'first_run' ? (
          <FirstRunState namespaces={namespaces} onRun={runScan} />
        ) : surfaceState === 'scanning' ? (
          <CenteredState
            loading
            title="Analyzing CPU and memory requests…"
            body="Comparing 7 days of CPU and memory usage with configured requests. Results appear as batches finish; scans run for up to three minutes."
          />
        ) : surfaceState === 'fatal_error' ? (
          <CenteredState
            title={scan.statusError ? "Could not retrieve scan status" : "Rightsizing scan failed"}
            body={errorMessage(scan.error)}
            action={
              <button
                type="button"
                onClick={scan.statusError ? () => void scan.retryStatus() : runScan}
                className="btn-brand px-3 py-1.5 text-xs font-medium"
              >
                {scan.statusError ? 'Retry status' : 'Try again'}
              </button>
            }
          />
        ) : surfaceState === 'unavailable' && result ? (
          <ScanUnavailableState result={result} onRun={runScan} />
        ) : result ? (
          <div
            className="flex flex-col gap-4"
          >
            {scan.error && (
              <div className="flex items-center gap-3">
                <Notice tone="warning" text={`${scan.statusError ? 'Could not retrieve scan status. The scan may still be running.' : 'Could not start or stop the scan.'} ${errorMessage(scan.error)}`} />
                <button type="button" className="btn-brand-muted shrink-0 px-3 py-1.5 text-xs" onClick={() => void scan.retryStatus()}>Retry status</button>
              </div>
            )}
            {scan.isPending && (
              <Notice loading text={`${scan.progress?.coverage.workloadsDiscovered ? `Scanning — ${scan.progress.coverage.workloadsEvaluated} of ${scan.progress.coverage.workloadsDiscovered} workloads attempted.` : 'Preparing scan…'} Results and rankings update as batches finish. You can leave this page and return.${scan.showingPrevious ? ' Showing previous results until the first batch is ready.' : ''}`} />
            )}
            <ScanSummary
              namespaces={namespaces}
              result={result}
              counts={counts}
              selected={classFilter}
              onSelect={(value) => setFilter('rfClass', value === 'actions' ? undefined : value)}
            />
            {!scan.isPending && <ScanNotices key={result.scannedAt} result={result} />}
            {scan.isPending && rows.length === 0 ? (
              <CenteredState loading title="Preparing recommendations…" body="The scan is running. Recommendations appear after each batch finishes." />
            ) : result.reason === 'only_daemonsets_without_nodes' ? (
              <EmptyState
                variant="card"
                headline="Only DaemonSets with no nodes in this scope"
                body="Every workload here is a DaemonSet that runs on no node right now, so none was scanned. Open one from Resources to see recommendations from its retained history."
              />
            ) : result.coverage.workloadsDiscovered === 0 || rows.length === 0 ? (
              <EmptyState
                variant="card"
                headline="No supported workloads in this scope"
                body="No Deployment, StatefulSet, or DaemonSet containers are visible in the selected namespaces."
              />
            ) : (
              <section className="overflow-visible rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
                <RightsizingResultFilters
                  search={search}
                  onSearch={(value) => setFilter('rfQ', value || undefined)}
                  kind={kindFilter}
                  kinds={kinds}
                  onKind={(value) => setFilter('rfKind', value || undefined)}
                  namespace={namespaceFilter}
                  namespaces={resultNamespaces}
                  onNamespace={(value) => setFilter('rfNs', value || undefined)}
                  includeSystem={includeSystem}
                  hasSystem={hasSystem}
                  onScope={(value) => setFilter('rfScope', value === 'all' ? 'all' : undefined)}
                  shown={filteredRows.length}
                  total={scopeRows.length}
                  onClear={clearFilters}
                  active={activeFilters}
                />
                {filteredRows.length === 0 && scan.isPending ? (
                  <EmptyState variant="card" headline="No matching results yet" body="The scan is still running. More workloads may match these filters as batches finish." />
                ) : filteredRows.length === 0 ? (
                  <ScanEmptyState
                    counts={counts}
                    classFilter={classFilter}
                    hasResultFilters={Boolean(search || kindFilter || namespaceFilter)}
                    onlySystemRowsAreHidden={onlySystemRowsAreHidden}
                    hasQueryErrors={scopeRows.some(
                      (row) => row.cpu?.queryError || row.memory?.queryError,
                    )}
                    onSelect={(value) => setFilter('rfClass', value)}
                    onClear={clearFilters}
                    onIncludeSystem={() => setFilter('rfScope', 'all')}
                  />
                ) : (
                  <div>
                    <RightsizingResultsTable
                      rows={visibleRows}
                      openRowId={openRow}
                      onOpenRowChange={setOpenRow}
                      renderWorkloadLink={row => (
                        <button type="button" onClick={() => openWorkload(row)} className="self-start text-xs font-medium text-accent-text hover:underline">
                          Open workload
                        </button>
                      )}
                    />
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
                )}
              </section>
            )}
          </div>
        ) : null}
      </div>
    </div>
  )
}

export function ScanEmptyState({
  counts,
  classFilter,
  hasResultFilters,
  onlySystemRowsAreHidden,
  hasQueryErrors,
  onSelect,
  onClear,
  onIncludeSystem,
}: {
  counts: ReturnType<typeof scanClassCounts>
  classFilter: ClassFilter
  hasResultFilters: boolean
  onlySystemRowsAreHidden: boolean
  hasQueryErrors: boolean
  onSelect: (value: ScanClass) => void
  onClear: () => void
  onIncludeSystem: () => void
}) {
  let headline = 'No results match the current filters'
  let body = 'Choose another result type or clear a filter.'
  let actionLabel = 'Clear filters'
  let onAction = onClear
  if (onlySystemRowsAreHidden) {
    headline = 'System workloads are hidden'
    body =
      'This scope contains only Kubernetes system workloads. Include them to review platform requests.'
    actionLabel = 'Include system workloads'
    onAction = onIncludeSystem
  } else if (
    classFilter === 'actions' &&
    !hasResultFilters &&
    counts.increase + counts.reduction + counts.review === 0
  ) {
    if (counts.need_data > 0) {
      headline =
        counts.in_range > 0
          ? 'No actionable changes in available evidence'
          : 'Not enough evidence for recommendations'
      body =
        counts.in_range > 0
          ? `${pluralize(counts.in_range, 'container')} had no meaningful request changes; ${pluralize(counts.need_data, 'container')} still ${counts.need_data === 1 ? 'needs' : 'need'} evidence. This is not a complete assessment.`
          : hasQueryErrors
            ? 'Some metrics queries failed. Inspect affected containers, then retry the scan or select fewer namespaces in the top bar.'
            : 'There is not enough usable CPU and memory history to recommend changes yet. Inspect the containers to see which resource needs more history.'
      actionLabel = 'View containers needing evidence'
      onAction = () => onSelect('need_data')
    } else {
      headline = 'No actionable changes'
      body = `No meaningful request changes were found among the ${pluralize(counts.in_range, 'evaluated container')}. This does not assess workloads outside the scan coverage.`
      actionLabel = 'View evaluated containers'
      onAction = () => onSelect('in_range')
    }
  }
  return (
    <EmptyState
      headline={headline}
      body={body}
      action={
        <button
          type="button"
          onClick={onAction}
          className="badge badge-sm border border-theme-border bg-theme-elevated text-theme-text-primary"
        >
          {actionLabel}
        </button>
      }
    />
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

export function ScanSummary({
  namespaces,
  result,
  counts,
  selected,
  onSelect,
}: {
  namespaces: string[]
  result: ScanResult
  counts: ReturnType<typeof scanClassCounts>
  selected: ClassFilter
  onSelect: (value: ClassFilter) => void
}) {
  return (
    <section className="rounded-xl border border-theme-border bg-theme-surface p-4 shadow-theme-sm">
      <p className="mb-3 break-words text-xs text-theme-text-secondary">
        Scanned scope:{' '}
        <span className="font-medium text-theme-text-primary">
          {namespaces.length ? namespaces.join(', ') : 'All visible namespaces'}
        </span>
        <span className="text-theme-text-tertiary"> · Change scan scope in the top bar</span>
      </p>
      <RightsizingActionFilters
        counts={counts}
        selected={selected}
        defaultClass="actions"
        onSelect={onSelect}
        coverage={
          <span>
            {result.coverage.workloadsEvaluated} of {result.coverage.workloadsDiscovered} workloads
            attempted{result.scanStatus === 'running' ? ' so far' : ''}
            {' · '}
            {result.coverage.workloadsWithData} with usage data · {result.window} window
          </span>
        }
      />
    </section>
  )
}

export function ScanNotices({ result }: { result: ScanResult }) {
  const [open, setOpen] = useState(false)
  const disclosure = useDisclosure(open)
  const notices: string[] = []
  if ((result.coverage.restrictedKinds?.length ?? 0) > 0)
    notices.push('Some workload kinds or namespaces were excluded by your Kubernetes access.')
  if ((result.coverage.unavailableKinds?.length ?? 0) > 0)
    notices.push('Some workload kinds could not be evaluated with the available ownership data.')
  if ((result.coverage.partiallyCachedKinds?.length ?? 0) > 0)
    notices.push(
      'Radar is caching only some namespaces, so this scan covered a narrower scope than the whole cluster.',
    )
  const daemonSetsWithoutNodes = result.coverage.daemonSetsWithoutNodes ?? 0
  // The DaemonSet-only empty state already says this.
  if (daemonSetsWithoutNodes > 0 && result.reason !== 'only_daemonsets_without_nodes')
    notices.push(
      `${pluralize(daemonSetsWithoutNodes, 'DaemonSet')} ${daemonSetsWithoutNodes === 1 ? 'runs' : 'run'} on no node right now and ${daemonSetsWithoutNodes === 1 ? 'is' : 'are'} not listed. Open one from Resources to see recommendations from its retained history.`,
    )
  const warnings = result.warnings ?? []
  const deadlineExceeded = result.scanStatus === 'timed_out' || warnings.some((warning) => warning.code === 'scan_deadline_exceeded')
  for (const warning of warnings) {
    if (warning.code !== 'scan_deadline_exceeded') notices.push(warningMessage(warning.code))
  }
  const partial = result.state === 'partial'
  if (!partial && notices.length === 0 && !deadlineExceeded) return null
  const details = [...new Set(notices)]
  const singleNote = details.length === 1 && !deadlineExceeded
  const hasDetails = details.length > 0 && !singleNote
  const summary = result.scanStatus === 'cancelled' ? 'Scan stopped. Available recommendations are shown. Run again to start a new scan.' : singleNote
    ? details[0]
    : deadlineExceeded
      ? 'The scan reached its time limit. Available recommendations are shown. For a narrower scan, select namespaces in the top bar and run again.'
      : partial
        ? 'Some workloads or metrics could not be fully evaluated. Available recommendations are shown.'
        : 'Some workloads were excluded from this scan.'
  return (
    <section
      aria-label="Scan coverage"
      className="rounded-lg border border-theme-border bg-theme-surface text-xs text-theme-text-secondary"
    >
      <div className="flex items-start gap-2 px-3 py-2">
        <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
        <div className="min-w-0 flex-1">
          <p className="font-medium text-theme-text-primary">
            {partial ? 'Partial results' : 'Scan notes'}
          </p>
          <p className="mt-1">{summary}</p>
        </div>
        {hasDetails && (
          <button
            type="button"
            {...disclosure.buttonProps}
            onClick={() => setOpen(!open)}
            className="inline-flex shrink-0 items-center gap-1 rounded px-1 py-0.5 text-accent-text hover:bg-theme-hover"
          >
            Details <CollapseChevron open={open} className="h-3.5 w-3.5" />
          </button>
        )}
      </div>
      {hasDetails && (
        <Collapse open={open} id={disclosure.panelId}>
          <ul className="list-disc space-y-1 border-t border-theme-border py-2 pl-9 pr-3">
            {details.map((text) => (
              <li key={text}>{text}</li>
            ))}
          </ul>
        </Collapse>
      )}
    </section>
  )
}

function Notice({ text, tone, loading }: { text: string; tone?: 'warning'; loading?: boolean }) {
  return (
    <div
      className={`flex items-start gap-2 rounded-lg border px-3 py-2 text-xs ${tone === 'warning' ? 'status-degraded' : 'border-theme-border bg-theme-surface text-theme-text-secondary'}`}
    >
      {loading ? <Loader2 className="mt-0.5 h-3.5 w-3.5 shrink-0 animate-spin" /> : <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />}
      {text}
    </div>
  )
}

// A withheld cluster scope replaces the whole result: no workload was
// measured, so none may read as needing no change.
export function ScanUnavailableState({ result, onRun }: { result: ScanResult; onRun: () => void }) {
  if (isClusterScopeWithheld(result.clusterScope)) {
    return <ClusterScopeNote scope={result.clusterScope} title="Recommendations withheld for this cluster" />
  }
  return (
    <CenteredState
      title="Rightsizing is unavailable"
      body={unavailableMessage(result.reason)}
      action={
        <button
          type="button"
          onClick={onRun}
          className="btn-brand px-3 py-1.5 text-xs font-medium"
        >
          Try again
        </button>
      }
    />
  )
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
    <div className="flex min-h-56 items-center justify-center rounded-xl border border-theme-border bg-theme-surface px-6 py-8">
      <div className="flex w-full max-w-xl flex-col items-center text-center">
        {loading ? (
          <Loader2 className="h-8 w-8 animate-spin text-theme-text-tertiary" />
        ) : (
          <Gauge className="h-8 w-8 text-theme-text-tertiary" />
        )}
        <h2 className="mt-3 text-base font-semibold text-theme-text-primary">{title}</h2>
        <p className="mt-1 whitespace-pre-line text-sm text-theme-text-secondary">{body}</p>
        {action && <div className="mt-4">{action}</div>}
      </div>
    </div>
  )
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
  if (reason === 'scan_capacity_exceeded') return 'This scope is too large for one scan. Select fewer namespaces in the top bar and run again.'
  if (reason === 'scan_cancelled') return 'The scan was stopped before any workloads were evaluated. Run again to start a new scan.'
  if (reason === 'scan_deadline_exceeded') return 'The scan reached its three-minute time limit. Select fewer namespaces in the top bar and run again.'
  if (reason === 'scan_incomplete')
    return 'The scan could not evaluate any workloads in the current scope.'
  return 'Prometheus or kube-state-metrics does not expose the metrics required for this scan.'
}

function warningMessage(code: string): string {
  if (code === 'owner_metrics_query_failed') return 'Workload ownership data could not be queried.'
  if (code === 'oom_evidence_unavailable')
    return 'Radar could not verify restart history for some containers, so it withheld their memory reductions.'
  if (code.endsWith('_query_failed'))
    return `Some ${code
      .replace('_query_failed', '')
      .replaceAll('_', ' ')
      .replace(/\bcpu\b/g, 'CPU')} data could not be queried.`
  return 'Some rightsizing data was unavailable.'
}

function formatScanTime(value: string): string {
  const timestamp = new Date(value)
  return Number.isNaN(timestamp.getTime())
    ? value
    : timestamp.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })
}
