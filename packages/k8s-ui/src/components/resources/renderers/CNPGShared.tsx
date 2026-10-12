import { formatRelativeAgeTime } from '../../../utils/format'

/**
 * A recovery point, stated absolutely with the interval beside it.
 *
 * "1d ago" is the wrong unit for this question on its own. An RPO conversation
 * needs a timestamp — and "Last Successful Backup 1d ago" next to "Last Failed
 * Attempt 1d ago" is the same string for events a week apart in the failure
 * history, which is precisely when you are reading this screen. Same treatment
 * the certificate rows already use.
 *
 * Shared by the Cluster and ObjectStore pages: the same recovery point is
 * reachable from both depending on whether backups run through the plugin, and
 * the two must not describe it differently.
 */
export function RecoveryTime({ at }: { at: string }) {
  const d = new Date(at)
  if (Number.isNaN(d.getTime())) return <>{at}</>
  return (
    <span className="inline-flex items-baseline gap-2 min-w-0">
      <span>{d.toISOString().replace('T', ' ').replace(/\.\d+Z$/, ' UTC')}</span>
      {/* One interpolation, so the age and its unit stay a single text node
          rather than being split by a renderer comment marker. */}
      <span className="text-xs text-theme-text-tertiary shrink-0">{formatRelativeAgeTime(at)}</span>
    </span>
  )
}
