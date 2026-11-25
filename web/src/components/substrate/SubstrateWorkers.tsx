import { Fragment, useMemo, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import { clsx } from 'clsx'
import { Badge, Collapse, CollapseChevron, Input, PaneLoader, SelectMenu, Tooltip, formatDuration } from '@skyhook-io/k8s-ui'
import {
  useSubstrateWorkers,
  type SubstrateAttention,
  type SubstrateCoverage,
  type SubstrateResources,
  type SubstrateStatus,
  type SubstratePoolRow,
  type SubstrateWorkerRow,
  type SubstrateWorkspace,
} from '../../api/substrateWorkspace'
import { Notice, RefreshFailedNotice, ScreenBody, Segments, Sub, type TableColumn } from '../workspace/layout'
import { ROW_HOVER, TABLE_HEAD, TABLE_WRAP, TBODY, TD, TH } from '../workspace/table'
import { useConnection } from '../../context/ConnectionContext'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import type { OpenSubject } from './SubstrateAttentionList'
import { SubstrateConnectNotice, SubstrateTierBanner } from './SubstrateConnect'
import { bytesText, coverageInPart, coverageReason, cpuText, poolReading, workerPodText } from './readings'
import { substrateActorsPath, substratePoolPath } from './routes'
import { ActorStateBadge, Name, SUBSTRATE_WIDE, SubstrateCoverageNotice, SubstrateHeader, SubstrateSourceLine, Term, Unread, WholeNames, WorkerStateChip } from './shared'
import { useMediaQuery } from '../../hooks/useMediaQuery'



/** What a worker's CPU and memory figures are, from Substrate's own fields. */
export const RESERVED_MEANING = 'Reserved: what the actors on it request, with their runtime’s overhead, of the capacity the worker reports to Substrate. Not measured usage.'

/** A worker's actors lost to its restart, while Substrate has not yet reconciled it: said beside the chip, so the chip stays one line. */
export function lostText(w: Pick<SubstrateWorkerRow, 'workerState'>): string | undefined {
  const lost = w.workerState?.kind === 'reconciling' ? w.workerState.lost : undefined
  return lost ? `${lost} ${lost === 1 ? 'actor' : 'actors'} lost` : undefined
}

function Reserved({ children }: { children: ReactNode }) {
  return (
    <Tooltip content={RESERVED_MEANING}>
      <span tabIndex={0} className="cursor-help underline decoration-dotted underline-offset-2">{children}</span>
    </Tooltip>
  )
}

function usage(allocated: SubstrateResources | undefined, capacity: SubstrateResources | undefined, dim: keyof SubstrateResources, fmt: (n: number) => string) {
  if (!allocated || !capacity) return <Unread>unknown</Unread>
  if (dim === 'actors' && capacity.actors === 0) return <Tooltip content="Substrate counts an unreported actor capacity as zero: this worker takes no actors."><span className="whitespace-nowrap">{allocated.actors} of 0</span></Tooltip>
  return <span className="whitespace-nowrap">{fmt(allocated[dim])} of {fmt(capacity[dim])}</span>
}

/** Wide: every column. Narrow: CPU and memory under the slots, the node under the Pod. */
export function workerColumns(onOpenSubject: OpenSubject, wide: boolean): TableColumn<SubstrateWorkerRow>[] {
  return [
    {
      header: 'Worker Pod',
      width: wide ? '20%' : '25%',
      cell: (w) => (
        <>
          <Name name={w.pod} />
          {!wide && w.node && <Sub><WholeNames text={w.node} /></Sub>}
          <button type="button" onClick={(e) => { e.stopPropagation(); onOpenSubject({ kind: 'Pod', namespace: w.namespace, name: w.pod }) }} className="text-xs text-accent-text hover:underline">Open Pod →</button>
        </>
      ),
    },
    {
      header: 'State',
      width: wide ? '20%' : '25%',
      cell: (w) => (
        <>
          <span className="inline-flex flex-wrap items-center gap-x-1.5">
            <WorkerStateChip w={w} />
            {w.heldForSeconds !== undefined && <span className="whitespace-nowrap text-xs text-theme-text-tertiary">for at least {formatDuration(w.heldForSeconds * 1000)}</span>}
          </span>
          <Sub>
            {[lostText(w), workerPodText(w)].filter(Boolean).join(' · ')}
          </Sub>
        </>
      ),
    },
    {
      header: <Term term="slot">Actor slots</Term>,
      width: wide ? '10%' : '28%',
      cell: (w) => (
        <>
          <span className="inline-flex flex-wrap items-center gap-1.5">
            {usage(w.allocated, w.capacity, 'actors', String)}
            {w.state === 'active' && !w.hasFreeSlot && (
              <Tooltip content="No actor slot left: Substrate places no new actor on this worker until one suspends. The pool’s own badge says whether any of its workers has room."><span><Badge severity="neutral" size="sm">Full</Badge></span></Tooltip>
            )}
          </span>
          {!wide && <Sub>{usage(w.allocated, w.capacity, 'cpuMillicores', cpuText)} CPU · {usage(w.allocated, w.capacity, 'memoryBytes', bytesText)} <Reserved>reserved</Reserved></Sub>}
        </>
      ),
    },
    ...(wide
      ? [
          { header: <Reserved>CPU reserved</Reserved>, width: '10%', cell: (w: SubstrateWorkerRow) => usage(w.allocated, w.capacity, 'cpuMillicores', cpuText) },
          { header: <Reserved>Memory reserved</Reserved>, width: '12%', cell: (w: SubstrateWorkerRow) => usage(w.allocated, w.capacity, 'memoryBytes', bytesText) },
          {
            header: 'Node',
            width: '17%',
            // A hostname truncates whole, with the name on hover; it never breaks inside a label.
            cell: (w: SubstrateWorkerRow) => (w.node ? <Tooltip content={w.node} wrapperClassName="min-w-0 max-w-full"><span className="block truncate whitespace-nowrap font-mono text-[12px]">{w.node}</span></Tooltip> : <Unread>unknown</Unread>),
          },
        ]
      : []),

  ]
}

const STATE_RANK: Record<SubstrateWorkerRow['state'], number> = { unavailable: 0, draining: 1, unknown: 2, active: 3 }

// A worker whose actors lost their sandboxes ranks with the unavailable ones: its actors are as affected.
const lostSandboxes = (w: SubstrateWorkerRow) => (w.workerState?.lost ?? 0) > 0
const ATTENTION_RANK = { critical: 0, alert: 1, warning: 2, info: 3 }
const rank = (w: SubstrateWorkerRow) =>
  w.attentionSeverity ? ATTENTION_RANK[w.attentionSeverity] : lostSandboxes(w) ? 0 : 4

/** Within a pool: the worst state first, then full workers, then by name. */
export function sortWorkers(workers: SubstrateWorkerRow[]): SubstrateWorkerRow[] {
  return [...workers].sort(
    (a, b) =>
      rank(a) - rank(b) ||
      STATE_RANK[a.state] - STATE_RANK[b.state] ||
      Number(a.state === 'active' && a.hasFreeSlot) - Number(b.state === 'active' && b.hasFreeSlot) ||
      a.pod.localeCompare(b.pod),
  )
}

type StateFilter = 'all' | 'attention' | SubstrateWorkerRow['state']

/** Substrate's workers by pool: state, slots, and the actors on each. */
export function SubstrateWorkersScreen({ workspace, status, onOpenSubject }: { workspace?: SubstrateWorkspace; status?: SubstrateStatus; onOpenSubject: OpenSubject }) {
  const [params, setParams] = useSearchParams()
  const navigate = useWorkspaceNavigate()
  const { connection } = useConnection()
  const [search, setSearch] = useState('')
  const tier = workspace?.apiTier
  const connected = tier?.state === 'connected'
  const query = useSubstrateWorkers(undefined, connected)
  const data = query.data
  const pool = params.get('pool') ?? ''
  const stateFilter = (params.get('state') as StateFilter | null) ?? 'all'
  const set = (k: string, v: string) => {
    const p = new URLSearchParams(params)
    if (v && v !== 'all') p.set(k, v)
    else p.delete(k)
    setParams(p, { replace: true })
  }
  const registeredPools = useMemo(() => new Set((data?.workers ?? []).map((w) => `${w.namespace}/${w.pool}`)), [data])
  const emptyPools = data?.coverage.workers.state === 'full'
    ? (workspace?.pools ?? []).filter((p) => p.kubernetes && !registeredPools.has(`${p.namespace}/${p.name}`))
    : []
  const pools = [...new Set([...registeredPools, ...emptyPools.map((p) => `${p.namespace}/${p.name}`)])].sort()
  const scoped = (data?.workers ?? []).filter((w) => (!pool || `${w.namespace}/${w.pool}` === pool) && w.pod.toLowerCase().includes(search.trim().toLowerCase()))
  const needsAttention = (w: SubstrateWorkerRow) => w.attentionSeverity !== undefined && w.attentionSeverity !== 'info'
  const shown = scoped.filter((w) => stateFilter === 'all' || (stateFilter === 'attention' ? needsAttention(w) : w.state === stateFilter))
  const partial = coverageInPart(data?.coverage.workers)
  const count = (n: number) => partial ? n > 0 ? `≥${n}` : undefined : n
  const attentionPartial = partial || coverageInPart(data?.coverage.actors)
  const attentionCount = scoped.filter(needsAttention).length
  const poolFindings = workspace?.needsAttention.filter((a) => a.subject.kind === 'WorkerPool')
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <SubstrateHeader title="Workers" intro="Cluster-wide" />
      <ScreenBody>
        {data && (
          <div className="flex flex-wrap items-center gap-2">
            <Segments
              label="Worker filter"
              value={stateFilter}
              onChange={(v) => set('state', v)}
              options={[
                { id: 'all', label: 'All', count: count(scoped.length) },
                { id: 'attention', label: 'Needs attention', count: attentionPartial ? attentionCount > 0 ? `≥${attentionCount}` : undefined : attentionCount },
                { id: 'active', label: 'Active', count: count(scoped.filter((w) => w.state === 'active').length) },
                { id: 'draining', label: 'Draining', count: count(scoped.filter((w) => w.state === 'draining').length) },
                { id: 'unavailable', label: 'Unavailable', count: count(scoped.filter((w) => w.state === 'unavailable').length) },
              ]}
            />
            <Input aria-label="Search worker names" placeholder="Search workers…" value={search} onChange={(e) => setSearch(e.target.value)} className="h-8 max-w-52 rounded-md border border-theme-border bg-theme-elevated px-3" />
            <SelectMenu ariaLabel="Pool" value={pool} options={[{ value: '', label: 'Every pool' }, ...pools.map((p) => ({ value: p, label: p.replace('/', ' / ') }))]} onChange={(v) => set('pool', v)} className="min-w-[12rem]" />
          </div>
        )}
        {!connected && tier ? (
          <>
            <SubstrateTierBanner tier={tier} />
            <SubstrateConnectNotice status={status} tier={tier} />
          </>
        ) : query.isLoading ? (
          <PaneLoader label="Loading workers…" />
        ) : !data ? (
          <Notice>Radar couldn’t read the workers: {query.error instanceof Error ? query.error.message : 'unknown error'}.</Notice>
        ) : (
          <>
            <RefreshFailedNotice queries={[query]} />
            <SubstrateCoverageNotice coverage={data.coverage} kinds={['workers', 'actors']} />
            <WorkersTable data={{ workers: shown, coverage: data.coverage }} pools={workspace?.pools} poolFindings={poolFindings} onOpenSubject={onOpenSubject} grouped />
            {emptyPools.length > 0 && <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-theme-text-secondary">
              <span>Pools with no registered workers</span>
              {emptyPools.map((p) => <button key={`${p.namespace}/${p.name}`} type="button" onClick={() => navigate(substratePoolPath(p, connection.context || undefined))} className="font-mono text-accent-text hover:underline">{p.namespace}/{p.name} →</button>)}
            </div>}
          </>
        )}
        {tier && <SubstrateSourceLine tier={tier} status={status} />}
      </ScreenBody>
    </div>
  )
}

/** The worker's actors, under its row: each opens in the actor drawer. */
function WorkerActors({ w, onOpenSubject, partial }: { w: SubstrateWorkerRow; onOpenSubject: OpenSubject; partial: boolean }) {
  const navigate = useWorkspaceNavigate()
  if (!w.actors) return <p className="text-xs text-theme-text-tertiary">Actors were not read, so which actors are on this worker is unknown.</p>
  if (w.actors.length === 0) return <p className="text-xs text-theme-text-tertiary">{partial ? 'No assignments found in the actors Radar could read.' : 'No actor is on this worker.'}</p>
  return (
    <div className="space-y-1.5">
      <ul className="grid grid-cols-[repeat(auto-fill,minmax(22rem,1fr))] gap-x-4 gap-y-1">
        {w.actors.map((a) => (
          <li key={`${a.atespace}/${a.name}`} className="flex min-w-0 items-center gap-2 text-xs">
            <span className="shrink-0 whitespace-nowrap"><ActorStateBadge state={a.state} impact={a.workerImpact} updateTime={a.updateTime} /></span>
            <button type="button" onClick={() => onOpenSubject({ kind: 'Actor', atespace: a.atespace, name: a.name })} className="min-w-0 truncate font-mono text-accent-text hover:underline">{a.name}</button>
            <span className="min-w-0 truncate text-theme-text-tertiary">{a.atespace}</span>
          </li>
        ))}
      </ul>
      <button type="button" onClick={() => navigate(substrateActorsPath({ worker: w.name, pool: `${w.namespace}/${w.pool}` }))} className="text-xs text-accent-text hover:underline">In the Actors list →</button>
    </div>
  )
}

function WorkerRows({ workers, columns, onOpenSubject, actorsPartial }: { workers: SubstrateWorkerRow[]; columns: TableColumn<SubstrateWorkerRow>[]; onOpenSubject: OpenSubject; actorsPartial: boolean }) {
  const [open, setOpen] = useState<Set<string>>(() => new Set())
  const toggle = (name: string) =>
    setOpen((prev) => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  return (
    <>
      {workers.map((w) => {
        const isOpen = open.has(w.name)
        const panel = `worker-actors-${w.name}`
        const full = w.state === 'active' && !w.hasFreeSlot
        return (
          <Fragment key={w.name}>
            <tr className={clsx(ROW_HOVER, 'cursor-pointer', full && 'bg-theme-elevated/40')} onClick={() => toggle(w.name)}>
              {columns.map((c, i) => <td key={i} className={clsx(TD, c.className)}>{c.cell(w)}</td>)}
              <td className={TD}>
                <button
                  type="button"
                  aria-expanded={isOpen}
                  aria-controls={panel}
                  aria-label={`${isOpen ? 'Hide' : 'Show'} assigned actors for ${w.pod}`}
                  onClick={(e) => { e.stopPropagation(); toggle(w.name) }}
                  className="inline-flex items-center gap-1 text-left text-xs text-accent-text hover:underline"
                >
                  <CollapseChevron open={isOpen} />
                  {isOpen ? 'Hide assigned actors' : w.actors && (!actorsPartial || w.actors.length > 0) ? `Show ${actorsPartial ? '≥' : ''}${w.actors.length} assigned actors` : 'View assigned actors'}
                </button>
              </td>
            </tr>
            <tr>
              <td colSpan={columns.length + 1} className="p-0">
                <Collapse open={isOpen} id={panel} unmountOnExit>
                  <div className="border-t-subtle bg-theme-base/40 py-2.5 pl-11 pr-4">
                    <WorkerActors w={w} onOpenSubject={onOpenSubject} partial={actorsPartial} />
                  </div>
                </Collapse>
              </td>
            </tr>
          </Fragment>
        )
      })}
    </>
  )
}

/**
 * Workers as a table; `grouped` puts them under a header per pool with its
 * slots used of total and its reading. Each row expands to the actors on it;
 * full workers carry a badge and a tint.
 */
export function WorkersTable({
  data,
  pools,
  poolFindings,
  emptyMessage,
  onOpenSubject,
  grouped,
  title,
}: {
  data: { workers: SubstrateWorkerRow[]; coverage: { workers: SubstrateCoverage; actors?: SubstrateCoverage } }
  pools?: SubstratePoolRow[]
  poolFindings?: SubstrateAttention[]
  emptyMessage?: ReactNode
  onOpenSubject: OpenSubject
  grouped?: boolean
  title?: string
}) {
  const navigate = useWorkspaceNavigate()
  const { connection } = useConnection()
  const wide = useMediaQuery(SUBSTRATE_WIDE)
  const columns = workerColumns(onOpenSubject, wide)
  const groups = useMemo(() => {
    const by = new Map<string, SubstrateWorkerRow[]>()
    for (const w of data.workers) {
      const k = grouped ? `${w.namespace}/${w.pool}` : ''
      by.set(k, [...(by.get(k) ?? []), w])
    }
    return [...by].map(([key, ws]) => ({ key, workers: sortWorkers(ws) }))
      .sort((a, b) => rank(a.workers[0]) - rank(b.workers[0]) || a.key.localeCompare(b.key))
  }, [data.workers, grouped])
  const empty = coverageInPart(data.coverage.workers) ? `No workers: ${coverageReason(data.coverage.workers, 'workers')}.` : emptyMessage ?? 'No workers match.'
  return (
    <section>
      {title && <h2 className="mb-2 text-sm font-semibold text-theme-text-primary">{title}</h2>}
      <div className="overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
        {data.workers.length === 0 ? (
          <div className="px-4 py-5 text-sm text-theme-text-tertiary">{empty}</div>
        ) : (
          <div className={TABLE_WRAP}>
            <table className="w-full table-fixed" style={{ minWidth: wide ? 900 : 700 }}>
              <colgroup>
                {columns.map((c, i) => <col key={i} style={c.width ? { width: c.width } : undefined} />)}
                <col style={{ width: '10rem' }} />
              </colgroup>
              <thead className={TABLE_HEAD}>
                <tr>
                  {columns.map((c, i) => <th key={i} className={TH}>{c.header}</th>)}
                  <th className={TH}>Assignments</th>
                </tr>
              </thead>
              {groups.map((g) => {
                const row = pools?.find((p) => `${p.namespace}/${p.name}` === g.key)
                const slots = row?.placement?.slots
                const [ns, name] = g.key.split('/')
                return (
                  <tbody key={g.key} className={TBODY}>
                    {grouped && (
                      <tr className="bg-theme-base/60">
                        <td colSpan={columns.length + 1} className="px-3 py-2 text-xs">
                          <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
                            {row?.kubernetes
                              ? <button type="button" className="font-mono text-sm font-medium text-accent-text hover:underline" onClick={() => navigate(substratePoolPath(row, connection.context || undefined))}>{name} →</button>
                              : <span className="font-mono text-sm font-medium text-theme-text-primary">{name}</span>}
                            <span className="text-theme-text-tertiary">
                              {ns} · {poolWorkersText(row, g.workers)}
                              {slots ? ` · ${row?.placement?.complete === false ? slots.used > 0 ? `at least ${slots.used} actor slots used on active workers` : 'actor slot usage unknown' : `${slots.used} of ${slots.total} actor slots used on active workers`}` : ''}
                            </span>
                            {row && <PoolReadingBadge p={row} finding={poolFindings?.find((f) => f.subject.namespace === ns && f.subject.name === name)} />}
                          </span>
                        </td>
                      </tr>
                    )}
                    <WorkerRows workers={g.workers} columns={columns} onOpenSubject={onOpenSubject} actorsPartial={coverageInPart(data.coverage.actors)} />
                  </tbody>
                )
              })}
            </table>
          </div>
        )}
      </div>
    </section>
  )
}

/**
 * A pool's workers by name: registered with Substrate, of those active, and
 * the desired count Kubernetes holds. From the pool's own totals, not the
 * rows a state filter left; a partial read is a lower bound.
 */
export function poolWorkersText(row: Pick<SubstratePoolRow, 'placement' | 'kubernetes'> | undefined, shown: Pick<SubstrateWorkerRow, 'state'>[]): string {
  const counts = row?.placement?.workers
  const registered = counts?.total ?? shown.length
  const active = counts?.active ?? shown.filter((w) => w.state === 'active').length
  const partial = row?.placement?.complete === false
  const atLeast = partial ? 'at least ' : ''
  const desired = row?.kubernetes?.desired
  const reg = partial && registered === 0 ? 'registrations unknown'
    : desired !== undefined ? `${atLeast}${registered} of ${desired} desired workers registered` : `${atLeast}${registered} ${registered === 1 ? 'worker' : 'workers'} registered`
  return `${reg}, ${partial && active === 0 ? 'active worker count unknown' : `${atLeast}${active} active`}`
}

function PoolReadingBadge({ p, finding }: { p: SubstratePoolRow; finding?: SubstrateAttention }) {
  const r = poolReading(p, finding)
  return <Tooltip content={r.meaning}><span><Badge severity={r.severity} size="sm">{r.label}</Badge></span></Tooltip>
}
