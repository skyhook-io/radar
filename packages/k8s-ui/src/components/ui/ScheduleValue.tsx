import { Tooltip } from './Tooltip'
import { cronToHuman } from '../resources/resource-utils'
import { formatRelativeAgeTime } from '../../utils/format'

/**
 * Hover affordance used across the drawer: without it a tooltip is a secret.
 * Matches the Created row in drawer-components and the QoS label in PodRenderer.
 */
const HOVERABLE = 'border-b border-dotted border-theme-text-tertiary cursor-help'

/** Which cron dialect the operator that owns this field parses. */
export type CronDialect =
  /** Five fields, minute first. Kubernetes CronJob, Kyverno, Velero, Argo. */
  | 'standard'
  /** Six fields, seconds first. CloudNativePG. */
  | 'seconds'

interface CronValueProps {
  cron: string
  /**
   * Defaults to the Kubernetes dialect. Pass 'seconds' for CloudNativePG: read
   * as five fields, `0 0 2 * * *` describes midnight instead of 2am, and a
   * backup window stated an hour wrong is worse than one left unexplained.
   */
  dialect?: CronDialect
}

/**
 * A cron expression, verbatim, with its meaning on hover.
 *
 * The expression stays the value because it is the data — operators copy it,
 * diff it against Git, and paste it into `kubectl`. The plain-English reading
 * is the annotation, not the replacement.
 */
/**
 * The plain-English reading to offer for an expression, or undefined when there
 * is none worth offering.
 *
 * cronToHuman returns its input when it cannot describe an expression with
 * confidence — a monthly schedule, an unusual step. A tooltip repeating the
 * value teaches the reader that the underline is not worth hovering, which
 * costs more than the one it would have explained.
 *
 * Separate from the component because the tooltip renders into a portal on
 * hover, so the wording is not in the server-rendered markup and cannot be
 * asserted there.
 */
export function cronHint(cron: string, dialect: CronDialect = 'standard'): string | undefined {
  if (!cron || dialect !== 'standard') return undefined
  const human = cronToHuman(cron)
  return human === cron || human === '-' ? undefined : human
}

export function CronValue({ cron, dialect = 'standard' }: CronValueProps) {
  const hint = cronHint(cron, dialect)
  if (!hint) return <span className="font-mono">{cron}</span>

  return (
    <Tooltip content={hint} position="right">
      <span className={`font-mono ${HOVERABLE}`}>{cron}</span>
    </Tooltip>
  )
}

interface TimeValueProps {
  /** RFC3339 from the apiserver. */
  timestamp?: string
  /** Shown when there is no timestamp. "Never" reads better than a dash on a
   *  field that records something having happened. */
  fallback?: string
}

/**
 * A moment in time as an age, with the exact local time on hover.
 *
 * "6h ago" answers the question actually being asked — is this recent? — and
 * the raw RFC3339 answers it in UTC, which is nobody's wall clock.
 */
export function TimeValue({ timestamp, fallback = '-' }: TimeValueProps) {
  if (!timestamp) return <>{fallback}</>
  return (
    <Tooltip content={new Date(timestamp).toLocaleString()} position="right">
      <span className={HOVERABLE}>{formatRelativeAgeTime(timestamp)}</span>
    </Tooltip>
  )
}
