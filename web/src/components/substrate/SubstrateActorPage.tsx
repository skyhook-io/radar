import { useState, type ReactNode } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import { Bot, Camera, FileCode, Globe, LayoutDashboard, RefreshCw, ScrollText } from 'lucide-react'
import { Badge, CodeViewer, DetailShell, FactGrid, FactRow, PaneLoader, SectionHeading, StatusDot, Tooltip, toneTextClass, type DetailShellTab } from '@skyhook-io/k8s-ui'
import {
  substrateErrorCode,
  useSubstrateActor,
  useSubstrateActorLogs,
  type SubstrateActorDetail,
  type SubstrateActorLogs,
  type SubstrateLogSource,
  type SubstrateRef,
} from '../../api/substrateWorkspace'
import { RefreshFailedNotice, ScreenEmptyState, Segments, SectionTable, type TableColumn } from '../workspace/layout'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { type OpenSubject } from './SubstrateAttentionList'
import { NotInContext, useContextPin } from './contextPin'
import { ActorLogsBody, SubstrateActorOverview, egressText } from './SubstrateActorOverview'
import { bytesText, cpuText, definitiveFailure, fidelityLabel, fidelityResume, logsCommand, tierReading } from './readings'
import { SUBSTRATE_PATH, SUBSTRATE_SCREENS, substrateActorsPath } from './routes'
import { ActorStateBadge, Term, Unread, When } from './shared'

export type ActorTab = 'overview' | 'snapshots' | 'logs' | 'egress' | 'spec'

const TABS: ActorTab[] = ['overview', 'snapshots', 'logs', 'egress', 'spec']

const CARD = 'rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm'

function TabBody({ children }: { children: ReactNode }) {
  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto max-w-5xl space-y-4 px-6 py-4 phone:px-4">{children}</div>
    </div>
  )
}

type SnapshotRow = NonNullable<SubstrateActorDetail['actor']['snapshots']>[number]

/** A node's hostname, whole: it truncates with the name on hover rather than breaking inside a label. */
function NodeName({ node }: { node: string }) {
  return (
    <Tooltip content={node} wrapperClassName="min-w-0 max-w-full">
      <span className="block truncate whitespace-nowrap font-mono text-[12.5px]">{node}</span>
    </Tooltip>
  )
}

/**
 * Where a snapshot's state is kept: a durable copy in object storage, which
 * outlives the node, or files on one node only. Substrate records no upload
 * time, so none is shown.
 */
export function snapshotWhere(s: Pick<SnapshotRow, 'durable' | 'locality'>): { label: string; tone: 'healthy' | 'degraded'; detail: string; node?: string } {
  switch (s.durable) {
    case 'completed':
      return s.locality
        ? { label: 'Durable copy', tone: 'healthy', detail: 'Uploaded to object storage. Its files are also on', node: s.locality }
        : { label: 'Durable copy', tone: 'healthy', detail: 'Uploaded to object storage.' }
    case 'pending':
      return s.locality
        ? { label: 'Only on its node', tone: 'degraded', detail: 'Upload pending. Until it finishes, its state is only on', node: s.locality }
        : { label: 'Upload pending', tone: 'degraded', detail: 'Substrate names no node holding its files.' }
    default:
      return s.locality
        ? { label: 'Only on its node', tone: 'degraded', detail: 'No durable copy recorded. Its files are only on', node: s.locality }
        : { label: 'Not stored', tone: 'degraded', detail: 'No durable copy recorded, and no node holds its files.' }
  }
}

/** Generations below the newest that the list doesn't hold. */
export function unlistedGenerations(rows: Pick<SnapshotRow, 'generation'>[]): number {
  if (rows.length === 0) return 0
  const newest = Math.max(...rows.map((r) => r.generation))
  return newest - new Set(rows.map((r) => r.generation)).size
}

function SnapshotsTab({ detail }: { detail: SubstrateActorDetail }) {
  const rows = [...(detail.actor.snapshots ?? [])].sort((a, b) => b.generation - a.generation)
  const columns: TableColumn<SnapshotRow>[] = [
    { header: 'Generation', width: '100px', cell: (s) => <span className="font-mono">{s.generation}</span> },
    {
      header: 'Holds',
      cell: (s) => (
        <>
          <span>{fidelityLabel(s.fidelity)}</span>
          <div className="text-xs text-theme-text-tertiary">{fidelityResume(s.fidelity)}</div>
        </>
      ),
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
            {w.node && <NodeName node={w.node} />}
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
      <SectionTable
        title={<Term term="snapshot">Snapshots</Term>}
        subtitle={
          <>
            Newest first. A resume restores from the newest one with a durable copy or files still on its node.
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
  const logs = useSubstrateActorLogs(detail.actor, { source: view, container: view === 'lifecycle' || view === 'worker' ? undefined : container || undefined, tailLines: 500 })
  // A view that leaves out the worker's records can hide a memory-limit kill; its own small read says whether it does.
  const workerRecords = useSubstrateActorLogs(filtered ? detail.actor : null, { source: 'worker', tailLines: 500 })
  const data = logs.data
  const hiddenKill = filtered && data && !data.memoryKill ? workerRecords.data?.memoryKill : undefined
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
            { id: 'worker', label: 'Worker records' },
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
      {hiddenKill && <HiddenKillNotice kill={hiddenKill} onShowAll={() => { setView('all'); setContainer('') }} />}
      <ActorLogsBody actor={detail.actor} logs={logs} />
      <p className="text-xs text-theme-text-tertiary">
        Lines from worker Pods that are gone are lost with them. While the actor runs, <span className="whitespace-nowrap font-mono">{logsCommand(detail.actor)}</span> shows the same lines.
      </p>
    </TabBody>
  )
}

/** A log view that leaves out the worker's own records, where a memory-limit kill is written. */
export function hidesWorkerRecords(view: SubstrateLogSource, container: string): boolean {
  return view === 'containers' || view === 'lifecycle' || (view === 'all' && !!container)
}

/** Says a filter hides the kill record rather than changing the filter for the reader. */
export function HiddenKillNotice({ kill, onShowAll }: { kill: Pick<NonNullable<SubstrateActorLogs['memoryKill']>, 'current'>; onShowAll: () => void }) {
  return (
    <p className="text-xs text-theme-text-secondary">
      This view hides the worker’s record of a memory-limit kill{kill.current ? ', the cause of this crash' : ' from an earlier run'}.{' '}
      <button type="button" onClick={onShowAll} className="font-medium text-accent-text hover:underline">Show all sources</button>
    </p>
  )
}

function EgressTab({ detail }: { detail: SubstrateActorDetail }) {
  const e = detail.egress
  const t = egressText(e)
  return (
    <TabBody>
      <section className={CARD}>
        <SectionHeading>Egress policy</SectionHeading>
        <p className={t.tone === 'unknown' ? 'text-sm text-theme-text-tertiary' : 'text-sm text-theme-text-primary'}>{e.state === 'set' ? `${e.rules} ${e.rules === 1 ? 'rule' : 'rules'}` : t.text}</p>
        {e.state === 'set' && e.destinations && e.destinations.length > 0 && (
          <>
            <div className="mt-3 text-xs text-theme-text-tertiary">Destinations the rules name</div>
            <ul className="mt-1 flex flex-wrap gap-1.5">
              {e.destinations.map((d) => <li key={d}><Badge tone="structural" size="sm">{d}</Badge></li>)}
              {e.more ? <li className="text-xs text-theme-text-tertiary">and {e.more} more</li> : null}
            </ul>
          </>
        )}
        <p className="mt-3 text-xs text-theme-text-tertiary">Substrate’s egress policy for this actor (kubectl ate get egresspolicy). Radar shows its rules’ destinations, not the full policy.</p>
      </section>
    </TabBody>
  )
}

function SpecTab({ detail }: { detail: SubstrateActorDetail }) {
  const t = detail.template
  const selectorText = (sel?: Record<string, string>) =>
    sel && Object.keys(sel).length ? <span className="font-mono">{Object.entries(sel).map(([k, v]) => `${k}=${v}`).join(', ')}</span> : <span className="text-theme-text-secondary">none</span>
  const pools = detail.matchingPools
  return (
    <TabBody>
      <section className={CARD}>
        <SectionHeading hint="from its template">What it runs with</SectionHeading>
        {t ? (
          <FactGrid>
            <FactRow label="Template"><span className="font-mono">{t.atespace} / {t.name}</span></FactRow>
            <FactRow label="CPU">{t.cpuMillicores ? cpuText(t.cpuMillicores) : <Unread>not set</Unread>}</FactRow>
            <FactRow label="Memory">{t.memoryBytes ? bytesText(t.memoryBytes) : <Unread>not set</Unread>}</FactRow>
            <FactRow label="Containers">{t.containers?.length ? <span className="font-mono">{t.containers.join(', ')}</span> : <Unread>not listed</Unread>}</FactRow>
            <FactRow label="Snapshots up to">{fidelityLabel(t.preferredFidelity)}</FactRow>
            <FactRow label="Stored in">{t.storageLocation ? <span className="font-mono">{t.storageLocation}</span> : 'the API server’s default location'}</FactRow>
            <FactRow label="Template’s selector">{selectorText(t.workerSelector)}</FactRow>
          </FactGrid>
        ) : (
          <p className="text-sm text-theme-text-tertiary">Substrate’s templates were not read, so what the actor runs with is unknown.</p>
        )}
      </section>
      <section className={CARD}>
        <SectionHeading hint="a worker must match both">Where it can be placed</SectionHeading>
        <FactGrid>
          <FactRow label="Actor’s own selector">{selectorText(detail.actor.workerSelector)}</FactRow>
          <FactRow label="Template’s selector">{t ? selectorText(t.workerSelector) : <Unread>unknown: templates were not read</Unread>}</FactRow>
          <FactRow label="Pools matching both">
            {pools === null ? (
              <Unread title={detail.matchingPoolsComplete ? undefined : 'None of the pools Radar could read matches; others may.'}>unknown</Unread>
            ) : pools.length === 0 ? (
              <span className="font-medium">none</span>
            ) : (
              <span className="font-mono">{pools.map((p) => p.name).join(', ')}</span>
            )}
            <div className="text-xs text-theme-text-tertiary">Matching labels, not a promise it fits: CPU, memory and its snapshot’s runtime decide that.</div>
          </FactRow>
        </FactGrid>
      </section>
      <section>
        <SectionHeading hint="as Substrate’s API returned it">Actor</SectionHeading>
        <CodeViewer code={JSON.stringify(detail.actor, null, 2)} language="json" showLineNumbers={false} maxHeight="32rem" />
      </section>
    </TabBody>
  )
}

/** The actor page's or drawer's read failed: Substrate has no such actor, denied it, or the tier is down. */
export function ActorReadFailure({ actor, error, onBack, backLabel = 'Go to Actors' }: { actor: SubstrateRef; error: unknown; onBack: () => void; backLabel?: string }) {
  const code = substrateErrorCode(error)
  // Not a failure: in Radar Hub only org owners read Substrate's API.
  const title = code === 'ownersOnly' ? 'Actors are for org owners' : code === 'notFound' ? `Substrate has no actor ${actor.name}` : code === 'denied' ? `Substrate denied reading ${actor.name}` : `${actor.name} couldn’t be read`
  const tier = code && code !== 'notFound' && code !== 'denied' ? tierReading({ state: 'failed', code, reason: error instanceof Error ? error.message : undefined }) : undefined
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
      <button type="button" onClick={() => navigate(substrateActorsPath({ atespace: actor.atespace }))} className="font-mono hover:text-theme-text-primary hover:underline">{actor.atespace}</button>
      <span>/</span>
    </nav>
  )
  const identity = (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
      <h1 className="min-w-0 truncate font-mono text-lg font-semibold text-theme-text-primary">{actor.name}</h1>
      <Badge tone="structural" size="sm">Actor</Badge>
      {detail && <ActorStateBadge state={detail.actor.state} impact={detail.workerImpact} updateTime={detail.actor.updateTime} />}
      <span className="text-sm text-theme-text-secondary"><Term term="atespace">atespace</Term> <span className="font-mono">{actor.atespace}</span></span>
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
  else if (tab === 'snapshots') body = <SnapshotsTab detail={detail} />
  else if (tab === 'logs') body = <LogsTab detail={detail} />
  else if (tab === 'egress') body = <EgressTab detail={detail} />
  else if (tab === 'spec') body = <SpecTab detail={detail} />
  else body = <TabBody><SubstrateActorOverview detail={detail} onOpenSubject={onOpenSubject} onShowLogs={() => setTab('logs')} /></TabBody>

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col">
      <DetailShell
        breadcrumb={breadcrumb}
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
