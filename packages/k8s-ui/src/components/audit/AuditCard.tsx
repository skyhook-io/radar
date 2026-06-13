import { ClipboardCheck, ArrowRight, Check } from 'lucide-react'
import { clsx } from 'clsx'
import { SEVERITY_TEXT, SEVERITY_DOT } from '../../utils/badge-colors'

export interface AuditCardData {
  passing: number
  warning: number
  danger: number
  categories: Record<string, { passing: number; warning: number; danger: number }>
}

interface AuditCardProps {
  data: AuditCardData
  onNavigate: () => void
}

type SeverityLevel = 'success' | 'warning' | 'error'

function getSeverityLevel(data: AuditCardData): SeverityLevel {
  if (data.warning + data.danger === 0) return 'success'
  const dangerRatio = data.danger / (data.warning + data.danger)
  if (dangerRatio > 0.2) return 'error'
  return 'warning'
}

// Card-specific accent backgrounds (light opacity variants, work in both themes)
const ACCENT_BG: Record<SeverityLevel, string> = {
  success: 'bg-green-500/10',
  warning: 'bg-yellow-500/10',
  error: 'bg-red-500/10',
}

export function AuditCard({ data, onNavigate }: AuditCardProps) {
  const total = data.passing + data.warning + data.danger
  const issueCount = data.warning + data.danger
  const allPassing = issueCount === 0
  const level = getSeverityLevel(data)
  const accentColor = SEVERITY_TEXT[level]
  const accentBg = ACCENT_BG[level]

  return (
    <button
      onClick={onNavigate}
      className="group h-[260px] rounded-xl bg-theme-surface shadow-theme-sm hover:-translate-y-1 hover:shadow-theme-md transition-all duration-200 text-left animate-fade-in-up"
    >
      <div className="flex flex-col h-full w-full">
        <div className="flex items-center justify-between px-5 py-3 border-b border-theme-border/50">
          <div className="flex items-center gap-2">
            <ClipboardCheck className={clsx('w-4 h-4', accentColor)} />
            <span className={clsx('text-xs font-semibold uppercase tracking-wider', accentColor)}>Cluster Checks</span>
            {issueCount > 0 ? (
              <span className={clsx('badge-sm', accentBg, accentColor)}>
                {issueCount}
              </span>
            ) : (
              <span className={clsx('badge-sm', accentBg, accentColor)}>
                <Check className="w-3 h-3" />
              </span>
            )}
          </div>
        </div>

        <div className="flex-1 min-h-0 flex flex-col items-center justify-center px-4 py-4">
          {allPassing ? (
            <div className="flex flex-col items-center gap-2">
              <Check className={clsx('w-8 h-8', SEVERITY_TEXT.success)} />
              <span className={clsx('text-sm font-medium', SEVERITY_TEXT.success)}>All checks passing</span>
              {total > 0 && (
                <span className="text-xs text-theme-text-tertiary">{total} checks across {Object.keys(data.categories).length} categories</span>
              )}
            </div>
          ) : (
            <>
              {/* Distribution bar — only show when we have passing data for context */}
              {data.passing > 0 && total > 0 && (
                <div className="flex items-center gap-3 w-full">
                  <div className="flex-1 h-3 rounded-full overflow-hidden bg-theme-hover flex">
                    <div className={clsx('h-full', SEVERITY_DOT.success)} style={{ width: `${(data.passing / total) * 100}%` }} />
                    {data.warning > 0 && (
                      <div className={clsx('h-full', SEVERITY_DOT.warning)} style={{ width: `${(data.warning / total) * 100}%` }} />
                    )}
                    {data.danger > 0 && (
                      <div className={clsx('h-full', SEVERITY_DOT.error)} style={{ width: `${(data.danger / total) * 100}%` }} />
                    )}
                  </div>
                </div>
              )}

              {/* Category breakdown */}
              <div className="grid grid-cols-1 gap-y-2 mt-4 w-full">
                {Object.entries(data.categories).map(([category, counts]) => {
                  const catIssues = counts.warning + counts.danger
                  const dotColor = counts.danger > 0 ? SEVERITY_DOT.error : counts.warning > 0 ? SEVERITY_DOT.warning : SEVERITY_DOT.success
                  return (
                    <div key={category} className="flex items-center gap-2">
                      <span className={clsx('w-2 h-2 rounded-full shrink-0', dotColor)} />
                      <span className="text-xs text-theme-text-secondary flex-1">{category}</span>
                      {catIssues > 0 ? (
                        <div className="flex items-center gap-2">
                          {counts.danger > 0 && (
                            <span className={clsx('text-xs font-semibold tabular-nums', SEVERITY_TEXT.error)}>{counts.danger} critical</span>
                          )}
                          {counts.warning > 0 && (
                            <span className={clsx('text-xs font-semibold tabular-nums', SEVERITY_TEXT.warning)}>{counts.warning} warning</span>
                          )}
                        </div>
                      ) : (
                        <span className={clsx('text-xs font-semibold', SEVERITY_TEXT.success)}>All passing</span>
                      )}
                    </div>
                  )
                })}
              </div>
            </>
          )}
        </div>

        <div className="px-4 py-1.5 border-t border-theme-border/50 flex items-center justify-end">
          <span className={clsx(
            'flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wider transition-colors',
            accentColor,
          )}>
            View Details
            <ArrowRight className="w-3.5 h-3.5 transition-transform group-hover:translate-x-0.5" />
          </span>
        </div>
      </div>
    </button>
  )
}
