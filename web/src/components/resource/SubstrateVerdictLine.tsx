import { Badge, Tooltip } from '@skyhook-io/k8s-ui'
import { formatMetricValue } from '@skyhook-io/k8s-ui/components/charts'
import { isForbiddenError, usePrometheusStatus } from '../../api/client'
import { useSubstrateHealth, type SubstrateFact, type SubstrateTarget, type SubstrateVerdict } from '../../api/substrateHealth'

const OVERDUE_BANDS: Record<string, string> = {
  under_1m: 'under 1m',
  '1m_to_5m': '1–5m',
  '5m_to_15m': '5–15m',
  over_15m: 'over 15m',
}

/**
 * One line on the Overview tab that judges a WorkerPool's capacity, or a
 * Node's snapshot safety, from the same response as the Metrics tab's charts.
 * Shown only when there is a verdict to read; the charts explain every other
 * state. onOpenMetrics switches to a Metrics tab; without one (the drawer,
 * which shows the charts below), the link scrolls to them.
 */
export function SubstrateVerdictLine({ target, onOpenMetrics }: { target: SubstrateTarget; onOpenMetrics?: () => void }) {
  const { data: status } = usePrometheusStatus()
  const connected = status?.connected === true
  const { data, error } = useSubstrateHealth(target, '1h', connected && Boolean(target.name))
  // A failed refresh leaves the last answer cached; it must not keep asserting it.
  const verdict: SubstrateVerdict | undefined = !connected || isForbiddenError(error)
    ? undefined
    : error
      ? { state: 'unknown', reason: `Radar could not read Prometheus: ${error.message}`, facts: [] }
      : data?.status === 'available' ? data.verdict : undefined
  if (!verdict) return null
  const title = target.kind === 'workerpool' ? 'Capacity' : 'Snapshots'
  return (
    <section aria-label={title} className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-theme-text-secondary">
      <span className="font-medium text-theme-text-tertiary">{title}</span>
      <VerdictState verdict={verdict} />
      {verdict.facts.map(f => <Fact key={f.id} fact={f} />)}
      <button type="button" onClick={e => (onOpenMetrics ? onOpenMetrics() : scrollToCharts(e.currentTarget))} className="text-theme-text-tertiary underline-offset-2 hover:text-theme-text-primary hover:underline">
        View metrics
      </button>
    </section>
  )
}

function VerdictState({ verdict }: { verdict: SubstrateVerdict }) {
  switch (verdict.state) {
    case 'short':
      return <Badge severity="warning" size="sm" title="Fewer schedulable workers than desired for five minutes: Substrate's own capacity alert condition">Short of workers</Badge>
    case 'behind':
      return <Badge severity="warning" size="sm" title="Some actors' snapshot uploads to object storage are overdue">Uploads behind</Badge>
    case 'full':
      return <Badge severity="neutral" size="sm" title="No API replica sees a free worker slot">Full</Badge>
    case 'noWorkers':
      return <Badge severity="neutral" size="sm">No workers</Badge>
    case 'serving':
      return <span className="text-theme-text-primary">Serving</span>
    case 'current':
      // Only what the count proves: snapshots with no storage destination are
      // not counted as overdue, so zero overdue is not "everything is saved".
      return <span className="text-theme-text-primary">No overdue uploads</span>
  }
  return (
    <Tooltip content={verdict.reason ?? 'Radar could not read this from Prometheus'}>
      <span className="text-theme-text-tertiary">Unknown</span>
    </Tooltip>
  )
}

// Facts read over the hour the charts cover; the others are now.
const HOURLY_FACTS = new Set(['resumeP95', 'resumeFailures', 'crashes'])

function Fact({ fact }: { fact: SubstrateFact }) {
  return (
    <span className="tabular-nums">
      <span className="text-theme-text-tertiary">{fact.label}{HOURLY_FACTS.has(fact.id) ? ' (1h)' : ''} </span>
      <FactValue fact={fact} />
    </span>
  )
}

function FactValue({ fact }: { fact: SubstrateFact }) {
  if (fact.state === 'unavailable' || (fact.state === 'ok' && fact.value === undefined)) {
    return (
      <Tooltip content={fact.reason || 'Radar could not read this from Prometheus'}>
        <span className="text-theme-text-tertiary">Unavailable</span>
      </Tooltip>
    )
  }
  if (fact.state === 'empty') {
    return <span title={fact.reason}>None</span>
  }
  const value = fact.value!
  switch (fact.id) {
    case 'freeWorkers':
      return <span>{count(value)}{fact.of !== undefined ? ` of ${count(fact.of)}` : ''}</span>
    case 'crashes':
      return <span>{count(value)}</span>
    case 'overdueUploads': {
      const parts = fact.parts?.map(p => `${OVERDUE_BANDS[p.label] ?? p.label}: ${count(p.value)}`)
      return <span>{count(value)}{parts?.length ? ` (${parts.join(', ')})` : ''}</span>
    }
  }
  return <span>{formatMetricValue(value, fact.unit)}</span>
}

const CHARTS = 'section[aria-label="Agent Substrate metrics"]'

// The nearest charts below this line, not another mounted view's.
function scrollToCharts(from: HTMLElement) {
  let el: HTMLElement | null = from.parentElement
  while (el && !el.querySelector(CHARTS)) el = el.parentElement
  el?.querySelector(CHARTS)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

// Replica averages and extrapolated increases can be fractional; they are
// shown as approximate, never rounded to a whole count.
function count(value: number): string {
  if (Number.isInteger(value)) return String(value)
  return `≈${Math.abs(value) < 10 ? value.toFixed(1) : Math.round(value)}`
}
