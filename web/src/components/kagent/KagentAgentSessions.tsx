import type { ReactNode } from 'react'
import { useLocation } from 'react-router-dom'
import { MessagesSquare } from 'lucide-react'
import { RadarUpgradeNote, Section, WorkspaceCard } from '@skyhook-io/k8s-ui'
import { ApiError } from '../../api/client'
import { substrateErrorCode, useKagentAgentSessions, type KagentAgentRef, type SubstrateRef } from '../../api/substrateWorkspace'
import { useConnection } from '../../context/ConnectionContext'
import { Notice } from '../workspace/layout'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { agentFilterValue, agentText, shortSessionId, tierReading } from '../substrate/readings'
import { stateCellReading } from '../substrate/SubstrateActors'
import { substrateActorPath, substrateActorsPath } from '../substrate/routes'
import { ActorStateBadge, When } from '../substrate/shared'
import { SubstrateStateBar } from '../substrate/SubstrateStateBar'

function count(n: number, lowerBound: boolean): string {
  return `${lowerBound ? 'at least ' : ''}${n.toLocaleString('en-US')}`
}

export interface KagentAgentSessionsProps {
  agent: KagentAgentRef
  framed?: boolean
  /** Opens one session's actor; defaults to its page in the Agent Substrate workspace. */
  onOpenActor?: (ref: SubstrateRef) => void
  /** Opens the Actors list narrowed to this Agent's sessions; defaults to navigating there. */
  onOpenSessions?: (agent: KagentAgentRef) => void
}

/**
 * One kagent Agent's chat sessions as Substrate runs them, inside the Agent's drawer: how many, on the
 * current and earlier revisions, by state, and the few that need attention. Counts only exact matches;
 * sessions Radar can't prove are this Agent's are a hint beside them. Where new sessions start and what
 * Radar can't see of a conversation are the drawer's Agent section's to say.
 */
export function KagentAgentSessions({ agent, onOpenActor, onOpenSessions, framed }: KagentAgentSessionsProps) {
  const navigate = useWorkspaceNavigate()
  const location = useLocation()
  const { connection } = useConnection()
  const query = useKagentAgentSessions(agent)
  const openActor = onOpenActor ?? ((ref: SubstrateRef) => navigate(substrateActorPath(ref, connection.context || undefined), location.pathname.startsWith('/kagent/agents/') ? {
    state: { returnLabel: `Agent: ${agent.name}`, returnPath: location.pathname + location.search, returnCtx: connection.context },
  } : undefined))
  const openSessions = onOpenSessions ?? ((a: KagentAgentRef) => navigate(substrateActorsPath({ agent: agentFilterValue(a) })))
  const frame = (body: ReactNode) => framed ? <WorkspaceCard title="Sessions"><div className="space-y-3">{body}</div></WorkspaceCard> : <Section title="Sessions" icon={MessagesSquare} defaultExpanded><div className="space-y-2">{body}</div></Section>

  const upgrade = query.kagentUpgrade ?? query.upgrade
  if (upgrade) return frame(<RadarUpgradeNote requirement={upgrade} />)
  const data = query.data
  if (!data) {
    if (query.isLoading || !query.error) return frame(<p className="text-sm text-theme-text-tertiary">Reading this Agent’s sessions from Substrate…</p>)
    return frame(<Notice>{sessionsFailure(query.error, agent)}</Notice>)
  }

  const lower = !data.complete
  const total = data.sessions.total
  const current = data.revisions.filter(r => r.newSessions).reduce((n, r) => n + r.sessions, 0)
  const other = total - current
  return frame(
    <>
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <p className="text-sm text-theme-text-primary">
          <span className="font-medium">{count(total, lower)}</span> {total === 1 && !lower ? 'session' : 'sessions'}
          {total > 0 && (
            <span className="text-theme-text-secondary"> · {count(current, lower)} on the new-sessions revision, {count(other, lower)} on other revisions</span>
          )}
          {data.needAttention > 0 && <span className="text-theme-text-secondary"> · {count(data.needAttention, lower)} {data.needAttention === 1 ? 'needs' : 'need'} attention</span>}
        </p>
        {total > 0 && !framed && (
          <button type="button" onClick={() => openSessions(agent)} className="text-sm text-accent-text hover:underline">List its sessions in Agent Substrate →</button>
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
      {data.revisions.some((r) => r.ambiguous) && (
        <p className="text-xs text-theme-text-secondary">Another Agent’s revision makes a template with the same name, so Radar counts the sessions on it for neither Agent.</p>
      )}
      {data.hintedEarlierRevisions > 0 && (
        <p className="text-xs text-theme-text-secondary">
          {count(data.hintedEarlierRevisions, lower)} more {data.hintedEarlierRevisions === 1 ? 'session runs' : 'sessions run'} on templates named for {agentText(agent)} that Radar can’t prove are its: a deleted Agent with the same name leaves the same names{data.hintShared ? ', and another Agent’s name fits some of them too' : ''}. Radar doesn’t count {data.hintedEarlierRevisions === 1 ? 'it' : 'them'}.
        </p>
      )}
    </>,
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
