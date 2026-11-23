import type { ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useSubstratePool, substrateErrorCode } from '../../api/substrateWorkspace'
import { useConnection } from '../../context/ConnectionContext'
import { substratePoolPath } from '../substrate/routes'
import { definitiveFailure, poolReadFailure, poolReading, tierReading } from '../substrate/readings'
import {
  FactGrid,
  FactRow,
  FoldSection,
  PaneLoader,
  WorkspaceCard,
  DimensionValue,
  TimeValue,
  StatusDot,
  mapHealthToTone,
  formatRelativeAgeTime,
} from '@skyhook-io/k8s-ui'
import {
  readKagent,
  getKagentHarnessDeclarations,
  getKagentAgentRevisions,
  getKagentAgentTemplateRef,
  getKagentDiscoveredTools,
  stripKagentURLCredentials,
  redactKagentURLsInText,
  redactKagentLiterals,
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
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { WorkspaceConditions } from '../workspace/WorkspaceConditions'
import { KagentAgentRenderer } from './KagentAgentRenderer'
import { KagentAttention } from './KagentOverview'
import { modelStatus, mcpStatus } from './KagentLists'
import {
  changeText,
  consumersText,
  CredentialText,
  ModelCredentialText,
  effectText,
  KagentName,
  Reading,
  refKey,
  refName,
  revisionText,
  roleText,
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
  return <WorkspaceCard title={title}>{children}</WorkspaceCard>
}

function UsedBy({ impact, data, onOpen, server }: { impact: KagentImpact; data: KagentWorkspace; onOpen: Open; server?: KagentMCPServer }) {
  const agents = new Map(data.agents.map((a) => [refKey(a.ref), a]))
  return (
    <SectionTable
      title="Used by"
      subtitle={impact.agents.length ? consumersText(impact) : undefined}
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
    enabled:
      reference.unread !== 'outside_namespace_pin' &&
      (reference.state === 'unread' || ref.kind === 'Harness') &&
      ref.group === 'api.kagent.dev',
  })
  const { connection } = useConnection()
  const poolQuery = useSubstratePool(ref.kind === 'WorkerPool' ? ref : null)
  const poolCode = substrateErrorCode(poolQuery.error)
  const pool = definitiveFailure(poolCode) || poolCode === 'incomplete' ? undefined : poolQuery.data
  const direct =
    reference.unread !== 'outside_namespace_pin' && !isForbiddenError(query.error) && !isNotFoundError(query.error) && query.data
      ? redactKagentLiterals(query.data)
      : undefined
  const harness = direct && ref.kind === 'Harness' ? getKagentHarnessDeclarations(direct) : undefined
  const directReading = direct ? readKagent(direct) : undefined
  const state =
    reference.unread === 'outside_namespace_pin'
      ? 'unread'
      : reference.state !== 'unread'
        ? reference.state
        : direct
          ? 'present'
          : isNotFoundError(query.error)
            ? 'absent'
            : 'unread'
  const reading = directReading ?? reference.reading
  const model = data.models.find((m) => refKey(m.ref) === refKey(ref))
  const server = data.mcpServers.find((s) => refKey(s.ref) === refKey(ref))
  const offeredKnown = server?.toolsOfferedKnown ?? (direct && directReading?.state === 'ready' && ref.kind === 'RemoteMCPServer')
  const offered = server?.toolsOffered ?? (offeredKnown ? getKagentDiscoveredTools(direct) : undefined)
  return (
    <FactRow label={roleText(reference.role)}>
      <div className="space-y-1 text-sm">
        <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
          {ref.kind === 'WorkerPool' ? (
            <Link className="font-mono text-xs text-accent-text hover:underline" to={substratePoolPath(ref, connection.context)}>
              {ref.name}
            </Link>
          ) : (
            <KagentName object={ref} onOpen={onOpen} />
          )}
          <div className="inline-flex items-center gap-1.5 text-theme-text-secondary">
            {(ref.kind === 'Harness' || ref.kind === 'WorkerPool') && (
              <StatusDot
                tone={
                  ref.kind === 'WorkerPool'
                    ? pool?.pool.capacity
                      ? mapHealthToTone(poolReading(pool.pool).severity)
                      : 'unknown'
                    : state === 'present'
                      ? 'neutral'
                      : 'unknown'
                }
              />
            )}
            {ref.kind === 'WorkerPool' ? (
              pool ? (
                poolReading(pool.pool).label
              ) : poolQuery.isLoading ? (
                'Reading worker pool…'
              ) : (
                "Substrate's pools aren't readable with this connection."
              )
            ) : ref.kind === 'Harness' && state === 'present' ? (
              harness?.type ? (
                `${harness.type} runtime`
              ) : query.isLoading ? (
                'Reading runtime…'
              ) : (
                'kagent reports no status for Harnesses'
              )
            ) : state === 'present' ? (
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
                <span className="inline-flex items-center gap-1.5">
                  <StatusDot tone="neutral" />
                  Present · declaration only
                </span>
              )
            ) : state === 'absent' ? (
              <span className="inline-flex items-center gap-1.5">
                <StatusDot tone="unhealthy" />
                Absent
              </span>
            ) : state === 'notPermitted' ? (
              <span className="inline-flex items-center gap-1.5">
                <StatusDot tone="unhealthy" />
                Not permitted by allowedNamespaces
              </span>
            ) : state === 'declared' ? (
              <span className="inline-flex items-center gap-1.5"><StatusDot tone="neutral" />Declared · not read here</span>
            ) : (
              <span className="inline-flex items-center gap-1.5"><StatusDot tone="unknown" />Unread</span>
            )}
          </div>
        </div>
        {reference.from.kind !== 'Agent' && (
          <Sub>
            via {reference.from.kind === 'AgentTemplate' ? 'template' : reference.from.kind} {reference.from.name}
          </Sub>
        )}
        {state === 'unread' && (
          <p className="text-xs text-theme-text-secondary">
            {query.isLoading ? (
              'Reading this object directly…'
            ) : isForbiddenError(query.error) ? (
              <>
                Needs{' '}
                <GrantText
                  grant={{
                    verb: 'get',
                    group: ref.group,
                    resource: PLURAL[ref.kind] ?? ref.kind.toLowerCase(),
                    namespace: ref.namespace,
                  }}
                />
                .
              </>
            ) : (
              `Not read: ${reference.unread ?? 'unknown'}.`
            )}
          </p>
        )}
        {ref.kind === 'WorkerPool' && (
          <Sub>
            {pool
              ? pool.apiTier.state !== 'connected'
                ? tierReading(pool.apiTier).detail
                : undefined
              : poolQuery.isLoading
                ? undefined
                : poolReadFailure(poolCode, poolQuery.error instanceof Error ? poolQuery.error.message : undefined)}
          </Sub>
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
            <ModelCredentialText spec={direct.spec ?? {}} namespace={ref.namespace} />
          </p>
        )}
        {(reference.role === 'mcpServer' || reference.role === 'subagentMcpServer') && (
          <>
            <p className="text-xs text-theme-text-secondary">
              Tools asked:{' '}
              {reference.tools?.length
                ? reference.tools.map((tool, i) => (
                    <span key={tool}>
                      {i > 0 && ', '}
                      <span className={offeredKnown && !offered?.includes(tool) ? 'text-warning-text' : undefined}>
                        {tool}
                        {offeredKnown && !offered?.includes(tool) ? ' (missing)' : ''}
                      </span>
                    </span>
                  ))
                : 'all offered tools'}
              .
            </p>
            {offeredKnown && offered?.length ? (
              <FoldSection title={`Tools offered (${offered.length})`} summary="" attention={false}>
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
    </FactRow>
  )
}
export function KagentAgentOverview({ resource, onOpen }: { resource: any; onOpen: Open }) {
  const query = useKagentWorkspace([])
  const [params, setParams] = useSearchParams()
  const navigate = useWorkspaceNavigate()
  const { connection } = useConnection()
  const namespace = resource.metadata.namespace
  const name = resource.metadata.name
  const runtime = useKagentRuntime([namespace])
  const data = query.data
  const agent = data?.agents.find((a) => a.ref.namespace === namespace && a.ref.name === name)
  const session = runtime.rows.find((r) => r.agent.namespace === namespace && r.agent.name === name)
  const harnessReference = agent?.references.find((r) => r.role === 'harness')
  const harnessRef = harnessReference?.target
  const harness = useResource<any>('harnesses', harnessRef?.namespace ?? namespace, harnessRef?.name ?? '', 'api.kagent.dev', {
    enabled: !!harnessRef && harnessReference?.unread !== 'outside_namespace_pin',
  })
  if (!data && query.isLoading) return <PaneLoader label="Reading Agent configuration…" />
  if (!agent || !data)
    return (
      <div className="space-y-4">
        <Notice>
          Radar cannot compose this Agent’s workspace from its list read. The individual object reads below use your get access.
        </Notice>
        <KagentAgentRenderer data={resource} onNavigate={(r) => onOpen({ ...r, group: r.group ?? '' })} />
      </div>
    )
  const goTab = (tab: string) => {
    const next = new URLSearchParams(params)
    next.set('tab', tab)
    setParams(next, { replace: true })
  }
  const latest = agent.latestChange.state
  const suffix = latest === 'prepared' ? '' : latest === 'notProcessed' ? ' (not yet processed)' : ` (${changeText(latest).toLowerCase()})`
  const model = agent.model
  const modelConfig = model && data.models.find((m) => refKey(m.ref) === refKey(model.ref))
  const pool = session?.placement.pools[0]
  const navigatePool = pool
  const revision = getKagentAgentRevisions(resource)
  const template = agent.references.find((r) => r.role === 'template')?.target
  const harnessInfo = harness.data ? getKagentHarnessDeclarations(redactKagentLiterals(harness.data)) : undefined
  const snapshot = runtime.tier
  const sessionSource = (
    <>
      from Substrate{snapshot?.readAt && ` · ${formatRelativeAgeTime(snapshot.readAt)}`}
      {snapshot?.refreshing && ' · refreshing…'}
      {snapshot?.lastError && ` · Last Substrate read failed: ${snapshot.lastError.message}. Showing the previous snapshot.`}
    </>
  )
  return (
    <div className="space-y-4">
      <RefreshFailedNotice queries={[query, runtime.query]} />
      <KagentAttention data={data} agent={agent.ref} onOpen={onOpen} />
      <PageSection title="At a glance">
        <FactGrid>
          <FactRow label="Latest change">
            <DimensionValue
              tone={agent.reading.tone}
              source="kagent · Agent conditions for the latest change"
              linkLabel="Revisions"
              onOpen={() => goTab('revisions')}
            >
              {changeText(latest)}
            </DimensionValue>
          </FactRow>
          <FactRow label="New sessions run">
            <DimensionValue
              tone={agent.newSessionsRevision ? 'neutral' : 'unknown'}
              source="Agent status.latestSuccessfulRevision"
              linkLabel="Revisions"
              onOpen={() => goTab('revisions')}
            >
              <span className={agent.newSessionsRevision ? 'font-mono text-xs' : undefined}>{revisionText(agent.newSessionsRevision)}</span>
            </DimensionValue>
          </FactRow>
          <FactRow label="Model">
            <DimensionValue
              tone={model?.state === 'absent' || model?.state === 'notPermitted' ? 'unhealthy' : model?.state === 'present' ? model.reading?.tone ?? 'unknown' : 'unknown'}
              source={
                modelConfig
                  ? `${modelConfig.provider || 'Provider not reported'} · ${modelConfig.model || 'Model not reported'}`
                  : 'Latest change’s declared model'
              }
              linkLabel="Model"
              onOpen={model ? () => onOpen(model.ref) : undefined}
            >
              {model
                ? `${model.state === 'present' && model.reading ? modelStatus(model.reading) : model.state === 'absent' ? 'Model missing' : model.state === 'notPermitted' ? 'Reference not permitted' : 'Model unread'} · ${model.ref.name}`
                : agent.incomplete
                  ? 'Not fully read'
                  : 'No model declared'}
            </DimensionValue>
          </FactRow>
          <FactRow label="Tools">
            <DimensionValue
              tone={
                agent.mcpServers.failing || agent.mcpServers.absent || agent.mcpServers.notPermitted || agent.toolsNotOffered?.length
                  ? 'degraded'
                  : agent.incomplete || agent.mcpServers.unread
                    ? 'unknown'
                    : 'neutral'
              }
              source={`Latest change’s declarations · ${agent.mcpServers.unread} unread · ${agent.mcpServers.absent} absent · ${agent.mcpServers.notPermitted} not permitted`}
              linkLabel="MCP servers"
              onOpen={() => navigate('/kagent/mcpservers')}
            >
              {agent.incomplete ? '≥' : ''}
              {agent.mcpServers.declared} MCP {agent.mcpServers.declared === 1 && !agent.incomplete ? 'server' : 'servers'} · {agent.mcpServers.failing} failing · {agent.incomplete || agent.mcpServers.unread || agent.mcpServers.failing || agent.mcpServers.absent || agent.mcpServers.notPermitted
                ? agent.toolsNotOffered?.length ? `≥${agent.toolsNotOffered.length} missing tools` : 'Missing tools unknown'
                : `${agent.toolsNotOffered?.length ?? 0} missing tools`}
            </DimensionValue>
          </FactRow>
          <FactRow label="Sessions">
            <DimensionValue
              tone={session ? (session.crashed ? 'unhealthy' : 'neutral') : 'unknown'}
              source={sessionSource}
              linkLabel="Sessions"
              onOpen={() => goTab('sessions')}
            >
              {session
                ? `${session.complete ? '' : '≥'}${session.sessions.total} ${session.sessions.total === 1 && session.complete ? 'session' : 'sessions'} · ${session.complete ? '' : '≥'}${session.crashed} crashed · ${session.complete ? '' : '≥'}${session.onOtherRevisions} on other revisions`
                : runtime.completePage
                  ? 'No summary for this Agent in what Radar could read.'
                  : runtime.unavailable}
            </DimensionValue>
          </FactRow>
          {agent.newSessionsRevision && (
            <FactRow label="Placement">
              <DimensionValue
                tone={
                  !session || session.placement.state === 'unknown'
                    ? 'unknown'
                    : session.placement.state === 'mayFit'
                      ? 'neutral'
                      : 'degraded'
                }
                source={session?.placement.reading ?? 'Prepared revision’s Substrate template and active workers were not read'}
                linkLabel="Worker pool"
                onOpen={navigatePool ? () => navigate(substratePoolPath(navigatePool, connection.context)) : undefined}
              >
                {session
                  ? {
                      unknown: 'Placement unknown',
                      mayFit: 'May fit on an active worker',
                      noActiveWorkers: 'No active workers',
                      full: 'All actor slots booked',
                      noRoom: 'No active worker has room',
                    }[session.placement.state]
                  : 'Not read'}
              </DimensionValue>
            </FactRow>
          )}
          <FactRow label="Conversation outcomes">
            <DimensionValue tone="unknown" source="Conversation outcomes are not collected">
              Not visible to Radar
            </DimensionValue>
          </FactRow>
        </FactGrid>
      </PageSection>
      <div id="kagent-configuration">
        <PageSection title={`Configuration of the latest change${suffix}`}>
          {latest !== 'prepared' && agent.newSessionsRevision && (
            <p className="mb-3 text-xs text-theme-text-secondary">
              New sessions still run revision {revisionText(agent.newSessionsRevision)}, prepared before this change. The configuration
              below describes the latest change; Radar does not reconstruct that prepared revision’s configuration.
            </p>
          )}
          {agent.incomplete && (
            <Notice>
              Some declared references were not read or the template traversal reached its limit. The chain below is incomplete.
            </Notice>
          )}
          <FactGrid>
            {agent.references.map((r, i) => (
              <ReferenceLine key={`${refKey(r.from)}/${r.field}/${i}`} reference={r} data={data} onOpen={onOpen} />
            ))}
          </FactGrid>
          {agent.references.length === 0 && (
            <p className="text-sm text-theme-text-tertiary">
              {agent.incomplete ? 'References could not be read.' : 'No references declared.'}
            </p>
          )}
          {agent.incomplete && (
            <FoldSection title="Configuration from individual object reads" summary="Uses get access" attention={false}>
              <KagentAgentRenderer data={resource} onNavigate={(r) => onOpen({ ...r, group: r.group ?? '' })} />
            </FoldSection>
          )}
          {!!agent.cycle?.length && <Notice>Subagent template cycle: {agent.cycle.join(' → ')}</Notice>}
        </PageSection>
      </div>
      <PageSection title="About">
        <FactGrid>
          <FactRow label="Description">{agent.description || 'No description'}</FactRow>
          <FactRow label="Template">
            {agent.inlineTemplate ? (
              'Inline'
            ) : template ? (
              <KagentName object={template} onOpen={onOpen} />
            ) : (
              getKagentAgentTemplateRef(resource) || 'None declared'
            )}
          </FactRow>
          <FactRow label="Harness">
            {harnessRef && <KagentName object={harnessRef} onOpen={onOpen} />}{' '}
            <span className="text-theme-text-secondary">{agent.harnessType || harnessInfo?.type || 'Runtime not reported'}</span>
            {(revision.workloadImage || harnessInfo?.image) && <Sub>{revision.workloadImage || harnessInfo?.image}</Sub>}
          </FactRow>
          <FactRow label="Created">{agent.created ? <TimeValue timestamp={agent.created} /> : 'Not reported'}</FactRow>
        </FactGrid>
      </PageSection>
      <WorkspaceConditions title="kagent conditions" conditions={redactKagentLiterals(resource).status?.conditions ?? []} />
    </div>
  )
}

export function KagentDependencyOverview({ resource, onOpen }: { resource: any; onOpen: Open }) {
  const query = useKagentWorkspace([])
  const data = query.data
  if (!data && query.isLoading) return <PaneLoader label="Reading dependency configuration…" />
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
  const storedURL = resource.spec?.url
  const url = storedURL ? stripKagentURLCredentials(storedURL) : undefined
  const tools = server?.toolsOfferedKnown
    ? server.toolsOffered
    : reading?.state === 'ready'
      ? getKagentDiscoveredTools(redactKagentLiterals(resource))
      : undefined
  return (
    <div className="space-y-4">
      <RefreshFailedNotice queries={[query]} />
      {data && <KagentAttention data={data} onOpen={onOpen} agent={ref} />}
      <PageSection title="At a glance">
        <FactGrid>
          <FactRow label="Status">
            <DimensionValue tone={reading?.tone ?? 'unknown'} source={`kagent · ${reading?.condition ?? 'status.conditions'}`}>
              {model ? modelStatus(model.reading) : server ? mcpStatus(server) : (reading?.text ?? 'Not reported')}
            </DimensionValue>
          </FactRow>
          {ref.kind === 'ModelConfig' ? (
            <FactRow label="Credential">
              <DimensionValue tone={reading?.tone ?? 'unknown'} source="Declared Secret/key coordinates; values are never read">
                {model ? (
                  <CredentialText credential={model.credential} />
                ) : (
                  <ModelCredentialText spec={resource.spec ?? {}} namespace={ref.namespace} />
                )}
              </DimensionValue>
            </FactRow>
          ) : (
            <FactRow label="Tools offered">
              <DimensionValue tone={tools ? 'neutral' : 'unknown'} source="kagent controller discovery">
                {tools ? `${tools.length} tools` : 'Unknown — controller discovery did not publish a tool list'}
              </DimensionValue>
              {tools && (
                <FoldSection title="Tools offered" summary="" attention={false}>
                  <p className="text-xs">{tools.join(', ') || 'None'}</p>
                </FoldSection>
              )}
            </FactRow>
          )}
          <FactRow label="Used by">
            <DimensionValue
              tone={object?.usedBy.lowerBound || !object ? 'unknown' : 'neutral'}
              source="Agents’ current declared references"
              linkLabel="Used by"
              onOpen={() => document.getElementById('kagent-used-by')?.scrollIntoView({ block: 'start' })}
            >
              {object ? consumersText(object.usedBy) : 'Unknown · Agent inventory not read'}
            </DimensionValue>
          </FactRow>
        </FactGrid>
      </PageSection>
      <PageSection title="About">
        <FactGrid>
          {ref.kind === 'ModelConfig' ? (
            <>
              <FactRow label="Provider">
                {model?.provider || (resource.spec?.provider ? redactKagentURLsInText(resource.spec.provider) : 'Not reported')}
              </FactRow>
              <FactRow label="Model">
                {model?.model || (resource.spec?.model ? redactKagentURLsInText(resource.spec.model) : 'Not reported')}
              </FactRow>
            </>
          ) : (
            <>
              <FactRow label={storedURL && storedURL !== url ? 'URL (credentials removed)' : 'URL'}>
                <span className="break-all font-mono text-xs">{server?.url || url || 'Not reported'}</span>
              </FactRow>
              <FactRow label="Header sources">
                {server?.headers?.length
                  ? server.headers
                      .map(
                        (h) =>
                          `${h.name}: ${h.from ? `${h.from.kind} ${refName(h.from)} · key ${h.key}` : h.inline ? 'inline value (hidden)' : 'source not reported'}`,
                      )
                      .join('; ')
                  : server
                    ? 'No headers declared'
                    : 'Not read'}
              </FactRow>
            </>
          )}
          <FactRow label="Namespace">{ref.namespace}</FactRow>
        </FactGrid>
      </PageSection>
      <div id="kagent-used-by">
        {data && object ? (
          <UsedBy impact={object.usedBy} data={data} onOpen={onOpen} server={server} />
        ) : (
          <Notice>
            Used by: unknown. Radar needs list access to this kind and its Agent consumers to compose the inventory; this page’s object was
            read with get access.
          </Notice>
        )}
      </div>
      <WorkspaceConditions title="kagent conditions" conditions={redactKagentLiterals(resource).status?.conditions ?? []} />
    </div>
  )
}
