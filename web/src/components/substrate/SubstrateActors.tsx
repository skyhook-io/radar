import { shortRevision } from '@skyhook-io/k8s-ui/components/resources/resource-utils-kagent'
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { useSearchParams } from 'react-router-dom'
import { Input, SelectMenu, Tooltip, toneTextClass, type SelectMenuOption } from '@skyhook-io/k8s-ui'
import { useRadarFeature } from '../../api/client'
import {
  useSubstrateActors,
  useSubstrateTemplates,
  type KagentActor,
  type KagentAgentRef,
  type KagentAttribution,
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
  agentFilterValue,
  agentText,
  kagentCellText,
  kagentUnreadText,
  kagentUnattributedText,
  shortSessionId,
  IN_PROGRESS_STATES,
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
import { actorAttentionParts, substrateScreenCounts } from './attention'
import { ActorStateBadge, Name, SUBSTRATE_WIDE, SubstrateHeader, SubstrateSourceLine, Term, Unread, When, WholeNames } from './shared'
import { useMediaQuery } from '../../hooks/useMediaQuery'

export const ACTORS_PAGE_SIZE = 50

export type ActorSort = 'attention' | 'updated' | 'name'

const SORT_OPTIONS = [
  { value: 'attention', label: 'Needs attention first', description: 'Crashed, then stuck in a transition, then newest change' },
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

/** The URL filters that narrow the Actors list to a subset; the state segment and sort are not among them. */
const SCOPING_PARAMS = ['atespace', 'template', 'pool', 'q', 'worker', 'impact', 'node', 'nodeHas', 'agent', 'kagent'] as const

/** The kagent filter's choices: which kagent sessions to list. */
const KAGENT_FILTER_LABEL: Record<KagentAttribution['kind'] | 'sessions', string> = {
  sessions: 'kagent sessions',
  agentSession: 'Agent sessions',
  sandbox: 'kagent sandboxes',
  unattributed: 'Not tied to an Agent',
}
const KAGENT_OPTIONS: SelectMenuOption[] = [
  { value: '', label: 'Any kagent link' },
  ...(Object.keys(KAGENT_FILTER_LABEL) as (KagentAttribution['kind'] | 'sessions')[]).map((k) => ({ value: k, label: KAGENT_FILTER_LABEL[k] })),
]

/**
 * Why a kagent-filtered list isn't asked for: support is still being checked (capabilities loading), the
 * check failed (support unknown with an upgrade note, as useRadarFeature reports a failed check), or this
 * Radar is too old. failed: the capabilities read settled without an answer.
 */
export function kagentSupportText(support: 'supported' | 'unsupported' | 'unknown', failed: boolean): string {
  if (support === 'unsupported') return 'Listing actors by kagent Agent needs a newer Radar in this cluster.'
  return failed
    ? 'Radar couldn’t check whether this Radar supports kagent filters, so it doesn’t apply them.'
    : 'Checking whether this Radar supports kagent filters…'
}

/** The support check is over, so the filters stay unapplied and can only be cleared. */
export function kagentSupportSettled(support: 'supported' | 'unsupported' | 'unknown', failed: boolean): boolean {
  return support === 'unsupported' || (support === 'unknown' && failed)
}

/**
 * A row's kagent Agent: a button that lists that Agent's sessions, or what the session is instead.
 * Only exact attributions name an Agent; a hint stays in the hover. The Agent's namespace is the
 * actor's atespace, shown under its name, so the cell names the Agent alone.
 */
function AgentCell({ row, onAgent }: { row: SubstrateActorRow; onAgent?: (agent: KagentAgentRef) => void }) {
  const k: KagentActor | undefined = row.kagent
  if (!k) return <span className="text-theme-text-tertiary">—</span>
  const session = `kagent session ${shortSessionId(k.sessionId)}`
  if (k.kind === 'agentSession') {
    const agent = k.agent
    // Wraps after a hyphen, never mid-word, as the template column does.
    const label = <Name name={agent.name} className="text-[12px]" />
    const hover = `${session} of Agent ${agentText(agent)}${k.earlierRevision ? `, on an earlier revision (${k.revision}) than the one new sessions start on` : ''}.${onAgent ? ' Click to list its sessions.' : ''}`
    return (
      <Tooltip content={hover} wrapperClassName="min-w-0 w-full">
        <span className="block min-w-0">
          {onAgent ? (
            <button type="button" onClick={(e) => { e.stopPropagation(); onAgent(agent) }} className="text-left text-accent-text hover:underline">{label}</button>
          ) : label}
          <span className="block text-xs leading-4 text-theme-text-tertiary">{k.newSessions ? 'new sessions' : k.earlierRevision ? 'earlier revision' : k.desired ? 'desired revision' : 'revision'} · {shortRevision(k.revision) || 'not reported'}</span>
        </span>
      </Tooltip>
    )
  }
  return (
    <Line title={k.kind === 'sandbox' ? `${session}: a kagent sandbox, not an Agent’s chat.` : `${session}. ${kagentUnattributedText(k, row.atespace)}`} className="text-xs text-theme-text-tertiary">
      {kagentCellText(k)}
    </Line>
  )
}

/** An actor as the workspace tables' row identity. Actors are not Kubernetes objects; the drawer this opens is Substrate's own. */
export const ACTOR_ROW_KIND = 'SubstrateActor'

export function actorRowResource(ref: SubstrateRef): SelectedResource {
  return { kind: ACTOR_ROW_KIND, namespace: ref.atespace, name: ref.name }
}

/** The states a segment asks /actors for; undefined for every state. */
export function segmentStates(segment: ActorSegment, linked: string | null): SubstrateActorState[] | undefined {
  // A link to one transitional state ("Resuming 6") keeps that state inside the In progress segment.
  if (segment === 'inProgress' && linked && (IN_PROGRESS_STATES as readonly string[]).includes(linked)) return [linked as SubstrateActorState]
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

/**
 * An actor's name in a row. A kagent session's is session-<UUID>, which would wrap over several lines:
 * it reads "session <random tail>" (shortSessionId), the whole name on hover. Search still matches the
 * whole name.
 */
function ActorName({ row }: { row: SubstrateActorRow }) {
  if (row.kagent) {
    return (
      <Tooltip content={row.name}>
        <span className="whitespace-nowrap font-mono text-[12.5px]">session {shortSessionId(row.kagent.sessionId)}</span>
      </Tooltip>
    )
  }
  return <Name name={row.name} className="text-[12.5px]" />
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

/** The state chip and, beside it on the same line, what the chip leaves out. */
function StateCell({ row }: { row: SubstrateActorRow }) {
  const { text, hover } = stateCellReading(row)
  // The one cell that may take a second line: a crash message or a wait reads whole at 1440.
  // The badge is a crash's one red signal; its message reads neutral, so a different cause stands out by its words.
  // A stuck transition's amber is on its badge, which says "stuck"; the line stays quiet.
  const line = text ? <span className={clsx('line-clamp-2 min-w-0 text-xs leading-4', row.state === 'crashed' ? 'text-theme-text-secondary' : 'text-theme-text-tertiary')}>{text}</span> : null
  // Beside the chip while there is room for a few words, under it when there isn't.
  const place = 'min-w-[8rem] flex-1 pt-0.5'
  return (
    <span className="flex min-w-0 flex-wrap items-start gap-x-1.5 gap-y-0.5">
      <span className="shrink-0"><ActorStateBadge state={row.state} impact={row.workerImpact} updateTime={row.updateTime} /></span>
      {line && (hover ? <Tooltip content={hover} wrapperClassName={place}>{line}</Tooltip> : <span className={place}>{line}</span>)}
    </span>
  )
}

function WorkerCell({ row }: { row: SubstrateActorRow }) {
  if (row.worker) {
    return <Line title={row.node ? `${row.worker.pod}, on node ${row.node}` : row.worker.pod} className="font-mono text-[11.5px]">{row.worker.pod}</Line>
  }
  if (!row.node) return <span className="text-theme-text-tertiary">none</span>
  // A node's DNS suffix is the same on every row; the hover has it whole.
  return (
    <Line title={`No worker now. It last ran on node ${row.node}, or its snapshot is there.`} className="font-mono text-[12.5px] text-theme-text-tertiary">
      {row.node.split('.')[0]}
    </Line>
  )
}

/**
 * One line per row, so a screen holds a dozen actors; the rest of each value
 * is on hover and in the drawer. State gets the width to say its reading
 * whole: the atespace rides beside the name rather than in a column of its
 * own. Narrow folds the atespace and template under the name.
 */
function actorColumns(wide: boolean, agent?: { onAgent?: (agent: KagentAgentRef) => void; revisionOnly?: boolean }): TableColumn<SubstrateActorRow>[] {
  // With the Agent column, the others give it room.
  const w = agent?.revisionOnly ? { actor: '18%', state: '22%', template: '0%', agent: '16%', worker: '18%', snapshot: '16%', changed: '10%' } : agent
    ? { actor: wide ? '15%' : '24%', state: wide ? '23%' : '27%', template: '15%', agent: wide ? '13%' : '15%', worker: wide ? '14%' : '17%', snapshot: wide ? '12%' : '17%', changed: '8%' }
    : { actor: wide ? '16%' : '27%', state: wide ? '26%' : '30%', template: '18%', agent: '0%', worker: wide ? '17%' : '21%', snapshot: wide ? '14%' : '22%', changed: '9%' }
  return [
    {
      header: agent?.revisionOnly ? 'Session' : 'Actor',
      width: w.actor,
      cell: (r) => (
        wide ? (
          <span className="flex min-w-0 flex-wrap items-baseline gap-x-1">
            <ActorName row={r} />
            <span className="text-[11.5px] text-theme-text-tertiary"><WholeNames text={r.atespace} /></span>
          </span>
        ) : (
          <>
            <ActorName row={r} />
            <Sub><WholeNames text={`${r.atespace} · ${r.template.name}`} /></Sub>
          </>
        )
      ),
    },
    { header: 'State', width: w.state, cell: (r) => <StateCell row={r} /> },
    ...(wide && !agent?.revisionOnly ? [{ header: 'Template', width: w.template, cell: (r: SubstrateActorRow) => <Line title={r.template.name} className="font-mono text-[12px]">{r.template.name}</Line> }] : []),
    ...(agent ? [{ header: agent.revisionOnly ? 'Revision' : 'kagent Agent', width: w.agent, cell: (r: SubstrateActorRow) => agent.revisionOnly ? <span className="text-xs">{r.kagent?.kind === 'agentSession' ? <>{r.kagent.newSessions ? 'Current' : r.kagent.earlierRevision ? 'Earlier' : 'Latest change'}<span className="block font-mono text-theme-text-tertiary">{shortRevision(r.kagent.revision)}</span></> : 'Unattributed'}</span> : <AgentCell row={r} onAgent={agent.onAgent} /> }] : []),
    { header: 'Worker or last node', width: w.worker, cell: (r) => <WorkerCell row={r} /> },
    { header: agent?.revisionOnly ? 'Saved session state' : 'Newest snapshot', width: w.snapshot, cell: (r) => <SnapshotCell row={r} /> },
    ...(wide
      ? [{
          header: agent?.revisionOnly ? 'Updated' : 'Changed',
          width: w.changed,
          cell: (r: SubstrateActorRow) =>
            r.updateTime ? (
              <span className="text-xs"><When iso={r.updateTime} hint="Substrate’s last change to the actor: the current state has held at least since" /></span>
            ) : (
              <Unread>unknown</Unread>
            ),
        }]
      : []),
  ]
}

/**
 * What the Actors badge in the sidebar counts, said on the page it opens:
 * "N need attention: C crashed · S stuck", from the same read and the same
 * count. Each part opens its segment. The count is always over every actor:
 * while a filter narrows the list it says so ("across all actors"), and its
 * parts open their segment with the filters cleared, so a link never lands on
 * a tab that counts zero.
 */
export function ActorAttentionSummary({ workspace, filtered, onCrashed, onStuck }: { workspace: SubstrateWorkspace; filtered?: boolean; onCrashed: () => void; onStuck: () => void }) {
  const c = substrateScreenCounts(workspace).actors
  const { crashed, stuck } = actorAttentionParts(workspace)
  if (c.count === null) {
    return (
      <p className="text-sm text-theme-text-secondary">
        <Tooltip content={c.title}><span tabIndex={0} className="font-medium text-theme-text-primary">Needing attention{filtered ? ' across all actors' : ''}: unknown</span></Tooltip>
      </p>
    )
  }
  if (c.count === 0 && !c.lowerBound) return null
  const parts = [
    crashed > 0 && (
      <button key="crashed" type="button" onClick={onCrashed} className="text-accent-text hover:underline">{crashed.toLocaleString('en-US')} crashed</button>
    ),
    stuck > 0 && (
      <Tooltip key="stuck" content="Opens In progress, with the actors stuck longest first. Stuck: held in a transition past Substrate’s 5m threshold.">
        <button type="button" onClick={onStuck} className="text-accent-text hover:underline">{stuck.toLocaleString('en-US')} stuck</button>
      </Tooltip>
    ),
  ].filter(Boolean)
  return (
    <p className="text-sm text-theme-text-secondary">
      <Tooltip content={c.title}>
        <span tabIndex={0} className="font-medium text-theme-text-primary">
          {c.lowerBound ? 'At least ' : ''}{c.count.toLocaleString('en-US')} {c.count === 1 ? 'needs' : 'need'} attention{filtered ? ' across all actors' : ''}
        </span>
      </Tooltip>
      {parts.length > 0 && <>: {parts.map((p, i) => <span key={i}>{i > 0 && ' · '}{p}</span>)}</>}
    </p>
  )
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
  const segment = segmentOfState(linkedState)
  const atespace = params.get('atespace') ?? ''
  const template = params.get('template') ?? ''
  const pool = params.get('pool') ?? ''
  const q = params.get('q') ?? ''
  const worker = params.get('worker') ?? ''
  const rawImpact = params.get('impact')
  const impact = rawImpact && rawImpact in IMPACT_FILTER_LABEL ? (rawImpact as keyof typeof IMPACT_FILTER_LABEL) : undefined
  const node = params.get('node') ?? ''
  const agent = params.get('agent') ?? ''
  const rawKagent = params.get('kagent')
  const kagentKind = rawKagent && rawKagent in KAGENT_FILTER_LABEL ? (rawKagent as KagentAttribution['kind'] | 'sessions') : undefined
  // An older Radar ignores the agent and kagent filters and lists every actor, so they are sent only to one that serves them.
  const attributionFeature = useRadarFeature('kagentAttribution')
  const summaryFeature = useRadarFeature('kagentSessionSummary')
  const kagentFeature = kagentKind === 'sessions' ? summaryFeature : attributionFeature
  const kagentFilters = kagentFeature.support === 'supported'
  const kagentRequested = !!(agent || kagentKind)
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
  useEffect(() => setSearch(q), [q])
  useEffect(() => {
    if (search === q) return
    // Debounced: every keystroke would be a request.
    const t = setTimeout(() => setQ(search), 250)
    return () => clearTimeout(t)
  }, [search]) // eslint-disable-line react-hooks/exhaustive-deps

  const states = segmentStates(segment, linkedState)
  const query = useSubstrateActors({
    atespace: atespace || undefined,
    template: template || undefined,
    pool: pool || undefined,
    worker: worker || undefined,
    impact,
    node: node || undefined,
    nodeHas: node ? nodeHas : undefined,
    state: states,
    q: q || undefined,
    agent: kagentFilters ? agent || undefined : undefined,
    kagent: kagentFilters ? kagentKind : undefined,
    namespaces: kagentKind === 'sessions' ? params.get('namespaces') ?? undefined : undefined,
    sort,
    limit: ACTORS_PAGE_SIZE,
    offset: page * ACTORS_PAGE_SIZE,
  }, (!workspace || workspace.apiTier.state === 'connected') && (!kagentRequested || kagentFilters))
  const connected = (query.data?.apiTier ?? workspace?.apiTier)?.state === 'connected'
  const templates = useSubstrateTemplates(connected)

  const setSegment = (id: ActorSegment) => {
    const p = new URLSearchParams(params)
    const next = segmentStates(id, null)
    if (id === 'all' || !next) p.delete('state')
    else p.set('state', id === 'inProgress' ? 'inProgress' : next.join(','))
    p.delete('page')
    setParams(p, { replace: true })
  }
  // Every filter but the state segment: the attention count covers all actors, so with any of these set it says so.
  const filtered = !!(atespace || template || pool || q || worker || impact || node || kagentRequested)
  // Opens the summary's segment over every actor, the set its count is over. Needs attention first puts the stuck ones on top.
  const openAttention = (state: 'crashed' | 'inProgress') => {
    const p = new URLSearchParams(params)
    for (const k of SCOPING_PARAMS) p.delete(k)
    p.set('state', state)
    if (state === 'inProgress') p.delete('sort')
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

  // kagent's controls show where its sessions can be: a cluster serving kagent says so with kagentAgents.
  const showKagent = !!query.data?.kagentAgents || !!query.data?.rows.some((r) => r.kagent)
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
    ...(agent ? [{ label: `kagent Agent: ${agent}`, onClear: () => update({ agent: '' }) }] : []),
    // Also a way out of a link's filter on a Radar that can't apply it, where the menu is hidden.
    ...(kagentKind ? [{ label: `kagent: ${KAGENT_FILTER_LABEL[kagentKind]}`, onClear: () => update({ kagent: '' }) }] : []),
    ...(linkedState && segment === 'inProgress' && linkedState !== 'inProgress' ? [{ label: `State: ${ACTOR_STATE_LABEL[linkedState as SubstrateActorState]}`, onClear: () => setSegment('inProgress') }] : []),
  ]

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <SubstrateHeader title="Actors">
        <Segments
          label="Actor state"
          value={segment}
          onChange={setSegment}
          options={ACTOR_SEGMENTS.map((s) => {
            const n = segmentCount(query.data?.counts, s.id)
            return { id: s.id, label: s.label, count: n !== undefined ? n.toLocaleString('en-US') : undefined }
          })}
        />
      </SubstrateHeader>
      <ScreenBody>
        {tier && <SubstrateSourceLine tier={tier} status={status} />}
        {workspace && workspace.apiTier.state === 'connected' && !notConnected && (
          <ActorAttentionSummary
            workspace={workspace}
            filtered={filtered}
            onCrashed={() => openAttention('crashed')}
            onStuck={() => openAttention('inProgress')}
          />
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
        {!notConnected && <div className="flex flex-wrap items-center gap-2">
          <SelectMenu ariaLabel="Atespace" value={atespace} options={extra(atespaceOptions, atespace)} onChange={(v) => update({ atespace: v, ...(template && v && !template.startsWith(`${v}/`) ? { template: '' } : {}) })} className="min-w-[10rem]" />
          <SelectMenu ariaLabel="Template" value={template} options={extra(templateOptions, template)} onChange={(v) => update({ template: v })} className="min-w-[12rem]" searchPlaceholder="Search templates" />
          <SelectMenu ariaLabel="Pool" value={pool} options={extra(poolOptions, pool)} onChange={(v) => update({ pool: v })} className="min-w-[10rem]" />
          <Input
            aria-label="Search actor names"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search actor name…"
            className="h-8 min-w-[12rem] rounded-md border border-theme-border bg-theme-elevated px-3 text-sm text-theme-text-primary placeholder:text-theme-text-tertiary focus:border-skyhook-500 focus:outline-none"
            spellCheck={false}
          />
          {(showKagent || kagentRequested) && kagentFilters && (
            <SelectMenu ariaLabel="kagent" value={kagentKind ?? ''} options={KAGENT_OPTIONS.filter(o => o.value !== 'sessions' || summaryFeature.support === 'supported')} onChange={(v) => update({ kagent: v })} className="min-w-[12rem]" />
          )}
          <SelectMenu ariaLabel="Sort" value={sort} options={SORT_OPTIONS} onChange={(v) => update({ sort: v === 'attention' ? '' : v })} className="min-w-[11rem]" />
          <FilterChips chips={chips} />
        </div>}
        {kagentRequested && !kagentFilters && (
          <Notice>
            {kagentSupportText(kagentFeature.support, !!kagentFeature.upgrade)}
            {kagentSupportSettled(kagentFeature.support, !!kagentFeature.upgrade) && (
              <>{' '}<button type="button" onClick={() => update({ agent: '', kagent: '' })} className="text-accent-text hover:underline">Clear kagent filters</button></>
            )}
          </Notice>
        )}
        {kagentUnreadText(query.data?.kagentUnread, query.data?.kagentAgents) && (
          <p className="text-xs text-theme-text-secondary">{kagentUnreadText(query.data?.kagentUnread, query.data?.kagentAgents)}</p>
        )}
        {notConnected && tier ? (
          <>
            <SubstrateTierBanner tier={tier} />
            <SubstrateConnectNotice status={status} tier={tier} />
          </>
        ) : query.error && !query.data ? (
          <Notice>Radar couldn’t read the actors: {query.error instanceof Error ? query.error.message : 'unknown error'}.</Notice>
        ) : (
          <>
            <RefreshFailedNotice queries={[query]} />
            <ActorsTable
              sort={sort}
              data={query.data}
              loading={query.isLoading}
              placeholder={query.isPlaceholderData}
              page={page}
              onPage={setPage}
              onOpenActor={onOpenActor}
              openActor={openActor}
              onAgent={kagentFilters ? (a) => update({ agent: agentFilterValue(a), kagent: '' }) : undefined}
            />
          </>
        )}
        <p className="text-xs text-theme-text-tertiary">
          A <Term term="snapshot">snapshot</Term> of memory holds the running process, so the actor resumes where it left off. A snapshot of the root filesystem or volumes restarts the agent on its saved files. An actor without one boots fresh.
        </p>
      </ScreenBody>
    </div>
  )
}

export function ActorsTable({
  sort = 'attention',
  data,
  loading,
  placeholder,
  page,
  onPage,
  onOpenActor,
  openActor,
  onAgent,
  revisionOnly,
}: {
  sort?: ActorSort
  data?: SubstrateActorsResponse
  loading?: boolean
  /** The rows are the page before, kept while this page loads. */
  placeholder?: boolean
  page: number
  onPage: (page: number) => void
  onOpenActor: (ref: SubstrateRef) => void
  openActor: SubstrateRef | null
  /** Lists an Agent's sessions; absent where the Radar can't filter by Agent. */
  onAgent?: (agent: KagentAgentRef) => void
  revisionOnly?: boolean
}) {
  const wide = useMediaQuery(SUBSTRATE_WIDE)
  const rows = data?.rows ?? []
  const total = data?.total
  const from = (data?.offset ?? page * ACTORS_PAGE_SIZE) + 1
  const to = (data?.offset ?? 0) + rows.length
  const cov = data?.coverage
  // A kagent filter judged where the Agents weren't read: an Agent's count is a lower bound.
  const agentsUnread = !!data?.kagentUnread?.length
  const empty = loading
    ? 'Loading actors…'
    : !data
      ? 'Actors were not read.'
      : cov && (cov.state === 'denied' || cov.state === 'failed')
        ? `No actors: ${coverageReason(cov, 'actors')}.`
        : coverageInPart(cov)
          ? `No actors match in what Radar could read (${coverageReason(cov, 'actors')}).`
          : agentsUnread
            ? `No actors match where Radar could read kagent’s Agents; it couldn’t read them in ${data!.kagentUnread!.join(', ')}.`
            : 'No actors match.'
  const pages = total !== undefined ? Math.ceil(total / ACTORS_PAGE_SIZE) : undefined
  return (
    <div aria-busy={placeholder || undefined} className={placeholder ? 'opacity-60' : undefined}>
      <SectionTable
        columns={actorColumns(wide, data?.kagentAgents || rows.some((r) => r.kagent) ? { onAgent, revisionOnly } : undefined)}
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
                  : <>{from.toLocaleString('en-US')}–{to.toLocaleString('en-US')} of {coverageInPart(cov) || agentsUnread ? 'at least ' : ''}{total.toLocaleString('en-US')} · {SORT_LABEL[sort]}</>}
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
