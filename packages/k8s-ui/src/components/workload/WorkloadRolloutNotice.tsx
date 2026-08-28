import { AlertTriangle, Check, Clock3, Loader2, Pause } from 'lucide-react'
import { Badge, type BadgeSeverity } from '../ui/Badge'
import type { WorkloadRolloutActivity } from '../../utils/workload-rollout'

export function WorkloadRolloutNotice({
  activity,
  recentImageSave = false,
  className = '',
}: {
  activity: WorkloadRolloutActivity
  recentImageSave?: boolean
  className?: string
}) {
  if (activity.phase === 'idle' && !recentImageSave) return null
  const shown = activity.phase === 'idle' && recentImageSave
    ? { ...activity, phase: 'applying' as const, active: true, label: 'Waiting for controller', detail: 'The template change was accepted; waiting for live rollout status' }
    : activity
  const severity: BadgeSeverity = shown.phase === 'stalled'
    ? 'error'
    : shown.phase === 'paused' || shown.manual
      ? 'warning'
      : 'info'
  const Icon = shown.phase === 'stalled'
    ? AlertTriangle
    : shown.phase === 'paused'
      ? Pause
      : shown.phase === 'waiting'
        ? Clock3
        : Loader2
  const warning = shown.phase === 'paused' || shown.manual
  const containerTone = shown.phase === 'stalled'
    ? 'border-semantic-error/30 bg-semantic-error/5'
    : warning
      ? 'border-semantic-warning/30 bg-semantic-warning/5'
      : 'border-theme-border bg-accent-muted'
  const iconTone = shown.phase === 'stalled'
    ? 'text-semantic-error'
    : warning
      ? 'text-semantic-warning'
      : 'text-accent-text'

  return (
    <div className={`rounded-lg border px-3 py-2.5 shadow-theme-sm ${containerTone} ${className}`} role={shown.phase === 'stalled' ? 'alert' : 'status'}>
      <div className="flex min-w-0 items-start gap-2.5">
        <Icon className={`mt-0.5 h-4 w-4 shrink-0 ${iconTone} ${shown.active && (shown.phase === 'applying' || shown.phase === 'progressing') ? 'animate-spin' : ''}`} />
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 flex-wrap items-center gap-1.5">
            {recentImageSave && (
              <span className="inline-flex items-center gap-1 text-xs font-medium text-theme-text-secondary">
                <Check className="h-3.5 w-3.5" /> Image saved <span aria-hidden>·</span>
              </span>
            )}
            <Badge severity={severity}>{shown.label}</Badge>
          </div>
          {shown.detail && <div className="mt-1 text-xs leading-5 text-theme-text-secondary">{shown.detail}</div>}
        </div>
      </div>
    </div>
  )
}
