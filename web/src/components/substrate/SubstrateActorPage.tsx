import { useState, type ReactNode } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import { Bot, Camera, FileCode, Globe, LayoutDashboard, RefreshCw, ScrollText } from 'lucide-react'
import { Badge, CodeViewer, DetailShell, DimensionValue, FactGrid, FactRow, FoldSection, PaneLoader, StatusDot, Tooltip, WorkspaceCard, toneTextClass, type DetailShellTab } from '@skyhook-io/k8s-ui'
import {
  substrateErrorCode,
  useSubstrateActor,
  useSubstrateActorLogs,
  useSubstrateWorkspace,
  type SubstrateActorDetail,
  type SubstrateActorLogs,
  type SubstrateLogSource,
  type SubstrateRef,
} from '../../api/substrateWorkspace'
import { RefreshFailedNotice, ScreenEmptyState, Segments, SectionTable, type TableColumn } from '../workspace/layout'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { SubjectLink, type OpenSubject } from './SubstrateAttentionList'
import { NotInContext, useContextPin } from './contextPin'
import { ActorLogsBody, SubstrateActorOverview, egressText, workerLogsAvailable } from './SubstrateActorOverview'
import { bytesText, cpuText, definitiveFailure, fidelityLabel, killTie, resumeConditionSentence, tierReading } from './readings'
import { SUBSTRATE_PATH, SUBSTRATE_SCREENS } from './routes'
import { ActorStateBadge, PoolMatchLinks, Term, Unread, When } from './shared'

export type ActorTab = 'overview' | 'snapshots' | 'logs' | 'egress' | 'spec'

const TABS: ActorTab[] = ['overview', 'snapshots', 'logs', 'egress', 'spec']

function TabBody({ children }: { children: ReactNode }) {
  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto max-w-5xl space-y-4 px-6 py-4 phone:px-4">{children}</div>
    </div>
  )
}

type SnapshotRow = NonNullable<SubstrateActorDetail['actor']['snapshots']>[number]

/**
 * Where a snapshot's state is kept: a durable copy in object storage, which
 * outlives the node, or files on one node only. Substrate records no upload
 * time, so none is shown.
 */
export function snapshotWhere(s: Pick<SnapshotRow, 'durable' | 'locality'>): { label: string; tone: 'healthy' | 'degraded'; detail: string; node?: string } {
  switch (s.durable) {
    case 'completed':
      return s.locality
        ? { label: 'Durable copy', tone: 'healthy', detail: 'Uploaded to object storage. Recorded locality:', node: s.locality }
        : { label: 'Durable copy', tone: 'healthy', detail: 'Uploaded to object storage.' }
    case 'pending':
      return s.locality
        ? { label: 'Only on its node', tone: 'degraded', detail: 'Upload pending. Recorded locality:', node: s.locality }
        : { label: 'Upload pending', tone: 'degraded', detail: 'Substrate names no node holding its files.' }
    default:
      return s.locality
        ? { label: 'Only on its node', tone: 'degraded', detail: 'No durable copy recorded. Recorded locality:', node: s.locality }
        : { label: 'Not stored', tone: 'degraded', detail: 'No durable copy or node locality recorded.' }
  }
}

/** Generations below the newest that the list doesn't hold. */
export function unlistedGenerations(rows: Pick<SnapshotRow, 'generation'>[]): number {
  if (rows.length === 0) return 0
  const newest = Math.max(...rows.map((r) => r.generation))
  return newest - new Set(rows.map((r) => r.generation)).size
}

function SnapshotsTab({ detail, onOpenSubject }: { detail: SubstrateActorDetail; onOpenSubject: OpenSubject }) {
  const rows = [...(detail.actor.snapshots ?? [])].sort((a, b) => b.generation - a.generation)
  const source = rows.find((s) => s.generation === detail.row.resumeGeneration)
  const place = source && snapshotWhere(source)
  const columns: TableColumn<SnapshotRow>[] = [
    { header: 'Generation', width: '100px', cell: (s) => <span className="font-mono">{s.generation}</span> },
    {
      header: 'Holds',
      cell: (s) => fidelityLabel(s.fidelity),
    },
    {
      header: 'Where it is kept',
      width: '260px',
      cell: (s) => {
        const w = snapshotWhere(s)
        return (
          <span className="block min-w-0">
            <span className={w.tone === 'healthy' ? undefined : toneTextClass(w.tone)}>{w.label}</span>
            <span className="block text-xs text-theme-text-tertiary">{w.detail}</span>
            {w.node && <SubjectLink subject={{ kind: 'Node', name: w.node }} onOpen={onOpenSubject} withKind={false} />}
          </span>
        )
      },
    },
    { header: 'Taken by', width: '100px', cell: (s) => (s.owner === 'tag' ? <Tooltip content="A tag’s copy, such as the template’s golden snapshot."><span>a tag</span></Tooltip> : s.owner === 'actor' ? 'this actor' : <Unread>unknown</Unread>) },
    { header: 'Sandbox class', width: '120px', cell: (s) => (s.sandboxClass ? <span className="font-mono text-[12.5px]">{s.sandboxClass}</span> : <Unread>unknown</Unread>) },
  ]
  const unlisted = unlistedGenerations(rows)
  return (
    <TabBody>
      <WorkspaceCard title="Resume source">
        <FactGrid>
          <FactRow label="Generation">
            <DimensionValue tone={place?.tone ?? 'neutral'} source="Substrate’s recorded resume choice on the current template">
              {detail.row.resumeGeneration === undefined ? 'Fresh boot' : `Generation ${detail.row.resumeGeneration}`}
              {source && <> · {fidelityLabel(source.fidelity)} · {place?.label}</>}
            </DimensionValue>
          </FactRow>
          <FactRow label="What returns">{resumeConditionSentence(detail.row.resumesWith, detail.row.resumeCondition)}</FactRow>
          {place?.node && <FactRow label="Recorded locality"><SubjectLink subject={{ kind: 'Node', name: place.node }} onOpen={onOpenSubject} withKind={false} /></FactRow>}
        </FactGrid>
      </WorkspaceCard>
      <SectionTable
        title="Snapshots"
        subtitle={
          <>
            Newest recorded generation first.
            {unlisted > 0 && <> Substrate’s record of an actor lists only its active and in-progress generations, so {unlisted === 1 ? '1 earlier generation is' : `${unlisted} earlier generations are`} not listed.</>}
          </>
        }
        columns={columns}
        rows={rows}
        rowKey={(s) => String(s.generation)}
        empty="No snapshots yet: the actor has not suspended, so it would boot fresh."
        minWidth={760}
      />
    </TabBody>
  )
}

/**
 * The actor's log lines with a source switch: container output or the
 * worker's lifecycle events for it. From its current worker Pod, or, with no
 * worker, candidates from the worker Pods on the node it last ran on; when
 * neither holds them, what is missing and how to look further.
 */
function LogsTab({ detail }: { detail: SubstrateActorDetail }) {
  const [view, setView] = useState<SubstrateLogSource>('all')
  const [container, setContainer] = useState('')
  const containers = detail.template?.containers ?? []
  const filtered = hidesWorkerRecords(view, container)
  const { actor } = detail
  const logs = useSubstrateActorLogs(actor, {
    source: view,
    container: view === 'lifecycle' || view === 'worker' ? undefined : container || undefined,
    tailLines: 500,
    pollMs: actor.assignment?.pod && actor.assignment.namespace ? undefined : false,
  })
  const data = definitiveFailure(substrateErrorCode(logs.error)) ? undefined : logs.data
  if (!data || !workerLogsAvailable(data)) {
    const title = logs.isLoading ? 'Worker logs' : 'Worker logs unavailable'
    return <TabBody><WorkspaceCard title={title}><ActorLogsBody actor={detail.actor} logs={logs} /></WorkspaceCard></TabBody>
  }
  // The server weighs a memory-limit kill on every source, so a view that leaves out the worker's records still knows of one.
  const hiddenKill = filtered ? data?.memoryKill : undefined
  return (
    <TabBody>
      <div className="flex flex-wrap items-center gap-3">
        <Segments
          label="Log source"
          value={view}
          onChange={setView}
          options={[
            { id: 'all', label: 'All' },
            { id: 'containers', label: 'Container output' },
            { id: 'lifecycle', label: 'Lifecycle events' },
            { id: 'worker', label: 'Worker process records' },
          ]}
        />
        {(view === 'all' || view === 'containers') && containers.length > 1 && (
          <label className="flex items-center gap-2 text-xs text-theme-text-secondary">
            Container
            <select value={container} onChange={(e) => setContainer(e.target.value)} className="rounded border border-theme-border bg-theme-base px-1.5 py-0.5 font-mono text-xs text-theme-text-primary">
              <option value="">every container</option>
              {containers.map((c) => <option key={c} value={c}>{c}</option>)}
            </select>
          </label>
        )}
        {data?.origin === 'worker' && logs.dataUpdatedAt > 0 && (
          <span className="inline-flex items-center gap-1.5 text-xs text-theme-text-secondary">
            <StatusDot tone="healthy" />
            Live: re-read while this tab is open, newest at the bottom · <When iso={new Date(logs.dataUpdatedAt).toISOString()} prefix="read" />
          </span>
        )}
      </div>
      {hiddenKill && <HiddenKillNotice kill={hiddenKill} crashTime={detail.actor.state === 'crashed' ? detail.actor.crash?.time : undefined} onShowAll={() => { setView('all'); setContainer('') }} />}
      <ActorLogsBody actor={detail.actor} logs={logs} killNoticeAbove={!!hiddenKill} />
    </TabBody>
  )
}

/** A log view that leaves out the worker's own records, where a memory-limit kill is written. */
export function hidesWorkerRecords(view: SubstrateLogSource, container: string): boolean {
  return view === 'containers' || view === 'lifecycle' || (view === 'all' && !!container)
}

/** Says a filter hides the kill record rather than changing the filter for the reader. */
export function HiddenKillNotice({ kill, crashTime, onShowAll }: { kill: Pick<NonNullable<SubstrateActorLogs['memoryKill']>, 'current' | 'time'>; crashTime?: string; onShowAll: () => void }) {
  // A kill tied to this crash says what ties it: its timing.
  const tie = killTie(kill, crashTime)
  return (
    <p className="text-xs text-theme-text-secondary">
      This view hides the worker’s record of a memory-limit kill{kill.current ? `, ${tie ?? 'tied to this crash'}` : ' attempt whose tie to the crash is unconfirmed'}.{' '}
      <button type="button" onClick={onShowAll} className="font-medium text-accent-text hover:underline">Show all sources</button>
    </p>
  )
}

function EgressTab({ detail }: { detail: SubstrateActorDetail }) {
  const e = detail.egress
  const t = egressText(e)
  return (
    <TabBody>
      <WorkspaceCard title="Outbound policy">
        <DimensionValue tone={t.tone} source="Actor egress policy from Substrate">
          {e.state === 'set' && e.rules > 0 ? `${e.rules} ${e.rules === 1 ? 'rule' : 'rules'}` : t.text}
        </DimensionValue>
        {e.state === 'set' && e.destinations && e.destinations.length > 0 && (
          <>
            <div className="mt-3 text-xs text-theme-text-tertiary">Destinations from policy</div>
            <ul className="mt-1 flex flex-wrap gap-1.5">
              {e.destinations.map((d) => <li key={d}><Badge tone="structural" size="sm">{d}</Badge></li>)}
              {e.more ? <li className="text-xs text-theme-text-tertiary">and {e.more} more</li> : null}
            </ul>
          </>
        )}
      </WorkspaceCard>
    </TabBody>
  )
}

function SpecTab({ detail, onOpenSubject }: { detail: SubstrateActorDetail; onOpenSubject: OpenSubject }) {
  const t = detail.template
  const selectorText = (sel?: Record<string, string>) =>
    sel && Object.keys(sel).length ? <span className="font-mono">{Object.entries(sel).map(([k, v]) => `${k}=${v}`).join(', ')}</span> : <span className="text-theme-text-secondary">none</span>
  const pools = detail.matchingPools
  const workspace = useSubstrateWorkspace()
  return (
    <TabBody>
      <WorkspaceCard title="Template configuration">
        <FactGrid>
          <FactRow label="Template"><SubjectLink subject={{ kind: 'ActorTemplate', ...detail.actor.template }} onOpen={onOpenSubject} withKind={false} /></FactRow>
          {t ? (<>
            <FactRow label="CPU">{t.cpuMillicores ? cpuText(t.cpuMillicores) : <Unread>not set</Unread>}</FactRow>
            <FactRow label="Memory">{t.memoryBytes ? bytesText(t.memoryBytes) : <Unread>not set</Unread>}</FactRow>
            <FactRow label="Containers">{t.containers?.length ? <span className="font-mono">{t.containers.join(', ')}</span> : <Unread>not listed</Unread>}</FactRow>
            <FactRow label="Snapshots up to">{fidelityLabel(t.preferredFidelity)}</FactRow>
            <FactRow label="Stored in">{t.storageLocation ? <span className="font-mono">{t.storageLocation}</span> : 'the API server’s default location'}</FactRow>
          </>) : (
            <FactRow label="Configuration"><Unread>Substrate’s templates were not read.</Unread></FactRow>
          )}
        </FactGrid>
      </WorkspaceCard>
      <WorkspaceCard title="Placement rules">
        <FactGrid>
          <FactRow label="Actor’s own selector">{selectorText(detail.actor.workerSelector)}</FactRow>
          <FactRow label="Template’s selector">{t ? selectorText(t.workerSelector) : <Unread>unknown: templates were not read</Unread>}</FactRow>
          <FactRow label="Pools matching both">
            {pools === null ? (
              <Unread title={detail.matchingPoolsComplete ? undefined : 'None of the pools Radar could read matches; others may.'}>unknown</Unread>
            ) : pools.length === 0 ? (
              <span className="font-medium">none</span>
            ) : (
              <>
                {!detail.matchingPoolsComplete && <span>At least these pools match</span>}
                <PoolMatchLinks pools={pools} workspace={workspace.data} />
              </>
            )}
            <div className="text-xs text-theme-text-tertiary">Matching labels, not a promise it fits: CPU, memory and its snapshot’s runtime decide that.</div>
          </FactRow>
        </FactGrid>
      </WorkspaceCard>
      <WorkspaceCard>
        <FoldSection title="Raw actor JSON" summary="Substrate API record" attention={false}>
          <CodeViewer code={JSON.stringify(detail.actor, null, 2)} language="json" showLineNumbers={false} maxHeight="32rem" />
        </FoldSection>
      </WorkspaceCard>
    </TabBody>
  )
}

/** The actor page's or drawer's read failed: Substrate has no such actor, denied it, or the tier is down. */
export function ActorReadFailure({ actor, error, onBack, backLabel = 'Go to Actors' }: { actor: SubstrateRef; error: unknown; onBack: () => void; backLabel?: string }) {
  const code = substrateErrorCode(error)
  const tier = code && code !== 'notFound' && code !== 'denied' ? tierReading({ state: 'failed', code, reason: error instanceof Error ? error.message : undefined }) : undefined
  const title = code === 'notFound' ? `Substrate has no actor ${actor.name}` : code === 'denied' ? `Substrate denied reading ${actor.name}` : tier?.title ?? `${actor.name} couldn’t be read`
  const detail = code === 'notFound'
    ? `There is no actor ${actor.name} in atespace ${actor.atespace}. It may have been deleted since this link was made.`
    : tier
      ? `${tier.detail}${tier.nextStep ? ` ${tier.nextStep}` : ''}`
      : error instanceof Error
        ? error.message
        : 'Radar could not read the actor.'
  return (
    <ScreenEmptyState
      icon={Bot}
      title={title}
      detail={detail}
      action={<button type="button" onClick={onBack} className="btn-secondary mt-3 px-3 py-1.5 text-sm">{backLabel}</button>}
    />
  )
}

/**
 * An actor's page: the crumb (Agent Substrate / Actors / atespace /), the
 * Overview first, then its snapshots, logs (container output and lifecycle events), egress
 * policy and spec. `ctx` pins the Kubernetes context the link was made in.
 */
export function SubstrateActorPage({ actor, onOpenSubject }: { actor: SubstrateRef; onOpenSubject: OpenSubject }) {
  const location = useLocation()
  const navigate = useWorkspaceNavigate()
  const [params, setParams] = useSearchParams()
  const pin = useContextPin()
  const rawTab = params.get('tab') as ActorTab | null
  const tab: ActorTab = rawTab && TABS.includes(rawTab) ? rawTab : 'overview'
  const query = useSubstrateActor(pin.mismatch ? null : actor)
  // A refresh that says the actor is gone or no longer readable replaces what was kept.
  const detail = definitiveFailure(substrateErrorCode(query.error)) ? undefined : query.data

  const setTab = (id: ActorTab) => {
    const p = new URLSearchParams(params)
    if (id === 'overview') p.delete('tab')
    else p.set('tab', id)
    setParams(p, { replace: true, state: location.state })
  }

  if (pin.mismatch) {
    return <NotInContext name={actor.name} what={`actor ${actor.atespace} / ${actor.name}`} pinnedContext={pin.pinned!} activeContext={pin.active} />
  }

  const breadcrumb = (
    <nav aria-label="Location" className="flex flex-wrap items-center gap-1.5 text-sm text-theme-text-tertiary">
      <Bot className="h-3.5 w-3.5" />
      <button type="button" onClick={() => navigate(SUBSTRATE_PATH)} className="hover:text-theme-text-primary hover:underline">Agent Substrate</button>
      <span>/</span>
      <button type="button" onClick={() => navigate(SUBSTRATE_SCREENS.actors.path)} className="hover:text-theme-text-primary hover:underline">Actors</button>
      <span>/</span>
    </nav>
  )
  const identity = (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
      {breadcrumb}
      <h1 className="min-w-0 truncate font-mono text-lg font-semibold text-theme-text-primary">{actor.name}</h1>
      {detail && <ActorStateBadge state={detail.actor.state} impact={detail.row.workerImpact} updateTime={detail.actor.updateTime} />}
      <span className="text-sm text-theme-text-secondary"><Term term="atespace">Atespace</Term>: <span className="font-mono">{actor.atespace}</span></span>
    </div>
  )
  const snapshots = detail?.actor.snapshots?.length
  const tabs: DetailShellTab<ActorTab>[] = [
    { id: 'overview', label: 'Overview', icon: <LayoutDashboard className="h-4 w-4" /> },
    { id: 'snapshots', label: 'Snapshots', icon: <Camera className="h-4 w-4" />, badge: snapshots ? <span className="font-mono text-xs text-theme-text-tertiary">{snapshots}</span> : undefined },
    { id: 'logs', label: 'Logs', icon: <ScrollText className="h-4 w-4" /> },
    { id: 'egress', label: 'Egress', icon: <Globe className="h-4 w-4" /> },
    { id: 'spec', label: 'Spec', icon: <FileCode className="h-4 w-4" /> },
  ]
  let body: ReactNode
  if (!detail && query.isLoading) body = <PaneLoader label={`Loading ${actor.name}…`} className="h-full" />
  else if (!detail) body = <ActorReadFailure actor={actor} error={query.error} onBack={() => navigate(SUBSTRATE_SCREENS.actors.path)} />
  else if (tab === 'snapshots') body = <SnapshotsTab detail={detail} onOpenSubject={onOpenSubject} />
  else if (tab === 'logs') body = <LogsTab detail={detail} />
  else if (tab === 'egress') body = <EgressTab detail={detail} />
  else if (tab === 'spec') body = <SpecTab detail={detail} onOpenSubject={onOpenSubject} />
  else body = <TabBody><SubstrateActorOverview detail={detail} onOpenSubject={onOpenSubject} onShowLogs={() => setTab('logs')} onShowSnapshots={() => setTab('snapshots')} onShowEgress={() => setTab('egress')} /></TabBody>

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col">
      <DetailShell
        identity={identity}
        headerActions={
          <Tooltip content="Refresh" delay={150} position="bottom">
            <button type="button" onClick={() => void query.refetch()} disabled={query.isFetching} aria-label="Refresh" className="mt-0.5 rounded p-1.5 text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary disabled:opacity-50">
              <RefreshCw className="h-5 w-5" />
            </button>
          </Tooltip>
        }
        tabs={tabs}
        activeTab={tab}
        onTabChange={setTab}
      >
        <RefreshFailedNotice queries={[query]} className="px-4 pt-3 xl:px-6" />
        {body}
      </DetailShell>
    </div>
  )
}
