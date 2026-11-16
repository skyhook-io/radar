import { CloudUpload, Gauge, Info } from 'lucide-react'
import { Badge, Property, PropertyList, Section, Tooltip } from '@skyhook-io/k8s-ui'
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
 * A section at the top of the Overview tab that judges a WorkerPool's
 * capacity, or a Node's snapshot uploads, from the same response as the
 * Metrics tab's charts. A Node is a generic page whose readers may never have
 * heard of Agent Substrate, so the title names it and a sentence says what the
 * state means. Shown only when there is a verdict to read; the charts explain
 * every other state. onOpenMetrics switches to a Metrics tab; without one (the
 * drawer, which shows the charts below), the link scrolls to them.
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
  const pool = target.kind === 'workerpool'
  const title = pool ? 'Agent Substrate capacity' : 'Agent Substrate snapshots'
  // A pool's only qualifier is a shortfall that could not be judged. On
  // Substrate's default install that is every pool, so it is said in the
  // section, not only in a tooltip.
  const qualifier = verdict.state !== 'unknown' ? verdict.reason : undefined
  // Series Radar could not tie to this cluster.
  const unverified = error ? undefined : data?.charts.find(c => c.state === 'ok' && c.isolation.mode === 'unverified')?.isolation.note
  return (
    <section aria-label={title}>
      <Section title={title} icon={pool ? Gauge : CloudUpload}>
        <div className="space-y-3 text-sm">
          <div className="flex items-start gap-2">
            <span className="shrink-0 pt-px"><VerdictState verdict={verdict} /></span>
            <p className="min-w-0 text-theme-text-secondary">
              {meaning(verdict, pool)}
              {qualifier && <> <span className="text-theme-text-tertiary">{sentence(qualifier)}</span></>}
              {unverified && (
                <Tooltip content={sentence(unverified)}>
                  <Info aria-label="Qualified" className="ml-1 inline h-3.5 w-3.5 align-[-2px] text-theme-text-tertiary" />
                </Tooltip>
              )}
            </p>
          </div>
          {verdict.facts.length > 0 && (
            <PropertyList>
              {verdict.facts.map(f => <Fact key={f.id} fact={f} />)}
            </PropertyList>
          )}
          <button type="button" onClick={e => (onOpenMetrics ? onOpenMetrics() : scrollToCharts(e.currentTarget))} className="text-xs text-theme-text-tertiary underline-offset-2 hover:text-theme-text-primary hover:underline">
            View metrics
          </button>
        </div>
      </Section>
    </section>
  )
}

const sentence = (s: string) => (s.endsWith('.') ? s : `${s}.`)

// What the state means, in the reader's terms. Claims only what the count
// proves: a free slot is not proof the next actor fits, and zero overdue
// uploads is not proof every snapshot is saved.
function meaning(verdict: SubstrateVerdict, pool: boolean): string {
  switch (verdict.state) {
    case 'workerShortfall':
      return 'Fewer workers can take actors than the pool wants, and it has stayed that way for five minutes. New and resuming actors may wait or fail to start.'
    case 'noFreeActorSlots':
      return 'No worker in this pool has a free actor slot. Actor starts and resumes may wait or fail until there is capacity.'
    case 'noSchedulableWorkers':
      return 'No worker can take an actor right now.'
    case 'freeActorSlots':
      return 'Some worker has a free actor slot. Slots count actors only, so an actor that needs more CPU or memory than that worker has left can still fail to start.'
    case 'uploadsOverdue':
      return 'Some actor snapshots on this node are overdue for upload to object storage. Until Substrate confirms them stored, losing this node could lose that snapshot state.'
    case 'noUploadsOverdue':
      return 'No snapshot upload is overdue on this node. Snapshots with no storage destination are not counted.'
  }
  return verdict.reason
    ? sentence(verdict.reason)
    : `Radar could not read this ${pool ? "pool's capacity" : "node's snapshot uploads"} from Prometheus.`
}

function VerdictState({ verdict }: { verdict: SubstrateVerdict }) {
  switch (verdict.state) {
    case 'workerShortfall':
      return <Badge severity="warning" size="sm">Short of workers</Badge>
    case 'uploadsOverdue':
      return <Badge severity="warning" size="sm">Uploads overdue</Badge>
    case 'noFreeActorSlots':
      return <Badge severity="neutral" size="sm">Full</Badge>
    case 'noSchedulableWorkers':
      return <Badge severity="neutral" size="sm">No schedulable workers</Badge>
    case 'freeActorSlots':
      return <Badge severity="neutral" size="sm">Free slots</Badge>
    case 'noUploadsOverdue':
      return <Badge severity="neutral" size="sm">No overdue uploads</Badge>
  }
  return <Badge severity="neutral" size="sm">Unknown</Badge>
}

// Facts read over the hour the charts cover; the others are now.
const HOURLY_FACTS = new Set(['resumeP95', 'resumeFailures', 'crashes'])

// Resumes carry a pool only once a worker is assigned.
const POOL_PLACED = 'Resumes of actors placed on this pool. A resume that failed before it got a worker names no pool and is not counted.'

// A range summary covers only the time Prometheus collected: a rate stays
// representative, but a count misses whatever happened during a gap.
const HOURLY = 'Computed from the data Prometheus holds for the last hour. If collection had a gap, the crash count covers less than the hour.'

function Fact({ fact }: { fact: SubstrateFact }) {
  const placed = fact.id === 'resumeP95' || fact.id === 'resumeFailures'
  const hourly = HOURLY_FACTS.has(fact.id)
  const title = [placed && POOL_PLACED, hourly && HOURLY].filter(Boolean).join(' ') || undefined
  return (
    <Property
      label={<span title={title}>{fact.label}{hourly ? ' (1h)' : ''}</span>}
      value={<span className="tabular-nums"><FactValue fact={fact} /></span>}
    />
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
    case 'workersWithFreeActorSlots':
      return <span>{count(value)}{fact.of !== undefined ? ` of ${count(fact.of)}` : ''}</span>
    case 'schedulableWorkers':
      return <span>{count(value)}{fact.of !== undefined ? ` of ${count(fact.of)} desired` : ''}</span>
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
