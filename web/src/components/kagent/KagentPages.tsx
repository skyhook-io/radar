import type { ReactNode } from 'react'
import { FactGrid, FactRow, FoldSection, PaneLoader, SectionHeading } from '@skyhook-io/k8s-ui'
import {
  readKagent,
  getKagentDiscoveredTools,
  stripKagentURLCredentials,
  redactKagentURLsInText,
} from '@skyhook-io/k8s-ui/components/resources/resource-utils-kagent'
import { useResource, isForbiddenError, isNotFoundError } from '../../api/client'
import {
  useKagentWorkspace,
  type KagentImpact,
  type KagentMCPServer,
  type KagentModel,
  type KagentRef,
  type KagentReference,
  type KagentWorkspace,
} from '../../api/kagent'
import { Notice, GrantText, RefreshFailedNotice, SectionTable, Sub } from '../workspace/layout'
import { KagentAgentRenderer } from './KagentAgentRenderer'
import { KagentAttention } from './KagentOverview'
import { modelStatus, mcpStatus } from './KagentLists'
import {
  changeText,
  consumersText,
  CredentialText,
  effectText,
  KagentName,
  Reading,
  refKey,
  refName,
  revisionText,
  roleText,
  SessionValue,
  KagentRuntimeRead,
  useKagentRuntime,
} from './shared'

const PLURAL: Record<string, string> = {
  Agent: 'agents',
  AgentTemplate: 'agenttemplates',
  Harness: 'harnesses',
  ModelConfig: 'modelconfigs',
  RemoteMCPServer: 'remotemcpservers',
  EgressPolicy: 'egresspolicies',
  WorkerPool: 'workerpools',
}
type Open = (ref: KagentRef) => void
function PageSection({ title, children }: { title: ReactNode; children: ReactNode }) {
  return (
    <section className="space-y-2">
      <SectionHeading>{title}</SectionHeading>
      {children}
    </section>
  )
}
function UsedBy({ impact, data, onOpen, server }: { impact: KagentImpact; data: KagentWorkspace; onOpen: Open; server?: KagentMCPServer }) {
  const agents = new Map(data.agents.map((a) => [refKey(a.ref), a]))
  return (
    <SectionTable
      title="Used by"
      subtitle={consumersText(impact)}
      rows={impact.agents}
      rowKey={(a) => refKey(a.agent)}
      minWidth={0}
      empty={consumersText(impact)}
      columns={[
        {
          header: 'Agent',
          width: '30%',
          cell: (a) => (
            <>
              <KagentName object={a.agent} onOpen={onOpen} />
              <Sub>{a.agent.namespace}</Sub>
            </>
          ),
        },
        {
          header: server ? 'Tools asked / Effect' : 'Role / Effect',
          width: '40%',
          cell: (a) => (
            <>
              {server
                ? (() => {
                    const agent = agents.get(refKey(a.agent))
                    const refs = agent?.references.filter((r) => refKey(r.target) === refKey(server.ref))
                    return refs
                      ? refs.map((r) => (r.tools?.length ? r.tools.join(', ') : 'All offered tools')).join('; ')
                      : 'Agent configuration outside this inventory'
                  })()
                : a.roles.map(roleText).join(', ')}
              <Sub>{effectText(a.effect)}</Sub>
            </>
          ),
        },
        {
          header: 'Latest change',
          cell: (a) => {
            const agent = agents.get(refKey(a.agent))
            return agent ? changeText(agent.latestChange.state) : 'Outside this inventory'
          },
        },
      ]}
      footer={
        impact.count > impact.agents.length
          ? `Showing ${impact.agents.length} of ${impact.lowerBound ? 'at least ' : ''}${impact.count} Agents.`
          : undefined
      }
    />
  )
}
function ReferenceLine({
  reference,
  data,
  onOpen,
  placement,
}: {
  reference: KagentReference
  data: KagentWorkspace
  onOpen: Open
  placement?: string
}) {
  const ref = reference.target
  const query = useResource<any>(PLURAL[ref.kind] ?? ref.kind.toLowerCase(), ref.namespace, ref.name, ref.group, {
    enabled: reference.state === 'unread' && ref.group === 'api.kagent.dev',
  })
  const direct = query.data
  const directReading = direct ? readKagent(direct) : undefined
  const state = reference.state !== 'unread' ? reference.state : direct ? 'present' : isNotFoundError(query.error) ? 'absent' : 'unread'
  const reading = directReading ?? reference.reading
  const model = data.models.find((m) => refKey(m.ref) === refKey(ref))
  const server = data.mcpServers.find((s) => refKey(s.ref) === refKey(ref))
  const offeredKnown = server?.toolsOfferedKnown ?? (direct && directReading?.state === 'ready' && ref.kind === 'RemoteMCPServer')
  const offered = server?.toolsOffered ?? (offeredKnown ? getKagentDiscoveredTools(direct) : undefined)
  return (
    <div className="card-inner-lg space-y-1 text-sm">
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
        <span className="font-medium text-theme-text-primary">{roleText(reference.role)}</span>
        <KagentName object={ref} onOpen={onOpen} />
        <div className="text-theme-text-secondary">
          {state === 'present' ? (
            reading ? (
              <Reading
                reading={reading}
                text={
                  ref.kind === 'ModelConfig'
                    ? modelStatus(reading)
                    : ref.kind === 'RemoteMCPServer' && server
                      ? mcpStatus(server)
                      : reading.text
                }
              />
            ) : (
              'Present · declaration only'
            )
          ) : state === 'absent' ? (
            'Absent'
          ) : state === 'notPermitted' ? (
            'Not permitted by allowedNamespaces'
          ) : state === 'declared' ? (
            'Declared · not read here'
          ) : (
            'Unread'
          )}
        </div>
      </div>
      <Sub>
        {ref.kind} {refName(ref)} · declared by {reference.from.kind} {refName(reference.from)}
      </Sub>
      {state === 'unread' && (
        <p className="text-xs text-theme-text-secondary">
          {query.isLoading ? (
            'Reading this object directly…'
          ) : isForbiddenError(query.error) ? (
            <>
              Needs{' '}
              <GrantText
                grant={{ verb: 'get', group: ref.group, resource: PLURAL[ref.kind] ?? ref.kind.toLowerCase(), namespace: ref.namespace }}
              />
              .
            </>
          ) : (
            `Not read: ${reference.unread ?? 'unknown'}.`
          )}
        </p>
      )}
      {reference.policyUnknown && (
        <p className="text-xs text-theme-text-secondary">
          Namespace labels were not read, so whether allowedNamespaces permits this reference is unknown.
        </p>
      )}
      {model && (
        <p className="text-xs text-theme-text-secondary">
          {model.provider || 'Provider not reported'} → {model.model || 'Model not reported'} →{' '}
          <CredentialText credential={model.credential} />
        </p>
      )}
      {!model && direct && ref.kind === 'ModelConfig' && (
        <p className="text-xs text-theme-text-secondary">
          {direct.spec?.provider ? redactKagentURLsInText(direct.spec.provider) : 'Provider not reported'} →{' '}
          {direct.spec?.model ? redactKagentURLsInText(direct.spec.model) : 'Model not reported'} →{' '}
          {direct.spec?.apiKeySecret
            ? `Secret ${ref.namespace}/${direct.spec.apiKeySecret} · key ${direct.spec.apiKeySecretKey || 'not named'}`
            : 'No API key Secret declared'}
        </p>
      )}
      {(reference.role === 'mcpServer' || reference.role === 'subagentMcpServer') && (
        <>
          <p className="text-xs text-theme-text-secondary">
            Tools asked: {reference.tools?.length ? reference.tools.join(', ') : 'all offered tools'}.
          </p>
          {offeredKnown && offered?.length ? (
            <FoldSection title="Tools offered" summary={`${offered.length} tools`} attention={false}>
              <p className="break-words text-xs text-theme-text-secondary">{offered.join(', ')}</p>
            </FoldSection>
          ) : (
            <p className="text-xs text-theme-text-secondary">Tools offered: {offeredKnown ? 'none' : 'unknown'}.</p>
          )}
          {offeredKnown && reference.tools?.some((t) => !offered?.includes(t)) && (
            <p className="text-xs text-warning-text">Not offered: {reference.tools.filter((t) => !offered?.includes(t)).join(', ')}</p>
          )}
        </>
      )}
      {reference.key && <Sub>Key: {reference.key}</Sub>}
      {reference.fallback && <Sub>Falls back to the Agent model for summarization.</Sub>}
      {placement && <p className="text-xs text-theme-text-secondary">{placement}</p>}
    </div>
  )
}
export function KagentAgentOverview({ resource, onOpen }: { resource: any; onOpen: Open }) {
  const query = useKagentWorkspace([])
  const namespace = resource.metadata.namespace
  const name = resource.metadata.name
  const runtime = useKagentRuntime([namespace])
  const data = query.data
  const agent = data?.agents.find((a) => a.ref.namespace === namespace && a.ref.name === name)
  const session = runtime.rows.find((r) => r.agent.namespace === namespace && r.agent.name === name)
  if (!data && query.isLoading) return <PaneLoader label="Reading Agent configuration…" />
  if (!agent || !data)
    return (
      <div className="space-y-3">
        <Notice>
          Radar cannot compose this Agent’s workspace from its list read. The individual object reads below use your get access.
        </Notice>
        <KagentAgentRenderer data={resource} onNavigate={(r) => onOpen({ ...r, group: r.group ?? '' })} />
      </div>
    )
  const latest = agent.latestChange.state
  const suffix = latest === 'prepared' ? '' : latest === 'notProcessed' ? ' (not yet processed)' : ` (${changeText(latest).toLowerCase()})`
  return (
    <div className="space-y-5">
      <RefreshFailedNotice queries={[query, runtime.query]} />
      <KagentRuntimeRead runtime={runtime} />
      <KagentAttention data={data} agent={agent.ref} onOpen={onOpen} />
      <PageSection title="What new sessions run">
        <FactGrid>
          <FactRow label="Revision">
            <span className="font-mono">{revisionText(agent.newSessionsRevision)}</span>
            {!agent.newSessionsRevision && <Sub>No session can start until kagent prepares a revision.</Sub>}
          </FactRow>
          <FactRow label="Latest change">
            <Reading reading={agent.reading} text={`Latest change ${changeText(latest).toLowerCase()}`} />
            {agent.latestChange.message && (
              <FoldSection title="kagent's reason" summary="" attention={false}>
                <p className="break-words text-xs text-theme-text-secondary">{agent.latestChange.message}</p>
              </FoldSection>
            )}
          </FactRow>
          <FactRow label="Placement">
            {session ? (
              <>
                <span>
                  {session.placement.state === 'mayFit'
                    ? 'May fit'
                    : session.placement.state === 'noRoom'
                      ? 'No room: CPU or memory'
                      : session.placement.state === 'full'
                        ? 'No free session slots'
                        : session.placement.state === 'noActiveWorkers'
                          ? 'No active workers'
                          : 'Unknown'}
                </span>
                <Sub>{session.placement.reading}</Sub>
                <Sub>
                  Prepared template{' '}
                  {session.placement.template ? `${session.placement.template.atespace}/${session.placement.template.name}` : 'not read'}
                  {!session.placement.complete ? ' · worker read incomplete' : ''}
                </Sub>
              </>
            ) : (
              runtime.unavailable
            )}
          </FactRow>
        </FactGrid>
      </PageSection>
      <PageSection title={`Configuration of the latest change${suffix}`}>
        {latest !== 'prepared' && agent.newSessionsRevision && (
          <Notice>
            New sessions still run revision {revisionText(agent.newSessionsRevision)}, prepared before this change. The configuration below
            describes the latest change; Radar does not reconstruct that prepared revision’s configuration.
          </Notice>
        )}
        {agent.incomplete && (
          <Notice>
            Some declared references were not read or the template traversal reached its limit. The chain below is incomplete.
          </Notice>
        )}
        <div className="space-y-2">
          {agent.references.map((r, i) => (
            <ReferenceLine
              key={`${refKey(r.from)}/${r.field}/${i}`}
              reference={r}
              data={data}
              onOpen={onOpen}
              placement={
                r.role === 'workerPool' && latest === 'prepared' && session
                  ? `Placement of the prepared revision: ${session.placement.reading}`
                  : undefined
              }
            />
          ))}
          {agent.references.length === 0 && (
            <p className="text-sm text-theme-text-tertiary">
              {agent.incomplete ? 'References could not be read.' : 'No references declared.'}
            </p>
          )}
        </div>
        {agent.incomplete && (
          <FoldSection title="Configuration from individual object reads" summary="Uses get access" attention={false}>
            <KagentAgentRenderer data={resource} onNavigate={(r) => onOpen({ ...r, group: r.group ?? '' })} />
          </FoldSection>
        )}
        {!!agent.cycle?.length && <Notice>Subagent template cycle: {agent.cycle.join(' → ')}</Notice>}
      </PageSection>
      <PageSection title="Sessions">
        <div className="text-sm text-theme-text-primary">
          <SessionValue row={session} runtime={runtime} />
        </div>
        {session && (
          <Sub>
            {session.complete ? '' : 'At least '}
            {session.onNewSessionsRevision} on the new-sessions revision · {session.complete ? '' : 'at least '}
            {session.onOtherRevisions} on other revisions
          </Sub>
        )}
      </PageSection>
      <p className="text-sm text-theme-text-tertiary">Conversation outcomes: not visible to Radar.</p>
    </div>
  )
}
export function KagentDependencyOverview({ resource, onOpen }: { resource: any; onOpen: Open }) {
  const query = useKagentWorkspace([])
  const data = query.data
  const ref: KagentRef = {
    kind: resource.kind,
    group: 'api.kagent.dev',
    namespace: resource.metadata.namespace,
    name: resource.metadata.name,
  }
  const object = data && (ref.kind === 'ModelConfig' ? data.models : data.mcpServers).find((o) => refKey(o.ref) === refKey(ref))
  const reading = object?.reading ?? readKagent(resource)
  const model = object && ref.kind === 'ModelConfig' ? (object as KagentModel) : undefined
  const server = object && ref.kind === 'RemoteMCPServer' ? (object as KagentMCPServer) : undefined
  return (
    <div className="space-y-5">
      <RefreshFailedNotice queries={[query]} />
      {data && <KagentAttention data={data} onOpen={onOpen} agent={ref} />}
      <PageSection title="Reading">
        {reading && <Reading reading={reading} text={model ? modelStatus(model.reading) : server ? mcpStatus(server) : reading.text} />}
        {server?.failing && (
          <p className="text-sm text-theme-text-secondary">
            kagent's controller can't discover this server's tools; sessions may run without them.
          </p>
        )}
        {reading?.message && (
          <FoldSection title="kagent's message" summary="" attention={false}>
            <p className="break-words text-xs text-theme-text-secondary">{reading.message}</p>
          </FoldSection>
        )}
      </PageSection>
      {model && (
        <PageSection title="Model and credentials">
          <FactGrid>
            <FactRow label="Provider">{model.provider || 'Not reported'}</FactRow>
            <FactRow label="Model">{model.model || 'Not reported'}</FactRow>
            <FactRow label="Credential reference">
              <CredentialText credential={model.credential} />
            </FactRow>
          </FactGrid>
        </PageSection>
      )}
      {!model && ref.kind === 'ModelConfig' && (
        <PageSection title="Model and credentials">
          <FactGrid>
            <FactRow label="Provider">{resource.spec?.provider ? redactKagentURLsInText(resource.spec.provider) : 'Not reported'}</FactRow>
            <FactRow label="Model">{resource.spec?.model ? redactKagentURLsInText(resource.spec.model) : 'Not reported'}</FactRow>
            <FactRow label="Credential reference">
              {resource.spec?.apiKeySecret
                ? `Secret ${ref.namespace}/${resource.spec.apiKeySecret} · key ${resource.spec.apiKeySecretKey || 'not named'}`
                : 'No API key Secret declared'}
            </FactRow>
          </FactGrid>
        </PageSection>
      )}
      {!server && ref.kind === 'RemoteMCPServer' && (
        <PageSection title="MCP server">
          <FactGrid>
            <FactRow label="URL (redacted)">{resource.spec?.url ? stripKagentURLCredentials(resource.spec.url) : 'Not reported'}</FactRow>
            <FactRow label="Tools offered">
              {reading?.state === 'ready'
                ? getKagentDiscoveredTools(resource).join(', ') || 'None'
                : 'Unknown — controller discovery did not publish a tool list'}
            </FactRow>
          </FactGrid>
        </PageSection>
      )}
      {server && (
        <PageSection title="MCP server">
          <FactGrid>
            <FactRow label="URL (redacted)">
              <span className="break-all font-mono text-xs">{server.url || 'Not reported'}</span>
            </FactRow>
            <FactRow label="Tools offered">
              {server.toolsOfferedKnown
                ? server.toolsOffered.join(', ') || 'None'
                : 'Unknown — controller discovery did not publish a tool list'}
            </FactRow>
            <FactRow label="Header sources">
              {server.headers?.length
                ? server.headers
                    .map(
                      (h) =>
                        `${h.name}: ${h.from ? `${h.from.kind} ${refName(h.from)} · key ${h.key}` : h.inline ? 'inline value (hidden)' : 'source not reported'}`,
                    )
                    .join('; ')
                : 'No headers declared'}
            </FactRow>
          </FactGrid>
        </PageSection>
      )}
      {data && object ? (
        <UsedBy impact={object.usedBy} data={data} onOpen={onOpen} server={server} />
      ) : (
        <Notice>
          Used by: unknown. Radar needs list access to this kind and its Agent consumers to compose the inventory; this page’s object was
          read with get access.
        </Notice>
      )}
    </div>
  )
}
