import { Disclosure } from '../ui/Disclosure'

export function IssueGuidance({ action, primary, secondaryTitle, secondary, className }: {
  action?: string
  primary?: string
  secondaryTitle?: string
  secondary?: string[]
  className?: string
}) {
  if (!action) return null
  return (
    <div className={className}>
      <p>{primary || action}</p>
      {secondaryTitle && secondary && (
        <Disclosure summary={secondaryTitle} className="mt-2" summaryClassName="font-medium">
          <div className="space-y-2 pt-2">
            {secondary.map((paragraph) => <p key={paragraph}>{paragraph}</p>)}
          </div>
        </Disclosure>
      )}
    </div>
  )
}
