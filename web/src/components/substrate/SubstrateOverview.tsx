import { Fragment, useState, type ReactNode } from 'react'
import { toneTextClass, Badge, CertaintyGlyph, DimensionValue, FactGrid, FactRow, FoldSection, Input, Tooltip, WorkspaceCard, worseTone, type StatusTone } from '@skyhook-io/k8s-ui'
import type { SubstrateAtespaceRead, SubstrateAttention, SubstratePoolRow, SubstrateStatus, SubstrateWorkspace } from '../../api/substrateWorkspace'
import type { SelectedResource } from '../../types'
import { refToSelectedResource } from '../../utils/navigation'
import { ScreenBody, SectionTable, Segments, Sub, type TableColumn } from '../workspace/layout'
import { INCOMPLETE_COVERAGE_STATES, coverageEmpty, coverageLabel } from '../workspace/coverage'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { attentionArea, coverageInPart, countText, lackingText, poolReading, poolReadyPods, worstAttentionTone, type AttentionArea } from './readings'
import { SUBSTRATE_GROUP, SUBSTRATE_POOLS_PATH, SUBSTRATE_SCREENS, substrateActorsPath } from './routes'
import { SubstrateAttentionList, type OpenSubject } from './SubstrateAttentionList'
import { SubstrateConnectNotice, SubstrateConnectionLink, SubstrateTierBanner, showsConnectNotice } from './SubstrateConnect'
import { SubstrateStateBar } from './SubstrateStateBar'
import { NOT_CONNECTED_TITLE, Name, SubstrateCoverageNotice, SubstrateHeader, SubstrateRefreshFailure, SubstrateSourceLine, Term, Unread } from './shared'

export const SUBSTRATE_NO_PROBLEMS = 'No problems found by the checks that ran.'

/**
 * What the checks covered. An empty list says only that these checks found
 * nothing: it names them, the ones that could not run and why, and what no
 * check reads, so it never reads as "healthy". With problems listed, the same
 * list folds under them.
 */
function ChecksSummary({ data, empty }: { data: SubstrateWorkspace; empty?: boolean }) {
  const ran = data.checks.filter((c) => c.ran)
  // A check waiting on Radar's first collection from Prometheus runs on a later read: it is still running, not one that could not.
  const running = data.checks.filter((c) => !c.ran && c.collecting)
  const skipped = data.checks.filter((c) => !c.ran && !c.collecting)
  const counts = [running.length > 0 && `${running.length} still running`, skipped.length > 0 && `${skipped.length} could not`].filter(Boolean)
  const body = (
    <div className="space-y-2 text-xs text-theme-text-secondary">
      {ran.length > 0 && <CheckList title="Checks that ran" items={ran.map((c) => c.label)} />}
      {running.length > 0 && <CheckList title={`Still running: ${running[0].why ?? 'their inputs are still being collected'}`} items={running.map((c) => c.label)} />}
      {skipped.length > 0 && <CheckList title="Not checked on this read" items={skipped.map((c) => `${c.label}: ${c.why ?? 'its inputs were not read'}`)} />}
      {data.notAssessed.length > 0 && <CheckList title="Not assessed by any check" items={data.notAssessed} />}
    </div>
  )
  return (
    <>
      {empty && <p className="text-sm text-theme-text-primary">{ran.length > 0 ? SUBSTRATE_NO_PROBLEMS : 'No check could run on this read.'}</p>}
      <div className="mt-2 border-t-subtle pt-2">
        <FoldSection title="What was checked" summary={`${ran.length} of ${data.checks.length} checks ran${counts.length ? ` · ${counts.join(', ')}` : ''}`} attention={false}>
          {body}
        </FoldSection>
      </div>
    </>
  )
}

function CheckList({ title, items }: { title?: string; items: string[] }) {
  return (
    <div>
      {title && <div className="text-theme-text-tertiary">{title}</div>}
      <ul className="mt-0.5 list-disc space-y-0.5 pl-4">
        {items.map((i) => <li key={i}>{i}</li>)}
      </ul>
    </div>
  )
}

export interface SubstrateOverviewProps {
  data: SubstrateWorkspace
  status?: SubstrateStatus
  onOpenSubject: OpenSubject
}

interface Fact {
  label: ReactNode
  value: ReactNode
  tone: StatusTone
  /** The count covers only part of what exists. */
  lowerBound?: boolean
  title?: string
}

function FleetValue({ fact }: { fact: Fact }) {
  return (
    <>
      {fact.lowerBound && <CertaintyGlyph certainty="lower_bound" title="At least: some of it could not be read" />}
      {fact.title ? <Tooltip content={fact.title}><span>{fact.value}</span></Tooltip> : fact.value}
    </>
  )
}

/**
 * A fact's tone where Needs attention has rows about its kind: the worst of
 * theirs, so a condition reads one tone in its row and in the fact. Info rows
 * are expected and do not colour it.
 */
function areaTone(data: SubstrateWorkspace, area: AttentionArea, base: StatusTone): StatusTone {
  const rows = data.needsAttention.filter((a) => a.severity !== 'info' && attentionArea(a) === area)
  const worst = worstAttentionTone(rows)
  return worst ? worseTone(base, worst) : base
}

/**
 * The actor-slot fact. Free slots on a pool whose workers lack the CPU or
 * memory for an actor of its templates are not room: the line says so rather
 * than reading them as free capacity.
 */
function slotsFact(slots: NonNullable<SubstrateWorkspace['summary']['slots']>, freeSlots: SubstrateWorkspace['summary']['freeSlots'], partial: boolean): Omit<Fact, 'label'> {
  if (partial) return {
    value: slots.total > 0
      ? `${slots.used > 0 ? `≥${countText(slots.used)} actor slots used` : 'Slot usage unknown'} · ≥${countText(slots.total)} actor slots`
      : 'Actor slot capacity unknown',
    tone: 'unknown',
  }
  const used = `${countText(slots.used)} of ${countText(slots.total)} actor slots used`
  const title = 'Over active workers, the only ones Substrate places actors on.'
  const free = slots.total - slots.used
  if (free <= 0) return { value: used, tone: 'degraded', title }
  const roomy = (freeSlots ?? []).filter((f) => !f.lacking)
  const blocked = (freeSlots ?? []).filter((f) => f.lacking)
  const blockedFree = blocked.reduce((n, f) => n + f.free, 0)
  const lacking = new Set(blocked.map((f) => f.lacking))
  const what = lacking.size === 1 ? lackingText([...lacking][0]) : 'CPU or memory'
  const unfit = `${blocked.length === 1 ? `${blocked[0].pool.name}’s workers have` : 'their workers have'} no ${what} left for an actor`
  const fitTitle = `${title} Slots count actors only: placement also books each actor’s CPU and memory.`
  if (blockedFree > 0 && blockedFree >= free) {
    return { value: `${used} (${countText(free)} free, but ${unfit})`, tone: 'degraded', title: fitTitle }
  }
  const top = roomy[0]
  const where = top && roomy.length > 1 ? `, ${top.free} of them on ${top.pool.name}` : top ? ` on ${top.pool.name}` : ''
  if (blockedFree > 0) {
    return { value: `${used} (${countText(free - blockedFree)} free${where}; ${countText(blockedFree)} more where ${unfit})`, tone: 'healthy', title: fitTitle }
  }
  return { value: `${used} (${countText(free)} free${where})`, tone: 'healthy', title }
}

/** The status line: pools, workers, slots, actors, templates. A value Radar did not read says so, never 0. */
export function overviewFacts(data: SubstrateWorkspace): Fact[] {
  const { summary } = data
  const connected = data.apiTier.state === 'connected'
  const cov = data.coverage
  const poolsPartial = INCOMPLETE_COVERAGE_STATES.has(data.coverage.pools.state)
  const facts: Fact[] = []

  const pools = summary.pools
  if (pools) {
    const noRoom = pools.noRoom ?? 0
    const poolNotes = [pools.short > 0 && `${pools.short} short of workers`, pools.full > 0 && `${pools.full} full`, noRoom > 0 && `${noRoom} with no room for an actor`].filter(Boolean)
    facts.push({
      label: 'Worker pools',
      value: poolsPartial && pools.total === 0 ? 'Pool count unknown' : [`${countText(pools.total)} ${pools.total === 1 ? 'pool' : 'pools'}`, ...poolNotes].join(' · '),
      tone: areaTone(data, 'pools', poolNotes.length > 0 ? 'degraded' : pools.total > 0 ? 'healthy' : poolsPartial ? 'unknown' : 'neutral'),
      lowerBound: poolsPartial && pools.total > 0,
      title: poolsPartial ? `WorkerPools: ${coverageLabel(data.coverage.pools)}` : undefined,
    })
  } else {
    facts.push({ label: 'Worker pools', value: <Unread title={`WorkerPools: ${coverageLabel(data.coverage.pools)}`}>unknown</Unread>, tone: 'unknown' })
  }

  if (summary.workers) {
    const w = summary.workers
    const partial = coverageInPart(cov.workers)
    const notes = [w.draining > 0 && `${w.draining} draining`, w.unavailable > 0 && `${w.unavailable} unavailable`].filter(Boolean)
    facts.push({
      label: 'Workers',
      value: [partial && w.active === 0 ? 'Active worker count unknown' : `${partial ? '≥' : ''}${countText(w.active)} active`, ...notes.map((note) => partial ? `≥${note}` : note)].join(' · '),
      // An unavailable worker is at risk (alert), the tone of its chip and its row.
      tone: areaTone(data, 'workers', w.unavailable > 0 ? 'alert' : partial ? 'unknown' : w.total > 0 ? 'healthy' : 'neutral'),
    })
  } else {
    // Without the API, the worker Pods Kubernetes calls ready stand in, said as Pods.
    const k8s = data.pools.map((p) => p.kubernetes).filter((p): p is NonNullable<SubstratePoolRow['kubernetes']> => !!p)
    const readable = k8s.filter((p) => p.pods || p.reportedReady !== undefined)
    const ready = readable.reduce((n, p) => n + poolReadyPods(p), 0)
    const podsPartial = poolsPartial || readable.length < k8s.length
    const desired = k8s.every((p) => p.desired !== undefined) ? k8s.reduce((n, p) => n + (p.desired ?? 0), 0) : undefined
    facts.push({
      label: 'Worker Pods',
      value: (readable.length === 0 || (podsPartial && ready === 0)) ? 'Worker Pod readiness unknown' : `${countText(ready)} Pods ready${desired !== undefined ? ` of ${countText(desired)} desired` : ''}`,
      tone: readable.length === 0 || podsPartial ? 'unknown' : desired !== undefined && ready < desired ? 'degraded' : 'neutral',
      title: 'Ready worker Pods, from Kubernetes. Whether Substrate places actors on them needs its API.',
      lowerBound: podsPartial && ready > 0,
    })
  }

  if (summary.slots) {
    facts.push({ ...slotsFact(summary.slots, summary.freeSlots, coverageInPart(cov.workers)), label: <Term term="slot">Actor slots</Term> })
  } else {
    facts.push({ label: <Term term="slot">Actor slots</Term>, value: <Unread title={NOT_CONNECTED_TITLE}>{connected ? 'unknown' : 'Not connected'}</Unread>, tone: 'unknown' })
  }

  if (summary.actors) {
    facts.push({
      label: 'Actors',
      value: coverageInPart(cov.actors) && summary.actors.total === 0 ? 'Actor count unknown' : `${countText(summary.actors.total)} total${summary.actors.crashed > 0 ? `, ${countText(summary.actors.crashed)} crashed` : ''}`,
      tone: summary.actors.crashed > 0 ? 'unhealthy' : summary.actors.total > 0 ? 'healthy' : 'neutral',
      lowerBound: coverageInPart(cov.actors) && summary.actors.total > 0,
    })
  } else {
    facts.push({ label: 'Actors', value: <Unread title={NOT_CONNECTED_TITLE}>{connected ? 'unknown' : 'Not connected'}</Unread>, tone: 'unknown' })
  }

  if (summary.templates) {
    const t = summary.templates
    const noPool = t.noMatchingPool ?? 0
    const notes: ReactNode[] = [
      t.goldenFailed > 0 && <>{t.goldenFailed} <Term term="golden">golden snapshot</Term> failed</>,
      noPool > 0 && `${noPool} with no pool`,
    ].filter(Boolean)
    facts.push({
      label: 'Templates',
      // Fragments, not spans: the value sits in a flex row whose gap would push each comma away from its word.
      value: coverageInPart(cov.templates) && t.total === 0 ? 'Template count unknown' : <span>{[`${countText(t.total)} ${t.total === 1 ? 'template' : 'templates'}`, ...notes].map((n, i) => <Fragment key={i}>{i > 0 && ' · '}{n}</Fragment>)}</span>,
      // A template no pool matches is at risk (alert), as its badge and row say; a failed golden snapshot is a warning.
      tone: areaTone(data, 'templates', noPool > 0 ? 'alert' : t.goldenFailed > 0 ? 'degraded' : t.total > 0 ? 'healthy' : 'neutral'),
      lowerBound: coverageInPart(cov.templates) && t.total > 0,
    })
  } else {
    facts.push({ label: 'Templates', value: <Unread title={NOT_CONNECTED_TITLE}>{connected ? 'unknown' : 'Not connected'}</Unread>, tone: 'unknown' })
  }
  facts.push(...serviceFacts(data))
  return facts
}

const pct = (v: number) => `${v < 10 ? v.toFixed(1).replace(/\.0$/, '') : Math.round(v)}%`
const secs = (v: number) => `${v < 10 ? v.toFixed(1).replace(/\.0$/, '') : Math.round(v)}s`

/** Whether Prometheus gave any Substrate reading at all on this read. */
export function serviceMeasured(svc: SubstrateWorkspace['service']): boolean {
  return svc.read && (svc.slowestResume !== undefined || svc.crashes !== undefined || svc.nodesOverdue > 0 || svc.mostFailedResumes !== undefined)
}

/**
 * What Prometheus says about delivery: resume latency and failures, crashes
 * and overdue uploads. Two rules keep it honest beside the inventory: a value
 * Prometheus didn't give is unknown, never a calm "none"; and where Substrate's
 * API holds a definite problem (crashed actors, unstored snapshots), that sets
 * the colour, whatever Prometheus counted.
 */
export function serviceFacts(data: Pick<SubstrateWorkspace, 'service' | 'summary' | 'needsAttention'>): Fact[] {
  const svc = data.service
  const window = svc.window ?? '1h'
  const crashedNow = data.summary.actors?.crashed ?? 0
  const uploadRows = data.needsAttention.filter((a) => a.rule === 'uploadOverdue' || a.rule === 'nodeUploadsOverdue')
  const unstored = data.needsAttention.filter((a) => a.rule === 'uploadOverdue').reduce((n, a) => n + a.count, 0)
  const facts: Fact[] = []
  if (!serviceMeasured(svc)) {
    facts.push({
      label: `Delivery (${window})`,
      // While the first collection runs, "no Substrate metrics" would claim an absence Radar hasn't read yet.
      value: svc.collecting
        ? <Unread title={svc.why}>being collected from Prometheus</Unread>
        : <Unread title={svc.why ?? 'Prometheus holds no Agent Substrate series, so resume latency, failed resumes and crash rates are unknown.'}>Unavailable · no Substrate series</Unread>,
      tone: 'unknown',
    })
  } else {
    const resume = [
      svc.slowestResume && `slowest p95 ${secs(svc.slowestResume.value)} on ${svc.slowestResume.pool.name}`,
      svc.mostFailedResumes && svc.mostFailedResumes.value > 0 && `${pct(svc.mostFailedResumes.value)} failed on ${svc.mostFailedResumes.pool.name}`,
    ].filter(Boolean)
    facts.push({
      label: `Resumes (${window})`,
      value: resume.length ? resume.join(', ') : <Unread>unknown</Unread>,
      tone: !resume.length ? 'unknown' : svc.mostFailedResumes && svc.mostFailedResumes.value > 0 ? 'degraded' : 'healthy',
      title: 'From Substrate’s Prometheus metrics, over resumes placed on a pool. A resume that fails before placement names no pool and is not counted.',
      lowerBound: resume.length > 0 && svc.poolsUnknown > 0,
    })
    const crashes = svc.crashes !== undefined ? Math.round(svc.crashes) : undefined
    facts.push({
      label: `Crashes (${window})`,
      value: crashes !== undefined ? countText(crashes) : <Unread>unknown</Unread>,
      // Crashed actors in the inventory are definite: they colour the fact even when Prometheus counted none.
      tone: (crashes ?? 0) > 0 || crashedNow > 0 ? 'unhealthy' : crashes === undefined ? 'unknown' : 'healthy',
      title: crashedNow > 0 && (crashes ?? 0) === 0 ? `Prometheus counts ${crashes ?? 'no'} crashes in the last ${window}; Substrate’s API lists ${crashedNow} crashed ${crashedNow === 1 ? 'actor' : 'actors'} now.` : undefined,
      lowerBound: (crashes ?? 0) > 0 && svc.poolsUnknown > 0,
    })
  }
  if (unstored > 0) {
    // The tone of the worst upload row: alert while the nodes are up, critical once one holding them is down.
    const worst = worstAttentionTone(uploadRows) ?? 'degraded'
    facts.push({
      label: 'Uploads',
      value: `${countText(unstored)} ${unstored === 1 ? 'snapshot' : 'snapshots'} not stored yet`,
      tone: worst,
      title: svc.nodesOverdue > 0 ? `Substrate actor snapshots · atelet metrics show overdue uploads on ${svc.nodesOverdue} ${svc.nodesOverdue === 1 ? 'node' : 'nodes'}` : 'From suspended actor snapshots in Substrate',
    })
  } else if (svc.read && svc.nodesOverdue > 0) {
    facts.push({
      label: 'Uploads',
      value: `overdue on ${svc.nodesOverdue} ${svc.nodesOverdue === 1 ? 'node' : 'nodes'}`,
      tone: worstAttentionTone(uploadRows) ?? 'alert',
      title: 'atelet’s own count of snapshot uploads past their deadline.',
      lowerBound: svc.nodesUnknown > 0,
    })
  }
  return facts
}

/** The pool's one reading, the same as its page header: Substrate's verdict, Kubernetes' phase beneath. */
export function PoolReadingCell({ p, finding }: { p: SubstratePoolRow; finding?: SubstrateAttention }) {
  const r = poolReading(p, finding)
  return (
    <>
      <Tooltip content={r.kubernetes ? `${r.meaning} Kubernetes: ${r.kubernetes}.` : r.meaning}><span><Badge severity={r.severity} size="sm" className="whitespace-normal text-left">{r.label}</Badge></span></Tooltip>
      {/* Kubernetes' own verdict earns a line only when it explains a problem. */}
      {p.kubernetes?.healthDetail && <Sub>{p.kubernetes.healthReason === 'Provisioning stalled' ? 'No Deployment reported' : p.kubernetes.healthDetail}</Sub>}
      {!p.kubernetes?.healthDetail && finding && p.capacity !== 'short' && <Sub>{finding.reading.split('. ')[0]}</Sub>}
    </>
  )
}

function WorkersCell({ p }: { p: SubstratePoolRow }) {
  const desired = p.kubernetes?.desired
  if (p.placement) {
    const w = p.placement.workers
    if (!p.placement.complete && w.active === 0) return <Unread>Unknown</Unread>
    const notes = [w.draining > 0 && `${w.draining} draining`, w.unavailable > 0 && `${w.unavailable} unavailable`].filter(Boolean)
    return (
      <>
        <span className="inline-flex items-center gap-1 whitespace-nowrap">
          {!p.placement.complete && <CertaintyGlyph certainty="lower_bound" title="At least: Radar read only part of Substrate’s workers" />}
          {w.active} active{desired !== undefined ? ` of ${desired}` : ''}
        </span>
        {notes.length > 0 && <Sub>{notes.join(' · ')}</Sub>}
      </>
    )
  }
  return <Unread>Unknown</Unread>
}

export function slotsOffActive(p: SubstratePoolRow, actorsPartial: boolean): number {
  if (actorsPartial || !p.placement?.complete || p.actors === undefined || p.placement.workers.draining + p.placement.workers.unavailable === 0) return 0
  return p.actors - p.placement.slots.used
}

export function SlotsCell({ p, connected, inline, actorsPartial }: { p: SubstratePoolRow; connected: boolean; inline?: boolean; actorsPartial: boolean }) {
  const placement = p.placement
  if (!placement) return <Unread title={NOT_CONNECTED_TITLE}>{connected ? 'Unknown' : 'Not connected'}</Unread>
  const s = placement.slots
  if (s.total === 0) return placement.complete
    ? <Tooltip content="No active worker of this pool has an actor slot, so Substrate places no actors here."><span>0 slots</span></Tooltip>
    : <Unread>Unknown</Unread>
  const elsewhere = slotsOffActive(p, actorsPartial)
  return (
    <>
      <span className="inline-flex items-center gap-1 whitespace-nowrap">
        {!placement.complete && s.used > 0 && <CertaintyGlyph certainty="lower_bound" title="At least: Radar read only part of Substrate’s workers" />}
        {placement.complete ? `${s.used} of ${s.total} used` : `${s.used === 0 ? 'Usage unknown' : `${s.used} used`} · ≥${s.total} slots`}
      </span>
      {!inline && elsewhere > 0 && <Sub>+{elsewhere} on non-active workers</Sub>}
    </>
  )
}

export function PoolsTable({ data, onInspect, inspected }: {
  data: SubstrateWorkspace
  onInspect: (resource: SelectedResource) => void
  inspected: SelectedResource | null
}) {
  const navigate = useWorkspaceNavigate()
  const [search, setSearch] = useState('')
  const [filter, setFilter] = useState<'all' | 'attention' | 'free'>('all')
  const findings = new Map(data.needsAttention
    .filter((f) => f.subject.kind === 'WorkerPool' && f.severity !== 'info')
    .map((f) => [`${f.subject.namespace}/${f.subject.name}`, f]))
  const key = (p: SubstratePoolRow) => `${p.namespace}/${p.name}`
  const finding = (p: SubstratePoolRow) => findings.get(key(p))
  const rank = (p: SubstratePoolRow) => {
    const ranks = { error: 0, alert: 1, warning: 2, info: 3, neutral: 3, success: 4 }
    return ranks[poolReading(p, finding(p)).severity]
  }
  const scoped = data.pools.filter((p) => key(p).toLowerCase().includes(search.trim().toLowerCase()))
  const needsAttention = (p: SubstratePoolRow) => !!finding(p)
  const free = (p: SubstratePoolRow) => p.capacity === 'free' && !needsAttention(p)
  const rows = scoped.filter((p) => filter === 'all' || (filter === 'attention' ? needsAttention(p) : free(p)))
    .sort((a, b) => rank(a) - rank(b) || key(a).localeCompare(key(b)))
  const partial = INCOMPLETE_COVERAGE_STATES.has(data.coverage.pools.state) || coverageInPart(data.coverage.workers)
  const count = (n: number) => partial ? n > 0 ? `≥${n}` : undefined : n
  const namespaces = [...new Set(data.pools.map((p) => p.namespace))]
  const oneNamespace = namespaces.length === 1 ? namespaces[0] : undefined
  const openWorkers = (p: SubstratePoolRow) => navigate(`${SUBSTRATE_SCREENS.workers.path}?${new URLSearchParams({ pool: key(p) })}`)
  const columns: TableColumn<SubstratePoolRow>[] = [
    {
      header: 'Pool',
      width: '21%',
      cell: (p) => (
        <>
          <Name name={p.name} />
          {!oneNamespace && <Sub>{p.namespace}</Sub>}
          {!p.kubernetes && <Sub>Named by Substrate workers</Sub>}
        </>
      ),
    },
    { header: 'Workers accepting actors', width: '18%', cell: (p) => <WorkersCell p={p} /> },
    { header: 'Slots on active workers', width: '17%', cell: (p) => <SlotsCell p={p} connected={data.apiTier.state === 'connected'} actorsPartial={coverageInPart(data.coverage.actors)} /> },
    {
      header: 'Kubernetes readiness',
      width: '19%',
      cell: ({ kubernetes: k }) => k
        ? <>{k.reportedReady === undefined ? 'Ready replicas unreported' : `${k.reportedReady} ready · controller`}{k.pods && <Sub>{k.pods.created === 0 ? '0 Pods observed' : `${k.pods.ready} of ${k.pods.created} observed ${k.pods.created === 1 ? 'Pod' : 'Pods'} ready`}</Sub>}</>
        : <Unread>WorkerPool not read</Unread>,
    },
    { header: 'Needs attention', cell: (p) => <PoolReadingCell p={p} finding={finding(p)} /> },
    {
      header: <span className="sr-only">Open pool</span>,
      width: '76px',
      cell: (p) => (
        <button type="button" aria-label={p.kubernetes ? `Open pool ${p.name}` : `Open workers for ${p.name}`} onClick={(e) => {
          e.stopPropagation()
          if (p.kubernetes) onInspect(refToSelectedResource({ kind: 'WorkerPool', group: SUBSTRATE_GROUP, namespace: p.namespace, name: p.name }))
          else openWorkers(p)
        }} className="whitespace-nowrap text-xs text-accent-text hover:underline">Open →</button>
      ),
    },
  ]
  const empty = data.coverage.pools.state === 'notInstalled' ? 'The WorkerPool kind is not installed.' : coverageEmpty(data.coverage.pools, 'WorkerPools')
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <Segments label="Pool filter" value={filter} onChange={setFilter} options={[
          { id: 'all', label: 'All', count: count(scoped.length) }, { id: 'attention', label: 'Needs attention', count: count(scoped.filter(needsAttention).length) }, { id: 'free', label: 'Free slots', count: count(scoped.filter(free).length) },
        ]} />
        <Input aria-label="Search pools" placeholder="Search pools…" value={search} onChange={(e) => setSearch(e.target.value)} className="h-8 max-w-52 rounded-md border border-theme-border bg-theme-elevated px-3" />
      </div>
      <SectionTable
        subtitle={oneNamespace ? <>Namespace: <span className="font-mono">{oneNamespace}</span></> : undefined}
        columns={columns}
        rows={rows}
        rowKey={key}
        rowResource={(p) => refToSelectedResource({ kind: 'WorkerPool', group: SUBSTRATE_GROUP, namespace: p.namespace, name: p.name })}
        onInspect={(res) => {
          const p = rows.find((row) => row.namespace === res.namespace && row.name === res.name)
          if (p?.kubernetes) onInspect(res)
          else if (p) openWorkers(p)
        }}
        inspected={inspected}
        empty={data.pools.length && !rows.length ? 'No pools match these filters.' : empty}
        minWidth={850}
        footer={INCOMPLETE_COVERAGE_STATES.has(data.coverage.pools.state) ? `WorkerPools Radar couldn’t read are not listed: ${coverageLabel(data.coverage.pools)}.` : undefined}
      />
    </>
  )
}

type AtespaceRow = NonNullable<SubstrateWorkspace['atespaces']>[number]

/** One atespace's count of a kind: a lower bound when only part was read, and no number when none was. */
function AtespaceCount({ read, count, noun }: { read: SubstrateAtespaceRead; count?: number; noun: string }) {
  if (read === 'denied') return <Unread title={`Substrate denied reading this atespace’s ${noun}.`}>No access</Unread>
  if (read === 'partial' && count === 0) return <Unread title={`Radar found no ${noun} in the part of this atespace it could read.`}>Unknown</Unread>
  if (read === 'unread' || count === undefined) return <Unread title={`Radar didn’t read this atespace’s ${noun}: the read’s budget ran out first, or the connection’s atespaces don’t name it.`}>not read</Unread>
  return (
    <span className="inline-flex items-center gap-1">
      {read === 'partial' && <CertaintyGlyph certainty="lower_bound" title={`At least: Radar read only part of this atespace’s ${noun}`} />}
      {countText(count)}
    </span>
  )
}

function AtespacesTable({ data }: { data: SubstrateWorkspace }) {
  const navigate = useWorkspaceNavigate()
  if (!data.atespaces) return null
  const columns: TableColumn<AtespaceRow>[] = [
    { header: 'Atespace', cell: (a) => <Name name={a.name} /> },
    { header: 'Actors', width: '100px', cell: (a) => <AtespaceCount read={a.actorsRead} count={a.actors?.total} noun="actors" /> },
    { header: 'States', width: '200px', cell: (a) => (a.actors ? <div className="pt-1.5"><SubstrateStateBar counts={a.actors} compact /></div> : <Unread>—</Unread>) },
    { header: 'Templates', width: '100px', cell: (a) => <AtespaceCount read={a.templatesRead} count={a.templates} noun="templates" /> },
    {
      header: 'Crashed',
      width: '90px',
      cell: (a) => {
        if (!a.actors) return <Unread>—</Unread>
        const n = a.actors.byState.crashed
        const partial = a.actorsRead === 'partial'
        if (partial && n === 0) return <Unread>Unknown</Unread>
        return (
          <span className={`inline-flex items-center gap-1 ${n > 0 ? `font-medium ${toneTextClass('unhealthy')}` : 'text-theme-text-tertiary'}`}>
            {partial && <CertaintyGlyph certainty="lower_bound" title="At least: Radar read only part of this atespace’s actors" />}
            {n}
          </span>
        )
      },
    },
  ]
  return (
    <section>
      <SectionTable
        title={<Term term="atespace">Atespaces</Term>}
        subtitle="Open one to see its actors."
        columns={columns}
        rows={[...data.atespaces].sort((a, b) => (b.actors?.byState.crashed ?? 0) - (a.actors?.byState.crashed ?? 0) || a.name.localeCompare(b.name))}
        rowKey={(a) => a.name}
        // Not a Kubernetes object: the row opens the Actors list narrowed to it.
        rowResource={(a) => ({ kind: 'Atespace', namespace: '', name: a.name })}
        onInspect={(res) => navigate(substrateActorsPath({ atespace: res.name }))}
        empty={coverageInPart(data.coverage?.atespaces) ? 'Atespaces could not be read.' : 'No atespaces.'}
        minWidth={640}
      />
    </section>
  )
}

export function SubstrateOverview({ data, status, onOpenSubject }: SubstrateOverviewProps) {
  const navigate = useWorkspaceNavigate()
  const tier = data.apiTier
  const [pools, workers, slots, actors, templates, ...service] = overviewFacts(data)
  const uploads = service.find((f) => f.label === 'Uploads')
  const delivery = service.filter((f) => f !== uploads)
  const uploadCheck = data.checks.find((c) => c.rule === 'uploadOverdue')
  const uploadsInPart = coverageInPart(data.coverage.actors)
  const uploadReading: Fact = uploads ?? {
    label: 'Uploads',
    value: uploadCheck?.ran && !uploadsInPart ? 'No overdue uploads found' : 'Unknown',
    tone: uploadCheck?.ran && !uploadsInPart ? 'healthy' as const : 'unknown' as const,
  }
  const uploadRow = data.needsAttention.find((f) => f.rule === 'uploadOverdue')
  const actorsExtra = data.summary.actors && data.needsAttention.some((a) => a.rule === 'goldenSnapshotFailed') && !coverageInPart(data.coverage.templates)
    ? data.summary.actors.crashed - (data.needsAttention.find((f) => f.rule === 'crashedActors')?.count ?? 0)
    : 0
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <SubstrateHeader title="Overview" intro="Cluster-wide" />
      <ScreenBody>
        <SubstrateTierBanner tier={tier} />
        {showsConnectNotice(tier) && <SubstrateConnectNotice status={status} tier={tier} />}
        <SubstrateCoverageNotice coverage={data.coverage} kinds={['actors', 'templates', 'workers', 'atespaces']} />
        <SubstrateRefreshFailure tier={tier} />
        <WorkspaceCard title="Needs attention">
          {data.needsAttention.length > 0 && <SubstrateAttentionList rows={data.needsAttention} onOpen={onOpenSubject} />}
          <ChecksSummary data={data} empty={data.needsAttention.length === 0} />
        </WorkspaceCard>
        <WorkspaceCard title="At a glance">
          <FactGrid>
            <FactRow label="Actors">
              <DimensionValue tone={actors.tone} source={actorsExtra > 0 ? `Reported phases from Substrate · ${actorsExtra} failed golden ${actorsExtra === 1 ? 'build is' : 'builds are'} tracked under Templates` : 'Reported phases from Substrate'} linkLabel="Actors" onOpen={() => navigate(SUBSTRATE_SCREENS.actors.path)}>
                <FleetValue fact={actors} />
              </DimensionValue>
            </FactRow>
            <FactRow label="Worker capacity">
              <DimensionValue
                tone={worseTone(pools.tone, workers.tone)}
                source={<>
                  <div className={`text-sm ${toneTextClass(slots.tone)}`}><FleetValue fact={slots} /></div>
                  <div>Kubernetes WorkerPools and Substrate API</div>
                </>}
                linkLabel="Pools"
                onOpen={() => navigate(SUBSTRATE_POOLS_PATH)}
              >
                <FleetValue fact={pools} /> · <FleetValue fact={workers} />
              </DimensionValue>
            </FactRow>
            <FactRow label="Template startup">
              <DimensionValue tone={templates.tone} source="Template records from Substrate" linkLabel="Templates" onOpen={() => navigate(SUBSTRATE_SCREENS.templates.path)}>
                <FleetValue fact={templates} />
              </DimensionValue>
            </FactRow>
            <FactRow label="Snapshot uploads">
              <DimensionValue tone={uploadReading.tone} source={uploadReading.title ?? (uploadCheck?.why || 'Latest suspended snapshots from Substrate')} linkLabel="View all suspended actors" onOpen={uploadRow ? () => onOpenSubject(uploadRow.subject) : undefined}>
                <FleetValue fact={{ ...uploadReading, lowerBound: uploadsInPart && !!uploadRow }} />
              </DimensionValue>
            </FactRow>
            <FactRow label="Delivery metrics">
              <DimensionValue tone={delivery.reduce<StatusTone>((tone, f) => worseTone(tone, f.tone), delivery[0].tone)} source={data.service.why || `Substrate Prometheus series · last ${data.service.window ?? '1h'}`}>
                {delivery.map((f, i) => <Fragment key={i}>{i > 0 && ' · '}{delivery.length > 1 && <>{f.label}: </>}<FleetValue fact={f} /></Fragment>)}
              </DimensionValue>
            </FactRow>
          </FactGrid>
        </WorkspaceCard>
        <WorkspaceCard>
          <FoldSection title="Coverage and connection" summary={data.atespaces ? 'Atespaces, data sources and connection settings' : 'Data sources and connection settings'} attention={false}>
            <div className="space-y-3">
              <SubstrateSourceLine tier={tier} status={status} showRefreshFailure={false} />
              <SubstrateConnectionLink status={status} />
              <AtespacesTable data={data} />
            </div>
          </FoldSection>
        </WorkspaceCard>
      </ScreenBody>
    </div>
  )
}
