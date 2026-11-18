import type { ReactNode } from 'react'
import { Boxes } from 'lucide-react'
import type { UseQueryResult } from '@tanstack/react-query'
import { PaneLoader, RadarUpgradeAction, getRadarUpgradeRequirement, radarUpgradeDetail, radarUpgradeHeadline, type StatusTone, type WorkspaceProblem } from '@skyhook-io/k8s-ui'
import { formatDuration, summarizeSchedulerMessage } from '@skyhook-io/k8s-ui/components/resources/resource-utils'
import { gapReasonText } from '@skyhook-io/k8s-ui/utils/read-actions'
import type {
  AgentSandboxAttention,
  AgentSandboxController,
  AgentSandboxCount,
  AgentSandboxRelation,
  AgentSandboxWorkspaceKey,
  AgentSandboxWorkspaceResponse,
} from '../../api/agentSandbox'
import { useConnection } from '../../context/ConnectionContext'
import { INCOMPLETE_COVERAGE_STATES, coverageLabel } from '../workspace/coverage'
import { Notice, RefreshFailedNotice, ScreenEmptyState } from '../workspace/layout'
import { KIND_LABEL } from './shared'

// Presentation of GET /api/agentsandbox/workspace. The server decides every
// reading, row and count; this file only words them.

/** A row's severity as the health tone the problem components color by. */
export function severityTone(s: AgentSandboxAttention['severity']): 'unhealthy' | 'alert' | 'degraded' {
  return s === 'critical' ? 'unhealthy' : s === 'alert' ? 'alert' : 'degraded'
}

// Why the controller Deployment was not read, from its pkg/readreason code.
const CONTROLLER_UNREAD: Record<string, string> = {
  user_forbidden: 'no access',
  outside_namespace_pin: 'outside Radar’s namespace scope',
  radar_namespace_not_collected: 'not collected by Radar',
  radar_kind_not_collected: 'not collected by Radar',
  kind_sync_pending: 'still loading',
  kind_sync_timed_out: 'loading is overdue',
  read_error: 'couldn’t be read',
}

/** Radar's Issues raise an in-progress state after this (pkg/agentsandbox IncidentAfter). */
export const ISSUE_AFTER_MINUTES = 30

const sentenceCase = (t: string) => (t ? `${t[0].toUpperCase()}${t.slice(1)}` : t)

/**
 * How long a row's state has lasted, beside its title, from the server's
 * `since`. For most rules `since` is the latest condition transition Radar
 * can see, and a condition keeps its transition time when only its reason
 * changes, so the state began no earlier: the age is a ceiling, shown "≤1d".
 * The controller's row is a real status flip, so its age is exact. Nothing
 * without `since`; creation time would claim more than Radar knows.
 */
export function attentionAge(a: AgentSandboxAttention, now = Date.now()): { text: string; tip: string } | undefined {
  const at = a.since ? Date.parse(a.since) : NaN
  if (!Number.isFinite(at) || now <= at) return undefined
  const d = formatDuration(now - at)
  if (a.rule === 'controllerNotRunning') return { text: d, tip: `Not running for ${d}, since its Available condition turned False.` }
  return {
    text: `≤${d}`,
    tip: `Began at most ${d} ago. Radar sees when the condition last changed; Kubernetes keeps that time when only the reason changes, so this state may have begun later.`,
  }
}

/** A row's title: the server's, which carries no elapsed time. */
export function attentionTitle(a: AgentSandboxAttention): string {
  return a.title
}

/**
 * The reading in plain words: the server's reading, the scheduler's verdict
 * put plainly when it is the source, and for a slow state that it is an
 * advisory Radar's Issues don't raise yet.
 */
export function attentionDetail(a: AgentSandboxAttention): string | undefined {
  const parts: string[] = []
  if (a.reading) parts.push(a.reading)
  if (a.rawDetailSource === 'scheduler' && a.rawDetail) {
    const plain = summarizeSchedulerMessage(a.rawDetail, { plain: true })
    if (plain) parts.push(`Scheduler: ${plain}.`)
  }
  if (a.rule === 'slow') parts.push(`Radar’s Issues raise it after ${ISSUE_AFTER_MINUTES} minutes.`)
  return parts.length ? parts.join(' ') : undefined
}

/** The row as a WorkspaceProblem, so ProblemMeta names its subject and offers "See in Issues" for `issue` rows. */
export function attentionProblem(a: AgentSandboxAttention): WorkspaceProblem {
  return {
    id: a.id,
    category: a.category,
    title: attentionTitle(a),
    detail: attentionDetail(a),
    rawDetail: a.rawDetail,
    rawDetailLabel: a.rawDetailLabel,
    nextStep: a.nextStep,
    subject: { kind: a.subject.kind, group: a.subject.group, namespace: a.subject.namespace ?? '', name: a.subject.name },
    related: a.related ? { lead: a.related.lead, refs: a.related.refs.map((r) => ({ ...r, namespace: r.namespace ?? '' })) } : undefined,
    ...(a.issue ? { source: 'issue' as const } : { source: 'reading' as const }),
    tone: severityTone(a.severity),
  }
}

const ROLE_LABEL: Record<AgentSandboxRelation['role'], string> = { warmPool: 'Warm pool', template: 'Template', sandbox: 'Sandbox', owner: 'Owner' }

export function relationLabel(r: AgentSandboxRelation): string {
  if (r.role === 'owner') return r.ref.kind === 'SandboxWarmPool' ? 'Warm pool' : r.ref.kind === 'SandboxClaim' ? 'Claim' : r.ref.kind
  return ROLE_LABEL[r.role]
}

/** What a relation's presence says: absent only where its kind was read in full there. */
export const PRESENCE_TEXT: Record<AgentSandboxRelation['presence'], string | undefined> = { present: undefined, absent: 'not found', unread: 'not read' }

// The controller's since is its Available=False transition, a real status
// flip, so its age is exact.
const withHeld = (text: string, since: string | undefined, now = Date.now()) => {
  const at = since ? Date.parse(since) : NaN
  return Number.isFinite(at) && now > at ? `${text} for ${formatDuration(now - at)}` : text
}

export function controllerValue(c: AgentSandboxController): { value: string; tone: StatusTone; unknown: boolean } {
  switch (c.state) {
    case 'running':
      return { value: 'running', tone: 'healthy', unknown: false }
    case 'notReady':
      return { value: withHeld('not ready', c.since), tone: 'unhealthy', unknown: false }
    case 'scaledToZero':
      return { value: withHeld('scaled to zero', c.since), tone: 'unhealthy', unknown: false }
    case 'notInstalled':
      return { value: 'not installed', tone: 'neutral', unknown: false }
    case 'notFound':
      return { value: `unknown — not found as ${c.namespace}/${c.name}`, tone: 'unknown', unknown: true }
  }
  const why = c.code ? CONTROLLER_UNREAD[c.code] ?? gapReasonText(c.code) : 'not read'
  return { value: `unknown — ${why}`, tone: 'unknown', unknown: true }
}

/** "unknown — no access" for a kind the summary left out; "not installed" for one whose CRD isn't served. */
export function leftOutText(data: AgentSandboxWorkspaceResponse, key: AgentSandboxWorkspaceKey): string {
  const cov = data.coverage[key]
  if (cov?.state === 'notInstalled') return 'not installed'
  return `unknown — ${coverageLabel(cov)}`
}

/** A badge from the server's count: "≥N" for a lower bound, the unknown dash for a lower-bound zero. */
export function sidebarCount(c: AgentSandboxCount, noun: [string, string], controller = false): { count: number | null; lowerBound: boolean; title: string } {
  if (c.count === null) {
    return { count: null, lowerBound: true, title: `Unknown: Radar couldn’t read every ${noun[0]}, so nothing found doesn’t mean nothing needs attention.` }
  }
  const shared = controller ? ' One is the controller’s row, which every namespace shares.' : ''
  const partial = c.lowerBound ? ' Some couldn’t be read, so there may be more.' : ''
  return {
    count: c.count,
    lowerBound: c.lowerBound,
    title: `${c.lowerBound ? 'At least ' : ''}${c.count} ${c.count === 1 ? `${noun[0]} needs` : `${noun[1]} need`} attention.${partial}${shared}`,
  }
}

/** Says which kinds the summary read only in part, and why. */
export function WorkspaceCoverageNotice({ data }: { data: AgentSandboxWorkspaceResponse }) {
  const gaps = (['sandboxes', 'claims', 'warmPools', 'templates'] as const).flatMap((k) => {
    const cov = data.coverage[k]
    return cov && INCOMPLETE_COVERAGE_STATES.has(cov.state) ? [`${KIND_LABEL[k]} (${coverageLabel(cov)})`] : []
  })
  const controller = controllerValue(data.controller)
  if (controller.unknown) gaps.push(`the controller Deployment (${controller.value.replace(/^unknown — /, '')})`)
  if (gaps.length === 0 && !data.forcedNamespace) return null
  return (
    <Notice>
      {data.forcedNamespace && <>Radar is limited to namespace {data.forcedNamespace}. </>}
      {gaps.length > 0 && <>Radar couldn’t read all of Agent Sandbox here: {gaps.join('; ')}. What it couldn’t read shows as unknown, and the counts that rest on it as lower bounds.</>}
    </Notice>
  )
}

type WorkspaceQuery = Pick<UseQueryResult<AgentSandboxWorkspaceResponse>, 'data' | 'error' | 'isLoading' | 'isFetching' | 'refetch' | 'isRefetchError' | 'dataUpdatedAt'>

/** Loading, needs-newer-Radar, error and not-installed states of the Overview, as Velero's gate. */
export function AgentSandboxWorkspaceGate({
  query,
  discoveryPending,
  discovered,
  children,
}: {
  query: WorkspaceQuery
  discoveryPending: boolean
  discovered: boolean
  children: (data: AgentSandboxWorkspaceResponse) => ReactNode
}) {
  const { connection } = useConnection()
  const data = query.data
  const notInstalled = <ScreenEmptyState icon={Boxes} title="Agent Sandbox is not installed" detail={`No agents.x-k8s.io resources are served in ${connection.context || 'this cluster'}.`} />
  if (!data && (query.isLoading || discoveryPending)) return <PaneLoader label="Loading Agent Sandbox…" className="flex-1" />
  if (!data && !discovered) return notInstalled
  if (!data) {
    const upgrade = getRadarUpgradeRequirement(query.error)
    if (upgrade) {
      return (
        <ScreenEmptyState
          icon={Boxes}
          title={radarUpgradeHeadline('The Agent Sandbox overview')}
          detail={radarUpgradeDetail(upgrade)}
          action={
            <div className="mt-3 text-sm">
              <RadarUpgradeAction requirement={upgrade} />
              <p className="mt-2 text-theme-text-tertiary">The Sandboxes, Claims and Warm pools lists work on this Radar.</p>
            </div>
          }
        />
      )
    }
    return (
      <ScreenEmptyState
        icon={Boxes}
        title="Agent Sandbox data unavailable"
        detail={query.error instanceof Error ? sentenceCase(query.error.message) : 'Radar could not load the Agent Sandbox overview.'}
        action={
          <button type="button" onClick={() => void query.refetch()} disabled={query.isFetching} className="btn-secondary mt-3 px-3 py-1.5 text-sm">
            {query.isFetching ? 'Retrying…' : 'Retry'}
          </button>
        }
      />
    )
  }
  if (!data.installed) return notInstalled
  return (
    <>
      <RefreshFailedNotice queries={[query]} className="px-5 pt-3 xl:px-7" />
      {children(data)}
    </>
  )
}
