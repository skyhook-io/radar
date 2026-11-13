import { clsx } from 'clsx'
import { SEVERITY_BADGE } from '../../utils/badge-colors'
import { Tooltip } from '../ui/Tooltip'

/**
 * Why some of a release's objects weren't checked. "denied": the caller can't
 * read them. "unconfirmed": the access check went unanswered. "either": both,
 * or not known which (a count alone doesn't say).
 */
export type NotCheckedReason = 'denied' | 'unconfirmed' | 'either'

export function notCheckedTooltip(count: number, reason: NotCheckedReason = 'either', scope: 'objects' | 'health' = 'objects'): string {
  const one = count === 1
  const objects = `${count} of this release’s ${scope === 'health' ? 'workloads or Pods' : 'objects'}`
  const coverage = scope === 'health'
    ? 'Release health covers only the workloads and Pods that were checked.'
    : 'Release health uses workload and Pod status. This count can include other objects.'
  switch (reason) {
    case 'denied':
      return `You can’t read ${objects}, so ${one ? 'it wasn’t' : 'they weren’t'} checked. ${coverage}`
    case 'unconfirmed':
      return `Radar couldn’t confirm your access to ${objects}, so ${one ? 'it wasn’t' : 'they weren’t'} checked. Refresh to try again.`
    default:
      return `${objects.charAt(0).toUpperCase()}${objects.slice(1)} ${one ? 'wasn’t' : 'weren’t'} checked: you can’t read ${one ? 'it' : 'them'}, or Radar couldn’t confirm your access. ${coverage}`
  }
}

/** Grey "N not checked" mark for a release whose health leaves objects out. */
export function NotCheckedBadge({ count }: { count?: number }) {
  if (!count || count <= 0) return null
  return (
    <Tooltip content={<div className="max-w-xs">{notCheckedTooltip(count, 'either', 'health')}</div>}>
      <span className={clsx('badge-sm shrink-0 whitespace-nowrap', SEVERITY_BADGE.neutral)}>
        <span className="w-1.5 h-1.5 rounded-full bg-theme-text-tertiary" />
        {count} {count === 1 ? 'object' : 'objects'} not checked
      </span>
    </Tooltip>
  )
}
