import { useState } from 'react'
import { useLocation } from 'react-router-dom'
import { PaneLoader, TimeValue } from '@skyhook-io/k8s-ui'
import { useRadarFeature } from '../../api/client'
import {
  useKagentAgentSessions,
  useSubstrateActors,
  useSubstrateTemplate,
  useSubstrateTemplates,
  type KagentAgentRef,
  type SubstrateTemplateRow,
} from '../../api/substrateWorkspace'
import { useConnection } from '../../context/ConnectionContext'
import { ActorsTable } from '../substrate/SubstrateActors'
import { substrateActorPath, substrateActorsPath } from '../substrate/routes'
import { Notice, RefreshFailedNotice, SectionTable, Sub } from '../workspace/layout'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { KagentAgentSessions, sessionsFailure } from './KagentAgentSessions'
import { changeText, revisionText } from './shared'

export function KagentSessionsTab({ agent }: { agent: KagentAgentRef }) {
  const [page, setPage] = useState(0)
  const feature = useRadarFeature('kagentSessionSummary')
  const navigate = useWorkspaceNavigate()
  const location = useLocation()
  const { connection } = useConnection()
  const query = useSubstrateActors(
    { agent: `${agent.namespace}/${agent.name}`, sort: 'attention', limit: 50, offset: page * 50 },
    feature.support === 'supported',
  )
  if (feature.support !== 'supported')
    return (
      <div className="p-5">
        <Notice>Sessions need a newer Radar in this cluster.</Notice>
      </div>
    )
  return (
    <div className="space-y-4 p-4">
      <KagentAgentSessions framed agent={agent} />
      {query.data?.rows.some((r) => r.state === 'crashed') && (
        <Notice>
          Recover crashed sessions through kagent’s SessionService/RevertSession, or stop active turns and delete the chat in kagent. Moving
          or reverting the actor directly can lose the session.
        </Notice>
      )}
      <RefreshFailedNotice queries={[query]} />
      {query.error && !query.data ? (
        <Notice>{sessionsFailure(query.error, agent)}</Notice>
      ) : (
        <ActorsTable
          data={query.data}
          revisionOnly
          loading={query.isLoading}
          placeholder={query.isPlaceholderData}
          page={page}
          onPage={setPage}
          onOpenActor={(actor) =>
            navigate(substrateActorPath(actor, connection.context), {
              state: {
                returnLabel: `Agent: ${agent.name}`,
                returnPath: location.pathname + location.search,
                returnCtx: connection.context,
              },
            })
          }
          openActor={null}
        />
      )}
      <p className="text-sm text-theme-text-tertiary">Conversation outcomes: not visible to Radar.</p>
    </div>
  )
}
function TemplateCreated({ template }: { template: SubstrateTemplateRow }) {
  const query = useSubstrateTemplate(template)
  return (
    <span className="text-xs">
      {query.data?.spec?.createTime ? (
        <TimeValue timestamp={query.data.spec.createTime} />
      ) : query.isLoading ? (
        'Reading…'
      ) : (
        'Not reported or unread'
      )}
    </span>
  )
}
export function KagentRevisions({ agent, latestChange }: { agent: KagentAgentRef; latestChange?: string }) {
  const summary = useKagentAgentSessions(agent)
  const feature = useRadarFeature('kagentSessionSummary')
  const templates = useSubstrateTemplates(feature.support === 'supported' && !!summary.data)
  const navigate = useWorkspaceNavigate()
  const data = summary.data
  if (feature.support !== 'supported')
    return (
      <div className="p-5">
        <Notice>Revisions from Substrate need a newer Radar in this cluster.</Notice>
      </div>
    )
  if (!data && summary.isLoading) return <PaneLoader label="Reading revisions…" className="flex-1" />
  if (!data)
    return (
      <div className="p-5">
        <Notice>{sessionsFailure(summary.error, agent)}</Notice>
      </div>
    )
  const attributed =
    templates.data?.templates.filter(
      (t) => t.kagent?.kind === 'agentSession' && t.kagent.agent.namespace === agent.namespace && t.kagent.agent.name === agent.name,
    ) ?? []
  const revisions = new Map(data.revisions.map((r) => [r.revision, r]))
  const digests = [
    ...new Set(
      [
        data.agent.latestSuccessfulRevision,
        data.agent.desiredRevision,
        ...data.revisions.map((r) => r.revision),
        ...attributed.map((t) => (t.kagent?.kind === 'agentSession' ? t.kagent.revision : undefined)),
      ].filter((r): r is string => !!r),
    ),
  ]
  const changeWithoutRevision = !!latestChange && latestChange !== 'prepared' && !data.agent.desiredRevision
  const rows = digests.slice(0, changeWithoutRevision ? 49 : 50).map((revision) => ({
    revision,
    evidence: revisions.get(revision),
    template: attributed.find((t) => t.kagent?.kind === 'agentSession' && t.kagent.revision === revision),
    current: revision === data.agent.latestSuccessfulRevision,
    desired: revision === data.agent.desiredRevision,
  }))
  if (changeWithoutRevision)
    rows.unshift({ revision: '', evidence: undefined, template: undefined, current: false, desired: true })
  const total = digests.length + Number(changeWithoutRevision)
  return (
    <div className="space-y-4 p-5">
      <RefreshFailedNotice queries={[summary, templates]} />
      <SectionTable
        rows={rows}
        rowKey={(r) => r.revision || 'latest-change'}
        minWidth={0}
        empty={
          templates.isLoading
            ? 'Reading templates…'
            : templates.error
              ? 'Templates could not be read.'
              : 'No attributed templates in what Radar can read.'
        }
        columns={[
          {
            header: 'Revision',
            width: '19%',
            cell: (t) => <span className="font-mono text-xs">{t.revision ? revisionText(t.revision) : latestChange === 'blocked' || latestChange === 'failed' ? 'Not compiled' : 'Not reported'}</span>,
          },
          {
            header: 'Role',
            width: '28%',
            cell: (r) => (
              <span>
                {r.evidence?.ambiguous
                  ? 'Ambiguous — not counted for this Agent'
                  : r.current
                    ? 'New sessions start here'
                    : r.desired
                      ? `Latest change${latestChange ? `, ${changeText(latestChange).toLowerCase()}` : ''}`
                      : 'Earlier'}
                {r.current && r.desired && <Sub>Latest change</Sub>}
              </span>
            ),
          },
          {
            header: 'Template created',
            width: '20%',
            cell: (r) =>
              r.template ? (
                <TemplateCreated template={r.template} />
              ) : (
                <span className="text-xs text-theme-text-tertiary">
                  {r.desired && !r.current ? 'No prepared template read' : 'Not reported or unread'}
                </span>
              ),
          },
          { header: 'Golden snapshot', width: '19%', cell: (r) => r.template?.golden.state ?? 'Unknown' },
          {
            header: 'Sessions',
            width: '14%',
            cell: (r) =>
              r.evidence?.ambiguous ? (
                'Not counted'
              ) : r.evidence ? (
                <>
                  {!data.complete ? '≥' : ''}
                  {r.evidence.sessions}
                </>
              ) : r.template?.actors ? (
                <>
                  {templates.data?.coverage.actors.state !== 'full' ? '≥' : ''}
                  {r.template.actors.total}
                </>
              ) : (
                'Not read'
              ),
          },
        ]}
        footer={total > rows.length ? `Showing ${rows.length} of ${total} revisions.` : undefined}
      />
      <p className="text-xs text-theme-text-tertiary">
        A template’s creation time is when Substrate created it. kagent reports no time when a revision became prepared.
      </p>
      {data.revisions
        .filter((r) => r.ambiguous || !attributed.some((t) => t.atespace === r.template.atespace && t.name === r.template.name))
        .map((r) => (
          <Notice key={r.revision}>
            Revision {revisionText(r.revision)}:{' '}
            {r.ambiguous
              ? 'ambiguous attribution; sessions not counted'
              : `template ${r.template.atespace}/${r.template.name} was not read or could not be attributed`}
            .
          </Notice>
        ))}
      {data.hintedEarlierRevisions > 0 && (
        <Notice>
          {data.complete ? '' : 'At least '}
          {data.hintedEarlierRevisions}{' '}
          {data.hintedEarlierRevisions === 1 ? 'session has an unprovable earlier revision' : 'sessions have unprovable earlier revisions'}.
          They are hints and do not count as this Agent’s sessions.{' '}
          <button
            type="button"
            onClick={() => navigate(substrateActorsPath({ kagent: 'unattributed', atespace: agent.namespace }))}
            className="text-accent-text hover:underline"
          >
            Inspect unattributed sessions →
          </button>
        </Notice>
      )}
    </div>
  )
}
