import type { ReactNode } from 'react'
import clsx from 'clsx'
import { Collapse } from '../ui/Collapse'
import type { WorkloadPodInfo } from '../../types'
import type { NavigateToResource } from '../../utils/navigation'
import { formatAge } from '../resources/resource-utils'

export function PodRow({
  name,
  namespace,
  ready,
  healthLevel,
  detail,
  onNavigate,
}: {
  name: string
  namespace: string
  ready: boolean | null
  healthLevel?: WorkloadPodInfo['healthLevel']
  detail: string
  onNavigate?: NavigateToResource
}) {
  const statusLabel = podStatusLabel(healthLevel, ready)
  const statusClass = podStatusClass(healthLevel, ready)
  const content = (
    <>
      <span className={clsx('h-2 w-2 shrink-0 rounded-full', podDotClass(healthLevel, ready))} />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm font-medium text-theme-text-primary">{name}</span>
        <span className="block truncate text-xs text-theme-text-tertiary">{detail}</span>
      </span>
      <span className={clsx('badge-sm shrink-0', statusClass)}>
        {statusLabel}
      </span>
    </>
  )

  if (!onNavigate) {
    return (
      <div className="flex w-full min-w-0 items-center gap-3 rounded-md border border-theme-border bg-theme-base px-3 py-2 text-left">
        {content}
      </div>
    )
  }

  return (
    <button
      type="button"
      onClick={() => onNavigate({ kind: 'pods', namespace, name })}
      className="flex w-full min-w-0 items-center gap-3 rounded-md border border-theme-border bg-theme-base px-3 py-2 text-left transition-colors hover:bg-theme-hover"
    >
      {content}
    </button>
  )
}

export function PodListFrame({
  expanded,
  hasOverflow,
  overflow,
  toggle,
  children,
}: {
  expanded: boolean
  hasOverflow: boolean
  overflow?: ReactNode
  toggle?: ReactNode
  children: ReactNode
}) {
  return (
    <div className="space-y-2">
      <div className="space-y-2">
        {children}
      </div>
      {hasOverflow && (
        <Collapse open={expanded}>
          {/* Long overflow lists scroll inside a capped box once open; the
              cap comes off while closed so the collapse measures to zero. */}
          <div className={clsx(expanded && 'max-h-[26rem] overflow-y-auto pr-1')}>
            <div className="space-y-2">
              {overflow}
            </div>
          </div>
        </Collapse>
      )}
      {toggle}
    </div>
  )
}

export function PodListToggle({
  expanded,
  hiddenCount,
  label,
  onToggle,
}: {
  expanded: boolean
  hiddenCount: number
  label: string
  onToggle: () => void
}) {
  if (!expanded && hiddenCount <= 0) return null

  return (
    <div className="pt-1">
      <button
        type="button"
        aria-expanded={expanded}
        onClick={onToggle}
        className="text-xs font-medium text-accent-text hover:underline"
      >
        {expanded ? `Collapse ${label}` : `Show ${hiddenCount} more ${label}`}
      </button>
    </div>
  )
}

export function workloadPodDetail(pod: WorkloadPodInfo): string {
  const parts: string[] = []
  if (pod.phase) parts.push(pod.reason ? `${pod.phase} / ${pod.reason}` : pod.phase)
  else if (pod.reason) parts.push(pod.reason)
  parts.push(`${pod.containers.length} container${pod.containers.length === 1 ? '' : 's'}`)
  if ((pod.restartCount ?? 0) > 0) parts.push(`${pod.restartCount} restart${pod.restartCount === 1 ? '' : 's'}`)
  if (pod.lastTerminationReason && !['Unknown', 'Completed'].includes(pod.lastTerminationReason)) {
    parts.push(`last ${pod.lastTerminationReason}`)
  }
  if (pod.createdAt) parts.push(`${formatAge(pod.createdAt)} old`)
  return parts.join(' · ')
}

export function podStatusLabel(healthLevel: WorkloadPodInfo['healthLevel'] | undefined, ready: boolean | null): string {
  if (healthLevel === 'unhealthy') return 'Unhealthy'
  if (healthLevel === 'degraded') return 'Degraded'
  if (healthLevel === 'neutral') return ready ? 'Ready' : 'Neutral'
  if (healthLevel === 'unknown') return 'Unknown'
  return ready === null ? 'Unknown' : ready ? 'Ready' : 'Not ready'
}

export function podStatusClass(healthLevel: WorkloadPodInfo['healthLevel'] | undefined, ready: boolean | null): string {
  if (healthLevel === 'unhealthy') return 'status-unhealthy'
  if (healthLevel === 'degraded') return 'status-degraded'
  if (healthLevel === 'neutral') return 'status-neutral'
  if (healthLevel === 'unknown' || ready === null) return 'status-unknown'
  return ready ? 'status-healthy' : 'status-degraded'
}

export function podDotClass(healthLevel: WorkloadPodInfo['healthLevel'] | undefined, ready: boolean | null): string {
  if (healthLevel === 'unhealthy') return 'bg-red-500'
  if (healthLevel === 'degraded') return 'bg-amber-500'
  if (healthLevel === 'neutral') return 'bg-skyhook-500'
  if (healthLevel === 'unknown' || ready === null) return 'bg-theme-text-tertiary'
  return ready ? 'bg-emerald-500' : 'bg-amber-500'
}

