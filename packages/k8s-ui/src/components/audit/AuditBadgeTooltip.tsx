import { ShieldAlert, AlertTriangle } from 'lucide-react'
import { clsx } from 'clsx'
import { SEVERITY_TEXT } from '../../utils/badge-colors'

export interface AuditBadgeMessage {
  severity: string
  message: string
}

interface AuditBadgeTooltipProps {
  messages: AuditBadgeMessage[]
  /** Max messages to list before collapsing the rest into "+N more". */
  max?: number
  /** Hint that clicking opens the resource — omitted when the badge isn't clickable. */
  clickHint?: boolean
}

/**
 * Inline tooltip body for an audit badge: lists the actual finding messages
 * (danger-first) so the operator reads WHAT is wrong on hover, instead of a
 * content-free "N findings". Shared by the resource-list and topology-node
 * badges so the two can't drift.
 */
export function AuditBadgeTooltip({ messages, max = 3, clickHint = true }: AuditBadgeTooltipProps) {
  const shown = messages.slice(0, max)
  const overflow = messages.length - shown.length
  return (
    <div className="flex flex-col gap-1 text-left">
      {shown.map((m, i) => {
        const isDanger = m.severity === 'danger'
        const Icon = isDanger ? ShieldAlert : AlertTriangle
        return (
          <div key={i} className="flex items-start gap-1.5">
            <Icon className={clsx('w-3 h-3 shrink-0 mt-0.5', isDanger ? SEVERITY_TEXT.error : SEVERITY_TEXT.warning)} />
            <span>{m.message}</span>
          </div>
        )
      })}
      {overflow > 0 && (
        <div className="text-theme-text-tertiary">{`+${overflow} more`}</div>
      )}
      {clickHint && (
        <div className="text-theme-text-tertiary mt-0.5">Click to open →</div>
      )}
    </div>
  )
}
