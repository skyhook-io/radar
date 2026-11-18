import { Fragment, useState, type ReactNode } from 'react'
import { Badge, CertaintyGlyph, Collapse, CollapseChevron, StatusDot, Tooltip, worseTone, type StatusTone } from '@skyhook-io/k8s-ui'
import type { SubstrateAtespaceRead, SubstratePoolRow, SubstrateStatus, SubstrateWorkspace } from '../../api/substrateWorkspace'
import type { SelectedResource } from '../../types'
import { refToSelectedResource } from '../../utils/navigation'
import { ScreenBody, SectionTable, Sub, type TableColumn } from '../workspace/layout'
import { INCOMPLETE_COVERAGE_STATES, coverageEmpty, coverageLabel } from '../workspace/coverage'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { attentionArea, coverageInPart, countText, poolReading, poolReadyPods, worstAttentionTone, type AttentionArea } from './readings'
import { SUBSTRATE_GROUP, substrateActorsPath } from './routes'
import { SubstrateAttentionList, type OpenSubject } from './SubstrateAttentionList'
import { SubstrateConnectNotice, SubstrateConnectionLink, SubstrateTierBanner, showsConnectNotice } from './SubstrateConnect'
import { SubstrateStateBar } from './SubstrateStateBar'
import { NOT_CONNECTED_TITLE, Name, SUBSTRATE_WHAT_IT_IS, SUBSTRATE_WIDE, SubstrateCoverageNotice, SubstrateHeader, SubstrateSourceLine, Term, Unread, WholeNames } from './shared'
import { useMediaQuery } from '../../hooks/useMediaQuery'

export const SUBSTRATE_NO_PROBLEMS = 'No problems found by these checks:'

/**
 * What the checks covered. An empty list says only that these checks found
 * nothing: it names them, the ones that could not run and why, and what no
 * check reads, so it never reads as "healthy". With problems listed, the same
 * list folds under them.
 */
function ChecksSummary({ data, empty }: { data: SubstrateWorkspace; empty?: boolean }) {
  const [open, setOpen] = useState(false)
  const ran = data.checks.filter((c) => c.ran)
  // A check waiting on Radar's first collection from Prometheus runs on a later read: it is still running, not one that could not.
  const running = data.checks.filter((c) => !c.ran && c.collecting)
  const skipped = data.checks.filter((c) => !c.ran && !c.collecting)
  const counts = [running.length > 0 && `${running.length} still running`, skipped.length > 0 && `${skipped.length} could not`].filter(Boolean)
  const body = (
    <div className="space-y-2 text-xs text-theme-text-secondary">
      {ran.length > 0 && <CheckList title={empty ? undefined : 'Checks that ran'} items={ran.map((c) => c.label)} />}
      {running.length > 0 && <CheckList title={`Still running: ${running[0].why ?? 'their inputs are still being collected'}`} items={running.map((c) => c.label)} />}
      {skipped.length > 0 && <CheckList title="Not checked on this read" items={skipped.map((c) => `${c.label}: ${c.why ?? 'its inputs were not read'}`)} />}
      {data.notAssessed.length > 0 && <CheckList title="Not assessed by any check" items={data.notAssessed} />}
    </div>
  )
  if (empty) {
    return (
      <div>
        <p className="mb-1.5 text-sm text-theme-text-primary">{ran.length > 0 ? SUBSTRATE_NO_PROBLEMS : 'No check could run on this read.'}</p>
        {body}
      </div>
    )
  }
  return (
    <div className="mt-3 border-t-subtle pt-2">
      <button type="button" aria-expanded={open} aria-controls="substrate-checks" onClick={() => setOpen((v) => !v)} className="inline-flex items-center gap-1 text-xs font-medium text-accent-text hover:underline">
        <CollapseChevron open={open} />
        What was checked: {ran.length} of {data.checks.length} checks ran{counts.length > 0 ? `, ${counts.join(', ')}` : ''}
      </button>
      <Collapse open={open} id="substrate-checks"><div className="pt-2">{body}</div></Collapse>
    </div>
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
  onInspect: (resource: SelectedResource) => void
  inspected: SelectedResource | null
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

function StatusLine({ facts }: { facts: Fact[] }) {
  return (
    <dl className="flex flex-wrap gap-x-7 gap-y-2 text-sm" aria-label="Agent Substrate at a glance">
      {facts.map((f, i) => (
        <div key={i} className="flex items-center gap-1.5">
          <StatusDot tone={f.tone} />
          <dt className="text-theme-text-tertiary">{f.label}:</dt>
          <dd className="flex items-center gap-1 font-medium text-theme-text-primary">
            {f.lowerBound && <CertaintyGlyph certainty="lower_bound" title="At least: some of it could not be read" />}
            {f.title ? <Tooltip content={f.title}><span>{f.value}</span></Tooltip> : f.value}
          </dd>
        </div>
      ))}
    </dl>
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

/** The status line: pools, workers, slots, actors, templates. A value Radar did not read says so, never 0. */
export function overviewFacts(data: SubstrateWorkspace): Fact[] {
  const { summary } = data
  const connected = data.apiTier.state === 'connected'
  const cov = data.coverage
  const poolsPartial = INCOMPLETE_COVERAGE_STATES.has(data.coverage.pools.state)
  const facts: Fact[] = []

  const pools = summary.pools
  if (pools) {
    const poolNotes = [pools.short > 0 && `${pools.short} short of workers`, pools.full > 0 && `${pools.full} full`].filter(Boolean)
    facts.push({
      label: <Term term="pool">Worker pools</Term>,
      value: [countText(pools.total), ...poolNotes].join(', '),
      tone: areaTone(data, 'pools', poolNotes.length > 0 ? 'degraded' : pools.total > 0 ? 'healthy' : poolsPartial ? 'unknown' : 'neutral'),
      lowerBound: poolsPartial,
      title: poolsPartial ? `WorkerPools: ${coverageLabel(data.coverage.pools)}` : undefined,
    })
  } else {
    facts.push({ label: <Term term="pool">Worker pools</Term>, value: <Unread title={`WorkerPools: ${coverageLabel(data.coverage.pools)}`}>unknown</Unread>, tone: 'unknown' })
  }

  if (summary.workers) {
    const w = summary.workers
    const notes = [w.draining > 0 && `${w.draining} draining`, w.unavailable > 0 && `${w.unavailable} unavailable`].filter(Boolean)
    facts.push({
      label: <Term term="worker">Workers</Term>,
      value: [`${countText(w.active)} active`, ...notes].join(', '),
      // An unavailable worker is at risk (alert), the tone of its chip and its row.
      tone: areaTone(data, 'workers', w.unavailable > 0 ? 'alert' : w.total > 0 ? 'healthy' : 'neutral'),
      lowerBound: coverageInPart(cov?.workers),
    })
  } else {
    // Without the API, the worker Pods Kubernetes calls ready stand in, said as Pods.
    const k8s = data.pools.map((p) => p.kubernetes).filter((p): p is NonNullable<SubstratePoolRow['kubernetes']> => !!p)
    const ready = k8s.reduce((n, p) => n + p.ready, 0)
    const desired = k8s.every((p) => p.desired !== undefined) ? k8s.reduce((n, p) => n + (p.desired ?? 0), 0) : undefined
    facts.push({
      label: 'Worker Pods',
      value: k8s.length === 0 ? 'unknown' : desired !== undefined ? `${countText(ready)} ready of ${countText(desired)}` : `${countText(ready)} ready`,
      tone: k8s.length === 0 ? 'unknown' : desired !== undefined && ready < desired ? 'degraded' : 'neutral',
      title: 'Ready worker Pods, from Kubernetes. Whether Substrate places actors on them needs its API.',
      lowerBound: poolsPartial,
    })
  }

  if (summary.slots) {
    const free = summary.slots.total - summary.slots.used
    const top = summary.freeSlots?.[0]
    const where = top && free > 0 && summary.freeSlots!.length > 1 ? `, ${top.free} of them on ${top.pool.name}` : top && free > 0 ? ` on ${top.pool.name}` : ''
    facts.push({
      label: <Term term="slot">Actor slots</Term>,
      value: `${countText(summary.slots.used)} of ${countText(summary.slots.total)} used${free > 0 ? ` (${countText(free)} free${where})` : ''}`,
      tone: free <= 0 ? 'degraded' : 'healthy',
      lowerBound: coverageInPart(cov?.workers),
      title: 'Over active workers, the only ones Substrate places actors on.',
    })
  } else {
    facts.push({ label: <Term term="slot">Actor slots</Term>, value: <Unread title={NOT_CONNECTED_TITLE}>{connected ? 'unknown' : 'Not connected'}</Unread>, tone: 'unknown' })
  }

  if (summary.actors) {
    facts.push({
      label: <Term term="actor">Actors</Term>,
      value: `${countText(summary.actors.total)}${summary.actors.crashed > 0 ? `, ${countText(summary.actors.crashed)} crashed` : ''}`,
      tone: summary.actors.crashed > 0 ? 'unhealthy' : summary.actors.total > 0 ? 'healthy' : 'neutral',
      lowerBound: coverageInPart(cov?.actors),
    })
  } else {
    facts.push({ label: <Term term="actor">Actors</Term>, value: <Unread title={NOT_CONNECTED_TITLE}>{connected ? 'unknown' : 'Not connected'}</Unread>, tone: 'unknown' })
  }

  if (summary.templates) {
    const t = summary.templates
    const noPool = t.noMatchingPool ?? 0
    const notes: ReactNode[] = [
      t.goldenFailed > 0 && <>{t.goldenFailed} <Term term="golden">golden snapshot</Term> failed</>,
      noPool > 0 && `${noPool} with no pool`,
    ].filter(Boolean)
    facts.push({
      label: <Term term="template">Templates</Term>,
      // Fragments, not spans: the value sits in a flex row whose gap would push each comma away from its word.
      value: <span>{[countText(t.total), ...notes].map((n, i) => <Fragment key={i}>{i > 0 && ', '}{n}</Fragment>)}</span>,
      // A template no pool matches is at risk (alert), as its badge and row say; a failed golden snapshot is a warning.
      tone: areaTone(data, 'templates', noPool > 0 ? 'alert' : t.goldenFailed > 0 ? 'degraded' : t.total > 0 ? 'healthy' : 'neutral'),
      lowerBound: coverageInPart(cov?.templates),
    })
  } else {
    facts.push({ label: <Term term="template">Templates</Term>, value: <Unread title={NOT_CONNECTED_TITLE}>{connected ? 'unknown' : 'Not connected'}</Unread>, tone: 'unknown' })
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
        : <Unread title={svc.why ?? 'Prometheus holds no Agent Substrate series, so resume latency, failed resumes and crash rates are unknown.'}>unknown (no Substrate metrics)</Unread>,
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
      title: svc.nodesOverdue > 0 ? `atelet reports overdue uploads on ${svc.nodesOverdue} ${svc.nodesOverdue === 1 ? 'node' : 'nodes'}.` : 'From Substrate’s API. atelet’s own overdue count, from Prometheus, reports none or wasn’t read.',
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

function ServiceCell({ p, svc }: { p: SubstratePoolRow; svc: SubstrateWorkspace['service'] }) {
  if (!svc.read) return <Unread title={svc.why}>unknown</Unread>
  const s = p.service
  if (!s || s.verdict === 'unknown') return <Unread title={s?.reason ?? 'Prometheus gave no reading for this pool.'}>unknown</Unread>
  return (
    <>
      <span className="whitespace-nowrap">{s.resumeP95Seconds !== undefined ? `p95 ${secs(s.resumeP95Seconds)}` : <span className="text-theme-text-tertiary">p95 unknown</span>}</span>
      <Sub>
        {[s.failedResumesPercent !== undefined && `${pct(s.failedResumesPercent)} failed`, s.crashes !== undefined && `${Math.round(s.crashes)} ${Math.round(s.crashes) === 1 ? 'crash' : 'crashes'}`].filter(Boolean).join(' · ') || 'no failures reported'}
      </Sub>
    </>
  )
}

/** The pool's one reading, the same as its page header: Substrate's verdict, Kubernetes' phase beneath. */
export function PoolReadingCell({ p }: { p: SubstratePoolRow }) {
  const r = poolReading(p)
  return (
    <>
      <Tooltip content={r.kubernetes ? `${r.meaning} Kubernetes: ${r.kubernetes}.` : r.meaning}><span><Badge severity={r.severity} size="sm" className="whitespace-normal text-left">{r.label}</Badge></span></Tooltip>
      {/* Kubernetes' own verdict earns a line only when it explains a problem. */}
      {r.kubernetes && p.capacity !== 'free' && <Sub>Kubernetes: {r.kubernetes}</Sub>}
    </>
  )
}

function WorkersCell({ p }: { p: SubstratePoolRow }) {
  const desired = p.kubernetes?.desired
  if (p.placement) {
    const w = p.placement.workers
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
  if (p.kubernetes) {
    return (
      <>
        <span>{poolReadyPods(p.kubernetes)} ready{desired !== undefined ? ` of ${desired}` : ''}</span>
        <Sub>worker Pods, from Kubernetes</Sub>
      </>
    )
  }
  return <Unread>unknown</Unread>
}

function SlotsCell({ p, connected }: { p: SubstratePoolRow; connected: boolean }) {
  if (!p.placement) return <Unread title={NOT_CONNECTED_TITLE}>{connected ? 'unknown' : 'Not connected'}</Unread>
  const s = p.placement.slots
  // Substrate counts an unreported actor capacity as zero: such a worker takes no actors.
  if (s.total === 0) return <Tooltip content="No active worker of this pool has an actor slot, so Substrate places no actors here."><span>0 slots</span></Tooltip>
  // Slots count active workers only; the pool's other actors sit on draining or unavailable workers, which take no new actors.
  // Saying so keeps "31 of 48 used" beside "38 actors" from reading as a contradiction.
  const w = p.placement.workers
  const elsewhere = p.actors !== undefined && w.draining + w.unavailable > 0 ? p.actors - s.used : 0
  const which = w.draining > 0 && w.unavailable > 0 ? 'draining or unavailable' : w.draining > 0 ? 'draining' : 'unavailable'
  return (
    <>
      <span className="inline-flex items-center gap-1 whitespace-nowrap">
        {!p.placement.complete && <CertaintyGlyph certainty="lower_bound" title="At least: Radar read only part of Substrate’s workers" />}
        {s.used} of {s.total} used
      </span>
      {elsewhere > 0 && <Sub>+{elsewhere} {elsewhere === 1 ? 'actor' : 'actors'} on {which} workers, which take no new actors</Sub>}
    </>
  )
}

export function PoolsTable({ data, onInspect, inspected }: Pick<SubstrateOverviewProps, 'data' | 'onInspect' | 'inspected'>) {
  const connected = data.apiTier.state === 'connected'
  const wide = useMediaQuery(SUBSTRATE_WIDE)
  const measured = serviceMeasured(data.service)
  const classes = (p: SubstratePoolRow) => (p.sandboxClasses?.length ? p.sandboxClasses.join(', ') : undefined)
  // One namespace for every pool is said once, in the title, not on each row.
  const namespaces = [...new Set(data.pools.map((p) => p.namespace))]
  const oneNamespace = namespaces.length === 1 ? namespaces[0] : undefined
  const columns: TableColumn<SubstratePoolRow>[] = [
    {
      header: 'Pool',
      width: wide ? (measured ? '16%' : '18%') : '30%',
      cell: (p) => {
        const sub = [!oneNamespace && p.namespace, !wide && classes(p), !p.kubernetes && 'named only by Substrate’s workers'].filter(Boolean).join(' · ')
        return (
          <>
            <Name name={p.name} />
            {sub && <Sub><WholeNames text={sub} /></Sub>}
          </>
        )
      },
    },
    ...(wide ? [{ header: 'Sandbox class', width: measured ? '10%' : '11%', cell: (p: SubstratePoolRow) => (classes(p) ? <span className="font-mono text-[12.5px]"><WholeNames text={classes(p)!} /></span> : <Unread>none named</Unread>) }] : []),
    { header: 'Workers', width: wide ? (measured ? '16%' : '17%') : '20%', cell: (p) => <WorkersCell p={p} /> },
    { header: 'Actor slots', width: wide ? (measured ? '12%' : '13%') : '16%', cell: (p) => <SlotsCell p={p} connected={connected} /> },
    ...(wide && measured ? [{ header: `Resumes (${data.service.window ?? '1h'})`, width: '12%', cell: (p: SubstratePoolRow) => <ServiceCell p={p} svc={data.service} /> }] : []),
    { header: 'Actors', width: wide ? (measured ? '8%' : '9%') : '12%', cell: (p) => (p.actors !== undefined ? <span>{countText(p.actors)}</span> : <Unread title={NOT_CONNECTED_TITLE}>{connected ? 'unknown' : '—'}</Unread>) },
    {
      header: 'Capacity',
      width: wide ? (measured ? '26%' : '32%') : '22%',
      cell: (p) => <PoolReadingCell p={p} />,
    },
  ]
  const empty = data.coverage.pools.state === 'notInstalled' ? 'The WorkerPool kind is not installed.' : coverageEmpty(data.coverage.pools, 'WorkerPools')
  return (
    <SectionTable
      title="Worker pools"
      subtitle={oneNamespace ? <>in namespace <span className="font-mono">{oneNamespace}</span></> : undefined}
      columns={columns}
      rows={data.pools}
      rowKey={(p) => `${p.namespace}/${p.name}`}
      rowResource={(p) => (p.kubernetes ? refToSelectedResource({ kind: 'WorkerPool', group: SUBSTRATE_GROUP, namespace: p.namespace, name: p.name }) : null)}
      onInspect={onInspect}
      inspected={inspected}
      empty={empty}
      minWidth={wide ? 900 : 640}
      footer={[
        INCOMPLETE_COVERAGE_STATES.has(data.coverage.pools.state) && data.pools.length > 0 ? `WorkerPools Radar couldn’t read are not listed: ${coverageLabel(data.coverage.pools)}.` : '',
        !measured ? 'No Agent Substrate metrics in Prometheus: resume latency and crash rates per pool are unknown.' : '',
      ].filter(Boolean).join(' ') || undefined}
    />
  )
}

type AtespaceRow = NonNullable<SubstrateWorkspace['atespaces']>[number]

/** One atespace's count of a kind: a lower bound when only part was read, and no number when none was. */
function AtespaceCount({ read, count, noun }: { read: SubstrateAtespaceRead; count?: number; noun: string }) {
  if (read === 'denied') return <Unread title={`Substrate denied reading this atespace’s ${noun}.`}>No access</Unread>
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
        return (
          <span className={`inline-flex items-center gap-1 ${n > 0 ? 'font-medium' : 'text-theme-text-tertiary'}`}>
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
        rows={data.atespaces}
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

export function SubstrateOverview({ data, status, onInspect, inspected, onOpenSubject }: SubstrateOverviewProps) {
  const navigate = useWorkspaceNavigate()
  const tier = data.apiTier
  const connected = tier.state === 'connected'
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <SubstrateHeader title="Overview" intro={SUBSTRATE_WHAT_IT_IS} />
      <ScreenBody>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <SubstrateSourceLine tier={tier} status={status} />
          {connected && <SubstrateConnectionLink status={status} />}
        </div>
        <SubstrateCoverageNotice coverage={data.coverage} kinds={['actors', 'templates', 'workers', 'atespaces']} />
        <SubstrateTierBanner tier={tier} />
        <StatusLine facts={overviewFacts(data)} />
        {showsConnectNotice(tier) && <SubstrateConnectNotice status={status} tier={tier} />}
        {data.actorStates && (
          <section aria-labelledby="substrate-states">
            <div className="mb-2 flex flex-wrap items-baseline gap-x-2.5">
              <h2 id="substrate-states" className="text-sm font-semibold text-theme-text-primary">Actor states</h2>
              <span className="text-xs text-theme-text-tertiary">
                counted by Substrate across{' '}
                {data.atespaces ? (
                  <>{data.atespaces.length.toLocaleString('en-US')} <Term term="atespace">{data.atespaces.length === 1 ? 'atespace' : 'atespaces'}</Term></>
                ) : (
                  <>every <Term term="atespace">atespace</Term> it lets Radar read</>
                )}
              </span>
            </div>
            <div className="rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
              <SubstrateStateBar counts={data.actorStates} onSelectState={(state) => navigate(substrateActorsPath({ state }))} />
            </div>
          </section>
        )}
        <section aria-labelledby="substrate-attention">
          <h2 id="substrate-attention" className="mb-2 text-sm font-semibold text-theme-text-primary">Needs attention</h2>
          <div className="rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
            {data.needsAttention.length === 0 ? (
              <ChecksSummary data={data} empty />
            ) : (
              <>
                <SubstrateAttentionList rows={data.needsAttention} onOpen={onOpenSubject} />
                <ChecksSummary data={data} />
              </>
            )}
          </div>
        </section>
        <PoolsTable data={data} onInspect={onInspect} inspected={inspected} />
        <AtespacesTable data={data} />
      </ScreenBody>
    </div>
  )
}
