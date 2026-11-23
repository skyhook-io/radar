import { ArrowRight, Loader2 } from 'lucide-react'
import { Badge } from '../ui/Badge'
import { managerRefLabel } from '../../utils/gitops-owner'
import { getRightsizingExplanation, getRightsizingPresentation } from './presentation'
import { groupRightsizingRows, needsManualReview, rightsizingPolicyCoverage } from './results-model'
import {
  explainRightsizingReasons,
  rowReviewReasons,
} from './reasons'
import type {
  RightsizingFit,
  RightsizingRow,
  WorkloadRightsizingEvidence,
  DatadogSizingWorkload,
} from '../../types/rightsizing'

export function RightsizingEvidence({
  data,
  compact = false,
}: {
  data: (WorkloadRightsizingEvidence & { source: 'radar' }) | DatadogSizingWorkload
  compact?: boolean
}) {
  const manager = data.managedBy
  const managerLabel =
    manager?.name && manager.kind
      ? managerRefLabel({
          kind: manager.kind,
          namespace: manager.namespace ?? '',
          name: manager.name,
          group: manager.group,
        })
      : manager?.name
  const managerEvidence = !compact && manager && (
    <p className="text-xs text-theme-text-secondary">
      Detected manager · {manager.tool}
      {managerLabel && ` (${managerLabel})`} · {manager.signal}
    </p>
  )
  if (data.rows.length === 0 || (!data.sampleAvailable && data.source === 'radar')) {
    return (
      <>
        {managerEvidence}
        <RightsizingEmptyState text={data.reason ?? 'No rightsizing evidence is available.'} />
      </>
    )
  }

  if (rightsizingPolicyCoverage(data.rows, data.source) === 'incomplete') {
    return <RightsizingEmptyState text="Incomplete sizing decisions. Refresh the workload to evaluate it again." />
  }
  const byContainer = groupRightsizingRows(data.rows)

  if (compact) {
    return (
      <div className="space-y-2">
        {Array.from(byContainer.entries()).map(([container, rows]) => (
          <div key={container}>
            <div className="mb-1 text-xs font-medium text-theme-text-secondary">{container}</div>
            <div className="space-y-1 pl-2">
              {rows.map((row) => (
                <CompactFitRow key={row.resource} row={row} review={needsManualReview(row)} reasons={rowReviewReasons(row, row.decision?.reasons)} />
              ))}
            </div>
          </div>
        ))}
      </div>
    )
  }

  return (
    <div className="space-y-4">
      {managerEvidence}
      {data.reason && <p className="text-sm text-theme-text-secondary">{data.reason}</p>}
      {(data.scaledToZero || data.ownerCoverage === 'current_pods') && (
        <div className="flex flex-wrap gap-2">
          {data.scaledToZero && (
            <Badge severity="neutral" size="sm">
              No current replicas · showing retained evidence
            </Badge>
          )}
          {data.ownerCoverage === 'current_pods' && (
            <Badge severity="info" size="sm">
              Current pods only · confidence capped
            </Badge>
          )}
        </div>
      )}
      {Array.from(byContainer.entries()).map(([container, rows]) => (
        <div key={container}>
          <h4 className="mb-2 text-xs font-semibold text-theme-text-secondary">{container}</h4>
          <div className="grid gap-3 md:grid-cols-2">
            {rows.map((row) => (
              <FitCard
                key={row.resource}
                row={row}
                window={data.window}
                reasons={rowReviewReasons(row, row.decision?.reasons)}
                manualReview={!!row.recommendedRequest && needsManualReview(row)}
              />
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}

function FitCard({
  row,
  window,
  manualReview,
  reasons,
}: {
  row: RightsizingRow
  window: string
  manualReview?: boolean
  reasons?: string[]
}) {
  const additionalReasons = reasons?.filter(
    (reason) =>
      reason !== row.recommendationReason &&
      (!row.recommendationReason || reason !== 'recommendation_withheld'),
  )
  return (
    <div className="rounded-md border border-theme-border bg-theme-base/60 p-3">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
          {row.resource}
        </span>
        <div className="flex flex-wrap items-center justify-end gap-1.5">
          <FitBadge fit={row.fit} queryError={row.queryError} />
          {row.hpaManaged && (
            <Badge severity="info" size="sm">
              HPA-managed
            </Badge>
          )}
          {row.bursty && (
            <Badge severity="warning" size="sm">
              Bursty CPU
            </Badge>
          )}
          {row.throttleRatio != null && row.throttleRatio >= 0.1 && (
            <Badge severity="alert" size="sm">
              Throttling observed
            </Badge>
          )}
          {(row.currentPodOOM || row.windowOomEvidence) && (
            <Badge severity="error" size="sm">
              OOM evidence
            </Badge>
          )}
        </div>
      </div>
      <div className="flex items-baseline gap-2 tabular-nums">
        <div>
          <div className="text-[10px] uppercase tracking-wide text-theme-text-quaternary">
            {row.podLevelRequest ? 'Pod-level request' : 'Current request'}
          </div>
          <div className="text-lg font-semibold text-theme-text-primary">
            {row.podLevelRequest ?? row.currentRequest ?? 'Unset'}
          </div>
        </div>
        <ArrowRight className="h-4 w-4 text-theme-text-quaternary" />
        <div>
          <div className="text-[10px] uppercase tracking-wide text-theme-text-quaternary">
            {manualReview ? 'Candidate for manual review' : 'Suggested request'}
          </div>
          <div className="text-lg font-semibold text-theme-text-primary">
            {row.recommendedRequest ?? '—'}
          </div>
        </div>
      </div>
      <div className="mt-3 grid grid-cols-2 gap-3 border-t border-theme-border pt-3 text-xs">
        <Metric
          label={`${row.observed?.name ?? (row.resource === 'cpu' ? 'P95' : 'Max')} observed`}
          value={row.observed?.formatted ?? 'No samples'}
        />
        {row.peak && <Metric label={`${row.peak.name} peak`} value={row.peak.formatted} />}
        <Metric
          label={`Evidence · ${window}`}
          value={`${confidenceLabel(row)} · ${Math.round(row.coverage * 100)}% coverage`}
        />
        <Metric
          label="Five-minute samples"
          value={`${row.sampleCount} of ${row.expectedSamples}`}
        />
      </div>
      <FitExplanation row={row} manualReview={manualReview} />
      {!!additionalReasons?.length && (
        <p className="mt-1 text-xs text-theme-text-secondary">
          {explainRightsizingReasons(additionalReasons, row.resource)}
        </p>
      )}
    </div>
  )
}

function CompactFitRow({ row, review, reasons }: { row: RightsizingRow; review: boolean; reasons?: string[] }) {
  return (
    <div className="flex flex-wrap items-center gap-2 text-xs tabular-nums text-theme-text-tertiary">
      <span className="w-12 text-[10px] uppercase tracking-wide text-theme-text-quaternary">
        {row.resource}
      </span>
      <span className="min-w-14 text-theme-text-secondary">
        {row.podLevelRequest ? `pod ${row.podLevelRequest}` : (row.currentRequest ?? 'unset')}
      </span>
      {row.recommendedRequest && (
        <>
          <ArrowRight className="h-3 w-3 text-theme-text-quaternary" />
          <span className="min-w-14 text-theme-text-primary">{row.recommendedRequest}</span>
        </>
      )}
      {row.observed && (
        <span className="text-[10px] text-theme-text-quaternary">
          ({row.observed.name} {row.observed.formatted})
        </span>
      )}
      {review && <span className="text-warning-text">Needs review{reasons?.length ? ' · ' + explainRightsizingReasons(reasons, row.resource) : ''}</span>}
      <span className="ml-auto">
        <FitBadge fit={row.fit} queryError={row.queryError} />
      </span>
    </div>
  )
}

function FitBadge({ fit, queryError }: { fit: RightsizingFit; queryError?: string }) {
  const presentation = getRightsizingPresentation(fit, queryError)
  return (
    <Badge severity={presentation.severity} size="sm">
      {presentation.label}
    </Badge>
  )
}

function FitExplanation({ row, manualReview }: { row: RightsizingRow; manualReview?: boolean }) {
  const message = getRightsizingExplanation(row, manualReview)
  if (!message) return null
  return <p className="mt-3 text-xs text-theme-text-tertiary">{message}</p>
}


function confidenceLabel(row: RightsizingRow) {
  return `${row.confidence[0].toUpperCase()}${row.confidence.slice(1)} confidence`
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-theme-text-quaternary">{label}</div>
      <div className="mt-0.5 text-theme-text-secondary">{value}</div>
    </div>
  )
}

export function RightsizingEmptyState({
  text,
  loading = false,
}: {
  text: string
  loading?: boolean
}) {
  return (
    <div className="flex min-h-16 items-center justify-center text-sm text-theme-text-tertiary">
      {loading && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
      {text}
    </div>
  )
}
