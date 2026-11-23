import { useState } from 'react'
import { useLocation } from 'react-router-dom'
import { FactGrid, FactRow, PaneLoader, TimeValue } from '@skyhook-io/k8s-ui'
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
import { revisionText } from './shared'

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
    <div className="space-y-4 p-5">
      <KagentAgentSessions agent={agent} />
      <RefreshFailedNotice queries={[query]} />
      {query.error && !query.data ? (
        <Notice>{sessionsFailure(query.error, agent)}</Notice>
      ) : (
        <ActorsTable
          data={query.data}
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
      <p className="text-sm text-theme-text-tertiary">
        Recover crashed sessions through kagent’s SessionService/RevertSession, or stop active turns and delete the chat in kagent. Moving
        or reverting the actor directly can lose the session.
      </p>
      <p className="text-sm text-theme-text-tertiary">Conversation outcomes: not visible to Radar.</p>
    </div>
  )
}
function RevisionTemplate({
  template,
  sessions,
  lowerBound,
  label,
}: {
  template: SubstrateTemplateRow
  sessions?: number
  lowerBound: boolean
  label: string
}) {
  const query = useSubstrateTemplate(template)
  return (
    <div className="space-y-1">
      <span>{label}</span>
      <Sub>
        Template {template.atespace}/{template.name}
      </Sub>
      <Sub>
        Template created:{' '}
        {query.data?.spec?.createTime ? (
          <TimeValue timestamp={query.data.spec.createTime} />
        ) : query.isLoading ? (
          'Reading…'
        ) : (
          'Not reported or unread'
        )}
      </Sub>
      <Sub>Golden snapshot: {template.golden.state}</Sub>
      <Sub>Sessions: {sessions === undefined ? 'Not read' : `${lowerBound ? '≥' : ''}${sessions}`}</Sub>
    </div>
  )
}
export function KagentRevisions({ agent }: { agent: KagentAgentRef }) {
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
  const rows = attributed.slice(0, 50)
  return (
    <div className="space-y-4 p-5">
      <RefreshFailedNotice queries={[summary, templates]} />
      <FactGrid>
        <FactRow label="Desired revision">
          <span className="font-mono text-xs">
            {revisionText(data.agent.desiredRevision) === 'No revision prepared'
              ? 'Not reported for the latest change'
              : revisionText(data.agent.desiredRevision)}
          </span>
        </FactRow>
        <FactRow label="New sessions run">
          <span className="font-mono text-xs">{revisionText(data.agent.latestSuccessfulRevision)}</span>
        </FactRow>
      </FactGrid>
      <p className="text-sm text-theme-text-secondary">
        A template’s creation time is when Substrate created it. kagent reports no time when a revision became prepared.
      </p>
      <SectionTable
        title="Attributed templates"
        rows={rows}
        rowKey={(t) => `${t.atespace}/${t.name}`}
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
            width: '35%',
            cell: (t) => (
              <span className="font-mono text-xs">{t.kagent?.kind === 'agentSession' ? revisionText(t.kagent.revision) : 'Unknown'}</span>
            ),
          },
          {
            header: 'Evidence',
            cell: (t) => {
              const rev = data.revisions.find((r) => r.template.atespace === t.atespace && r.template.name === t.name)
              return (
                <RevisionTemplate
                  template={t}
                  sessions={rev?.sessions ?? t.actors?.total}
                  lowerBound={!data.complete || templates.data?.coverage.actors.state !== 'full'}
                  label={
                    rev?.ambiguous
                      ? 'Ambiguous — not counted for this Agent'
                      : rev?.newSessions
                        ? 'New sessions start here'
                        : rev?.desired
                          ? 'Desired revision'
                          : 'Earlier revision'
                  }
                />
              )
            },
          },
        ]}
        footer={attributed.length > rows.length ? `Showing ${rows.length} of ${attributed.length} attributed templates.` : undefined}
      />
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
          {data.hintedEarlierRevisions} sessions have unprovable earlier revisions. They are hints and do not count as this Agent’s
          sessions.{' '}
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
