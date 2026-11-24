import type { ReactNode } from 'react'
import type { RightsizingRow } from '../../types/rightsizing'
import { formatRelativeAgeTime } from '../../utils/format'
import { Badge } from '../ui/Badge'
import { Collapse, CollapseChevron } from '../ui/Collapse'
import { currentPodEvidenceLabel, explainRightsizingReasons } from './reasons'
import {
  getResourceRequestPresentation,
  getRightsizingExplanation,
  type ResourceSignal,
  type RightsizingActionTone,
} from './presentation'
import { isReviewedIncrease, type RightsizingResultRow } from './results-model'

export type { RightsizingResultRow } from './results-model'

const gridColumns =
  'grid-cols-[minmax(230px,1.15fr)_minmax(190px,1fr)_minmax(200px,1fr)_minmax(220px,.9fr)_28px]'

export function RightsizingResultsTable<T extends RightsizingResultRow>({
  rows,
  openRowId,
  onOpenRowChange,
  renderWorkloadLink,
}: {
  rows: T[]
  openRowId: string | null
  onOpenRowChange: (id: string | null) => void
  renderWorkloadLink: (row: T) => ReactNode
}) {
  return (
    <div className="overflow-x-auto">
      <div className="min-w-[955px]">
        <div
          className={`grid ${gridColumns} gap-3 border-b border-theme-border px-3.5 py-2 text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary`}
        >
          <span>Workload / container</span>
          <span>CPU request</span>
          <span>Memory request</span>
          <span>Total request change</span>
          <span />
        </div>
        <div className="table-divide-subtle">
          {rows.map((row) => (
            <ScanResultRow
              key={row.id}
              row={row}
              open={openRowId === row.id}
              onToggle={() => onOpenRowChange(openRowId === row.id ? null : row.id)}
              workloadLink={openRowId === row.id && renderWorkloadLink(row)}
            />
          ))}
        </div>
      </div>
    </div>
  )
}

function ScanResultRow({
  row,
  open,
  onToggle,
  workloadLink,
}: {
  row: RightsizingResultRow
  open: boolean
  onToggle: () => void
  workloadLink: ReactNode
}) {
  return (
    <div>
      {row.workloadReason && (
        <p className="px-3.5 pt-2 text-xs text-theme-text-secondary">
          {row.name} · {row.workloadReason}
        </p>
      )}
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className={`grid w-full ${gridColumns} items-center gap-3 px-3.5 py-2.5 text-left hover:bg-theme-hover/50`}
      >
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <Badge kind={row.kind} size="sm">
              {row.kind}
            </Badge>
            <span className="truncate text-sm font-medium text-theme-text-primary">{row.name}</span>
          </div>
          <div
            className="mt-1 truncate text-xs text-theme-text-tertiary"
            title={
              row.clusterId && `${row.clusterId} / ${row.namespace} / ${row.container}`
            }
          >
            {row.clusterName && `${row.clusterName} · `}
            {row.namespace} · {row.container}
          </div>
          {row.source && (
            <div className="text-xs text-theme-text-tertiary">
              {row.source === 'datadog' ? 'Datadog' : 'Prometheus'}
              {row.observedAt && <> · Evaluated <time dateTime={row.observedAt} title={new Date(row.observedAt).toLocaleString()}>{formatRelativeAgeTime(row.observedAt)}</time></>}
            </div>
          )}
        </div>
        <ResourceRequestCell row={row.cpu} review={row.review?.cpu} scaledToZero={row.scaledToZero} />
        <ResourceRequestCell row={row.memory} review={row.review?.memory} scaledToZero={row.scaledToZero} />
        <div>
          {row.workloadImpact && (
            <div className="mb-1 text-xs font-medium text-theme-text-primary">
              Whole workload · {formatRequestChanges(row.workloadImpact).join(', ') || 'No net change'}
            </div>
          )}
          <ImpactCell row={row} />
          {row.source === 'prometheus' && (row.cpu || row.memory) && !(row.cpu ?? row.memory)?.decision &&
            <span className="block text-xs text-theme-text-tertiary">Earlier Radar policy</span>}
        </div>
        <CollapseChevron open={open} className="h-4 w-4" />
      </button>
      <Collapse open={open} mountLazily>
        <div className="grid gap-4 border-t border-theme-border bg-theme-base/40 px-4 py-3 md:grid-cols-[1fr_1fr_auto]">
          <FitWhy label="CPU" row={row.cpu} review={row.review?.cpu} source={row.source} />
          <FitWhy label="Memory" row={row.memory} review={row.review?.memory} source={row.source} />
          {workloadLink}
        </div>
      </Collapse>
    </div>
  )
}

function ResourceRequestCell({
  row,
  scaledToZero,
  review,
}: {
  review?: string
  row?: RightsizingRow
  scaledToZero: boolean
}) {
  const presentation = getResourceRequestPresentation(row, scaledToZero, review)
  return (
    <div className="min-w-0 text-xs">
      <div className="truncate font-medium tabular-nums text-theme-text-primary">{presentation.primary}</div>
      <div className={`mt-0.5 ${requestToneClass(presentation.tone)}`}>{presentation.secondary}</div>
      {presentation.signals.length > 0 && (
        <div className="mt-1 flex flex-wrap gap-1">
          {presentation.signals.map((signal) => (
            <SignalBadge key={signal} signal={signal} />
          ))}
        </div>
      )}
    </div>
  )
}

function requestToneClass(tone: RightsizingActionTone): string {
  if (tone === 'reduction') return 'text-accent-text'
  if (tone === 'increase') return 'text-warning-text'
  return 'text-theme-text-tertiary'
}

function ImpactCell({ row }: { row: RightsizingResultRow }) {
  if (row.classification === 'in_range') {
    return (
      <div className="text-xs text-theme-text-tertiary">
        {row.impact.cpuChange < 0 || row.impact.memoryChange < 0 ? 'Below action threshold' : 'No suggested change'}
      </div>
    )
  }
  if (row.classification === 'need_data' && row.source === 'datadog') {
    return <div className="text-xs text-theme-text-tertiary">Insufficient usage evidence</div>
  }
  const changes = formatRequestChanges(row.impact)
  return (
    <div className="min-w-0 text-xs text-theme-text-secondary">
      <div>
        {row.workloadImpact && 'This container · '}
        {row.replicas === 1 ? '1 replica' : `${row.replicas} replicas`}
      </div>
      {row.scaledToZero ? (
        <div className="mt-0.5 text-theme-text-tertiary">No current capacity impact</div>
      ) : changes.length > 0 ? (
        changes.map((change) => (
          <div key={change} className="mt-0.5 tabular-nums text-theme-text-tertiary">
            {change}
          </div>
        ))
      ) : (
        <div className="mt-0.5 text-theme-text-tertiary">
          {row.review?.cpu || row.review?.memory ? (isReviewedIncrease(row.cpu) || isReviewedIncrease(row.memory) ? 'Increase needs review' : 'Manual review required') : 'No suggested change'}
        </div>
      )}
      {changes.length > 0 && row.review?.cpu && <div>CPU request needs review</div>}
      {changes.length > 0 && row.review?.memory && <div>Memory request needs review</div>}
    </div>
  )
}

function SignalBadge({ signal }: { signal: ResourceSignal }) {
  return (
    <Badge
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
  )
}

function FitWhy({
  label,
  row,
  review,
  source,
}: {
  label: string
  row?: RightsizingRow
  review?: string
  source?: RightsizingResultRow['source']
}) {
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
    source === 'datadog'
      ? `${row.sampleCount} of ${row.expectedSamples} expected 5-minute samples (${Math.round(100 * row.coverage)}% coverage).`
      : row.coverage >= 0.95
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
        {row.queryError
          ? source === 'datadog'
            ? 'Datadog could not return these measurements. Open the workload to retry.'
            : source === 'prometheus'
              ? 'Prometheus could not return these measurements. Retry the scan.'
              : 'Metrics could not be queried, so usable history could not be determined. Retry the scan, or select fewer namespaces in the top bar.'
          : `${observation}${history}`}
      </p>
      <EvidenceNote row={row} review={review} />
    </div>
  )
}

function EvidenceNote({ row, review }: { row: RightsizingRow; review?: string }) {
  if (row.queryError) return null
  const guidance = review ?? requestChangeNote(row)
  let message = guidance ?? getRightsizingExplanation(row)
  if (guidance && row.liveInventoryUnavailable && row.resource === 'memory') {
    message +=
      " Radar could not read this workload's pods, so it could not check them for out-of-memory restarts."
  }
  return message ? <p className="mt-1 text-xs text-theme-text-tertiary">{message}</p> : null
}

function requestChangeNote(row: RightsizingRow): string | undefined {
  if (currentPodEvidenceLabel(row.recommendationReason)) return getRightsizingExplanation(row)
  if (row.fit === 'pod_level_request') return
  if (row.hpaManaged) return explainRightsizingReasons(['hpa_managed'], row.resource)
  if (row.limitConflict) {
    return explainRightsizingReasons([row.recommendationReason ?? 'recommended_request_exceeds_limit'])
  }
  if (row.fit === 'insufficient_history') return 'Wait for more runtime history before changing this request.'
  const isCpuReduction =
    row.resource === 'cpu' &&
    row.recommendedRequestValue != null &&
    row.currentRequestValue != null &&
    row.recommendedRequestValue < row.currentRequestValue
  if (isCpuReduction && (row.throttleRatio ?? 0) >= 0.1) {
    return 'CPU limit throttling was observed. Review the limit and burst behavior before reducing this request.'
  }
  if (isCpuReduction && row.bursty) {
    return 'CPU usage was bursty during the measured period. Review the peak before reducing this request.'
  }
  if (!row.calculatedAtMinimum && !row.recommendationReason && !row.reductionLimited && row.recommendedRequest) {
    return 'The target includes 15% headroom and is rounded to a practical request size.'
  }
}

function formatRequestChanges({
  cpuChange,
  memoryChange,
}: Pick<RightsizingResultRow['impact'], 'cpuChange' | 'memoryChange'>): string[] {
  const changes: string[] = []
  if (cpuChange !== 0) changes.push(formatCPUChange(cpuChange))
  if (memoryChange !== 0) changes.push(formatMemoryChange(memoryChange))
  return changes
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

function signalLabel(signal: ResourceSignal): string {
  return {
    hpa: 'HPA',
    oom: 'OOM restart',
    bursty: 'Bursty usage',
    throttling: 'Throttling',
    query_error: 'Metrics unavailable',
  }[signal]
}
