import { useEffect, useState } from 'react'
import type { KueuePendingPosition } from '../../types/scheduling'
import type { RadarUpgradeRequirement } from '../../types/fetch-error'
import { RadarUpgradeNote } from '../ui/RadarUpgradeNote'
import { formatRelativeAgeTime } from '../../utils/format'
import { readActionFor } from '../../utils/read-actions'

export interface KueuePendingPositionValueProps {
  position?: KueuePendingPosition
  loading?: boolean
  /** The request failed and there is no answer to show. */
  error?: string
  /** The last refresh failed; the answer shown is from an earlier read. */
  refreshFailed?: boolean
  /** The connected Radar predates the position endpoint. */
  upgrade?: RadarUpgradeRequirement
  onRetry?: () => void
  /** The Workload's status.requeueState.requeueAt: Kueue keeps a ranked Workload back until then. */
  requeueAt?: string
}

function ReadAge({ readAt }: { readAt?: string }) {
  const [, tick] = useState(0)
  useEffect(() => {
    if (!readAt) return
    const id = setInterval(() => tick(n => n + 1), 15_000)
    return () => clearInterval(id)
  }, [readAt])
  if (!readAt) return null
  return <time dateTime={readAt} title={readAt}>{formatRelativeAgeTime(readAt)}</time>
}

function Retry({ onRetry, label = 'Retry' }: { onRetry?: () => void; label?: string }) {
  if (!onRetry) return null
  return <button type="button" className="ml-1 text-accent-text hover:underline" onClick={onRetry}>{label}</button>
}

/**
 * One Workload's position as Kueue's visibility API reported it, or why
 * there is none. Radar never derives a position; every state says what Kueue
 * answered, when, or why it couldn't be asked.
 */
export function KueuePendingPositionValue({ position, loading, error, refreshFailed, upgrade, onRetry, requeueAt }: KueuePendingPositionValueProps) {
  if (upgrade) return <RadarUpgradeNote requirement={upgrade} />
  if (!position) {
    if (error) return <span className="text-theme-text-secondary">Couldn't ask Kueue: {error}<Retry onRetry={onRetry} /></span>
    return <span className="text-theme-text-tertiary">{loading ? 'Asking Kueue…' : 'Waiting for a current answer'}</span>
  }
  const queue = position.localQueue ? <span className="font-medium">{position.localQueue}</span> : 'its LocalQueue'
  const caption = (text: React.ReactNode) => <span className="mt-0.5 block text-xs text-theme-text-tertiary">{text}</span>
  const stale = refreshFailed && caption(<>Couldn't refresh{position.readAt && <> · last read <ReadAge readAt={position.readAt} /></>}<Retry onRetry={onRetry} /></>)
  const content = (() => {
    switch (position.status) {
      case 'listed':
        return <span>
          <span className="text-theme-text-primary">
            #{position.aheadInLocalQueue! + 1} in LocalQueue {queue}
            {' · #'}{position.aheadInClusterQueue! + 1} in its ClusterQueue
          </span>
          {caption(<>Kueue's pending order{!refreshFailed && <>, read <ReadAge readAt={position.readAt} /></>}. Not a promise of admission order or a start time.</>)}
          {requeueAt && Date.parse(requeueAt) > Date.now() && caption(<>Kueue won't try to admit it again before <time dateTime={requeueAt} title={requeueAt}>{new Date(requeueAt).toLocaleString()}</time>.</>)}
        </span>
      case 'not_listed':
        return <span>
          <span className="text-theme-text-secondary">Not in Kueue's pending list for LocalQueue {queue}</span>
          {caption(<>{!refreshFailed && <>Read <ReadAge readAt={position.readAt} />. </>}It may have been admitted or deactivated since, or Kueue hasn't picked it up yet.</>)}
        </span>
      case 'limit_reached':
        return <span>
          <span className="text-theme-text-secondary">Not among the first {position.rows!.toLocaleString()} Workloads Kueue listed for LocalQueue {queue}</span>
          {caption(<>{!refreshFailed && <>Read <ReadAge readAt={position.readAt} />. </>}Its position is unknown.</>)}
        </span>
      case 'queue_unknown':
        return <span className="text-theme-text-secondary">{position.message || <>Kueue doesn't know LocalQueue {queue}.</>}<Retry onRetry={onRetry} /></span>
      case 'no_local_queue':
        return <span className="text-theme-text-tertiary">No LocalQueue named, so Kueue holds no position for it.</span>
      case 'not_served':
        return <span className="text-theme-text-tertiary">Radar's API discovery didn't find Kueue's visibility API (visibility.kueue.x-k8s.io).<Retry onRetry={onRetry} label="Check again" /></span>
      case 'unsupported':
        return <span className="text-theme-text-tertiary">{position.message || "Kueue serves its visibility API only at versions Radar doesn't read."}</span>
      case 'unavailable':
        return <span className="text-theme-text-secondary">
          {position.message || "Radar couldn't get Kueue's answer."}
          {readActionFor(position.code) === 'retry' && <Retry onRetry={onRetry} />}
        </span>
      default:
        return <span className="text-theme-text-tertiary">Radar reported a position status this view doesn't know: {position.status}</span>
    }
  })()
  return <span>{content}{stale}</span>
}
