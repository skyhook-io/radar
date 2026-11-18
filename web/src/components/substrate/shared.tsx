import type { ReactNode } from 'react'
import type { UseQueryResult } from '@tanstack/react-query'
import { Bot, Info } from 'lucide-react'
import {
  Badge,
  PaneLoader,
  RadarUpgradeAction,
  StatusDot,
  Tooltip,
  getRadarUpgradeRequirement,
  radarUpgradeDetail,
  radarUpgradeHeadline,
  toneTextClass,
  type StatusTone,
} from '@skyhook-io/k8s-ui'
import type {
  SubstrateAPITier,
  SubstrateActorState,
  SubstrateSnapshotCoverage,
  SubstratePoolsCoverage,
  SubstrateStatus,
  SubstrateWorkerImpact,
  SubstrateWorkerRow,
} from '../../api/substrateWorkspace'
import { useConnection } from '../../context/ConnectionContext'
import { CopyButton } from '../diagnose/AgentSetupNotice'
import { usePrometheusStatus } from '../../api/client'
import { BreakText, Notice, RefreshFailedNotice, ScreenEmptyState } from '../workspace/layout'
import { INCOMPLETE_COVERAGE_STATES, coverageLabel } from '../workspace/coverage'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { ACTOR_STATE_LABEL, actorStateTone, coverageInPart, coverageReason, localTime, relativeTime, tierReading, toneSeverity, workerStateReading } from './readings'
import { SUBSTRATE_PATH } from './routes'

/** Substrate's terms, defined where they first appear on a screen. */
export const SUBSTRATE_TERMS = {
  actor: 'An agent’s sandboxed process. Substrate runs it on a worker while it works and suspends it to a snapshot when it is idle. Actors live in Substrate’s API, not in Kubernetes.',
  atespace: 'Substrate’s tenancy boundary for actors and templates. Like a namespace, but kept by Substrate, not Kubernetes.',
  worker: 'A worker Pod of a WorkerPool. Substrate places actors on workers; each takes a limited number of actors (its slots).',
  pool: 'A WorkerPool: the Kubernetes object that runs worker Pods of one sandbox class.',
  template: 'An ActorTemplate: the image, resources and snapshot settings every new actor of it starts from.',
  golden: 'The snapshot Substrate takes of a template once it has warmed up. New actors start from it; without it they boot cold.',
  slot: 'Room for one actor on a worker. A worker’s actor limit is its number of slots.',
  snapshot: 'A saved copy of an actor’s state, taken when it suspends. The actor resumes from the newest one that is stored or still on its node.',
} as const

export type SubstrateTerm = keyof typeof SUBSTRATE_TERMS

/** A Substrate term with its definition on hover. */
export function Term({ term, children }: { term: SubstrateTerm; children: ReactNode }) {
  return (
    <Tooltip content={SUBSTRATE_TERMS[term]}>
      <span tabIndex={0} className="cursor-help underline decoration-dotted underline-offset-2">{children}</span>
    </Tooltip>
  )
}

export const IMPACT_LABEL: Record<SubstrateWorkerImpact['kind'], string> = {
  unavailable: 'worker unavailable',
  draining: 'worker draining',
  restarted: 'sandbox lost',
}

/**
 * An actor's state chip. Substrate's state, qualified when its worker is in
 * trouble: "Running · worker unavailable" in amber, since Substrate still
 * records it running while it may not be serving.
 */
export function ActorStateBadge({ state, impact }: { state: SubstrateActorState; impact?: SubstrateWorkerImpact }) {
  if (impact) {
    return (
      <Tooltip content={impact.reading}>
        <span><Badge severity={impact.kind === 'restarted' ? 'error' : 'warning'} size="sm" className="whitespace-nowrap">{ACTOR_STATE_LABEL[state] ?? state} · {IMPACT_LABEL[impact.kind]}</Badge></span>
      </Tooltip>
    )
  }
  return <Badge severity={toneSeverity(actorStateTone(state))} size="sm">{ACTOR_STATE_LABEL[state] ?? state}</Badge>
}

/** What the workspace is, in one line, at the top of its Overview. */
export const SUBSTRATE_WHAT_IT_IS = 'Agent Substrate runs stateful agent actors on worker Pods and suspends them to snapshots between runs.'

/** The crumb and title every Substrate screen starts with; `intro` is a line under it, `children` sit below (filters). */
export function SubstrateHeader({ title, intro, children }: { title: string; intro?: ReactNode; children?: ReactNode }) {
  const navigate = useWorkspaceNavigate()
  return (
    <div className="px-5 pt-4 xl:px-7">
      <div className="flex flex-wrap items-center gap-x-2">
        <nav aria-label="Location" className="flex items-center gap-1.5 text-sm text-theme-text-tertiary">
          <Bot className="h-3.5 w-3.5" />
          {title === 'Overview' ? 'Agent Substrate' : <button type="button" onClick={() => navigate(SUBSTRATE_PATH)} className="hover:text-theme-text-primary hover:underline">Agent Substrate</button>}
          <span>/</span>
        </nav>
        <h1 className="text-lg font-semibold text-theme-text-primary">{title}</h1>
      </div>
      {intro && <p className="mt-0.5 text-sm text-theme-text-secondary">{intro}</p>}
      {children && <div className="mt-2.5 flex flex-wrap items-center gap-2">{children}</div>}
    </div>
  )
}

function SourcePart({ tone, label, children }: { tone: StatusTone; label: string; children: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <StatusDot tone={tone} />
      <span className="text-theme-text-tertiary">{label}</span>
      <span className="text-theme-text-secondary">{children}</span>
    </span>
  )
}

/** Who Radar reads Substrate as, and the endpoint, in a few words. */
export function identityText(tier: SubstrateAPITier): string {
  return tier.identity?.mode === 'tokenFile' ? 'a token file' : tier.identity?.principal ?? 'the shared identity'
}

/**
 * Radar Hub, for an org owner: Radar reads as Substrate's shared client
 * identity, with a token minted with the owner's own Kubernetes access, and
 * there is nothing to connect.
 */
function SharedAsCallerIdentity() {
  return (
    <>
      as Substrate’s shared client identity (<span className="font-mono">ate-system/ate-client</span>), minted with your Kubernetes access
      <Tooltip
        content="ate-client is usually one of Substrate’s global owners, so it can see every atespace. Radar only reads with it: it lists actors, templates and workers and never changes them. The token is minted with your own Kubernetes access, so it works only while you may create tokens for ate-system/ate-client. This may change to reading with your own Substrate access."
        wrapperClassName="ml-1 align-middle"
      >
        <span tabIndex={0} aria-label="About this identity" className="inline-flex text-theme-text-tertiary hover:text-theme-text-secondary">
          <Info className="h-3.5 w-3.5" />
        </span>
      </Tooltip>
    </>
  )
}

/**
 * Where the screen's values come from: Substrate's API (endpoint, identity,
 * read age), Prometheus, and Kubernetes, which Radar always reads.
 */
export function SubstrateSourceLine({ tier, status }: { tier: SubstrateAPITier; status?: SubstrateStatus }) {
  const { data: prometheus } = usePrometheusStatus()
  const service = status?.apiService
  const endpoint = tier.endpoint ?? (service ? `${service.namespace}/${service.name}:${service.port}` : undefined)
  const reading = tierReading(tier)
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs" aria-label="Sources">
      {tier.state === 'connected' ? (
        <SourcePart tone="healthy" label="Substrate API">
          {tier.identity?.mode === 'sharedAsCaller' ? (
            <>
              {endpoint && <><span className="font-mono">{endpoint}</span> </>}
              <SharedAsCallerIdentity />
            </>
          ) : (
            <>
              {endpoint && <span className="font-mono">{endpoint}</span>} as{' '}
              <span className={tier.identity?.mode === 'tokenFile' ? undefined : 'font-mono'}>{identityText(tier)}</span>
            </>
          )}
          {tier.readAt && (
            <>
              {' · '}
              <Tooltip content={tier.readStartedAt ? `Read from ${localTime(tier.readStartedAt)} to ${localTime(tier.readAt)}: the counts were observed over that interval, not at one instant.` : `The read finished ${localTime(tier.readAt)}.`}>
                <span>read {relativeTime(tier.readAt)}</span>
              </Tooltip>
            </>
          )}
          {tier.refreshing && !tier.lastError && <> · refreshing…</>}
        </SourcePart>
      ) : tier.state === 'connecting' ? (
        <SourcePart tone="unknown" label="Substrate API">connecting…</SourcePart>
      ) : (
        <SourcePart tone={reading.tone} label="Substrate API">{reading.title.toLowerCase()}</SourcePart>
      )}
      {tier.lastError && (
        <Tooltip content={tier.lastError.message}>
          <span className={toneTextClass('degraded')}>last read failed {relativeTime(tier.lastError.at)}: showing the one before</span>
        </Tooltip>
      )}
      <SourcePart tone={prometheus?.connected ? 'healthy' : 'unknown'} label="Prometheus">
        {prometheus === undefined ? 'unknown' : prometheus.connected ? 'connected' : 'not connected'}
      </SourcePart>
      <SourcePart tone="healthy" label="Kubernetes">WorkerPools and worker Pods</SourcePart>
    </div>
  )
}

const COVERAGE_NOUN: Record<keyof SubstrateSnapshotCoverage, string> = {
  actors: 'actors',
  templates: 'templates',
  workers: 'workers',
  atespaces: 'atespaces',
}

/** Says which kinds this screen reads were not read in full, and why. */
export function SubstrateCoverageNotice({
  coverage,
  kinds,
}: {
  coverage: Partial<SubstrateSnapshotCoverage> & { pools?: SubstratePoolsCoverage }
  kinds: readonly (keyof SubstrateSnapshotCoverage)[]
}) {
  const parts: string[] = []
  // An absent API-tier kind was not read because the tier is down; the tier banner says that.
  for (const k of kinds) {
    const cov = coverage[k]
    if (cov && coverageInPart(cov)) parts.push(`${COVERAGE_NOUN[k]}: ${coverageReason(cov, COVERAGE_NOUN[k])}`)
  }
  const pools = coverage.pools
  if (pools && INCOMPLETE_COVERAGE_STATES.has(pools.state)) parts.push(`WorkerPools: ${coverageLabel(pools)}`)
  if (parts.length === 0) return null
  return <Notice>Radar couldn’t read all of what this screen shows — {parts.join('; ')}. Counts over it are lower bounds, and what it couldn’t read shows as unknown.</Notice>
}

type GateQuery<T> = Pick<UseQueryResult<T>, 'data' | 'error' | 'isLoading' | 'isFetching' | 'refetch' | 'isRefetchError' | 'dataUpdatedAt'>

/**
 * Loading, error, needs-newer-Radar and not-installed states every Substrate
 * screen shares. Renders the screen only once its data is there.
 */
export function SubstrateScreenGate<T>({
  query,
  discoveryPending,
  discovered,
  label = 'Agent Substrate',
  children,
}: {
  query: GateQuery<T>
  discoveryPending: boolean
  /** Discovery serves ate.dev, or could not be read (the server is asked then). */
  discovered: boolean
  label?: string
  children: (data: T) => ReactNode
}) {
  const { connection } = useConnection()
  const data = query.data
  if (!data && (query.isLoading || discoveryPending)) return <PaneLoader label={`Loading ${label}…`} className="flex-1" />
  if (!data && !discovered) {
    return <ScreenEmptyState icon={Bot} title="Agent Substrate is not installed" detail={`No ate.dev resources are served in ${connection.context || 'this cluster'}.`} />
  }
  if (!data) {
    const upgrade = getRadarUpgradeRequirement(query.error)
    if (upgrade) {
      return (
        <ScreenEmptyState
          icon={Bot}
          title={radarUpgradeHeadline('Agent Substrate')}
          detail={radarUpgradeDetail(upgrade)}
          action={<div className="mt-3 text-sm"><RadarUpgradeAction requirement={upgrade} /></div>}
        />
      )
    }
    return (
      <ScreenEmptyState
        icon={Bot}
        title={`${label} unavailable`}
        detail={query.error instanceof Error ? query.error.message : `Radar could not load ${label}.`}
        action={
          <button type="button" onClick={() => void query.refetch()} disabled={query.isFetching} className="btn-secondary mt-3 px-3 py-1.5 text-sm">
            {query.isFetching ? 'Retrying…' : 'Retry'}
          </button>
        }
      />
    )
  }
  return <><RefreshFailedNotice queries={[query]} className="px-5 pt-3 xl:px-7" />{children(data)}</>
}

/** A value Radar could not read: its reason, never a zero. */
export function Unread({ children = 'Not connected', title }: { children?: ReactNode; title?: string }) {
  const body = <span className="text-theme-text-tertiary">{children}</span>
  return title ? <Tooltip content={title}><span>{body}</span></Tooltip> : body
}

/**
 * The window width every Substrate table shows all its columns at: the
 * Resources sidebar leaves the table about 1000px there. Below it each table
 * folds its secondary columns into the cell they qualify, so names keep their
 * room.
 */
export const SUBSTRATE_WIDE = '(min-width: 1400px)'

/**
 * An object's name in a table or card: mono and whole. Columns are sized so
 * names fit on one line; a longer one wraps after a hyphen, and only a single
 * segment wider than the column is cut (whole on hover).
 */
export function Name({ name, className }: { name: string; className?: string }) {
  // Wraps only after a hyphen, never mid-word: a generated name (a UUID) stays readable.
  return <BreakText value={name} after="-" className={`font-mono ${className ?? 'text-sm'}`} />
}

export { WholeNames } from '../velero/shared'

export const NOT_CONNECTED_TITLE = 'Kept in Substrate’s API, not Kubernetes. Radar reads it once it is connected to that API.'

/**
 * A time as this workspace shows every time: relative ("12m ago", "in 4m"),
 * with the local date and time on hover, after `hint` when there is one.
 */
export function When({ iso, hint, prefix }: { iso: string; hint?: string; prefix?: string }) {
  return (
    <Tooltip content={hint ? `${hint} ${localTime(iso)}.` : localTime(iso)}>
      <span className="whitespace-nowrap">{prefix ? `${prefix} ` : ''}{relativeTime(iso)}</span>
    </Tooltip>
  )
}

/** A shell command a next step names, as a code chip with a copy button. */
export function CommandChip({ command, inline }: { command: string; inline?: boolean }) {
  const chip = (
    <span className="inline-flex max-w-full items-center gap-1 rounded-md border border-theme-border bg-theme-elevated py-px pl-2 pr-0.5 align-middle">
      <code className="min-w-0 select-all whitespace-pre-wrap break-normal font-mono text-[11.5px] text-theme-text-primary">
        <Tokens text={command} />
      </code>
      <CopyButton text={command} label="Copy command" />
    </span>
  )
  // A command with <placeholders> is a template to fill in, not one to paste and run.
  const holes = [...new Set(command.match(/<[^<>\s]+>/g) ?? [])]
  const note = holes.length > 0 ? <span className="ml-1.5 text-[11px] text-theme-text-tertiary">fill in {holes.join(', ')}</span> : null
  return inline ? <>{chip}{note}</> : <div className="mt-1">{chip}{note}</div>
}

/** A worker's state chip, the same on Workers and on a pool's placement cards. */
export function WorkerStateChip({ w }: { w: Pick<SubstrateWorkerRow, 'state' | 'workerState'> }) {
  const r = workerStateReading(w)
  return (
    <Tooltip content={r.title}>
      <span className="max-w-full"><Badge severity={r.severity} size="sm" className="whitespace-normal text-left">{r.label}</Badge></span>
    </Tooltip>
  )
}

/**
 * Text whose words are identifiers (a command, a node or Pod name, "atespace /
 * name"): it wraps between words, never inside one, not even at a hyphen.
 */
export function Tokens({ text }: { text: string }) {
  return <>{text.split(/(\s+)/).map((part, i) => (/\s+/.test(part) ? part : <span key={i} className="inline-block whitespace-nowrap">{part}</span>))}</>
}
