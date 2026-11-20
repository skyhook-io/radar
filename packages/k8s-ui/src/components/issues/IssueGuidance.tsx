import { AlertTriangle } from 'lucide-react'
import { Disclosure } from '../ui/Disclosure'

export function issueGuidanceSummary(action?: string, primary?: string, hazard?: string) {
  const summary = primary || action
  if (!hazard || !summary) return summary
  if (summary === hazard) return undefined
  return summary.endsWith(` ${hazard}`) ? summary.slice(0, -hazard.length - 1) : summary
}

export function IssueGuidance({ action, primary, secondaryTitle, secondary, ordered, hazard, className }: {
  action?: string
  /** Replaces a supplied action; primary alone renders no action text. */
  primary?: string
  /** Paired with secondary; no disclosure is shown without both. */
  secondaryTitle?: string
  /** Paired with secondaryTitle; no disclosure is shown without both. */
  secondary?: string[]
  ordered?: boolean
  hazard?: string
  className?: string
}) {
  if (!action && !hazard && !(secondaryTitle && secondary?.length)) return null
  return (
    <div className={className}>
      {hazard && (
        <div className="mb-2 flex items-start gap-2 text-warning-text">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
          <p>{hazard}</p>
        </div>
      )}
      {action && <p>{primary || action}</p>}
      {secondaryTitle && secondary && (
        <Disclosure summary={secondaryTitle} className="mt-2" summaryClassName="font-medium">
          {ordered ? (
            <ol className="list-decimal space-y-2 pl-5 pt-2">
              {secondary.map((step) => <li key={step}>{step}</li>)}
            </ol>
          ) : (
            <div className="space-y-2 pt-2">
              {secondary.map((paragraph) => <p key={paragraph}>{paragraph}</p>)}
            </div>
          )}
        </Disclosure>
      )}
    </div>
  )
}
