import { SlidersHorizontal } from 'lucide-react'
import { SubstrateStateBar } from './SubstrateStateBar'
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { useSearchParams } from 'react-router-dom'
import { Collapse, Input, SelectMenu, Tooltip, TruncatedText, WorkspaceCard, toneTextClass, useDisclosure, type SelectMenuOption } from '@skyhook-io/k8s-ui'
import {
  useSubstrateActors,
  useSubstrateTemplates,
  type SubstrateAPITier,
  type SubstrateActorRow,
  type SubstrateActorState,
  type SubstrateActors as SubstrateActorsResponse,
  type SubstrateStatus,
  type SubstrateWorkspace,
  type SubstrateRef,
  substrateErrorCode,
} from '../../api/substrateWorkspace'
import type { SelectedResource } from '../../types'
import { FilterChips, Notice, RefreshFailedNotice, ScreenBody, Segments, SectionTable, Sub, type TableColumn } from '../workspace/layout'
import {
  ACTOR_SEGMENTS,
  ACTOR_STATE_LABEL,
  coverageInPart,
  coverageReason,
  durableText,
  fidelityLabel,
  resumeSentence,
  stateExtra,
  segmentCount,
  segmentOfState,
  templateFilterValue,
  type ActorSegment,
} from './readings'
import { SubstrateConnectNotice, SubstrateTierBanner } from './SubstrateConnect'
import { ActorStateBadge, SUBSTRATE_WIDE, SubstrateHeader, SubstrateSourceLine, Unread, When } from './shared'
import { useMediaQuery } from '../../hooks/useMediaQuery'

export const ACTORS_PAGE_SIZE = 50

export type ActorSort = 'attention' | 'updated' | 'name'

const SORT_OPTIONS = [
  { value: 'attention', label: 'Needs attention first', description: 'Crashed, then worker disruptions and stuck transitions' },
  { value: 'updated', label: 'Newest change first' },
  { value: 'name', label: 'By name' },
]

export const SORT_LABEL: Record<ActorSort, string> = { attention: 'needs attention first', updated: 'newest change first', name: 'by atespace and name' }

export type NodeHas = 'running' | 'localSnapshot'

/** What each node mode lists: what a node's loss stops, and what it could lose. */
export const NODE_HAS_MEANING: Record<NodeHas, string> = {
  running: 'Actors whose worker is on this node: losing the node stops them.',
  localSnapshot: 'Actors whose newest snapshot is stored only on this node: losing the node loses that state. Deleting the worker Pod alone doesn’t.',
}

/** Where the rows came from when the shared read didn't hold them all. */
export function sourcesNote(sources: ('snapshot' | 'atespace' | 'actor')[]): string {
  if (sources.includes('actor')) return 'Read directly by name from Substrate: the shared read and the atespace read stopped at their limit before this actor.'
  if (sources.includes('atespace')) return 'Read directly from this atespace: the shared read does not hold all of it.'
  return ''
}

/** An actor as the workspace tables' row identity. Actors are not Kubernetes objects; the drawer this opens is Substrate's own. */
export const ACTOR_ROW_KIND = 'SubstrateActor'

export function actorRowResource(ref: SubstrateRef): SelectedResource {
  return { kind: ACTOR_ROW_KIND, namespace: ref.atespace, name: ref.name }
}

/** The states a segment asks /actors for; undefined for every state. */
export function segmentStates(segment: ActorSegment, linked: string | null): SubstrateActorState[] | undefined {
  // A link to one transitional state ("Resuming 6") keeps that state inside the In progress segment.
  if (linked && linked in ACTOR_STATE_LABEL) return [linked as SubstrateActorState]
  const states = ACTOR_SEGMENTS.find((s) => s.id === segment)!.states
  return states.length ? [...states] : undefined
}

/** One line in a dense list: truncated, with the whole text on hover. */
function Line({ children, title, className }: { children: ReactNode; title?: ReactNode; className?: string }) {
  const line = <span className={clsx('block min-w-0 truncate', className)}>{children}</span>
  return title ? <Tooltip content={title} wrapperClassName="min-w-0 w-full">{line}</Tooltip> : line
}

function SnapshotCell({ row }: { row: SubstrateActorRow }) {
  const s = row.newestSnapshot
  if (!s) return <Line title="No snapshot yet: it boots fresh." className="text-theme-text-tertiary">none yet · boots fresh</Line>
  const d = durableText(s)
  const short = s.durable === 'none' && s.locality ? 'only on its node' : d.text
  // Stored is the expected case; a snapshot at risk says so on a second line, so the warning is never cut off.
  return (
    <Tooltip content={`Generation ${s.generation}, ${fidelityLabel(s.fidelity)}: ${d.title} ${resumeSentence(row)}`} wrapperClassName="min-w-0 w-full">
      <span className="block min-w-0 text-[12.5px]">
        <span className="block whitespace-nowrap">gen {s.generation} · {s.fidelity === 'rootfs' ? 'root fs' : fidelityLabel(s.fidelity)}</span>
        {d.tone !== 'healthy' && <span className={clsx('block text-xs leading-4', toneTextClass(d.tone))}>{short}</span>}
      </span>
    </Tooltip>
  )
}

/** The worker-impact filter a link can carry, as its chip reads. */
const IMPACT_FILTER_LABEL: Record<NonNullable<SubstrateActorRow['workerImpact']>['kind'], string> = {
  restarted: 'Sandbox lost',
  unavailable: 'Worker unavailable',
  draining: 'Worker draining',
}

const WAITING_SHORT: Record<NonNullable<SubstrateActorRow['waitingOn']>['on'], string> = {
  restore: 'restoring on its worker',
  checkpoint: 'checkpointing on its worker',
  revert: 'reverting in the API server',
  delete: 'deleting in the API server',
  unrecorded: 'no worker recorded',
}

/** A crash cause's label in a table cell: short, the same on every row it covers; Substrate's message is on hover. */
const CAUSE_LABEL: Record<NonNullable<SubstrateActorRow['cause']>, string> = { memoryLimit: 'Memory-limit kill' }

/**
 * What a state cell says beside its chip, and on hover: a crash's cause label
 * or message, how long, where it waits. A named cause reads in a few words,
 * the same on every row; Substrate's whole message is on hover.
 */
export function stateCellReading(row: SubstrateActorRow): { text: string; hover: string } {
  // A pending upload shows in the snapshot column; the state cell doesn't say it twice.
  const raw = row.state === 'crashed' && row.cause ? CAUSE_LABEL[row.cause] : stateExtra(row.state, row.reason)
  // The badge says "stuck" past Substrate's threshold; this line says only how long.
  const extra = row.state === 'suspended' && raw?.startsWith('upload pending') ? undefined : raw
  const waiting = row.waitingOn ? WAITING_SHORT[row.waitingOn.on] : undefined
  // Where it waits is what the In progress tab is for, so it comes before how long.
  const text = [waiting, extra].filter(Boolean).join(' · ')
  const message = row.state === 'crashed' && row.cause && row.reason ? `Substrate’s message: ${row.reason}.` : row.reason
  return { text, hover: [message, row.waitingOn?.reading].filter(Boolean).join(' ') }
}

function StateCell({ row }: { row: SubstrateActorRow }) {
  const { text, hover } = stateCellReading(row)
  const line = text ? <span className={clsx('line-clamp-2 min-w-0 text-xs leading-4', row.state === 'crashed' ? 'text-theme-text-secondary' : 'text-theme-text-tertiary')}>{text}</span> : null
  const place = 'min-w-[8rem] flex-1 pt-0.5'
  return (
    <span className="flex min-w-0 flex-wrap items-start gap-x-1.5 gap-y-0.5">
      <span className="shrink-0 basis-full"><ActorStateBadge state={row.state} impact={row.workerImpact} updateTime={row.updateTime} /></span>
      {line && (hover ? <Tooltip content={hover} wrapperClassName={place}>{line}</Tooltip> : <span className={place}>{line}</span>)}
    </span>
  )
}

function WorkerCell({ row }: { row: SubstrateActorRow }) {
  if (row.worker) {
    return <><Line title={row.worker.pod} className="font-mono text-[11.5px]">{row.worker.pod}</Line><Sub>Assigned worker</Sub></>
  }
  if (!row.node) return <span className="text-theme-text-tertiary">none</span>
  // A node's DNS suffix is the same on every row; the hover has it whole.
  return (
    <><Line title={`No worker now. It last ran on node ${row.node}, or its snapshot is there.`} className="font-mono text-[12.5px] text-theme-text-tertiary">
      {row.node.split('.')[0]}
    </Line><Sub>Last known node</Sub></>
  )
}

/**
 * One line per row, so a screen holds a dozen actors; the rest of each value
 * is on hover and in the drawer. State gets the width to say its reading
 * whole: the atespace rides beside the name rather than in a column of its
 * own. Narrow folds the atespace and template under the name.
 */
function actorColumns(wide: boolean, onOpen: (ref: SubstrateRef) => void): TableColumn<SubstrateActorRow>[] {
  return [
    {
      header: 'Actor',
      width: wide ? '15%' : '26%',
      cell: (r) => (
        <>
          <TruncatedText text={r.name} className="font-mono text-xs font-medium" />
          <Sub>{r.atespace}{!wide && <> · {r.template.name}</>}</Sub>
        </>
      ),
    },
    { header: 'State', width: wide ? '24%' : '29%', cell: (r) => <StateCell row={r} /> },
    ...(wide ? [{ header: 'Template', width: '16%', cell: (r: SubstrateActorRow) => <Line title={r.template.name} className="font-mono text-[12px]">{r.template.name}</Line> }] : []),
    { header: 'Worker or last node', width: wide ? '14%' : '20%', cell: (r) => <WorkerCell row={r} /> },
    { header: 'Newest snapshot', width: wide ? '15%' : '21%', cell: (r) => <SnapshotCell row={r} /> },
    ...(wide
      ? [{
          header: 'Last updated',
          width: '12%',
          cell: (r: SubstrateActorRow) =>
            r.updateTime ? (
              <span className="text-xs"><When iso={r.updateTime} hint="Substrate’s last change to the actor: the current state has held at least since" /></span>
            ) : (
              <Unread>unknown</Unread>
            ),
        }]
      : []),
    {
      header: <span className="sr-only">Open actor</span>,
      width: '76px',
      cell: (r) => <button type="button" aria-label={`Open actor ${r.name}`} className="whitespace-nowrap text-xs text-accent-text hover:underline" onClick={(e) => { e.stopPropagation(); onOpen(r) }}>Open →</button>,
    },
  ]
}

export interface SubstrateActorsProps {
  workspace?: SubstrateWorkspace
  status?: SubstrateStatus
  onOpenActor: (ref: SubstrateRef) => void
  openActor: SubstrateRef | null
}

/** The Actors list, filtered and paged by the server; counts per state come from Substrate's full read, not the page. */
export function SubstrateActorsScreen({ workspace, status, onOpenActor, openActor }: SubstrateActorsProps) {
  const [params, setParams] = useSearchParams()
  const linkedState = params.get('state')
  const attentionOnly = params.get('attention') === '1'
  const phase = segmentOfState(linkedState)
  const segment = attentionOnly ? 'attention' : phase
  const atespace = params.get('atespace') ?? ''
  const template = params.get('template') ?? ''
  const pool = params.get('pool') ?? ''
  const q = params.get('q') ?? ''
  const worker = params.get('worker') ?? ''
  const rawImpact = params.get('impact')
  const impact = rawImpact && rawImpact in IMPACT_FILTER_LABEL ? (rawImpact as keyof typeof IMPACT_FILTER_LABEL) : undefined
  const node = params.get('node') ?? ''
  const sort = (params.get('sort') as ActorSort | null) ?? 'attention'
  const nodeHas: NodeHas = params.get('nodeHas') === 'localSnapshot' ? 'localSnapshot' : 'running'
  // One write per change: filters reset the page, and the drawer's ?actor= stays.
  const update = (patch: Record<string, string>) => {
    const p = new URLSearchParams(params)
    for (const [k, v] of Object.entries(patch)) {
      if (v) p.set(k, v)
      else p.delete(k)
    }
    p.delete('page')
    setParams(p, { replace: true })
  }
  const setQ = (v: string) => update({ q: v })
  const page = Math.max(0, Number(params.get('page') ?? 0) || 0)
  const [search, setSearch] = useState(q)
  const [filtersOpen, setFiltersOpen] = useState(false)
  const filterDisclosure = useDisclosure(filtersOpen)
  const filterCount = [atespace, template, pool, sort !== 'attention'].filter(Boolean).length
  useEffect(() => setSearch(q), [q])
  useEffect(() => {
    if (search === q) return
    // Debounced: every keystroke would be a request.
    const t = setTimeout(() => setQ(search), 250)
    return () => clearTimeout(t)
  }, [search]) // eslint-disable-line react-hooks/exhaustive-deps

  const states = segmentStates(phase, linkedState)
  const query = useSubstrateActors({
    atespace: atespace || undefined,
    template: template || undefined,
    pool: pool || undefined,
    worker: worker || undefined,
    impact,
    node: node || undefined,
    nodeHas: node ? nodeHas : undefined,
    state: states,
    attention: attentionOnly ? 1 : undefined,
    q: q || undefined,
    sort,
    limit: ACTORS_PAGE_SIZE,
    offset: page * ACTORS_PAGE_SIZE,
  }, !workspace || workspace.apiTier.state === 'connected')
  const connected = (query.data?.apiTier ?? workspace?.apiTier)?.state === 'connected'
  const templates = useSubstrateTemplates(connected)

  const setSegment = (id: ActorSegment | 'attention') => {
    const p = new URLSearchParams(params)
    if (id === 'attention') p.set('attention', '1')
    else p.delete('attention')
    const next = id === 'attention' ? undefined : segmentStates(id, null)
    if (id === 'all' || !next) p.delete('state')
    else p.set('state', id === 'inProgress' ? 'inProgress' : next.join(','))
    p.delete('page')
    setParams(p, { replace: true })
  }
  const setPage = (n: number) => {
    const p = new URLSearchParams(params)
    if (n > 0) p.set('page', String(n))
    else p.delete('page')
    setParams(p, { replace: true })
  }

  const atespaceOptions: SelectMenuOption[] = useMemo(
    () => [{ value: '', label: 'All atespaces' }, ...(workspace?.atespaces ?? []).map((a) => ({ value: a.name, label: a.name, description: a.actorsRead === 'denied' ? 'no access' : a.actorsRead === 'unread' ? 'not read' : undefined }))],
    [workspace?.atespaces],
  )
  const templateOptions: SelectMenuOption[] = useMemo(
    () => [
      { value: '', label: 'Any template' },
      ...(templates.data?.templates ?? [])
        .filter((t) => !atespace || t.atespace === atespace)
        .map((t) => ({ value: templateFilterValue(t), label: `${t.atespace} / ${t.name}` })),
    ],
    [templates.data, atespace],
  )
  const poolOptions: SelectMenuOption[] = useMemo(
    () => [{ value: '', label: 'Any pool' }, ...(workspace?.pools ?? []).map((p) => ({ value: `${p.namespace}/${p.name}`, label: `${p.namespace} / ${p.name}` }))],
    [workspace?.pools],
  )
  // A filter from a link that the menus don't list still shows, and can be cleared.
  const extra = (options: SelectMenuOption[], value: string) => (value && !options.some((o) => o.value === value) ? [...options, { value, label: value }] : options)

  const code = substrateErrorCode(query.error)
  // A refused read says why in its code; that wins over the workspace's older tier.
  const tier: SubstrateAPITier | undefined = !query.data && code
    ? { state: code === 'notConfigured' || code === 'targetChanged' ? 'notConfigured' : code === 'sharedRadar' ? 'disabled' : 'failed', code, reason: query.error instanceof Error ? query.error.message : undefined }
    : query.data?.apiTier ?? workspace?.apiTier
  const notConnected = !query.data && !!tier && tier.state !== 'connected'

  const chips = [
    ...(worker ? [{ label: `Worker: ${worker}`, onClear: () => update({ worker: '' }) }] : []),
    ...(impact ? [{ label: IMPACT_FILTER_LABEL[impact], onClear: () => update({ impact: '' }) }] : []),
    ...(node ? [{ label: `Node: ${node}`, onClear: () => update({ node: '', nodeHas: '' }) }] : []),
    ...(linkedState && linkedState in ACTOR_STATE_LABEL && (phase === 'inProgress' || phase === 'all') ? [{ label: `Phase: ${ACTOR_STATE_LABEL[linkedState as SubstrateActorState]}`, onClear: () => setSegment(phase) }] : []),
  ]

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <SubstrateHeader title="Actors" intro={atespace ? <>Atespace: {atespace}</> : 'Cluster-wide · all atespaces'} />
      <ScreenBody>
        {!notConnected && query.data?.counts && (
          <WorkspaceCard title="Reported phases">
            <SubstrateStateBar counts={query.data.counts} partial={coverageInPart(query.data.coverage)} onSelectState={(state) => update({ state, attention: '' })} />
          </WorkspaceCard>
        )}
        {node && !notConnected && (
          <div className="flex flex-wrap items-center gap-3 text-sm text-theme-text-secondary">
            <Segments
              label="What the node holds"
              value={nodeHas}
              onChange={(v) => update({ nodeHas: v })}
              options={[
                { id: 'running', label: 'Running here' },
                { id: 'localSnapshot', label: 'Snapshots only on this node' },
              ]}
            />
            <span className="text-xs">{NODE_HAS_MEANING[nodeHas]}</span>
          </div>
        )}
        {query.data && query.data.sources.some((s) => s !== 'snapshot') && (
          <p className="text-xs text-theme-text-tertiary">{sourcesNote(query.data.sources)}</p>
        )}
        {!notConnected && <div>
          <div className="flex flex-wrap items-center gap-2">
            <Segments
              label="Actor filter"
              value={segment}
              onChange={setSegment}
              options={[
                ...ACTOR_SEGMENTS.slice(0, 1).map((s) => ({ id: s.id, label: s.label })),
                { id: 'attention' as const, label: 'Needs attention' },
                ...ACTOR_SEGMENTS.slice(1).map((s) => ({ id: s.id, label: s.label })),
              ].map((s) => {
                const n = s.id === 'attention' ? query.data?.attentionCount : segmentCount(query.data?.counts, s.id)
                const partial = coverageInPart(query.data?.coverage) || (s.id === 'attention' && coverageInPart(query.data?.workerCoverage))
                return { ...s, count: n === undefined ? undefined : partial ? (n > 0 ? `≥${n.toLocaleString('en-US')}` : undefined) : n.toLocaleString('en-US') }
              })}
            />
            <Input
              aria-label="Search actor names"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search actor name…"
              className="h-8 w-44 rounded-md border border-theme-border bg-theme-elevated px-3 text-sm text-theme-text-primary placeholder:text-theme-text-tertiary focus:border-skyhook-500 focus:outline-none"
              spellCheck={false}
            />
            <button type="button" {...filterDisclosure.buttonProps} onClick={() => setFiltersOpen((open) => !open)} className="btn-secondary inline-flex items-center gap-1.5 px-3 py-1.5 text-xs">
              <SlidersHorizontal size={14} /> Filters{filterCount > 0 && ` · ${filterCount}`}
            </button>
          </div>
          <Collapse open={filtersOpen} id={filterDisclosure.panelId}>
            <div className="flex flex-wrap items-center gap-2 pt-2">
              <SelectMenu ariaLabel="Atespace" value={atespace} options={extra(atespaceOptions, atespace)} onChange={(v) => update({ atespace: v, ...(template && v && !template.startsWith(`${v}/`) ? { template: '' } : {}) })} className="min-w-[10rem]" />
              <SelectMenu ariaLabel="Template" value={template} options={extra(templateOptions, template)} onChange={(v) => update({ template: v })} className="min-w-[12rem]" searchPlaceholder="Search templates" />
              <SelectMenu ariaLabel="Pool" value={pool} options={extra(poolOptions, pool)} onChange={(v) => update({ pool: v })} className="min-w-[10rem]" />
              <SelectMenu ariaLabel="Sort" value={sort} options={SORT_OPTIONS} onChange={(v) => update({ sort: v === 'attention' ? '' : v })} className="min-w-[11rem]" />
            </div>
          </Collapse>
          <FilterChips chips={chips} />
        </div>}
        {attentionOnly && query.data && query.data.attentionCount === undefined ? (
          <Notice>Filtering actors that need attention requires a newer Radar.</Notice>
        ) : notConnected && tier ? (
          <>
            <SubstrateTierBanner tier={tier} />
            <SubstrateConnectNotice status={status} tier={tier} />
          </>
        ) : query.error && !query.data ? (
          <Notice>Radar couldn’t read the actors: {query.error instanceof Error ? query.error.message : 'unknown error'}.</Notice>
        ) : (
          <>
            <RefreshFailedNotice queries={[query]} />
            <ActorsTable sort={sort} data={query.data} attentionPartial={attentionOnly && coverageInPart(query.data?.workerCoverage)} loading={query.isLoading} placeholder={query.isPlaceholderData} page={page} onPage={setPage} onOpenActor={onOpenActor} openActor={openActor} />
          </>
        )}
        {attentionOnly && <p className="text-xs text-theme-text-tertiary">Includes reported crashes, lost sandboxes, unavailable workers and stuck transitions. Failed golden builds also have a template finding.</p>}
        {tier && <SubstrateSourceLine tier={tier} status={status} />}
      </ScreenBody>
    </div>
  )
}

export function ActorsTable({
  sort = 'attention',
  data,
  attentionPartial,
  loading,
  placeholder,
  page,
  onPage,
  onOpenActor,
  openActor,
}: {
  sort?: ActorSort
  attentionPartial?: boolean
  data?: SubstrateActorsResponse
  loading?: boolean
  /** The rows are the page before, kept while this page loads. */
  placeholder?: boolean
  page: number
  onPage: (page: number) => void
  onOpenActor: (ref: SubstrateRef) => void
  openActor: SubstrateRef | null
}) {
  const wide = useMediaQuery(SUBSTRATE_WIDE)
  const rows = data?.rows ?? []
  const total = data?.total
  const from = (data?.offset ?? page * ACTORS_PAGE_SIZE) + 1
  const to = (data?.offset ?? 0) + rows.length
  const cov = data?.coverage
  const partial = coverageInPart(cov) || attentionPartial
  const empty = loading
    ? 'Loading actors…'
    : !data
      ? 'Actors were not read.'
      : cov && (cov.state === 'denied' || cov.state === 'failed')
        ? `No actors: ${coverageReason(cov, 'actors')}.`
        : attentionPartial
          ? 'No matching actors observed. Some worker disruptions could not be assessed.'
          : coverageInPart(cov)
          ? `No actors match in what Radar could read (${coverageReason(cov, 'actors')}).`
          : 'No actors match.'
  const pages = total !== undefined ? Math.ceil(total / ACTORS_PAGE_SIZE) : undefined
  return (
    <div aria-busy={placeholder || undefined} className={placeholder ? 'opacity-60' : undefined}>
      <SectionTable
        columns={actorColumns(wide, onOpenActor)}
        rows={rows}
        rowKey={(r) => `${r.atespace}/${r.name}`}
        rowResource={(r) => actorRowResource(r)}
        onInspect={(res) => onOpenActor({ atespace: res.namespace, name: res.name })}
        inspected={openActor ? actorRowResource(openActor) : null}
        empty={empty}
        minWidth={wide ? 900 : 720}
        footer={
          total !== undefined && total > 0 ? (
            <div className="flex flex-wrap items-center gap-3">
              <span>
                {placeholder
                  ? `Loading page ${page + 1}… showing the page before until it arrives`
                  : <>{from.toLocaleString('en-US')}–{to.toLocaleString('en-US')} of {partial ? 'at least ' : ''}{total.toLocaleString('en-US')} · {SORT_LABEL[sort]}</>}
              </span>
              {pages !== undefined && pages > 1 && (
                <span className="flex items-center gap-1.5">
                  <button type="button" disabled={page === 0} onClick={() => onPage(page - 1)} className="btn-secondary px-2 py-0.5 text-xs">Previous</button>
                  <button type="button" disabled={page + 1 >= pages} onClick={() => onPage(page + 1)} className="btn-secondary px-2 py-0.5 text-xs">Next</button>
                </span>
              )}
            </div>
          ) : undefined
        }
      />
    </div>
  )
}
