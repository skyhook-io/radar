import { AlertTriangle, Pause } from 'lucide-react'
import { Badge } from '../../ui/Badge'
import { Tooltip } from '../../ui/Tooltip'
import { getCrossplanePauseState } from '../resource-utils-crossplane'

function pauseDescription(state: 'requested' | 'acknowledged'): string {
  return state === 'acknowledged'
    ? "This resource's controller last reported reconciliation paused. Changes are deferred while paused. Readiness and errors are last reported."
    : "The pause annotation is set for this resource; the recorded conditions do not confirm the pause. Changes may be deferred while paused. Readiness and errors are last reported."
}

export function CrossplanePauseChip({ state, compact = false }: { state: ReturnType<typeof getCrossplanePauseState>; compact?: boolean }) {
  if (!state) return null
  return (
    <Tooltip content={pauseDescription(state)}>
      <Badge severity="neutral" size={compact ? 'sm' : 'default'} className="gap-1 whitespace-nowrap shrink-0">
        <Pause className="w-3 h-3 shrink-0" aria-hidden />
        {state === 'requested' ? 'Pause requested' : compact ? 'Paused' : 'Reconciliation paused'}
      </Badge>
    </Tooltip>
  )
}

export function CrossplanePauseNotice({ data }: { data: any }) {
  const state = getCrossplanePauseState(data)
  if (!state) return null
  if (data?.metadata?.deletionTimestamp) {
    return (
      <div className="space-y-1">
        <p className="flex items-start gap-1.5 text-sm text-warning-text">
          <AlertTriangle className="w-4 h-4 shrink-0 mt-0.5" aria-hidden />
          Deletion has been requested. Paused reconciliation may defer this resource’s controller cleanup.
        </p>
        <p className="text-sm text-theme-text-tertiary">{pauseDescription(state)}</p>
      </div>
    )
  }
  return <p className="text-sm text-theme-text-tertiary">{pauseDescription(state)}</p>
}
