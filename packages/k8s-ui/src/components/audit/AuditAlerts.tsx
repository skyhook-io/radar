import { useState } from 'react'
import { ClipboardCheck, ChevronRight, ShieldAlert, AlertTriangle, ArrowRight } from 'lucide-react'
import { clsx } from 'clsx'
import { SEVERITY_TEXT, BP_CATEGORY_BADGE, DEFAULT_BADGE_COLOR } from '../../utils/badge-colors'

export interface AuditFinding {
  kind: string
  /** API group, backfilled by the backend from the builtin Kind→group table
   *  (built-ins → e.g. "apps"/"batch"; CRDs → ""). Part of the resource key
   *  used to join findings onto topology nodes / list rows. */
  group?: string
  namespace: string
  name: string
  checkID: string
  category: string
  severity: string
  message: string
  /** Cluster context — set when findings come from multiple clusters
   *  (cross-cluster aggregation). Rendered as a Cluster column when the
   *  host passes `multiCluster` to AuditFindingsTable. Grouped as one
   *  optional object so id and name stay in lockstep — `name` is
   *  meaningless without `id`. */
  cluster?: { id: string; name: string }
}

interface AuditAlertsProps {
  findings: AuditFinding[]
  onViewAll?: () => void
}

/**
 * Subtle collapsible section showing audit findings for a resource.
 * Renders as a collapsed summary by default — not intrusive.
 */
export function AuditAlerts({ findings, onViewAll }: AuditAlertsProps) {
  const [expanded, setExpanded] = useState(false)

  if (findings.length === 0) return null

  const dangers = findings.filter(f => f.severity === 'danger').length
  const warnings = findings.filter(f => f.severity === 'warning').length

  return (
    <div className="border-b-subtle pb-4 last:border-0">
      <button
        onClick={() => setExpanded(!expanded)}
        className="flex items-center gap-2 w-full text-left mb-2 hover:text-theme-text-primary transition-colors"
      >
        <ChevronRight className={clsx('w-4 h-4 text-theme-text-tertiary transition-transform duration-200', expanded && 'rotate-90')} />
        <ClipboardCheck className="w-4 h-4 text-theme-text-secondary" />
        <span className="text-sm font-medium text-theme-text-secondary">Audit Findings</span>
        <div className="flex items-center gap-2 ml-1">
          {dangers > 0 && (
            <span className={clsx('text-xs font-medium tabular-nums', SEVERITY_TEXT.error)}>{dangers} critical</span>
          )}
          {warnings > 0 && (
            <span className={clsx('text-xs font-medium tabular-nums', SEVERITY_TEXT.warning)}>{warnings} warning</span>
          )}
        </div>
      </button>

      <div
        className="grid transition-[grid-template-rows] duration-200 ease-out"
        style={{ gridTemplateRows: expanded ? '1fr' : '0fr' }}
      >
        <div className="overflow-hidden">
          <div className="pl-6 pt-1 flex flex-col gap-0.5">
            {findings.map((f, i) => {
              const isDanger = f.severity === 'danger'
              return (
                <div key={`${f.checkID}-${i}`} className="flex items-start gap-2 py-1">
                  {isDanger ? (
                    <ShieldAlert className={clsx('w-3.5 h-3.5 shrink-0 mt-0.5', SEVERITY_TEXT.error)} />
                  ) : (
                    <AlertTriangle className={clsx('w-3.5 h-3.5 shrink-0 mt-0.5', SEVERITY_TEXT.warning)} />
                  )}
                  <span className="text-xs text-theme-text-secondary flex-1">{f.message}</span>
                  <span className={clsx('badge-sm text-[10px] shrink-0', BP_CATEGORY_BADGE[f.category] || DEFAULT_BADGE_COLOR)}>
                    {f.category}
                  </span>
                </div>
              )
            })}
            {onViewAll && (
              <button
                onClick={(e) => { e.stopPropagation(); onViewAll() }}
                className="flex items-center gap-1 text-xs text-skyhook-500 hover:text-skyhook-400 mt-1 py-1 transition-colors"
              >
                View all findings
                <ArrowRight className="w-3 h-3" />
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
