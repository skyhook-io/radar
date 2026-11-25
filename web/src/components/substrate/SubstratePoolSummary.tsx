import type { ReactNode } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import { clsx } from 'clsx'
import { Unlink } from 'lucide-react'
import { Badge, CertaintyGlyph, DimensionValue, FactGrid, FactRow, FactSource, FactValue, SectionHeading, Tooltip, WorkspaceCard, WorkspaceProblemCallout, formatDuration, toneFillClass, toneTextClass, type NavigateToResource, type StatusTone } from '@skyhook-io/k8s-ui'
import { getWorkerPoolSandboxConfig, getWorkerPoolStatus } from '@skyhook-io/k8s-ui/components/resources/resource-utils-agent-runtimes'
import { substrateErrorCode, useSubstratePool, type SubstratePool, type SubstrateRef, type SubstrateWorkerRow } from '../../api/substrateWorkspace'
import { useConnection } from '../../context/ConnectionContext'
import { isForbiddenError, usePrometheusStatus } from '../../api/client'
import { useSubstrateHealth } from '../../api/substrateHealth'
import { formatMetricValue } from '@skyhook-io/k8s-ui/components/charts'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { WorkspaceConditions } from '../workspace/WorkspaceConditions'
import { coverageLabel } from '../workspace/coverage'
import { RefreshFailedNotice } from '../workspace/layout'
import { ACTOR_STATE_LABEL, definitiveFailure, attentionTone, placementTone, poolReadFailure, poolReading, splitReading, bytesText, coverageInPart, coverageReason, coverageUnread, cpuText, tierReading } from './readings'
import { ACTOR_DRAWER_PARAM, SUBSTRATE_GROUP, SUBSTRATE_PATH, SUBSTRATE_SCREENS, encodeActorParam, substrateActorPath, substrateActorsPath, substratePoolPath, substrateTemplatePath } from './routes'
import { IMPACT_LABEL, Name, Term, Unread, WorkerStateChip } from './shared'
import { SlotsCell } from './SubstrateOverview'
import { RESERVED_MEANING, lostText } from './SubstrateWorkers'

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
 * "18 of 24 actor slots used · 6 free". A worker that takes no new actors has
 * unused slots, not free ones. Unknown capacity stays unknown.
 */
export function slotsText(w: Pick<SubstrateWorkerRow, 'state' | 'capacity' | 'allocated' | 'actors'>): string {
  const { actors, free, total } = workerSlots(w)
  const used = Math.max(actors.length, w.allocated?.actors ?? 0)
  if (total === undefined) return `${used} ${used === 1 ? 'actor' : 'actors'} · slot capacity unknown`
  const rest = w.state === 'active' ? `${free} free` : `${free} unused · takes no new actors`
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

function WorkerCard({ w, onOpenActor, onOpenPod, onOpenNode }: { w: SubstrateWorkerRow; onOpenActor: (ref: SubstrateRef) => void; onOpenPod: (w: SubstrateWorkerRow) => void; onOpenNode?: (node: string) => void }) {
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
        {w.node && onOpenNode
          ? <button type="button" onClick={() => onOpenNode(w.node)} className="break-all font-mono text-accent-text hover:underline">{w.node}</button>
          : <span className="break-all font-mono">{w.node || 'node unknown'}</span>}
        {resources && <> · {resources}</>}
      </div>
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
  const { data: prometheus, isError } = usePrometheusStatus()
  const connected = prometheus?.connected === true
  const health = useSubstrateHealth(target, connected)
  const delivery = !health.error && health.data?.status === 'available'
    ? health.data.verdict?.facts
      .filter((f) => f.id === 'resumeP95' || f.id === 'resumeFailures' || f.id === 'crashes')
      .map((f) => `${f.label} ${f.state === 'ok' && f.value !== undefined ? formatMetricValue(f.value, f.unit) : f.state === 'empty' ? 'none recorded' : 'unavailable'}`)
    : undefined
  const reason = isForbiddenError(health.error) ? `Requires get on workerpools.ate.dev in ${namespace}`
    : isError ? 'Prometheus connection status could not be read'
    : !prometheus ? 'Checking Prometheus…'
    : !connected ? 'Prometheus is not connected'
    : health.error ? health.error.message
    : health.data?.status === 'notFound' ? 'No Substrate series for this pool'
    : health.data?.status === 'available' ? (delivery?.length ? delivery.join(' · ') : 'Resume and crash readings unavailable')
    : health.data?.reason || health.blockedReason || (health.upgrade ? 'This Radar needs an upgrade' : 'Reading Prometheus…')
  return <DimensionValue tone={delivery?.length ? 'neutral' : 'unknown'} source="Substrate Prometheus series · last hour" linkLabel="Metrics" onOpen={onOpenMetrics}>{reason}</DimensionValue>
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

export function Placement({
  data, onOpenActor, onOpenPod, onOpenNode, drawer,
}: {
  data: SubstratePool
  onOpenActor: (ref: SubstrateRef) => void
  onOpenPod: (w: SubstrateWorkerRow) => void
  onOpenNode?: (node: string) => void
  drawer?: boolean
}) {
  const workers = [...data.workers].sort((a, b) => a.pod.localeCompare(b.pod))
  if (workers.length === 0 || coverageUnread(data.coverage.workers)) return null
  const partial = coverageInPart(data.coverage.workers)
  const nodes = [...new Set(workers.map((w) => w.node).filter(Boolean))]
  const content = <>
    {partial && <p className="mb-2 text-xs text-theme-text-secondary">Observed workers only · {coverageReason(data.coverage.workers, 'workers')}.</p>}
    {workers.length > PLACEMENT_CARD_LIMIT ? (
      <WorkerRows workers={workers} onOpenPod={onOpenPod} />
    ) : (
      <div className={clsx('grid items-start gap-3', drawer ? 'grid-cols-1' : 'grid-cols-1 lg:grid-cols-2')}>
        {workers.map((w) => <WorkerCard key={w.name} w={w} onOpenActor={onOpenActor} onOpenPod={onOpenPod} onOpenNode={onOpenNode} />)}
      </div>
    )}
    <div className="mt-3"><Legend /></div>
    <p className="mt-2 text-xs text-theme-text-tertiary">
      A <Term term="slot">slot</Term> holds one actor. Placement also uses CPU and memory.
    </p>
    {nodes.length > 0 && <div className="mt-2 flex flex-wrap gap-3 border-t-subtle pt-2">
      {nodes.map((node) => <div key={node} className="min-w-0 text-xs text-theme-text-secondary">
        <span className="break-all font-mono">{node}</span><NodeRiskLinks node={node} />
      </div>)}
    </div>}
  </>
  return drawer ? <section className="border-t-subtle pt-3"><SectionHeading>Placement</SectionHeading>{content}</section>
    : <WorkspaceCard title="Placement">{content}</WorkspaceCard>
}

interface PoolResource {
  metadata?: { namespace?: string; name?: string; generation?: number }
  spec?: { replicas?: number; sandboxClasses?: { name?: string }[] }
  status?: {
    replicas?: number
    readyReplicas?: number
    selector?: string
    sandboxClasses?: { name?: string; conditions?: { type: string; status: string; reason?: string; message?: string; lastTransitionTime?: string }[] }[]
  }
}

function poolConditions(resource: PoolResource) {
  return (resource.status?.sandboxClasses ?? []).flatMap((c) =>
    (c.conditions ?? []).map((condition) => ({ ...condition, type: c.name ? `${c.name} · ${condition.type}` : condition.type })),
  )
}

function PoolAbout({ resource, onNavigate }: { resource: PoolResource; onNavigate?: NavigateToResource }) {
  const classes = resource.spec?.sandboxClasses?.flatMap((c) => c.name ? [c.name] : []) ?? []
  const config = getWorkerPoolSandboxConfig(resource)
  return (
    <FactGrid>
      <FactRow label="Sandbox classes">{classes.length > 0 ? classes.join(' · ') : <Unread>None declared</Unread>}</FactRow>
      <FactRow label="SandboxConfig">
        {config === '-' ? <Unread>Unreported</Unread> : onNavigate
          ? <button type="button" onClick={() => onNavigate({ kind: 'SandboxConfig', group: SUBSTRATE_GROUP, namespace: '', name: config })} className="font-mono text-accent-text hover:underline">{config}</button>
          : <span className="font-mono">{config}</span>}
      </FactRow>
    </FactGrid>
  )
}

export function PoolSpecStatus({ resource }: { resource: PoolResource }) {
  const count = (n: number | undefined, source: string) => {
    const fact = { text: n === undefined ? 'Unreported' : String(n), tone: n === undefined ? 'unknown' as const : 'neutral' as const, source }
    return <><FactValue fact={fact} /><FactSource fact={fact} /></>
  }
  return (
    <div className="space-y-4 p-4">
      <WorkspaceCard title="Desired by spec">
        <FactGrid><FactRow label="Workers">{count(resource.spec?.replicas, 'spec.replicas')}</FactRow></FactGrid>
        <div className="mt-2"><PoolAbout resource={resource} /></div>
      </WorkspaceCard>
      <WorkspaceCard title="Reported by controller">
        <FactGrid>
          <FactRow label="Worker replicas">{count(resource.status?.replicas, 'status.replicas')}</FactRow>
          <FactRow label="Ready replicas">{count(resource.status?.readyReplicas, 'status.readyReplicas')}</FactRow>
          <FactRow label="Worker selector">{resource.status?.selector || <Unread>Unreported</Unread>}</FactRow>
        </FactGrid>
      </WorkspaceCard>
      <WorkspaceConditions conditions={poolConditions(resource)} title="Sandbox class conditions" />
    </div>
  )
}

export function SubstratePoolSummary({
  namespace, name, resource, context, onNavigate,
}: {
  namespace: string
  name: string
  resource: PoolResource
  context: 'drawer' | 'expanded'
  onNavigate?: NavigateToResource
}) {
  const query = useSubstratePool({ namespace, name })
  const [params, setParams] = useSearchParams()
  const navigate = useWorkspaceNavigate()
  const { connection } = useConnection()
  const openActor = useOpenActor()
  const code = substrateErrorCode(query.error)
  const data = definitiveFailure(code) || code === 'incomplete' ? undefined : query.data
  const k = data?.pool.kubernetes
  const workerRead = data?.coverage.workers
  const finding = data?.attention
  const placement = data?.pool.placement
  const partial = coverageInPart(workerRead)
  const workers = placement?.workers
  const openTab = (tab: string) => {
    const p = new URLSearchParams(params)
    if (tab === 'overview') p.delete('tab')
    else p.set('tab', tab)
    setParams(p, { replace: true })
  }
  const drawer = context === 'drawer'
  const desired = resource.spec?.replicas
  const replicas = resource.status?.readyReplicas
  const observed = k?.pods
  const [lead, rawDetail] = finding ? splitReading(finding.reading) : []
  const readState = query.isLoading ? 'Reading Substrate…'
    : !data ? poolReadFailure(code, query.error instanceof Error ? query.error.message : undefined)
    : data.apiTier.state !== 'connected' ? tierReading(data.apiTier).detail
    : coverageReason(workerRead, 'workers')
  const Frame = drawer ? FlatPoolSection : WorkspaceCard
  return (
    <div className="space-y-4 p-4">
      <RefreshFailedNotice queries={[query]} />
      {finding && <WorkspaceProblemCallout rootKind="WorkerPool" subjectIsSelf problem={{
        id: finding.id, category: finding.rule, source: 'reading', tone: attentionTone(finding),
        subject: { kind: 'WorkerPool', group: SUBSTRATE_GROUP, namespace, name },
        title: poolReading(data!.pool, finding).label,
        detail: lead, nextStep: k?.health === 'unhealthy' || finding.rule !== 'poolShort' ? finding.nextStep : 'Compare controller readiness with Substrate registrations below.',
        rawDetail, rawDetailLabel: 'Details',
        origin: { label: k?.health === 'unhealthy' ? 'WorkerPool controller status' : 'Kubernetes and Substrate' },
      }} />}
      {drawer && <div className="flex flex-wrap gap-2">
        <button type="button" className="btn-brand px-3 py-1 text-sm" onClick={() => navigate(substratePoolPath({ namespace, name }, connection.context || undefined))}>Open pool</button>
        <button type="button" className="btn-secondary px-3 py-1 text-sm" onClick={() => navigate(substratePoolPath({ namespace, name }, connection.context || undefined, 'workers'))}>Workers</button>
      </div>}
      <Frame title="At a glance">
        <FactGrid>
          <FactRow label="Provisioning">
            <DimensionValue tone={k?.health === 'unhealthy' ? 'unhealthy' : k?.health === 'healthy' && (!observed || observed.ready === replicas) ? 'healthy' : 'unknown'}
              source={<>{observed ? `Pods observed by Radar: ${observed.created} present, ${observed.ready} ready${observed.terminating ? `, ${observed.terminating} terminating` : ''}` : `Pod counts unavailable · requires collected Pods and list pods in ${namespace}`}
                {observed && replicas !== undefined && observed.ready !== replicas && ' · Counts differ from the controller’s status'}</>}>
              {desired === undefined ? 'Desired workers unreported' : `${desired} desired by spec`}
              {' · '}{replicas === undefined ? 'Controller readiness unreported' : `${replicas} ready reported by controller`}
            </DimensionValue>
          </FactRow>
          <FactRow label="Registration">
            <DimensionValue tone={finding ? attentionTone(finding) : !workers || partial ? 'unknown' : workers.active > 0 ? 'healthy' : 'neutral'}
              source={workers ? partial ? coverageReason(workerRead, 'workers') : 'Worker registrations from Substrate' : readState}
              linkLabel="Workers" onOpen={drawer ? undefined : () => openTab('workers')}>
              {workers && (!partial || workers.total > 0) ? <>{partial ? '≥' : ''}{workers.total} registered · {partial ? '≥' : ''}{workers.active} accepting actors</> : 'Unknown'}
            </DimensionValue>
          </FactRow>
          <FactRow label="Placement capacity">
            <DimensionValue tone={finding ? attentionTone(finding) : placement?.complete && data?.pool.capacity === 'free' ? 'healthy' : 'unknown'}
              source="Slots on active workers · actor CPU and memory can limit placement" linkLabel="Workers" onOpen={drawer ? undefined : () => openTab('workers')}>
              {data ? <SlotsCell p={data.pool} connected={data.apiTier.state === 'connected'} /> : <Unread>Unknown</Unread>}
            </DimensionValue>
          </FactRow>
          <FactRow label="Template matches">
            <DimensionValue tone={data?.templates && !coverageInPart(data.coverage.templates) ? 'neutral' : 'unknown'}
              source={data?.templates ? coverageInPart(data.coverage.templates) ? coverageReason(data.coverage.templates, 'templates') : 'Template selectors matched against this pool’s labels' : data ? coverageReason(data.coverage.templates, 'templates') : readState}
              linkLabel="Templates" onOpen={() => navigate(SUBSTRATE_SCREENS.templates.path)}>
              {data?.templates && (!coverageInPart(data.coverage.templates) || data.templates.length > 0)
                ? <>{coverageInPart(data.coverage.templates) && <CertaintyGlyph certainty="lower_bound" title="Some templates could not be read" />} {data.templates.length} label-matched</>
                : 'Unknown'}
            </DimensionValue>
            {!!data?.templates?.length && <div className="mt-1 flex flex-wrap gap-1.5">
              {data.templates.map((t) => <button type="button" key={`${t.atespace}/${t.name}`} onClick={() => navigate(substrateTemplatePath(t, connection.context || undefined))} className="btn-secondary px-2 py-0.5 font-mono text-xs">{t.atespace}/{t.name}</button>)}
            </div>}
          </FactRow>
          <FactRow label="Delivery metrics"><CapacityVerdict namespace={namespace} name={name} onOpenMetrics={drawer ? undefined : () => openTab('metrics')} /></FactRow>
        </FactGrid>
        {data && data.coverage.pools.state !== 'full' && <p className="mt-2 text-xs text-theme-text-tertiary">{coverageLabel(data.coverage.pools)}</p>}
      </Frame>
      {data && <Placement data={data} drawer={drawer} onOpenActor={openActor} onOpenPod={(w) => onNavigate?.({ kind: 'Pod', namespace: w.namespace, name: w.pod })}
        onOpenNode={onNavigate ? (node) => onNavigate({ kind: 'Node', namespace: '', name: node }) : undefined} />}
      <Frame title="About"><PoolAbout resource={resource} onNavigate={onNavigate} /></Frame>
      <WorkspaceConditions conditions={poolConditions(resource)} title="Sandbox class conditions" framed={!drawer} />
    </div>
  )
}

function FlatPoolSection({ title, children }: { title: string; children: ReactNode }) {
  return <section className="border-t-subtle pt-3"><SectionHeading>{title}</SectionHeading>{children}</section>
}

export function PoolHeaderBadge({ resource }: { resource: any }) {
  const namespace: string = resource?.metadata?.namespace ?? ''
  const name: string = resource?.metadata?.name ?? ''
  const query = useSubstratePool(namespace && name ? { namespace, name } : null)
  const code = substrateErrorCode(query.error)
  const data = definitiveFailure(code) || code === 'incomplete' ? undefined : query.data
  if (!data) {
    const k = getWorkerPoolStatus(resource)
    return <span className={clsx('badge whitespace-nowrap', k.color)}>{k.text}</span>
  }
  const r = poolReading(data.pool, data.attention)
  // Kubernetes' phase rides in the tooltip: beside the name it would crowd the title out.
  return (
    <Tooltip content={[r.meaning, r.kubernetes && `Kubernetes: ${r.kubernetes}.`].filter(Boolean).join(' ')}>
      <span><Badge severity={r.severity} size="sm" className="whitespace-nowrap">{r.label}</Badge></span>
    </Tooltip>
  )
}
