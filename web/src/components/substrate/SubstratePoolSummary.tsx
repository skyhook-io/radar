import type { ReactNode } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import { clsx } from 'clsx'
import { Unlink } from 'lucide-react'
import { Badge, SectionHeading, Tooltip, formatDuration, toneFillClass, toneTextClass, type NavigateToResource, type StatusTone } from '@skyhook-io/k8s-ui'
import { getWorkerPoolSandboxConfig, getWorkerPoolStatus } from '@skyhook-io/k8s-ui/components/resources/resource-utils-agent-runtimes'
import { substrateErrorCode, useSubstratePool, type SubstrateK8sPool, type SubstratePool, type SubstrateRef, type SubstrateWorkerRow } from '../../api/substrateWorkspace'
import { useConnection } from '../../context/ConnectionContext'
import { usePrometheusStatus } from '../../api/client'
import { useSubstrateHealth } from '../../api/substrateHealth'
import { SubstrateVerdictLine } from '../resource/SubstrateVerdictLine'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { RefreshFailedNotice } from '../workspace/layout'
import { ACTOR_STATE_LABEL, definitiveFailure, placementTone, poolPodsText, poolReadFailure, poolReading, bytesText, coverageInPart, coverageReason, coverageUnread, cpuText, tierReading } from './readings'
import { ACTOR_DRAWER_PARAM, SUBSTRATE_PATH, encodeActorParam, substrateActorPath, substrateActorsPath, substrateTemplatePath } from './routes'
import { IMPACT_LABEL, Name, Term, Unread, When, WorkerStateChip } from './shared'
import { RESERVED_MEANING, lostText } from './SubstrateWorkers'

const CARD = 'rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm'

/** Past this many workers the grid becomes one row per worker with a slot bar. */
export const PLACEMENT_CARD_LIMIT = 12



type WorkerActor = NonNullable<SubstrateWorkerRow['actors']>[number]

/**
 * Opens an actor from a pool's page or drawer: its drawer inside the
 * workspace, its page from anywhere else (the Resources list's drawer).
 */
export function useOpenActor() {
  const { pathname } = useLocation()
  const [params, setParams] = useSearchParams()
  const navigate = useWorkspaceNavigate()
  const { connection } = useConnection()
  return (ref: SubstrateRef) => {
    if (pathname === SUBSTRATE_PATH || pathname.startsWith(`${SUBSTRATE_PATH}/`)) {
      const p = new URLSearchParams(params)
      p.set(ACTOR_DRAWER_PARAM, encodeActorParam(ref))
      p.delete('drawer')
      setParams(p, { replace: true })
    } else {
      navigate(substrateActorPath(ref, connection.context || undefined))
    }
  }
}

/** What one worker's slots hold: an actor per filled slot, then the free ones. */
export function workerSlots(w: Pick<SubstrateWorkerRow, 'capacity' | 'allocated' | 'actors'>): { actors: WorkerActor[]; free: number; total: number | undefined } {
  const actors = w.actors ?? []
  const total = w.capacity?.actors
  const filled = Math.max(actors.length, w.allocated?.actors ?? 0)
  return { actors, free: total !== undefined ? Math.max(0, total - filled) : 0, total }
}

/** The mark of an actor whose sandbox is gone: an icon, not a strike, which would read as deleted. The legend names it; a cell keeps the room for the actor's name. */
function LostMarker() {
  return <Unlink aria-hidden className={clsx('ml-auto h-3 w-3 shrink-0', toneTextClass('unhealthy'))} />
}

function ActorCell({ actor, onOpen }: { actor: WorkerActor; onOpen: (ref: SubstrateRef) => void }) {
  // Running on a worker Substrate no longer trusts is not healthy running: its own colour, as in the legend.
  const tone = placementTone(actor.state, actor.workerImpact?.kind)
  // A lost sandbox is told apart by more than hue: a "lost" marker, so a 4px bar isn't the only cue.
  const lost = actor.workerImpact?.kind === 'restarted'
  const state = actor.workerImpact ? `${ACTOR_STATE_LABEL[actor.state]} · ${IMPACT_LABEL[actor.workerImpact.kind]}` : ACTOR_STATE_LABEL[actor.state]
  return (
    <Tooltip content={`${actor.atespace} / ${actor.name} · ${state} · template ${actor.template.name}${actor.workerImpact ? `. ${actor.workerImpact.reading}` : ''}`}>
      <button
        type="button"
        onClick={() => onOpen(actor)}
        className="flex w-full min-w-0 items-center gap-1.5 rounded-md border border-theme-border bg-theme-base px-1.5 py-1 text-left font-mono text-[11px] text-theme-text-primary hover:bg-theme-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
      >
        <span aria-hidden className={clsx('h-3 w-1 shrink-0 rounded-sm', toneFillClass(tone))} />
        <span className={clsx('min-w-0 truncate', lost && toneTextClass('unhealthy'))}>{actor.name}</span>
        {lost && <LostMarker />}
        <span className="sr-only">, {state}</span>
      </button>
    </Tooltip>
  )
}

/** The node's risk, as two actor lists: what its loss stops, and what state it alone holds. */
export function NodeRiskLinks({ node }: { node: string }) {
  const navigate = useWorkspaceNavigate()
  return (
    <div className="mt-1 flex flex-wrap gap-x-3 text-xs">
      <button type="button" onClick={() => navigate(substrateActorsPath({ node, nodeHas: 'running' }))} className="text-accent-text hover:underline">Actors running on this node →</button>
      <Tooltip content="Actors whose newest snapshot is stored only on this node: losing the node, not just this worker Pod, loses that state.">
        <button type="button" onClick={() => navigate(substrateActorsPath({ node, nodeHas: 'localSnapshot' }))} className="text-accent-text hover:underline">Snapshots only on this node →</button>
      </Tooltip>
    </div>
  )
}

/**
 * A worker's actor slots in words. Free slots are a count, not positions:
 * "18 of 24 actor slots used · 6 free". A worker that takes nothing new has
 * unused slots, not free ones. Unknown capacity stays unknown.
 */
export function slotsText(w: Pick<SubstrateWorkerRow, 'state' | 'capacity' | 'allocated' | 'actors'>): string {
  const { actors, free, total } = workerSlots(w)
  const used = Math.max(actors.length, w.allocated?.actors ?? 0)
  if (total === undefined) return `${used} ${used === 1 ? 'actor' : 'actors'} · slot capacity unknown`
  const rest = w.state === 'active' ? `${free} free` : `${free} unused · places nothing new`
  return `${used} of ${total} actor slots used · ${rest}`
}

function resourcesReserved(w: SubstrateWorkerRow): ReactNode {
  if (!w.capacity || !w.allocated) return undefined
  return (
    <>
      {cpuText(w.allocated.cpuMillicores)} of {cpuText(w.capacity.cpuMillicores)} CPU · {bytesText(w.allocated.memoryBytes)} of {bytesText(w.capacity.memoryBytes)}{' '}
      <Tooltip content={RESERVED_MEANING}><span tabIndex={0} className="cursor-help underline decoration-dotted underline-offset-2">reserved</span></Tooltip>
    </>
  )
}

function WorkerStateBadge({ w }: { w: SubstrateWorkerRow }) {
  const lost = lostText(w)
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      <WorkerStateChip w={w} />
      {w.state !== 'active' && w.heldForSeconds !== undefined && <span className="whitespace-nowrap text-xs text-theme-text-tertiary">for at least {formatDuration(w.heldForSeconds * 1000)}</span>}
      {lost && <span className="whitespace-nowrap text-xs text-theme-text-secondary">{lost}</span>}
    </span>
  )
}

function WorkerCard({ w, onOpenActor, onOpenPod }: { w: SubstrateWorkerRow; onOpenActor: (ref: SubstrateRef) => void; onOpenPod: (w: SubstrateWorkerRow) => void }) {
  const { actors, free } = workerSlots(w)
  const podMismatch = w.podReady === true && w.state === 'unavailable'
  const resources = resourcesReserved(w)
  return (
    <div className="min-w-0 rounded-lg border border-theme-border bg-theme-surface p-3">
      <div className="flex flex-wrap items-center justify-between gap-x-2 gap-y-1">
        <button type="button" onClick={() => onOpenPod(w)} className="min-w-0 text-left text-accent-text hover:underline">
          <Name name={w.pod} />
        </button>
        <WorkerStateBadge w={w} />
      </div>
      {w.actors === undefined ? (
        <p className="mt-2 text-xs text-theme-text-tertiary">Actors were not read, so which actor holds each slot is unknown.</p>
      ) : actors.length + free === 0 && w.capacity?.actors === 0 ? (
        <p className="mt-2 text-xs text-theme-text-tertiary">0 slots: the worker reports no actor capacity, so Substrate places nothing here.</p>
      ) : (
        <>
          {actors.length > 0 && (
            <div className="mt-2 grid grid-cols-[repeat(auto-fill,minmax(8.75rem,1fr))] gap-1">
              {actors.map((a) => <ActorCell key={`${a.atespace}/${a.name}`} actor={a} onOpen={onOpenActor} />)}
            </div>
          )}
          <div className="mt-2 text-xs text-theme-text-secondary">{slotsText(w)}</div>
        </>
      )}
      <div className="mt-0.5 text-xs text-theme-text-tertiary">
        <span className="whitespace-nowrap font-mono">{w.node || 'node unknown'}</span>
        {resources && <> · {resources}</>}
      </div>
      {w.node && <NodeRiskLinks node={w.node} />}
      {podMismatch && <div className="mt-1 text-xs text-theme-text-secondary">Its Pod is Ready, so the worker, not Kubernetes, is refusing work.</div>}
      {w.podTerminating && <div className="mt-1 text-xs text-theme-text-secondary">Its Pod is terminating, so Kubernetes doesn’t count it as ready.</div>}
    </div>
  )
}

/** A filled-slot bar for a worker row, one segment per state. */
function SlotBar({ w }: { w: SubstrateWorkerRow }) {
  const { actors, free, total } = workerSlots(w)
  const size = Math.max(total ?? 0, actors.length) || 1
  // One segment per grid colour, so the bar and the cards read the same.
  const byTone = new Map<StatusTone, number>()
  for (const a of actors) {
    const tone = placementTone(a.state, a.workerImpact?.kind)
    byTone.set(tone, (byTone.get(tone) ?? 0) + 1)
  }
  return (
    <div role="img" aria-label={`${actors.length} of ${total ?? 'unknown'} slots used`} className="flex h-2 w-full overflow-hidden rounded bg-theme-elevated">
      {[...byTone].map(([tone, n]) => (
        <span key={tone} style={{ flexGrow: n / size, flexBasis: 0 }} className={toneFillClass(tone)} />
      ))}
      <span style={{ flexGrow: free / size, flexBasis: 0 }} />
    </div>
  )
}

function WorkerRows({ workers, onOpenPod }: { workers: SubstrateWorkerRow[]; onOpenPod: (w: SubstrateWorkerRow) => void }) {
  const navigate = useWorkspaceNavigate()
  return (
    <ul className="divide-y divide-theme-border rounded-lg border border-theme-border bg-theme-surface">
      {workers.map((w) => (
        <li key={w.name} className="grid grid-cols-[minmax(12rem,1.4fr)_minmax(8rem,auto)_minmax(8rem,1fr)_auto] items-center gap-3 px-3 py-2 text-sm">
          <button type="button" onClick={() => onOpenPod(w)} className="min-w-0 text-left text-accent-text hover:underline"><Name name={w.pod} /></button>
          <WorkerStateBadge w={w} />
          <SlotBar w={w} />
          <button type="button" onClick={() => navigate(substrateActorsPath({ worker: w.pod }))} className="whitespace-nowrap text-xs text-accent-text hover:underline">
            {slotsText(w)}
          </button>
        </li>
      ))}
    </ul>
  )
}

/**
 * The capacity verdict from Prometheus. Where it has none to give, a line
 * says why, so the section's absence is not read as "no problem".
 */
function CapacityVerdict({ namespace, name, onOpenMetrics }: { namespace: string; name: string; onOpenMetrics?: () => void }) {
  const target = { kind: 'workerpool' as const, namespace, name }
  const { data: prometheus } = usePrometheusStatus()
  const connected = prometheus?.connected === true
  const health = useSubstrateHealth(target, connected)
  if (connected && health.data?.status === 'available') return <SubstrateVerdictLine target={target} onOpenMetrics={onOpenMetrics} />
  let why: string | undefined
  if (prometheus && !connected) why = 'Prometheus is not connected, so Radar has no capacity verdict over time for this pool.'
  else if (health.data?.status === 'notFound') why = 'Prometheus has no Agent Substrate series for this pool, so Radar has no capacity verdict over time. The placement below is Substrate’s own reading, now.'
  if (!why) return null
  return <p className="text-xs text-theme-text-tertiary">{why}</p>
}

/**
 * The pool's workers before its map: active, registered with Substrate and
 * desired, then any desired worker Substrate has no registration for. Only a
 * complete worker list can say one is missing; whether its Pod exists is
 * Kubernetes' to say, so its Pod count stands beside it.
 */
export function WorkerCounts({ workers, partial, k8s, short }: { workers: Pick<SubstrateWorkerRow, 'state'>[]; partial: boolean; k8s?: SubstrateK8sPool; short: boolean }) {
  const desired = k8s?.desired
  const atLeast = partial ? 'at least ' : ''
  const active = workers.filter((w) => w.state === 'active').length
  const missing = !partial && desired !== undefined ? Math.max(0, desired - workers.length) : 0
  // A partial read that saw none counts nothing: the line below says Radar saw none.
  const seen = !partial || workers.length > 0
  const parts = [seen && `${atLeast}${active} active`, seen && `${atLeast}${workers.length} registered with Substrate`, desired !== undefined && `${desired} desired`].filter(Boolean)
  if (parts.length === 0) return null
  return (
    <div className="mb-3 space-y-0.5">
      <div className={clsx('text-sm font-medium', short ? toneTextClass('degraded') : 'text-theme-text-primary')}>Workers: {parts.join(' · ')}</div>
      {missing > 0 && (
        <div className="text-xs text-theme-text-secondary">
          <span className="font-medium text-theme-text-primary">{missing === 1 ? '1 desired worker is not registered with Substrate.' : `${missing} desired workers are not registered with Substrate.`}</span>
          {k8s && <> {poolPodsText(k8s)}</>}
        </div>
      )}
      {missing === 0 && k8s && <div className="text-xs text-theme-text-secondary">{poolPodsText(k8s)}</div>}
    </div>
  )
}

function Legend() {
  const items: { label: string; tone: Parameters<typeof toneFillClass>[0] }[] = [
    { label: 'running', tone: placementTone('running', undefined) },
    { label: 'its worker is unavailable', tone: placementTone('running', 'unavailable') },
    { label: 'its worker is draining', tone: placementTone('running', 'draining') },
    { label: 'resuming, suspending or reverting', tone: placementTone('resuming', undefined) },
    { label: 'crashed, still assigned', tone: placementTone('crashed', undefined) },
  ]
  return (
    <ul className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-theme-text-tertiary" aria-label="Legend">
      {items.map((i) => (
        <li key={i.label} className="flex items-center gap-1.5"><span aria-hidden className={clsx('h-3 w-1 rounded-sm', toneFillClass(i.tone))} />{i.label}</li>
      ))}
      <li className="flex items-center gap-1.5">
        <span aria-hidden className={clsx('h-3 w-1 rounded-sm', toneFillClass('unhealthy'))} />
        <LostMarker />
        sandbox lost: its worker restarted under it
      </li>
    </ul>
  )
}

export function Placement({ data, onOpenActor, onOpenPod, drawer }: { data: SubstratePool; onOpenActor: (ref: SubstrateRef) => void; onOpenPod: (w: SubstrateWorkerRow) => void; drawer?: boolean }) {
  const k8s = data.pool.kubernetes
  const workers = [...data.workers].sort((a, b) => a.pod.localeCompare(b.pod))
  const tierRead = data.apiTier.readAt
  const heading = <SectionHeading hint={<>which actor holds which slot · from Substrate’s API{tierRead ? <> · <When iso={tierRead} prefix="read" /></> : ''}</>}>Placement</SectionHeading>
  const kubernetesReady = k8s ? <p className="mt-2 text-xs text-theme-text-secondary">{poolPodsText(k8s)}</p> : null
  // Unread workers: placement is unknown, not empty, and no count of missing workers can be made from it.
  if (coverageUnread(data.coverage.workers)) {
    return (
      <section className={CARD}>
        {heading}
        <p className="text-sm text-theme-text-secondary">Which actor holds which slot is unknown: Substrate’s workers were not read ({coverageReason(data.coverage.workers, 'workers')}).</p>
        {kubernetesReady}
      </section>
    )
  }
  const partial = coverageInPart(data.coverage.workers)
  const rows = workers.length > PLACEMENT_CARD_LIMIT
  return (
    <section className={CARD}>
      {heading}
      {partial && <p className="mb-2 text-xs text-theme-text-secondary">Not every worker was read, so these are the ones Radar saw: {coverageReason(data.coverage.workers, 'workers')}.</p>}
      {/* The shortage leads: how many workers the pool has and wants, above the map of what they hold. */}
      <WorkerCounts workers={workers} partial={partial} k8s={k8s} short={data.pool.capacity === 'short'} />
      <div className="mb-3"><Legend /></div>
      {workers.length === 0 ? (
        <p className="text-sm text-theme-text-tertiary">{partial ? 'Radar saw no worker of this pool in what it read.' : 'Substrate has no workers in this pool.'}</p>
      ) : rows ? (
        <WorkerRows workers={workers} onOpenPod={onOpenPod} />
      ) : (
        // items-start: each card ends at its content; order stays row by row.
        <div className={clsx('grid items-start gap-3', drawer ? 'grid-cols-1' : 'grid-cols-1 lg:grid-cols-2')}>
          {workers.map((w) => <WorkerCard key={w.name} w={w} onOpenActor={onOpenActor} onOpenPod={onOpenPod} />)}
        </div>
      )}
      <p className="mt-3 text-xs text-theme-text-tertiary">
        A <Term term="slot">slot</Term> is room for one actor; CPU or memory can run out first.
      </p>
    </section>
  )
}

function RunsHere({ data, loading, classes, config, onOpenTemplate }: { data?: SubstratePool; loading?: boolean; classes: string[]; config?: string; onOpenTemplate: (ref: SubstrateRef) => void }) {
  const templates = data?.templates
  let templatesBody: ReactNode
  if (loading) templatesBody = <span className="text-theme-text-tertiary">Reading…</span>
  else if (!data || data.apiTier.state !== 'connected' || templates === undefined) templatesBody = <Unread title="Kept in Substrate’s API.">unknown: templates were not read</Unread>
  else if (templates.length === 0) templatesBody = <span className="text-theme-text-secondary">None.</span>
  else {
    templatesBody = (
      <>
        {templates.map((t, i) => (
          <span key={`${t.atespace}/${t.name}`}>
            {i > 0 && ', '}
            <button type="button" onClick={() => onOpenTemplate(t)} className="whitespace-nowrap font-mono text-accent-text hover:underline">{t.atespace} / {t.name}</button>
          </span>
        ))}
        <span className="text-theme-text-tertiary"> · matching labels, not a promise each actor fits</span>
      </>
    )
  }
  return (
    <section className={CARD}>
      <dl className="grid grid-cols-[12.5rem_minmax(0,1fr)] items-baseline gap-x-4 gap-y-2 text-sm">
        <dt className="text-theme-text-tertiary">Sandbox classes</dt>
        <dd className="min-w-0">
          {classes.length ? classes.map((c) => <Badge key={c} tone="structural" size="sm" className="mr-1.5">{c}</Badge>) : <Unread>none named</Unread>}
          {config && <span className="text-theme-text-secondary">resolved to SandboxConfig <span className="font-mono">{config}</span></span>}
        </dd>
        <dt className="text-theme-text-tertiary"><Term term="template">Templates</Term> whose selector matches this pool</dt>
        <dd className="min-w-0 break-words">{templatesBody}</dd>
      </dl>
    </section>
  )
}

/**
 * A WorkerPool's composed Overview, on its page and in the drawer: Radar's
 * capacity verdict from Prometheus, which actor holds which slot, and what
 * can run here. The WorkerPool's own renderer moves to "Spec & status".
 */
export function SubstratePoolSummary({
  namespace,
  name,
  resource,
  context,
  onNavigate,
  leadContent,
}: {
  namespace: string
  name: string
  resource: any
  context: 'drawer' | 'expanded'
  onNavigate?: NavigateToResource
  leadContent?: ReactNode
}) {
  const query = useSubstratePool({ namespace, name })
  const [params, setParams] = useSearchParams()
  const navigate = useWorkspaceNavigate()
  const { connection } = useConnection()
  const openActor = useOpenActor()
  const code = substrateErrorCode(query.error)
  // A refresh that says the pool is gone or can't be read replaces what was kept.
  const data = definitiveFailure(code) || code === 'incomplete' ? undefined : query.data
  const specClasses: string[] = Array.isArray(resource?.spec?.sandboxClasses) ? resource.spec.sandboxClasses.map((c: any) => c?.name).filter(Boolean) : []
  const classes = [...new Set([...specClasses, ...(data?.pool.sandboxClasses ?? [])])]
  const config = getWorkerPoolSandboxConfig(resource)
  const openPod = (w: SubstrateWorkerRow) => onNavigate?.({ kind: 'Pod', namespace: w.namespace, name: w.pod })
  const openMetrics = context === 'expanded'
    ? () => {
        const p = new URLSearchParams(params)
        p.set('tab', 'metrics')
        setParams(p, { replace: true })
      }
    : undefined
  let placement: ReactNode
  if (query.isLoading) placement = <section className={CARD}><SectionHeading>Placement</SectionHeading><p className="text-sm text-theme-text-tertiary">Reading Substrate’s workers…</p></section>
  else if (!data) placement = <section className={CARD}><SectionHeading>Placement</SectionHeading><p className="text-sm text-theme-text-tertiary">{poolReadFailure(code, query.error instanceof Error ? query.error.message : undefined)}</p></section>
  else if (data.apiTier.state !== 'connected') placement = <section className={CARD}><SectionHeading>Placement</SectionHeading><p className="text-sm text-theme-text-secondary">{tierReading(data.apiTier).detail} Which actor holds which slot is kept in Substrate’s API.</p></section>
  else placement = <><RefreshFailedNotice queries={[query]} /><Placement data={data} onOpenActor={openActor} onOpenPod={openPod} drawer={context === 'drawer'} /></>
  return (
    <div className="space-y-4 p-4">
      {leadContent}
      <CapacityVerdict namespace={namespace} name={name} onOpenMetrics={openMetrics} />
      {placement}
      <RunsHere data={data} loading={query.isLoading} classes={classes} config={config === '-' ? undefined : config} onOpenTemplate={(t) => navigate(substrateTemplatePath(t, connection.context || undefined))} />
    </div>
  )
}

/**
 * A WorkerPool's header chip: the same reading as its row in the Pools list,
 * with Kubernetes' phase beside it. Until Substrate's side is read, the
 * Kubernetes reading stands alone.
 */
export function PoolHeaderBadge({ resource }: { resource: any }) {
  const namespace: string = resource?.metadata?.namespace ?? ''
  const name: string = resource?.metadata?.name ?? ''
  const { data } = useSubstratePool(namespace && name ? { namespace, name } : null)
  if (!data) {
    const k = getWorkerPoolStatus(resource)
    return <span className={clsx('badge whitespace-nowrap', k.color)}>{k.text}</span>
  }
  const r = poolReading(data.pool)
  // Kubernetes' phase rides in the tooltip: beside the name it would crowd the title out.
  return (
    <Tooltip content={[r.meaning, r.kubernetes && `Kubernetes: ${r.kubernetes}.`].filter(Boolean).join(' ')}>
      <span><Badge severity={r.severity} size="sm" className="whitespace-nowrap">{r.label}</Badge></span>
    </Tooltip>
  )
}
