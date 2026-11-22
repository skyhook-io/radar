import { RadarUpgradeNote, SectionHeading } from '@skyhook-io/k8s-ui'
import { ApiError } from '../../api/client'
import { substrateErrorCode, useKagentAgentSessions, type KagentAgentRef, type KagentAgentSessions as Sessions, type SubstrateRef } from '../../api/substrateWorkspace'
import { useConnection } from '../../context/ConnectionContext'
import { Notice } from '../workspace/layout'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { KAGENT_OUTCOME, agentFilterValue, agentText, shortSessionId, tierReading } from '../substrate/readings'
import { stateCellReading } from '../substrate/SubstrateActors'
import { substrateActorPath, substrateActorsPath } from '../substrate/routes'
import { ActorStateBadge, When } from '../substrate/shared'
import { SubstrateStateBar } from '../substrate/SubstrateStateBar'

const SHORT = 12

function count(n: number, lowerBound: boolean): string {
  return `${lowerBound ? 'at least ' : ''}${n.toLocaleString('en-US')}`
}

/** What the Agent's current revisions say about where its new sessions run. */
function RevisionsText({ data }: { data: Sessions }) {
  const newest = data.revisions.find((r) => r.newSessions)
  const desired = data.revisions.find((r) => r.desired && !r.newSessions)
  if (!newest && !desired) {
    return <p className="text-xs text-theme-text-secondary">It has no revision yet, so kagent starts no sessions for it. Its Ready condition says why.</p>
  }
  return (
    <div className="space-y-1 text-xs text-theme-text-secondary">
      {newest && (
        <p>
          New sessions start on revision <span className="font-mono">{newest.revision.slice(0, SHORT)}</span>, template <span className="font-mono">{newest.template.name}</span>.
        </p>
      )}
      {desired && (
        <p>
          Its desired revision <span className="font-mono">{desired.revision.slice(0, SHORT)}</span> isn’t prepared yet{newest ? ', so new sessions still start on the one above' : ''}. Its Ready condition says why.
        </p>
      )}
      {data.revisions.some((r) => r.ambiguous) && (
        <p>Another Agent’s revision makes a template with the same name, so Radar counts the sessions on it for neither Agent.</p>
      )}
    </div>
  )
}

export interface KagentAgentSessionsProps {
  agent: KagentAgentRef
  /** Opens one session's actor; defaults to its page in the Agent Substrate workspace. */
  onOpenActor?: (ref: SubstrateRef) => void
  /** Opens the Actors list narrowed to this Agent's sessions; defaults to navigating there. */
  onOpenSessions?: (agent: KagentAgentRef) => void
}

/**
 * One kagent Agent's chat sessions as Substrate runs them: how many, by state, the few that need
 * attention, where new sessions start, and what Radar can't see (the conversations themselves). Counts
 * only exact matches: sessions on a revision the Agent no longer names are a hint beside them.
 */
export function KagentAgentSessions({ agent, onOpenActor, onOpenSessions }: KagentAgentSessionsProps) {
  const navigate = useWorkspaceNavigate()
  const { connection } = useConnection()
  const query = useKagentAgentSessions(agent)
  const openActor = onOpenActor ?? ((ref: SubstrateRef) => navigate(substrateActorPath(ref, connection.context || undefined)))
  const openSessions = onOpenSessions ?? ((a: KagentAgentRef) => navigate(substrateActorsPath({ agent: agentFilterValue(a) })))
  const heading = <SectionHeading>Sessions in Agent Substrate</SectionHeading>

  const upgrade = query.kagentUpgrade ?? query.upgrade
  if (upgrade) {
    return <section>{heading}<RadarUpgradeNote requirement={upgrade} /></section>
  }
  const data = query.data
  if (!data) {
    if (query.isLoading || !query.error) {
      return <section>{heading}<p className="text-sm text-theme-text-tertiary">Reading this Agent’s sessions from Substrate…</p></section>
    }
    return <section>{heading}<Notice>{sessionsFailure(query.error, agent)}</Notice></section>
  }

  const lower = !data.complete
  const total = data.sessions.total
  return (
    <section className="space-y-2">
      {heading}
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <p className="text-sm text-theme-text-primary">
          <span className="font-medium">{count(total, lower)}</span> {total === 1 && !lower ? 'session' : 'sessions'}
          {data.needAttention > 0 && <span className="text-theme-text-secondary"> · {count(data.needAttention, lower)} {data.needAttention === 1 ? 'needs' : 'need'} attention</span>}
        </p>
        {total > 0 && (
          <button type="button" onClick={() => openSessions(agent)} className="text-sm text-accent-text hover:underline">List its sessions →</button>
        )}
      </div>
      {lower && (
        <p className="text-xs text-theme-text-tertiary">
          Lower bounds: Radar did not read every actor in atespace {agent.namespace}{data.coverage.actors.code === 'capExceeded' ? ', which holds more than its read limit' : ''}.
        </p>
      )}
      {total > 0 && <SubstrateStateBar counts={data.sessions} />}
      {data.attention.length > 0 && (
        <ul className="space-y-1">
          {data.attention.map((row) => (
            <li key={`${row.atespace}/${row.name}`} className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-sm">
              <ActorStateBadge state={row.state} impact={row.workerImpact} updateTime={row.updateTime} />
              <button type="button" onClick={() => openActor(row)} className="font-mono text-[12.5px] text-accent-text hover:underline">
                session {row.kagent ? shortSessionId(row.kagent.sessionId) : row.name}
              </button>
              {stateCellReading(row).text && <span className="min-w-0 truncate text-xs text-theme-text-secondary">{stateCellReading(row).text}</span>}
              {row.updateTime && <span className="text-xs text-theme-text-tertiary"><When iso={row.updateTime} /></span>}
            </li>
          ))}
        </ul>
      )}
      <RevisionsText data={data} />
      {data.earlierRevisions > 0 && (
        <p className="text-xs text-theme-text-secondary">
          {count(data.earlierRevisions, lower)} more {data.earlierRevisions === 1 ? 'session runs' : 'sessions run'} on templates named for {agentText(agent)} but for an earlier revision. Radar doesn’t count {data.earlierRevisions === 1 ? 'it' : 'them'}: a deleted Agent with the same name leaves the same names{data.earlierShared ? ', and another Agent’s name fits some of them too' : ''}.
        </p>
      )}
      <p className="text-xs text-theme-text-tertiary">{KAGENT_OUTCOME}</p>
    </section>
  )
}

/** Why the sessions could not be read, in a sentence. */
export function sessionsFailure(error: unknown, agent: KagentAgentRef): string {
  const code = substrateErrorCode(error)
  if (code === 'sharedRadar' || code === 'apiDisabled' || code === 'mintDenied' || code === 'notConfigured' || code === 'targetChanged') {
    const r = tierReading({ state: code === 'notConfigured' || code === 'targetChanged' ? 'notConfigured' : code === 'sharedRadar' || code === 'apiDisabled' ? 'disabled' : 'failed', code, reason: error instanceof Error ? error.message : undefined })
    return `Radar doesn’t read Substrate’s API here, so it can’t count this Agent’s sessions. ${r.title}${r.detail ? `: ${r.detail}` : '.'}`
  }
  if (error instanceof ApiError && error.status === 403) {
    return `You can’t list kagent Agents in namespace ${agent.namespace}, so Radar can’t say which actors are this Agent’s sessions.`
  }
  if (code === 'incomplete') return `Radar hasn’t read kagent’s Agents in namespace ${agent.namespace} yet. Try again shortly.`
  if (code === 'notFound') return `Radar doesn’t find Agent ${agentText(agent)} among kagent’s Agents.`
  return `Radar couldn’t read this Agent’s sessions: ${error instanceof Error ? error.message : 'unknown error'}.`
}
