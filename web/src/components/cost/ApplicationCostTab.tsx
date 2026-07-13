import { useEffect, useMemo, useState } from 'react'
import { AlertCircle, DollarSign, HelpCircle, Loader2, TrendingUp } from 'lucide-react'
import type { AppRow, AppWorkload } from '@skyhook-io/k8s-ui'
import {
  COST_DISCOVERY_GRACE_MS,
  useOpenCostApplicationCost,
  useOpenCostApplicationCostTrend,
  type CostTimeRange,
  type CostUnavailableReason,
  type OpenCostApplicationCostResponse,
  type OpenCostApplicationCostTrendResponse,
  type OpenCostApplicationWorkloadCost,
  type OpenCostTrendSeries,
} from '../../api/client'
import { Tooltip } from '../ui/Tooltip'
import { ChartLegend, CostTimeRangeSelector, StackedAreaChart } from './CostTrendChart'
import {
  formatCostPerHour,
  formatHistoricalSpend,
  formatProjectedDailyRate,
  formatProjectedMonthlyCost,
  formatProjectedMonthlyRate,
} from './format'
import { isOpenCostWorkloadKind } from './kinds'
import { CurrentAllocationUse } from './CurrentAllocationUse'
import { costUnavailableReasonFromError } from './errors'

type ApplicationCostState =
  | 'loading'
  | 'data'
  | 'partial_missing_history'
  | 'partial_missing_current'
  | 'zero'
  | 'load_error'
  | CostUnavailableReason

interface ApplicationCostQueryStatus {
  currentLoading?: boolean
  trendLoading?: boolean
  currentError?: unknown
  trendError?: unknown
}

interface ApplicationCostTabProps {
  app: AppRow
  workloads: AppWorkload[]
  onSelectWorkloadCost?: (workload: AppWorkload) => void
}

export function ApplicationCostTab({
  app,
  workloads,
  onSelectWorkloadCost,
}: ApplicationCostTabProps) {
  const [range, setRange] = useState<CostTimeRange>('24h')
  const [noPrometheusSince, setNoPrometheusSince] = useState<number | null>(null)
  const supportedWorkloads = useMemo(() => applicationCostWorkloads(workloads), [workloads])
  const unsupportedCount = workloads.length - supportedWorkloads.length
  const queriesEnabled = supportedWorkloads.length > 0
  const currentQuery = useOpenCostApplicationCost(supportedWorkloads, {
    enabled: queriesEnabled,
  })
  const trendQuery = useOpenCostApplicationCostTrend(supportedWorkloads, range, {
    enabled: queriesEnabled,
  })
  const trendMatchesRange = trendQuery.data?.range === range
  const trendData = trendMatchesRange ? trendQuery.data : undefined
  const trendLoading =
    trendQuery.isLoading ||
    (trendQuery.isFetching && Boolean(trendQuery.data) && !trendMatchesRange)
  const state = getApplicationCostState(currentQuery.data, trendData, {
    currentLoading: currentQuery.isLoading,
    trendLoading,
    currentError: currentQuery.error,
    trendError: trendQuery.error,
  })

  useEffect(() => {
    if (state === 'no_prometheus') {
      setNoPrometheusSince((prev) => prev ?? Date.now())
    } else {
      setNoPrometheusSince(null)
    }
  }, [state])

  const chartSeries = useMemo(() => applicationChartSeries(trendData), [trendData])
  const workloadByKey = useMemo(() => {
    const map = new Map<string, AppWorkload>()
    for (const workload of workloads) map.set(applicationCostKey(workload), workload)
    return map
  }, [workloads])

  if (supportedWorkloads.length === 0) {
    return (
      <ApplicationCostUnavailable
        state="no_metrics"
        message="No steady-state workloads in this app are currently cost-attributed."
      />
    )
  }

  if (state === 'loading') {
    return (
      <div className="flex h-full min-h-[320px] items-center justify-center text-theme-text-tertiary">
        <Loader2 className="mr-2 h-5 w-5 animate-spin" />
        Loading application cost…
      </div>
    )
  }

  if (
    state === 'no_prometheus' ||
    state === 'no_metrics' ||
    state === 'query_error' ||
    state === 'access_denied' ||
    state === 'not_found' ||
    state === 'load_error'
  ) {
    const discoveryAgeMs = noPrometheusSince == null ? 0 : Date.now() - noPrometheusSince
    if (state === 'no_prometheus' && discoveryAgeMs < COST_DISCOVERY_GRACE_MS) {
      return (
        <ApplicationCostDiscovering
          isFetching={currentQuery.isFetching || trendQuery.isFetching}
          onRetry={() => {
            setNoPrometheusSince(Date.now())
            currentQuery.refetch()
            trendQuery.refetch()
          }}
        />
      )
    }
    return <ApplicationCostUnavailable state={state} />
  }

  const current = currentQuery.data
  const trend = trendData
  const hasCurrent = current?.available === true && (current.coverage?.included ?? 0) > 0
  const totals = hasCurrent ? current?.totals : undefined
  const coverage =
    state === 'partial_missing_current'
      ? (trend?.coverage ?? current?.coverage)
      : (current?.coverage ?? trend?.coverage)
  const included = coverage?.included ?? 0
  const total = (coverage?.total ?? supportedWorkloads.length) + unsupportedCount
  const unavailableCount = coverage?.unavailable?.length ?? 0
  const hourly = totals?.hourlyCost ?? 0
  const points = trend?.available ? (trend.dataPoints ?? []) : []
  const hasTrend = points.length >= 2 && points.some((p) => p.value > 0)
  const rows = current?.workloads ?? []
  const maxCost = Math.max(...rows.map((row) => row.current?.hourlyCost ?? 0), 0)

  return (
    <div className="mx-auto w-full max-w-[1600px] space-y-4">
      {(current?.partial ||
        trend?.partial ||
        state === 'partial_missing_history' ||
        state === 'partial_missing_current' ||
        unsupportedCount > 0) && (
        <div className="flex items-start gap-2 rounded-lg border border-theme-border bg-theme-base px-3 py-2 text-sm text-theme-text-secondary">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-theme-text-tertiary" />
          <span>
            Showing tracked steady-state compute for {included} of {total} workloads.
            {unavailableCount > 0
              ? ` ${unavailableCount} workload${unavailableCount === 1 ? '' : 's'} could not be included for this window.`
              : ''}
            {unsupportedCount > 0
              ? ` ${unsupportedCount} batch or unsupported workload${unsupportedCount === 1 ? ' is' : 's are'} excluded from this compute view.`
              : ''}
          </span>
        </div>
      )}

      <section
        className="rounded-lg border border-theme-border bg-theme-surface/50"
        aria-label={`${app.name} application cost`}
      >
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-theme-border px-4 py-3">
          <div className="flex items-center gap-2">
            <TrendingUp className="h-4 w-4 text-theme-text-tertiary" />
            <div>
              <div className="flex items-center gap-1.5">
                <div className="text-sm font-semibold text-theme-text-primary">
                  Application compute cost
                </div>
                <CostInfoTooltip content="Dollars are based on OpenCost CPU and memory allocation over time, grouped by the workloads in this application. OpenCost allocation uses the greater of requested or observed resources." />
              </div>
              <div className="text-xs text-theme-text-tertiary">
                OpenCost CPU and memory allocation rate ($/hr) for Deployment, StatefulSet, and
                DaemonSet workloads
              </div>
            </div>
          </div>
          <CostTimeRangeSelector value={range} onChange={setRange} />
        </div>

        <div className="grid gap-4 p-4 lg:grid-cols-[240px_minmax(0,1fr)]">
          <div className="space-y-4">
            <CostMetricBlock
              label={`Spend over ${range}`}
              value={formatHistoricalSpend(
                points.length,
                trend?.windowTotalCost ?? 0,
                trendLoading || state === 'partial_missing_history',
              )}
              subvalue={
                state === 'partial_missing_history'
                  ? 'Historical data incomplete'
                  : `${included} of ${total} workloads included`
              }
            />
            <CostMetricBlock
              label="Projected monthly"
              value={totals ? formatProjectedMonthlyCost(hourly) : '—'}
              subvalue={
                totals
                  ? `${formatCostPerHour(hourly)} current rate`
                  : 'Current allocation unavailable'
              }
            />
          </div>
          <div className="min-w-0">
            {trendLoading ? (
              <div className="flex h-[240px] items-center justify-center rounded-md border border-dashed border-theme-border bg-theme-base/60 text-sm text-theme-text-tertiary">
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                Loading historical cost…
              </div>
            ) : hasTrend && chartSeries.length > 0 ? (
              <div className="min-w-0">
                <StackedAreaChart series={chartSeries} />
                <ChartLegend series={chartSeries} />
              </div>
            ) : (
              <div className="flex h-[240px] items-center justify-center rounded-md border border-dashed border-theme-border bg-theme-base/60 text-sm text-theme-text-tertiary">
                No historical workload owner cost points for this range.
              </div>
            )}
          </div>
        </div>
      </section>

      <div className="grid gap-4 md:grid-cols-2">
        <CostMetricTile
          label="Tracked workloads"
          value={`${included}/${total}`}
          subvalue={
            unsupportedCount > 0
              ? `${unsupportedCount} unsupported`
              : 'All supported workloads included'
          }
        />
        <CostMetricTile
          label="Projected daily"
          value={totals ? formatProjectedDailyRate(hourly) : '—'}
          subvalue={
            totals
              ? `${formatCostPerHour(hourly)} current hourly rate`
              : 'Current allocation unavailable'
          }
        />
      </div>

      <CurrentAllocationUse
        dataAvailable={Boolean(totals)}
        cpuCost={totals?.cpuCost ?? 0}
        memoryCost={totals?.memoryCost ?? 0}
        hourlyCost={hourly}
        cpuAllocationUse={totals?.cpuAllocationUse ?? 0}
        memoryAllocationUse={totals?.memoryAllocationUse ?? 0}
        cpuUsageAvailable={totals?.cpuUsageAvailable ?? false}
        memoryUsageAvailable={totals?.memoryUsageAvailable ?? false}
        scopeNote="Included workloads only"
      />

      <section className="rounded-lg border border-theme-border bg-theme-surface/50">
        <div className="flex items-center justify-between border-b border-theme-border px-4 py-3">
          <div>
            <div className="text-sm font-semibold text-theme-text-primary">
              Workload contributors
            </div>
            <div className="text-xs text-theme-text-tertiary">
              Projected monthly from current allocation, sorted by spend
            </div>
          </div>
          <div className="text-xs text-theme-text-tertiary">{rows.length} tracked</div>
        </div>
        {rows.length === 0 ? (
          <div className="px-4 py-6 text-sm text-theme-text-tertiary">
            No workload cost rows available for this application.
          </div>
        ) : (
          <div className="divide-y divide-theme-border/60">
            {rows.map((row) => {
              const appWorkload = workloadByKey.get(applicationCostKey(row))
              return (
                <ApplicationWorkloadCostRow
                  key={applicationCostKey(row)}
                  row={row}
                  maxCost={maxCost}
                  onOpen={
                    appWorkload && onSelectWorkloadCost
                      ? () => onSelectWorkloadCost(appWorkload)
                      : undefined
                  }
                />
              )
            })}
          </div>
        )}
      </section>

      <div className="text-xs text-theme-text-tertiary">
        Powered by OpenCost via Prometheus. Historical spend uses the selected range; projected
        monthly values multiply current hourly allocation. Batch/job cost is separate; storage/PVC
        and network costs remain at namespace and cluster level.
      </div>
    </div>
  )
}

export function getApplicationCostState(
  current: OpenCostApplicationCostResponse | undefined,
  trend: OpenCostApplicationCostTrendResponse | undefined,
  status: ApplicationCostQueryStatus,
): ApplicationCostState {
  const loading = Boolean(status.currentLoading || status.trendLoading)
  const queryError = Boolean(status.currentError || status.trendError)
  const currentHasData = current?.available === true && (current.coverage?.included ?? 0) > 0
  const trendHasData =
    trend?.available === true && (trend.dataPoints ?? []).some((p) => p.value > 0)
  if (currentHasData) {
    if (status.trendLoading && !trend) return 'data'
    if (status.trendError || (trend?.available === false && trend.reason !== 'no_metrics'))
      return 'partial_missing_history'
    if ((current?.totals?.hourlyCost ?? 0) === 0 && !trendHasData) return 'zero'
    if (!trend?.available) return 'partial_missing_history'
    return 'data'
  }
  if (trendHasData) return 'partial_missing_current'
  const reason =
    current?.reason ??
    trend?.reason ??
    costUnavailableReasonFromError(status.currentError) ??
    costUnavailableReasonFromError(status.trendError)
  if (
    reason === 'no_prometheus' ||
    reason === 'query_error' ||
    reason === 'access_denied' ||
    reason === 'not_found'
  )
    return reason
  if (queryError) return 'load_error'
  if (loading) return 'loading'

  return 'no_metrics'
}

export function applicationCostWorkloads(workloads: AppWorkload[]): AppWorkload[] {
  return workloads.filter((workload) => isOpenCostWorkloadKind(workload.kind))
}

function ApplicationWorkloadCostRow({
  row,
  maxCost,
  onOpen,
}: {
  row: OpenCostApplicationWorkloadCost
  maxCost: number
  onOpen?: () => void
}) {
  const current = row.current
  const hourly = current?.hourlyCost ?? 0
  const cpuPct = hourly > 0 ? ((current?.cpuCost ?? 0) / hourly) * 100 : 0
  const barWidth = maxCost > 0 ? Math.max((hourly / maxCost) * 100, 3) : 0
  const content = (
    <>
      <div className="min-w-0">
        <div className="flex min-w-0 items-center gap-2">
          <span className="shrink-0 rounded bg-theme-base px-1.5 py-0.5 text-[10px] uppercase text-theme-text-tertiary">
            {row.kind}
          </span>
          <Tooltip content={`${row.kind} ${row.namespace}/${row.name}`} wrapperClassName="min-w-0">
            <span className="block truncate text-sm font-medium text-theme-text-primary">
              {row.name}
            </span>
          </Tooltip>
          <Tooltip content={row.namespace} wrapperClassName="shrink-0">
            <span className="text-xs text-theme-text-tertiary">{row.namespace}</span>
          </Tooltip>
        </div>
        {!row.available && (
          <div className="mt-0.5 text-xs text-theme-text-tertiary">{reasonLabel(row.reason)}</div>
        )}
      </div>
      <div className="text-right text-sm font-medium tabular-nums text-theme-text-primary">
        {current ? formatProjectedMonthlyRate(hourly) : '—'}
      </div>
      <div className="hidden text-right text-xs tabular-nums text-theme-text-tertiary sm:block">
        {current ? formatCostPerHour(hourly) : '—'}
      </div>
      <div className="hidden min-w-0 items-center gap-2 md:flex">
        <div
          className="h-1.5 flex-1 overflow-hidden rounded-full bg-theme-hover"
          style={{ maxWidth: `${barWidth}%` }}
        >
          <div className="flex h-full">
            <div className="h-full bg-accent" style={{ width: `${cpuPct}%` }} />
            <div className="h-full bg-amber-500" style={{ width: `${100 - cpuPct}%` }} />
          </div>
        </div>
      </div>
      <div className="hidden text-right text-xs tabular-nums text-theme-text-tertiary lg:block">
        {current
          ? `${formatProjectedMonthlyCost(current.cpuCost)} / ${formatProjectedMonthlyCost(current.memoryCost)}`
          : '—'}
      </div>
    </>
  )

  if (!onOpen) {
    return (
      <div className="grid grid-cols-[minmax(180px,1fr)_100px] gap-3 px-4 py-3 sm:grid-cols-[minmax(180px,1fr)_100px_100px] md:grid-cols-[minmax(220px,1fr)_100px_100px_minmax(160px,1fr)] lg:grid-cols-[minmax(220px,1fr)_110px_110px_minmax(180px,1fr)_130px]">
        {content}
      </div>
    )
  }
  return (
    <button
      type="button"
      onClick={onOpen}
      className="grid w-full grid-cols-[minmax(180px,1fr)_100px] gap-3 px-4 py-3 text-left transition-colors hover:bg-theme-hover sm:grid-cols-[minmax(180px,1fr)_100px_100px] md:grid-cols-[minmax(220px,1fr)_100px_100px_minmax(160px,1fr)] lg:grid-cols-[minmax(220px,1fr)_110px_110px_minmax(180px,1fr)_130px]"
    >
      {content}
    </button>
  )
}

function applicationChartSeries(
  trend: OpenCostApplicationCostTrendResponse | undefined,
): OpenCostTrendSeries[] {
  return (trend?.series ?? [])
    .filter((series) => (series.dataPoints ?? []).length >= 2)
    .map((series) => ({
      namespace: `${series.name} (${series.kind})`,
      dataPoints: series.dataPoints ?? [],
    }))
}

function applicationCostKey(ref: { kind: string; namespace: string; name: string }) {
  return `${ref.kind}/${ref.namespace}/${ref.name}`
}

function reasonLabel(reason?: CostUnavailableReason) {
  if (reason === 'no_prometheus') return 'Prometheus not found'
  if (reason === 'query_error') return 'Cost query failed'
  if (reason === 'access_denied') return 'No access to this workload'
  if (reason === 'not_found') return 'Workload not found'
  return 'No workload cost metrics'
}

function ApplicationCostDiscovering({
  isFetching,
  onRetry,
}: {
  isFetching: boolean
  onRetry: () => void
}) {
  return (
    <div className="flex h-full min-h-[320px] items-center justify-center">
      <div className="flex max-w-md flex-col items-center gap-3 text-center text-theme-text-secondary">
        <Loader2 className="h-8 w-8 animate-spin text-theme-text-tertiary/60" />
        <div>
          <p className="text-sm font-medium text-theme-text-primary">
            Looking for Prometheus cost data…
          </p>
          <p className="mt-1 text-xs text-theme-text-tertiary">
            First discovery can take a few seconds while Radar checks cluster services and opens a
            local port-forward.
          </p>
        </div>
        <button
          onClick={onRetry}
          disabled={isFetching}
          className="text-xs text-accent-text transition-colors hover:text-theme-text-primary disabled:cursor-not-allowed disabled:text-theme-text-disabled"
        >
          {isFetching ? 'Checking…' : 'Check again'}
        </button>
      </div>
    </div>
  )
}

function ApplicationCostUnavailable({
  state,
  message,
}: {
  state: CostUnavailableReason | 'load_error'
  message?: string
}) {
  const text =
    message ??
    (state === 'no_prometheus'
      ? 'Prometheus not found. OpenCost application cost requires Prometheus or VictoriaMetrics.'
      : state === 'query_error'
        ? 'Cost data is temporarily unavailable. Prometheus was found, but application cost queries failed.'
        : state === 'access_denied'
          ? 'Cost data is unavailable because these workloads are not accessible with your current permissions.'
          : state === 'not_found'
            ? 'Cost data is unavailable because the referenced workloads no longer exist.'
            : state === 'load_error'
              ? 'Could not load application cost data. Check access to these workloads and try again.'
              : 'OpenCost workload metrics were not found for this application.')
  return (
    <div className="flex h-full min-h-[320px] items-center justify-center">
      <div className="flex max-w-md flex-col items-center gap-3 text-center text-theme-text-secondary">
        <DollarSign className="h-8 w-8 text-theme-text-tertiary/50" />
        <div className="text-sm">{text}</div>
      </div>
    </div>
  )
}

function CostMetricBlock({
  label,
  value,
  subvalue,
}: {
  label: string
  value: string
  subvalue?: string
}) {
  return (
    <div>
      <div className="text-xs font-medium uppercase text-theme-text-tertiary">{label}</div>
      <div className="mt-1 text-2xl font-semibold text-theme-text-primary tabular-nums">
        {value}
      </div>
      {subvalue && <div className="mt-1 text-xs text-theme-text-tertiary">{subvalue}</div>}
    </div>
  )
}

function CostMetricTile({
  label,
  value,
  subvalue,
  tooltip,
}: {
  label: string
  value: string
  subvalue?: string
  tooltip?: string
}) {
  return (
    <div className="rounded-lg border border-theme-border bg-theme-surface/50 p-4">
      <div className="flex items-center gap-1.5">
        <div className="text-xs font-medium uppercase text-theme-text-tertiary">{label}</div>
        {tooltip && <CostInfoTooltip content={tooltip} />}
      </div>
      <div className="mt-1 text-lg font-semibold text-theme-text-primary tabular-nums">{value}</div>
      {subvalue && <div className="mt-1 text-xs text-theme-text-tertiary">{subvalue}</div>}
    </div>
  )
}

function CostInfoTooltip({ content }: { content: string }) {
  return (
    <Tooltip content={content} className="max-w-[280px] whitespace-normal text-left" delay={150}>
      <HelpCircle className="h-3.5 w-3.5 cursor-help text-theme-text-tertiary transition-colors hover:text-theme-text-secondary" />
    </Tooltip>
  )
}
