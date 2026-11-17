import { ClipboardCheck, ArrowRight, Check, AlertTriangle } from 'lucide-react'
import { clsx } from 'clsx'
import { SEVERITY_TEXT, SEVERITY_DOT } from '../../utils/badge-colors'
import { SEVERITY_FILL_CLASS, SEVERITY_TEXT_CLASS } from '../checks/severity'

export interface AuditCardData {
  missingInputs?: string[]
  passing: number
  /** Raw compatibility counts; warning renders as Medium and danger as High. */
  warning: number
  danger: number
  /** Nested warning/danger counts follow the same Medium/High mapping. */
  categories: Record<string, { passing: number; warning: number; danger: number }>
}

/**
 * Known CVEs in running images, shown as a row of the Checks card when the
 * host has them (Radar: Trivy Operator reports). Totals are lower bounds
 * when some running images aren't scanned or some reports aren't readable,
 * and unknown when nothing is scanned.
 */
export interface AuditCardVulnerabilities {
  imagesWithCritical: number
  fixableCriticalHigh: number
  certainty: 'exact' | 'lower_bound' | 'unknown'
  /** The read failed: the row says so instead of showing counts. */
  readFailed?: boolean
  onNavigate: () => void
}

interface AuditCardProps {
  data: AuditCardData
  onNavigate: () => void
  vulnerabilities?: AuditCardVulnerabilities
}

type SeverityLevel = 'success' | 'high' | 'medium' | 'neutral'

function getSeverityLevel(data: AuditCardData): SeverityLevel {
  if (data.warning + data.danger === 0) return data.missingInputs?.length || data.passing === 0 ? 'neutral' : 'success'
  const highRatio = data.danger / (data.warning + data.danger)
  if (highRatio > 0.2) return 'high'
  return 'medium'
}

const ACCENT_BG: Record<SeverityLevel, string> = {
  neutral: 'bg-theme-elevated',
  success: 'bg-green-500/10',
  high: 'bg-orange-500/10',
  medium: 'bg-yellow-500/10',
}

export function AuditCard({ data, onNavigate, vulnerabilities }: AuditCardProps) {
  const total = data.passing + data.warning + data.danger
  const issueCount = data.warning + data.danger
  const incomplete = (data.missingInputs?.length ?? 0) > 0
  const allPassing = issueCount === 0 && total > 0 && !incomplete
  const level = getSeverityLevel(data)
  const accentColor = level === 'neutral' ? 'text-theme-text-secondary' : level === 'success' ? SEVERITY_TEXT.success : SEVERITY_TEXT_CLASS[level]
  const accentBg = ACCENT_BG[level]

  const body = (
      <div className="flex flex-col flex-1 min-h-0 h-full w-full">
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
                {allPassing ? <Check className="w-3 h-3" /> : <ClipboardCheck className="w-3 h-3" />}
              </span>
            )}
          </div>
        </div>

        <div className={clsx('flex-1 min-h-0 flex flex-col items-center px-4', vulnerabilities ? 'justify-end pt-4 pb-2' : 'justify-center py-4')}>
          {allPassing ? (
            <div className="flex flex-col items-center gap-2">
              <Check className={clsx('w-8 h-8', SEVERITY_TEXT.success)} />
              <span className={clsx('text-sm font-medium', SEVERITY_TEXT.success)}>All checks passing</span>
              {total > 0 && (
                <span className="text-xs text-theme-text-tertiary">{total} checks across {Object.keys(data.categories).length} categories</span>
              )}
            </div>
          ) : issueCount === 0 ? (
            <div className="flex flex-col items-center gap-2 text-center">
              {incomplete ? <AlertTriangle className="w-8 h-8 text-warning-text" /> : <ClipboardCheck className="w-8 h-8 text-theme-text-tertiary" />}
              <span className="text-sm font-medium text-theme-text-primary">{incomplete ? 'Some checks could not run' : 'No resources evaluated'}</span>
              <span className="text-xs text-theme-text-secondary">{incomplete ? 'No findings in the available data.' : 'Check the selected scope and check settings.'}</span>
            </div>
          ) : (
            <>
              {/* Distribution bar — only show when we have passing data for context */}
              {data.passing > 0 && total > 0 && (
                <div className="flex items-center gap-3 w-full">
                  <div className="flex-1 h-3 rounded-full overflow-hidden bg-theme-hover flex">
                    <div className={clsx('h-full', SEVERITY_DOT.success)} style={{ width: `${(data.passing / total) * 100}%` }} />
                    {data.warning > 0 && (
                      <div className={clsx('h-full', SEVERITY_FILL_CLASS.medium)} style={{ width: `${(data.warning / total) * 100}%` }} />
                    )}
                    {data.danger > 0 && (
                      <div className={clsx('h-full', SEVERITY_FILL_CLASS.high)} style={{ width: `${(data.danger / total) * 100}%` }} />
                    )}
                  </div>
                </div>
              )}

              {/* Category breakdown */}
              <div className="grid grid-cols-1 gap-y-2 mt-4 w-full">
                {Object.entries(data.categories).map(([category, counts]) => {
                  const catIssues = counts.warning + counts.danger
                  const dotColor = counts.danger > 0 ? SEVERITY_FILL_CLASS.high : counts.warning > 0 ? SEVERITY_FILL_CLASS.medium : SEVERITY_DOT.success
                  return (
                    <div key={category} className="flex items-center gap-2">
                      <span className={clsx('w-2 h-2 rounded-full shrink-0', dotColor)} />
                      <span className="text-xs text-theme-text-secondary flex-1">{category}</span>
                      {catIssues > 0 ? (
                        <div className="flex items-center gap-2">
                          {counts.danger > 0 && (
                            <span className={clsx('text-xs font-semibold tabular-nums', SEVERITY_TEXT_CLASS.high)}>{counts.danger} high</span>
                          )}
                          {counts.warning > 0 && (
                            <span className={clsx('text-xs font-semibold tabular-nums', SEVERITY_TEXT_CLASS.medium)}>{counts.warning} medium</span>
                          )}
                        </div>
                      ) : (
                        <span className={clsx('text-xs font-semibold', incomplete ? 'text-theme-text-secondary' : SEVERITY_TEXT.success)}>{incomplete ? 'No findings' : 'All passing'}</span>
                      )}
                    </div>
                  )
                })}
              </div>
            </>
          )}
        </div>

        {incomplete && issueCount > 0 && <p className="px-4 pb-2 text-xs text-warning-text">Some checks could not run.</p>}

      </div>
  )

  const footer = (
    <div className="px-4 py-1.5 border-t border-theme-border/50 flex items-center justify-end">
      <span className={clsx(
        'flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wider transition-colors',
        accentColor,
      )}>
        View Details
        <ArrowRight className="w-3.5 h-3.5 transition-transform group-hover:translate-x-0.5" />
      </span>
    </div>
  )

  if (!vulnerabilities) {
    return (
      <button
        onClick={onNavigate}
        className="group h-[260px] rounded-xl bg-theme-surface shadow-theme-sm hover:-translate-y-1 hover:shadow-theme-md transition-all duration-200 text-left animate-fade-in-up"
      >
        <div className="flex h-full flex-col">
          {body}
          {footer}
        </div>
      </button>
    )
  }

  // Two destinations on one card: the checks body and the vulnerabilities
  // row are sibling buttons, never one inside the other.
  return (
    <div className="flex h-[260px] flex-col overflow-hidden rounded-xl bg-theme-surface shadow-theme-sm hover:shadow-theme-md transition-shadow duration-200 animate-fade-in-up">
      <button onClick={onNavigate} className="group min-h-0 flex-1 text-left hover:bg-theme-hover/40 transition-colors">
        {body}
      </button>
      <VulnerabilitiesRow {...vulnerabilities} />
      <button onClick={onNavigate} className="group text-left hover:bg-theme-hover/40 transition-colors">
        {footer}
      </button>
    </div>
  )
}

// Styled as one more line of the category list: dot, label, one count. The
// fixable count and the caveats live in the tab it opens.
function VulnerabilitiesRow({ imagesWithCritical, fixableCriticalHigh, certainty, readFailed, onNavigate }: AuditCardVulnerabilities) {
  const plus = certainty === 'lower_bound' ? '+' : ''
  let text: string
  let tone: string
  let dot: string
  if (readFailed) {
    text = "Couldn't read"
    tone = 'text-theme-text-tertiary'
    dot = 'bg-theme-text-tertiary'
  } else if (certainty === 'unknown') {
    text = 'Not scanned yet'
    tone = 'text-theme-text-tertiary'
    dot = 'bg-theme-text-tertiary'
  } else if (imagesWithCritical > 0) {
    text = `${imagesWithCritical}${plus} ${imagesWithCritical === 1 ? 'image' : 'images'} with Critical`
    tone = SEVERITY_TEXT_CLASS.high
    dot = SEVERITY_FILL_CLASS.high
  } else if (fixableCriticalHigh > 0) {
    text = `${fixableCriticalHigh}${plus} fixable High`
    tone = SEVERITY_TEXT_CLASS.medium
    dot = SEVERITY_FILL_CLASS.medium
  } else {
    text = certainty === 'exact' ? 'No Critical' : 'No Critical found'
    tone = certainty === 'exact' ? SEVERITY_TEXT.success : 'text-theme-text-secondary'
    dot = certainty === 'exact' ? SEVERITY_DOT.success : 'bg-theme-text-tertiary'
  }
  return (
    <div className="mx-4 mb-1.5 border-t border-theme-border/50 pt-1">
      <button
        onClick={onNavigate}
        className="group/vuln -mx-2 flex w-[calc(100%+1rem)] items-center gap-2 rounded-md px-2 py-1.5 text-left hover:bg-theme-hover transition-colors"
      >
        <span className={clsx('h-2 w-2 shrink-0 rounded-full', dot)} />
        <span className="flex-1 truncate text-xs text-theme-text-secondary">Vulnerabilities</span>
        <span className={clsx('whitespace-nowrap text-xs font-semibold tabular-nums', tone)}>{text}</span>
        <ArrowRight className="h-3 w-3 shrink-0 text-theme-text-tertiary transition-transform group-hover/vuln:translate-x-0.5" aria-hidden />
      </button>
    </div>
  )
}
