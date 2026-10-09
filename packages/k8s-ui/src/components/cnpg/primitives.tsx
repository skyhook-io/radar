import { clsx } from 'clsx'
import { toneTextClass } from '../ui/status-tone'

/** status.currentPrimary and the primary role label disagree: both are named rather than one silently winning. */
export function PrimaryConflictNote({ conflict }: { conflict: { status: string; labelled: string } }) {
  return (
    <div className={clsx('mt-0.5 text-xs', toneTextClass('degraded'))}>
      CNPG status says primary <span className="font-mono">{conflict.status}</span>; the Pod labelled primary is{' '}
      <span className="font-mono">{conflict.labelled}</span>. Status may be stale, or a failover is under way.
    </div>
  )
}
