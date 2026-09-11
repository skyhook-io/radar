import { useEffect, useState } from 'react'
import { AlertCircle, Coins, HelpCircle, Loader2, TrendingUp } from 'lucide-react'
import {
  useOpenCostWorkload,
  useOpenCostWorkloadTrend,
  COST_DISCOVERY_GRACE_MS,
  type CostTimeRange,
  type CostUnavailableReason,
  type OpenCostWorkloadDetailResponse,
  type OpenCostWorkloadTrendResponse,
} from '../../api/client'
import { Tooltip } from '../ui/Tooltip'
import { CostTimeRangeSelector, StackedAreaChart } from './CostTrendChart'
import {
  DEFAULT_COST_CURRENCY,
  formatCostPerHour,
  formatHistoricalSpend,
  formatProjectedDailyRate,
  formatProjectedMonthlyCost,
} from './format'
import { CurrentAllocationUse } from './CurrentAllocationUse'
import { costUnavailableReasonFromError } from './errors'
import {
  costFreshnessLabel,
  costIntegrationUnavailableMessage,
  costRateLabels,
  costSourceLabel,
  isCostDiscoveryPending,
} from './source'
import { useNavCustomization } from '../../context/NavCustomization'

type WorkloadCostState =
  | 'loading'
  | 'data'
  | 'partial_missing_history'
  | 'partial_missing_current'
  | 'zero'
  | 'load_error'
  | CostUnavailableReason

interface WorkloadCostQueryStatus {
  currentLoading?: boolean
  trendLoading?: boolean
  currentError?: unknown
  trendError?: unknown
}

interface WorkloadCostTabProps {
  kind: string
  namespace: string
  name: string
}

export function WorkloadCostTab({ kind, namespace, name }: WorkloadCostTabProps) {
  const settingsAvailable = !useNavCustomization().embedded
  const [range, setRange] = useState<CostTimeRange>('24h')
  const [discoverySince, setDiscoverySince] = useState<number | null>(null)
  const currentQuery = useOpenCostWorkload(kind, namespace, name)
  const trendQuery = useOpenCostWorkloadTrend(kind, namespace, name, range)
  const trendMatchesRange = trendQuery.data?.range === range
  const trendData = trendMatchesRange ? trendQuery.data : undefined
  const trendLoading =
    trendQuery.isLoading ||
    (trendQuery.isFetching && Boolean(trendQuery.data) && !trendMatchesRange)

  const state = getWorkloadCostState(currentQuery.data, trendData, {
    currentLoading: currentQuery.isLoading,
    trendLoading,
    currentError: currentQuery.error,
    trendError: trendQuery.error,
  })

  useEffect(() => {
    if (isCostDiscoveryPending(state)) {
      setDiscoverySince((prev) => prev ?? Date.now())
    } else {
      setDiscoverySince(null)
    }
  }, [state])

  if (state === 'loading') {
    return (
      <div className="flex h-full min-h-[320px] items-center justify-center text-theme-text-tertiary">
        <Loader2 className="mr-2 h-5 w-5 animate-spin" />
        Loading workload cost…
      </div>
    )
  }

  if (
    state === 'no_prometheus' ||
    state === 'no_metrics' ||
    state === 'query_error' ||
    state === 'access_denied' ||
    state === 'not_found' ||
    state === 'no_cost_source' ||
    state === 'source_unavailable' ||
    state === 'authentication_error' ||
    state === 'configuration_mismatch' ||
    state === 'deployment_configuration_error' ||
    state === 'history_unsupported' ||
    state === 'load_error'
  ) {
    const discoveryAgeMs = discoverySince == null ? 0 : Date.now() - discoverySince
    if (isCostDiscoveryPending(state) && discoveryAgeMs < COST_DISCOVERY_GRACE_MS) {
      return (
        <WorkloadCostDiscovering
          isFetching={currentQuery.isFetching || trendQuery.isFetching}
          onRetry={() => {
            setDiscoverySince(Date.now())
            currentQuery.refetch()
            trendQuery.refetch()
          }}
        />
      )
    }
    return <WorkloadCostUnavailable state={state} settingsAvailable={settingsAvailable} />
  }

  const current = currentQuery.data?.current
  const trend = trendData
  const points = trend?.available ? (trend.dataPoints ?? []) : []
  const hasTrend = points.length >= 2 && points.some((p) => p.value > 0)
  const hasCurrent = Boolean(current)
  const hourly = current?.hourlyCost ?? 0
  const windowTotal = trend?.available ? (trend.windowTotalCost ?? 0) : 0
  const cpuCost = current?.cpuCost ?? 0
  const memoryCost = current?.memoryCost ?? 0
  const currentCurrency = currentQuery.data?.currency ?? trend?.currency ?? DEFAULT_COST_CURRENCY
  const trendCurrency = trend?.currency ?? currentQuery.data?.currency ?? DEFAULT_COST_CURRENCY
  const windowSpendValue = formatHistoricalSpend(
    points.length,
    windowTotal,
    trendLoading || state === 'partial_missing_history',
    trendCurrency,
  )
  const source = currentQuery.data?.source ?? trend?.source
  const historyUnsupported = trend?.reason === 'history_unsupported'
  const currentWindow = currentQuery.data?.window
  const rateLabels = costRateLabels(currentWindow)

  return (
    <div className="mx-auto w-full max-w-[1600px] space-y-4">
      <section className="rounded-lg border border-theme-border bg-theme-surface/50">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-theme-border px-4 py-3">
          <div className="flex items-center gap-2">
            <TrendingUp className="h-4 w-4 text-theme-text-tertiary" />
            <div>
              <div className="flex items-center gap-1.5">
                <div className="text-sm font-semibold text-theme-text-primary">
                  {historyUnsupported ? 'Compute allocation cost' : 'Historical compute cost'}
                </div>
                <MetricInfoTooltip content="Values are based on CPU and memory allocation, not raw utilization. The cost provider attributes the greater of requested or observed resources." />
              </div>
              <div className="text-xs text-theme-text-tertiary">
                CPU and memory allocation rate ({trendCurrency}/hr) attributed by workload ownership
              </div>
            </div>
          </div>
          {!historyUnsupported && <CostTimeRangeSelector value={range} onChange={setRange} />}
        </div>

        {historyUnsupported ? (
          <div className="grid gap-4 p-4 sm:grid-cols-2">
            <MetricBlock
              label={rateLabels.hourly}
              value={hasCurrent ? formatCostPerHour(hourly, currentCurrency) : '—'}
              subvalue="CPU and memory allocation"
            />
            <MetricBlock
              label="Projected monthly"
              value={hasCurrent ? formatProjectedMonthlyCost(hourly, currentCurrency) : '—'}
              subvalue={`${rateLabels.rate} × 730 hours`}
            />
          </div>
        ) : (
          <div className="grid gap-4 p-4 lg:grid-cols-[220px_minmax(0,1fr)]">
            <div className="space-y-4">
              <MetricBlock
                label={`Spend over ${range}`}
                value={windowSpendValue}
                subvalue={
                  state === 'partial_missing_history' ? 'Historical data unavailable' : undefined
                }
              />
              <MetricBlock
                label="Projected monthly"
                value={hasCurrent ? formatProjectedMonthlyCost(hourly, currentCurrency) : '—'}
                subvalue={
                  hasCurrent
                    ? `${formatCostPerHour(hourly, currentCurrency)} · ${rateLabels.rate}`
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
              ) : hasTrend ? (
                <StackedAreaChart
                  series={[{ namespace: 'Allocation rate', dataPoints: points }]}
                  currency={trendCurrency}
                />
              ) : (
                <div className="flex h-[240px] items-center justify-center rounded-md border border-dashed border-theme-border bg-theme-base/60 text-sm text-theme-text-tertiary">
                  No historical workload owner cost points for this range.
                </div>
              )}
            </div>
          </div>
        )}
      </section>

      {state === 'partial_missing_history' && !historyUnsupported && (
        <div className="flex items-start gap-2 rounded-lg border border-theme-border bg-theme-base px-3 py-2 text-sm text-theme-text-secondary">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-theme-text-tertiary" />
          <span>Current cost is available, but historical workload owner metrics are not available for this range.</span>
        </div>
      )}
      {state === 'partial_missing_current' && (
        <div className="flex items-start gap-2 rounded-lg border border-theme-border bg-theme-base px-3 py-2 text-sm text-theme-text-secondary">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-theme-text-tertiary" />
          <span>
            Historical cost is available, but current workload allocation metrics are not available.
          </span>
        </div>
      )}

      <div className="grid gap-4 md:grid-cols-2">
        <MetricTile label="Live replicas" value={hasCurrent ? String(current?.replicas ?? 0) : '—'} />
        <MetricTile
          label="Projected daily"
          value={hasCurrent ? formatProjectedDailyRate(hourly, currentCurrency) : '—'}
          subvalue={
            hasCurrent
              ? `${formatCostPerHour(hourly, currentCurrency)} · ${rateLabels.rate}`
              : 'Current allocation unavailable'
          }
        />
      </div>

      <CurrentAllocationUse
        currency={currentCurrency}
        dataAvailable={hasCurrent}
        cpuCost={cpuCost}
        memoryCost={memoryCost}
        hourlyCost={hourly}
        cpuAllocationUse={current?.cpuAllocationUse ?? 0}
        memoryAllocationUse={current?.memoryAllocationUse ?? 0}
        cpuUsageAvailable={current?.cpuUsageAvailable ?? false}
        memoryUsageAvailable={current?.memoryUsageAvailable ?? false}
        window={source === 'kubecost' ? currentWindow : undefined}
      />

      <div className="text-xs text-theme-text-tertiary">
        {costSourceLabel(source)} &middot; {costFreshnessLabel(source, source === 'kubecost' ? currentWindow : '1h', currentQuery.data?.dataThrough)}.{' '}
        {currentCurrency !== DEFAULT_COST_CURRENCY && (
          <>Labeled {currentCurrency}; no conversion. </>
        )}
        {historyUnsupported
          ? 'Historical workload charts are not available for Kubecost yet. '
          : 'Historical spend uses the selected range. '}
        Projected monthly values multiply the {rateLabels.rate}. Storage/PVC attribution
        remains at namespace and cluster level.
      </div>
    </div>
  )
}

export function getWorkloadCostState(
  current: OpenCostWorkloadDetailResponse | undefined,
  trend: OpenCostWorkloadTrendResponse | undefined,
  status: boolean | WorkloadCostQueryStatus,
): WorkloadCostState {
  const queryStatus: WorkloadCostQueryStatus =
    typeof status === 'boolean' ? { currentLoading: status, trendLoading: status } : status
  const loading = Boolean(queryStatus.currentLoading || queryStatus.trendLoading)
  const queryError = Boolean(queryStatus.currentError || queryStatus.trendError)

  const currentRow = current?.available ? current.current : undefined
  const trendHasData =
    trend?.available === true && (trend.dataPoints ?? []).some((p) => p.value > 0)
  if (currentRow) {
    if (queryStatus.trendLoading && !trend) return 'data'
    if (queryStatus.trendError || (trend?.available === false && trend.reason !== 'no_metrics'))
      return 'partial_missing_history'
    if (currentRow.hourlyCost === 0 && currentRow.replicas === 0 && !trendHasData) return 'zero'
    if (!trend?.available) return 'partial_missing_history'
    return 'data'
  }
  if (trendHasData) return 'partial_missing_current'
  const reason =
    current?.reason ??
    costUnavailableReasonFromError(queryStatus.currentError) ??
    trend?.reason ??
    costUnavailableReasonFromError(queryStatus.trendError)
  if (
    reason === 'no_prometheus' ||
    reason === 'no_cost_source' ||
    reason === 'query_error' ||
    reason === 'access_denied' ||
    reason === 'not_found' ||
    reason === 'source_unavailable' ||
    reason === 'authentication_error' ||
    reason === 'configuration_mismatch' ||
    reason === 'deployment_configuration_error'
  )
    return reason
  if (queryError) return 'load_error'
  if (loading) return 'loading'

  return 'no_metrics'
}

function WorkloadCostDiscovering({
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
            Looking for cost data…
          </p>
          <p className="mt-1 text-xs text-theme-text-tertiary">
            Radar is checking OpenCost metrics in a PromQL-compatible backend and a local Kubecost
            3 Aggregator. First discovery can take a few seconds.
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

function WorkloadCostUnavailable({
  state,
  settingsAvailable,
}: {
  state: CostUnavailableReason | 'load_error'
  settingsAvailable: boolean
}) {
  const message =
    costIntegrationUnavailableMessage(state, settingsAvailable) ??
    (state === 'no_prometheus'
      ? 'No compatible metrics backend was found. OpenCost workload cost requires OpenCost metrics in a PromQL-compatible backend.'
      : state === 'query_error'
        ? 'Cost data is temporarily unavailable. A metrics backend was found, but workload cost queries failed.'
        : state === 'history_unsupported'
          ? 'Historical workload cost is not available for Kubecost yet.'
        : state === 'access_denied'
          ? 'You do not have access to view cost for this workload.'
          : state === 'not_found'
            ? 'This workload no longer exists.'
            : state === 'load_error'
              ? 'Could not load workload cost data. Check access to this workload and try again.'
              : 'No workload cost data was returned for this workload by the active cost source.')

  return (
    <div className="flex h-full min-h-[320px] items-center justify-center">
      <div className="flex max-w-md flex-col items-center gap-3 text-center text-theme-text-secondary">
        <Coins className="h-8 w-8 text-theme-text-tertiary/50" />
        <div className="text-sm">{message}</div>
      </div>
    </div>
  )
}

function MetricBlock({
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

function MetricTile({
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
        {tooltip && <MetricInfoTooltip content={tooltip} />}
      </div>
      <div className="mt-1 text-lg font-semibold text-theme-text-primary tabular-nums">{value}</div>
      {subvalue && <div className="mt-1 text-xs text-theme-text-tertiary">{subvalue}</div>}
    </div>
  )
}

function MetricInfoTooltip({ content }: { content: string }) {
  return (
    <Tooltip content={content} className="max-w-[280px] whitespace-normal text-left" delay={150}>
      <HelpCircle className="h-3.5 w-3.5 cursor-help text-theme-text-tertiary transition-colors hover:text-theme-text-secondary" />
    </Tooltip>
  )
}
