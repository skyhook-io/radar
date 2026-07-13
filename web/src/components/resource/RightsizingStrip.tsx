import { ArrowRight, ExternalLink, Gauge, HelpCircle, Loader2 } from 'lucide-react'
import { Link, useLocation } from 'react-router-dom'
import { Badge } from '@skyhook-io/k8s-ui/components/ui/Badge'
import {
  usePrometheusRightsizing,
  usePrometheusStatus,
  useAutoPromConnect,
  type PrometheusRightsizing,
  type RightsizingFit,
  type RightsizingRow,
} from '../../api/client'
import { Tooltip } from '../ui/Tooltip'

const RIGHTSIZING_KINDS = new Set(['Deployment', 'StatefulSet', 'DaemonSet'])
export const RIGHTSIZING_DOCS_URL = 'https://radarhq.io/docs/features/rightsizing'
export const RIGHTSIZING_SUMMARY =
  'Evidence-based guidance for this workload, not a savings estimate or automatic change.'
export const RIGHTSIZING_METHODOLOGY =
  'Uses seven days of 5-minute samples: CPU P95 and memory maximum, plus 15% headroom. Reductions are staged and rounded to practical values; memory reductions also require verifiable restart history. Radar does not change requests.'

interface RightsizingProps {
  kind: string
  namespace: string
  name: string
}

function useRightsizingEvidence({ kind, namespace, name }: RightsizingProps) {
  useAutoPromConnect()
  const { data: status, isLoading: statusLoading } = usePrometheusStatus()
  const connected = status?.connected === true
  const supported = RIGHTSIZING_KINDS.has(kind)
  const query = usePrometheusRightsizing(kind, namespace, name, connected && supported)
  return { connected, supported, statusLoading, ...query }
}

export function RightsizingPanel(props: RightsizingProps) {
  const state = useRightsizingEvidence(props)
  const { search } = useLocation()
  if (!state.supported) return null

  return (
    <section className="mx-auto w-full max-w-[1600px] rounded-lg border border-theme-border bg-theme-surface/50">
      <header className="flex flex-wrap items-start justify-between gap-3 border-b border-theme-border px-4 py-3">
        <div className="flex items-start gap-2">
          <Gauge className="mt-0.5 h-4 w-4 text-theme-text-tertiary" />
          <div>
            <div className="flex items-center gap-1.5">
              <h3 className="text-sm font-semibold text-theme-text-primary">Rightsizing</h3>
              <Tooltip content={RIGHTSIZING_METHODOLOGY}>
                <HelpCircle className="h-3.5 w-3.5 text-theme-text-tertiary" />
              </Tooltip>
            </div>
            <p className="text-xs text-theme-text-tertiary">
              {RIGHTSIZING_SUMMARY}{' '}
              <a
                href={RIGHTSIZING_DOCS_URL}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-1 text-accent-text hover:underline"
              >
                How it works
                <ExternalLink className="h-3 w-3" />
              </a>
            </p>
          </div>
        </div>
        <div className="flex items-center gap-3">
          {state.data && <RightsizingContext data={state.data} />}
          <Link
            to={{ pathname: '/cost/rightsizing', search }}
            className="text-xs font-medium text-accent-text hover:underline"
          >
            Scan visible workloads
          </Link>
        </div>
      </header>
      <div className="p-4">
        <RightsizingBody state={state} compact={false} />
      </div>
    </section>
  )
}

export function RightsizingStrip(props: RightsizingProps) {
  const state = useRightsizingEvidence(props)
  if (!state.supported || !state.connected || (state.isLoading && !state.data)) return null
  return (
    <section className="mb-3 rounded-lg border border-theme-border bg-theme-surface/40 p-3">
      <header className="mb-2 flex items-center justify-between gap-3">
        <div className="flex items-center gap-1.5">
          <h3 className="text-sm font-medium text-theme-text-primary">Rightsizing</h3>
          <Tooltip content={RIGHTSIZING_METHODOLOGY}>
            <HelpCircle className="h-3.5 w-3.5 text-theme-text-tertiary" />
          </Tooltip>
        </div>
        {state.data && <RightsizingContext data={state.data} compact />}
      </header>
      <RightsizingBody state={state} compact />
    </section>
  )
}

function RightsizingBody({
  state,
  compact,
}: {
  state: ReturnType<typeof useRightsizingEvidence>
  compact: boolean
}) {
  if (state.statusLoading) {
    return <EmptyState text="Checking Prometheus connection…" loading />
  }
  if (!state.connected) {
    return <EmptyState text="Connect Prometheus to compare requests with observed demand." />
  }
  if (state.isLoading && !state.data) {
    return <EmptyState text="Analyzing seven days of workload demand…" loading />
  }
  if (state.error && !state.data) {
    const message = state.error instanceof Error ? state.error.message : String(state.error)
    return <EmptyState text={`Rightsizing unavailable — ${message}`} />
  }
  if (!state.data) return null
  if (!state.data.sampleAvailable || state.data.rows.length === 0) {
    return <EmptyState text={state.data.reason ?? 'No rightsizing evidence is available.'} />
  }

  const byContainer = new Map<string, RightsizingRow[]>()
  for (const row of state.data.rows) {
    const rows = byContainer.get(row.container) ?? []
    rows.push(row)
    byContainer.set(row.container, rows)
  }

  if (compact) {
    return (
      <div className="space-y-2">
        {Array.from(byContainer.entries()).map(([container, rows]) => (
          <div key={container}>
            <div className="mb-1 text-xs font-medium text-theme-text-secondary">{container}</div>
            <div className="space-y-1 pl-2">
              {rows.map((row) => (
                <CompactFitRow key={row.resource} row={row} />
              ))}
            </div>
          </div>
        ))}
      </div>
    )
  }

  return (
    <div className="space-y-4">
      {(state.data.scaledToZero || state.data.ownerCoverage === 'current_pods') && (
        <div className="flex flex-wrap gap-2">
          {state.data.scaledToZero && (
            <Badge severity="neutral" size="sm">
              No current replicas · showing retained evidence
            </Badge>
          )}
          {state.data.ownerCoverage === 'current_pods' && (
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
              <FitCard key={row.resource} row={row} window={state.data!.window} />
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}

function FitCard({ row, window }: { row: RightsizingRow; window: string }) {
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
            Current request
          </div>
          <div className="text-lg font-semibold text-theme-text-primary">
            {row.currentRequest ?? 'Unset'}
          </div>
        </div>
        <ArrowRight className="h-4 w-4 text-theme-text-quaternary" />
        <div>
          <div className="text-[10px] uppercase tracking-wide text-theme-text-quaternary">
            Suggested next step
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
        <Metric label={`Evidence · ${window}`} value={`${confidenceLabel(row)} · 5m samples`} />
      </div>
      <FitExplanation row={row} />
    </div>
  )
}

function CompactFitRow({ row }: { row: RightsizingRow }) {
  return (
    <div className="flex flex-wrap items-center gap-2 text-xs tabular-nums text-theme-text-tertiary">
      <span className="w-12 text-[10px] uppercase tracking-wide text-theme-text-quaternary">
        {row.resource}
      </span>
      <span className="min-w-14 text-theme-text-secondary">{row.currentRequest ?? 'unset'}</span>
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
      <span className="ml-auto">
        <FitBadge fit={row.fit} queryError={row.queryError} />
      </span>
    </div>
  )
}

function RightsizingContext({
  data,
  compact = false,
}: {
  data: PrometheusRightsizing
  compact?: boolean
}) {
  const evidence = data.ownerCoverage === 'ksm_history' ? 'retained ownership' : 'current pods only'
  return (
    <span className="text-[11px] text-theme-text-tertiary">
      {compact ? data.window : `Last ${data.window} · ${evidence}`}
    </span>
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

export function getRightsizingPresentation(fit: RightsizingFit, queryError?: string) {
  if (queryError) return { label: 'Query failed', severity: 'error' as const }
  const labels: Record<RightsizingFit, string> = {
    balanced: 'Balanced',
    oversized: 'Oversized',
    under_requested: 'Under-requested',
    missing_request: 'Missing request',
    insufficient_history: 'Insufficient history',
  }
  const severities: Record<RightsizingFit, 'success' | 'info' | 'warning' | 'neutral'> = {
    balanced: 'success',
    oversized: 'info',
    under_requested: 'warning',
    missing_request: 'warning',
    insufficient_history: 'neutral',
  }
  return { label: labels[fit], severity: severities[fit] }
}

function FitExplanation({ row }: { row: RightsizingRow }) {
  const message = getRightsizingExplanation(row)
  if (!message) return null
  return <p className="mt-3 text-xs text-theme-text-tertiary">{message}</p>
}

export function getRightsizingExplanation(row: RightsizingRow): string | undefined {
  const messages: Record<string, string> = {
    request_within_fit_range: 'The configured request is within 30% of the evidence-based target.',
    insufficient_history: 'At least six hours of samples are required before suggesting a request.',
    hpa_managed: `The HPA manages ${row.resource}; review its target before changing this request.`,
    hpa_evidence_unavailable:
      'Radar could not verify HPA ownership, so it withheld a suggested request.',
    oom_evidence: 'A lower memory request is withheld because OOM evidence exists.',
    oom_evidence_unavailable:
      'Radar could not verify recent OOM history, so it withheld a lower memory request.',
    recommended_request_exceeds_limit:
      'The evidence-based request would exceed the current limit; review the limit first.',
  }
  if (row.reductionLimited && row.calculatedRequest && row.recommendedRequest) {
    const burstNote = row.bursty && row.peak ? ` CPU P99 reached ${row.peak.formatted}.` : ''
    return `Demand-based target: ${row.calculatedRequest}. Radar suggests ${row.recommendedRequest} as a conservative next step; observe another full window before reducing further.${burstNote}`
  }
  const message = row.queryError
    ? `Partial result: ${row.queryError}.`
    : messages[row.recommendationReason ?? '']
  const throttleUnavailable = row.resource === 'cpu' && !row.throttleAvailable
  if (!message && !throttleUnavailable) return undefined
  return message ?? 'CPU throttling metrics are unavailable; no throttling conclusion was drawn.'
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

function EmptyState({ text, loading = false }: { text: string; loading?: boolean }) {
  return (
    <div className="flex min-h-16 items-center justify-center text-sm text-theme-text-tertiary">
      {loading && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
      {text}
    </div>
  )
}
