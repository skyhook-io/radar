import { ArrowRight, Check, Info, AlertTriangle } from 'lucide-react'
import { SEVERITY_TEXT, SEVERITY_BADGE, type Severity } from '@skyhook-io/k8s-ui/utils/badge-colors'
import { usePrometheusRightsizing, usePrometheusStatus, type RightsizingTone, type RightsizingRow } from '../../api/client'
import { Tooltip } from '../ui/Tooltip'

const RIGHTSIZING_KINDS = new Set(['Deployment', 'StatefulSet', 'DaemonSet'])

/**
 * RightsizingStrip — compact "current → recommended" table per container.
 *
 * Tone policy is deliberately mild:
 *  - "Well-sized" or "Nx headroom" → neutral, no badge
 *  - 5×+ over-provisioning → info ("could reduce"), not a problem
 *  - P95 exceeds request but within limit → warning
 *  - P95 exceeds CPU limit → alert (throttling)
 *  - Memory P95 near limit → critical (active OOM risk)
 *
 * Anything below severe over-provisioning displays without flagging it as
 * an issue. 2-3× headroom is the common, sensible default and should not nag.
 */
export function RightsizingStrip({ kind, namespace, name }: {
  kind: string
  namespace: string
  name: string
}) {
  const { data: status } = usePrometheusStatus()
  const isConnected = status?.connected === true
  const supported = RIGHTSIZING_KINDS.has(kind)
  const { data, error, isLoading } = usePrometheusRightsizing(kind, namespace, name, isConnected && supported)

  if (!supported || !isConnected || isLoading) return null
  if (error && !data) {
    const msg = error instanceof Error ? error.message : String(error)
    return (
      <section className="rounded-lg border border-theme-border bg-theme-surface/40 p-3 mb-3">
        <header className="flex items-center justify-between mb-1">
          <h3 className="text-sm font-medium text-theme-text-primary">Right-sizing</h3>
        </header>
        <Tooltip content={msg}>
        <p className="text-xs text-theme-text-tertiary">Right-sizing unavailable — Prometheus query failed.</p>
        </Tooltip>
      </section>
    )
  }
  if (!data) return null
  if (!data.sampleAvailable || data.rows.length === 0) {
    // Backend distinguishes "workload too new / retention short" from "Prometheus
    // query failed" — show the reason inline so operators have an actionable signal
    // instead of an empty section.
    if (!data.reason) return null
    return (
      <section className="rounded-lg border border-theme-border bg-theme-surface/40 p-3 mb-3">
        <header className="flex items-center justify-between mb-1">
          <h3 className="text-sm font-medium text-theme-text-primary">Right-sizing</h3>
        </header>
        <p className="text-xs text-theme-text-tertiary">{data.reason}</p>
      </section>
    )
  }

  // Group rows by container so each container is a compact two-row block (cpu+mem).
  const byContainer = new Map<string, RightsizingRow[]>()
  for (const row of data.rows) {
    if (!byContainer.has(row.container)) byContainer.set(row.container, [])
    byContainer.get(row.container)!.push(row)
  }

  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface/40 p-3 mb-3">
      <header className="flex items-center justify-between mb-2">
        <h3 className="text-sm font-medium text-theme-text-primary">Right-sizing</h3>
        <span className="text-[11px] text-theme-text-tertiary">
          based on last {data.window} · P95
        </span>
      </header>
      <div className="space-y-1.5">
        {Array.from(byContainer.entries()).map(([container, rows]) => (
          <div key={container} className="text-xs">
            <div className="text-theme-text-secondary font-medium mb-0.5">{container}</div>
            <div className="space-y-0.5 pl-2">
              {rows.map(row => (
                <RightsizingLine key={row.resource} row={row} />
              ))}
            </div>
          </div>
        ))}
      </div>
    </section>
  )
}

function RightsizingLine({ row }: { row: RightsizingRow }) {
  const showRec = row.recommendedRequest && row.recommendedRequest !== row.currentRequest
  const toneClass = toneClasses(row.tone)
  const Icon = toneIcon(row.tone)

  return (
    <div className="flex items-center gap-2 text-theme-text-tertiary tabular-nums">
      <span className="w-12 text-theme-text-quaternary uppercase tracking-wide text-[10px]">
        {row.resource}
      </span>

      <span className="text-theme-text-secondary min-w-[3.5rem]">
        {row.currentRequest ?? <span className="text-theme-text-quaternary italic">unset</span>}
      </span>

      {showRec && (
        <>
          <ArrowRight className="w-3 h-3 text-theme-text-quaternary shrink-0" />
          <span className={`min-w-[3.5rem] ${toneClass.value}`}>
            {row.recommendedRequest}
          </span>
        </>
      )}

      {row.p95 && (
        <span className="text-theme-text-quaternary text-[10px]">
          (P95 {row.p95})
        </span>
      )}

      {row.tone !== 'ok' && Icon && (
        <span className={`ml-auto inline-flex items-center gap-1 ${toneClass.badge} px-1.5 py-0.5 rounded text-[10px]`}>
          <Icon className="w-3 h-3" />
          <span>{row.message}</span>
        </span>
      )}

      {row.tone === 'ok' && row.message && (
        <span className="ml-auto text-theme-text-quaternary text-[10px]">{row.message}</span>
      )}
    </div>
  )
}

const TONE_TO_SEVERITY: Record<RightsizingTone, Severity> = {
  critical: 'error',
  alert: 'alert',
  warning: 'warning',
  info: 'info',
  ok: 'neutral',
}

function toneClasses(tone: RightsizingTone): { value: string; badge: string } {
  if (tone === 'ok') return { value: 'text-theme-text-secondary', badge: '' }
  if (tone === 'info') {
    // "Could reduce" is a suggestion, not a problem — mute the badge.
    return { value: SEVERITY_TEXT.info, badge: 'text-theme-text-tertiary bg-theme-elevated/60' }
  }
  const sev = TONE_TO_SEVERITY[tone]
  return { value: SEVERITY_TEXT[sev], badge: SEVERITY_BADGE[sev] }
}

function toneIcon(tone: RightsizingTone) {
  switch (tone) {
    case 'critical':
    case 'alert':
    case 'warning':
      return AlertTriangle
    case 'info':
      return Info
    case 'ok':
      return Check
    default:
      return null
  }
}
