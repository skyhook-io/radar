import type { ReactNode } from 'react'
import { Bot, Boxes, Wrench } from 'lucide-react'
import type { ResourceRef } from '../../../types'
import { Badge } from '../../ui/Badge'
import { healthToSeverity } from '../../../utils/badge-colors'
import { AlertBanner, ConditionsSection, Property, PropertyList, ResourceLink, Section } from '../../ui/drawer-components'
import {
  KAGENT_GROUP,
  getKagentAgentEgressPolicies,
  getKagentAgentHarness,
  getKagentAgentRevisions,
  getKagentAgentTemplateRef,
  getKagentHarnessDeclarations,
  getKagentTemplateDeclarations,
  getKagentDiscoveredTools,
  isKagentServerRef,
  kagentRefKey,
  readKagentAgent,
  readKagentRemoteMCPServer,
  shortRevision,
  type KagentReading,
  type KagentRef,
  type KagentToolBinding,
} from '../resource-utils-kagent'

type Navigate = (ref: ResourceRef) => void

/** How the host read an object the Agent references. */
export type KagentRelatedRead =
  | { state: 'loading' }
  | { state: 'read'; object: any }
  | { state: 'notFound' }
  | { state: 'denied' }
  | { state: 'failed'; message?: string }

export interface KagentAgentRelated {
  /** The AgentTemplate spec.template.ref names. */
  template?: KagentRelatedRead
  /** The Harness spec.harnessRef names. */
  harness?: KagentRelatedRead
  /**
   * The RemoteMCPServers the template binds tools from, keyed by
   * kagentRefKey. kagent drops the tools of a server whose discovery failed
   * and still reports the Agent Ready, so their own status is the only sign.
   */
  mcpServers?: Record<string, KagentRelatedRead>
}

export interface KagentAgentRendererProps {
  data: any
  onNavigate?: Navigate
  /** Objects the host read for the dependencies an AgentTemplate or Harness declares. Without them those rows say they weren't read. */
  related?: KagentAgentRelated
  /**
   * A host's Sessions section for this Agent. Sessions are not Kubernetes
   * objects, so the host reads them elsewhere; rendered after the declared
   * dependencies when set.
   */
  sessions?: ReactNode
}

/** How many of an Agent's references a host should read, and the drawer list. */
export const KAGENT_AGENT_LIST_CAP = 8
const LIST_CAP = KAGENT_AGENT_LIST_CAP
const WARNINGS_CAP = 10

const SPEC_NOTE = 'Declared by the current spec. A dependency kagent can’t resolve is named in the status above; these rows don’t check that each object exists.'

function Bounded<T>({ items, cap = LIST_CAP, render }: { items: T[]; cap?: number; render: (item: T, index: number) => ReactNode }) {
  const shown = items.slice(0, cap)
  return <div className="space-y-1">
    {shown.map((item, i) => <div key={i}>{render(item, i)}</div>)}
    {items.length > cap && <div className="text-xs text-theme-text-tertiary">+{items.length - cap} more</div>}
  </div>
}

function readNote(read: KagentRelatedRead | undefined, kind: string): string | undefined {
  switch (read?.state) {
    case undefined: return `${kind} not read`
    case 'loading': return `Reading the ${kind}…`
    case 'notFound': return `${kind} not found`
    case 'denied': return `You can’t read this ${kind}`
    case 'failed': return `Couldn’t read the ${kind}${read.message ? `: ${read.message}` : ''}`
  }
  return undefined
}

function Unread({ children }: { children: ReactNode }) {
  return <span className="text-theme-text-tertiary">{children}</span>
}

/** A reference, linked when it names the kagent kind it defaults to. */
function RefValue({ ref: r, expectedKind, onNavigate }: { ref: KagentRef; expectedKind: string; onNavigate?: Navigate }) {
  const label = r.namespace ? `${r.namespace}/${r.name}` : r.name
  if (r.group !== KAGENT_GROUP || r.kind !== expectedKind) {
    return <span>{label} <span className="text-xs text-theme-text-tertiary">({r.kind}, {r.group})</span></span>
  }
  return <ResourceLink kind={expectedKind} group={KAGENT_GROUP} namespace={r.namespace} name={r.name} label={label} onNavigate={onNavigate} />
}

const BANNER_VARIANT = { unhealthy: 'error', alert: 'alert', degraded: 'warning', unknown: 'unknown', neutral: 'info', healthy: 'success' } as const

function bannerTitle(reading: KagentReading): string {
  switch (reading.state) {
    case 'blocked': return 'Latest change blocked'
    case 'failed': return 'Failed'
    case 'preparing': return reading.tone === 'alert' ? 'Still preparing' : 'Preparing'
    case 'stale': return 'Status not yet updated for the latest change'
    case 'notReported': return 'kagent hasn’t reported status yet'
    case 'deleting': return 'Deleting'
    default: return reading.text
  }
}

function StatusBanners({ reading }: { reading: KagentReading }) {
  const title = bannerTitle(reading)
  let message = reading.state === 'stale' ? reading.message : reading.summary
  // The summary leads with the same words as the title ("Latest change blocked: …").
  if (message?.startsWith(`${title}: `)) message = message.slice(title.length + 2)
  return <>
    {reading.state !== 'ready' && <AlertBanner variant={BANNER_VARIANT[reading.tone]} title={title} message={message === title ? undefined : message} />}
    {reading.egress && <AlertBanner
      variant="alert"
      title="Egress not applied"
      message={`${reading.egress.message ?? reading.egress.reason}. The runtime runs without these grants; kagent reports this apart from readiness.`}
    />}
  </>
}

function ModelRow({ label, model, from, onNavigate }: { label: string; model: KagentRef | undefined; from?: string; onNavigate?: Navigate }) {
  if (!model) return <Property label={label} value={from ? <Unread>{from}</Unread> : <Unread>None declared</Unread>} />
  return <Property label={label} value={<RefValue ref={model} expectedKind="ModelConfig" onNavigate={onNavigate} />} />
}

/** A referenced RemoteMCPServer's own status, or why it wasn't read. */
function ServerStatus({ binding, read }: { binding: KagentToolBinding; read: KagentRelatedRead | undefined }) {
  if (!binding.server || !isKagentServerRef(binding.server)) return null
  if (read?.state !== 'read') {
    const note = readNote(read, 'RemoteMCPServer')
    return note ? <Unread> · {note}</Unread> : null
  }
  const r = readKagentRemoteMCPServer(read.object)
  return <span> <Badge severity={healthToSeverity(r.tone)} size="sm">{r.text}</Badge></span>
}

/** Selected tools the server's discovery doesn't list: kagent drops them without a word. */
function MissingTools({ binding, read }: { binding: KagentToolBinding; read: KagentRelatedRead | undefined }) {
  if (read?.state !== 'read' || binding.tools.length === 0 || readKagentRemoteMCPServer(read.object).state !== 'ready') return null
  const offered = new Set(getKagentDiscoveredTools(read.object))
  const missing = binding.tools.filter(t => !offered.has(t))
  if (missing.length === 0) return null
  return <div className="text-xs text-theme-text-secondary">Not offered by the server, so the agent runs without: {missing.slice(0, 3).join(', ')}{missing.length > 3 ? ` +${missing.length - 3} more` : ''}</div>
}

/** The MCP servers whose discovery failed: the agent runs without their tools, though kagent reports it Ready. */
function failedServers(bindings: KagentToolBinding[], reads: Record<string, KagentRelatedRead> | undefined): string[] {
  const out: string[] = []
  for (const b of bindings) {
    if (!b.server || !isKagentServerRef(b.server)) continue
    const read = reads?.[kagentRefKey(b.server)]
    if (read?.state !== 'read') continue
    const state = readKagentRemoteMCPServer(read.object).state
    if (state === 'failed' || state === 'notReady') out.push(`${b.server.namespace}/${b.server.name}`)
  }
  return [...new Set(out)]
}

export function KagentAgentRenderer({ data, onNavigate, related, sessions }: KagentAgentRendererProps) {
  const reading = readKagentAgent(data)
  const namespace: string = data?.metadata?.namespace ?? ''
  const rev = getKagentAgentRevisions(data)
  const warnings: string[] = Array.isArray(data?.status?.warnings) ? data.status.warnings.filter((w: unknown) => typeof w === 'string' && w) : []
  const templateRef = getKagentAgentTemplateRef(data)
  const inline = data?.spec?.template?.inline
  const templateSpec = inline ?? (related?.template?.state === 'read' ? related.template.object?.spec : undefined)
  const template = templateSpec ? getKagentTemplateDeclarations(templateSpec, namespace) : undefined
  const templateUnread = !inline && templateRef ? readNote(related?.template, 'AgentTemplate') : undefined
  const harnessName = getKagentAgentHarness(data)
  const harness = related?.harness?.state === 'read' ? getKagentHarnessDeclarations(related.harness.object) : undefined
  const harnessUnread = harnessName ? readNote(related?.harness, 'Harness') : undefined
  const egress = getKagentAgentEgressPolicies(data)
  const mcp = template?.tools.filter(t => t.server) ?? []
  const subAgents = template?.tools.filter(t => t.subAgent) ?? []

  const failed = failedServers(mcp, related?.mcpServers)

  return <>
    <StatusBanners reading={reading} />
    {failed.length > 0 && <AlertBanner
      variant="alert"
      title={failed.length === 1 ? `MCP server ${failed[0]} failed tool discovery` : `${failed.length} MCP servers failed tool discovery`}
      message={`kagent drops the tools it can't discover, so this agent runs without them${reading.state === 'ready' ? ' even though it reads Ready' : ''}.${failed.length > 1 ? ` ${failed.slice(0, 3).join(', ')}${failed.length > 3 ? ` +${failed.length - 3} more` : ''}.` : ''}`}
    />}
    <Section title="Agent" icon={Bot} defaultExpanded>
      <PropertyList>
        <Property label="Status" value={<Badge severity={healthToSeverity(reading.tone)}>{reading.text}</Badge>} />
        <Property label="Reading" value={reading.summary} />
        <Property label="New sessions start on" value={rev.latestSuccessful
          ? <span className="font-mono text-xs" title={rev.latestSuccessful}>{`revision ${shortRevision(rev.latestSuccessful)}`}</span>
          : <Unread>No prepared revision yet, so no session can start</Unread>} />
        <Property label="Latest change" value={rev.desired
          ? <span><span className="font-mono text-xs" title={rev.desired}>{`revision ${shortRevision(rev.desired)}`}</span>{rev.desired === rev.latestSuccessful ? <span className="text-theme-text-tertiary"> (the same)</span> : null}</span>
          : <Unread>Not compiled yet</Unread>} />
        <Property label="Workload image" value={rev.workloadImage
          ? <span className="font-mono text-xs break-all">{rev.workloadImage}</span>
          : <Unread>Not reported</Unread>} />
        {warnings.length > 0 && <Property label="Warnings" value={<Bounded items={warnings} cap={WARNINGS_CAP} render={w => <span>{w}</span>} />} />}
        <Property label="Conversation outcomes" value={<Unread>Not visible to Radar</Unread>} />
      </PropertyList>
      <p className="mt-3 text-xs text-theme-text-tertiary">
        Existing sessions keep the revision they started on. Ready means kagent can start sessions; a Ready agent’s conversations can still fail, for example on model errors.
      </p>
    </Section>

    <Section title="Dependencies" icon={Boxes} defaultExpanded>
      <PropertyList>
        <Property label="Template" value={inline
          ? 'Inline'
          : templateRef
            ? <span><ResourceLink kind="AgentTemplate" group={KAGENT_GROUP} namespace={namespace} name={templateRef} onNavigate={onNavigate} />{templateUnread && <Unread> · {templateUnread}</Unread>}</span>
            : <Unread>None declared</Unread>} />
        <ModelRow label="Model" model={template?.model} from={templateUnread ? 'From the AgentTemplate, not read' : undefined} onNavigate={onNavigate} />
        <Property label="Harness" value={harnessName
          ? <span><ResourceLink kind="Harness" group={KAGENT_GROUP} namespace={namespace} name={harnessName} onNavigate={onNavigate} />{harness?.type && <span className="text-theme-text-tertiary"> · {harness.type}</span>}{harnessUnread && <Unread> · {harnessUnread}</Unread>}</span>
          : <Unread>None declared</Unread>} />
        {harness && <>
          <Property label="Worker pool" value={harness.workerPool
            ? <ResourceLink kind="WorkerPool" group="ate.dev" namespace={namespace} name={harness.workerPool} onNavigate={onNavigate} />
            : <Unread>None declared</Unread>} />
          {harness.image && <Property label="Harness image" value={<span className="font-mono text-xs break-all">{harness.image}</span>} />}
          {harness.summarizerModel && <ModelRow label="Summarizer model" model={harness.summarizerModel} onNavigate={onNavigate} />}
          {harness.memoryModel && <ModelRow label="Memory model" model={harness.memoryModel} onNavigate={onNavigate} />}
        </>}
        {egress.length > 0 && <Property label="Egress policies" value={<Bounded items={egress} render={name => <ResourceLink kind="EgressPolicy" group={KAGENT_GROUP} namespace={namespace} name={name} onNavigate={onNavigate} />} />} />}
      </PropertyList>
      <p className="mt-3 text-xs text-theme-text-tertiary">{SPEC_NOTE}</p>
    </Section>

    {(mcp.length > 0 || subAgents.length > 0 || (template?.skills.length ?? 0) > 0) && <Section title="Tools" icon={Wrench} defaultExpanded>
      <PropertyList>
        {mcp.length > 0 && <Property label="MCP servers" value={<Bounded items={mcp} render={binding => <span>
          <RefValue ref={binding.server!} expectedKind="RemoteMCPServer" onNavigate={onNavigate} />
          <ServerStatus binding={binding} read={isKagentServerRef(binding.server) ? related?.mcpServers?.[kagentRefKey(binding.server!)] : undefined} />
          <span className="text-theme-text-tertiary"> · {binding.tools.length === 0 ? 'every tool' : binding.tools.length <= 3 ? binding.tools.join(', ') : `${binding.tools.slice(0, 3).join(', ')} +${binding.tools.length - 3} more`}{binding.requireApproval ? ' · approval required' : ''}</span>
          <MissingTools binding={binding} read={isKagentServerRef(binding.server) ? related?.mcpServers?.[kagentRefKey(binding.server!)] : undefined} />
        </span>} />} />}
        {subAgents.length > 0 && <Property label="Subagents" value={<Bounded items={subAgents} render={binding => <span>
          {binding.subAgent!.name}
          {binding.subAgent!.template && <span className="text-theme-text-tertiary"> · <ResourceLink kind="AgentTemplate" group={KAGENT_GROUP} namespace={namespace} name={binding.subAgent!.template} onNavigate={onNavigate} /></span>}
        </span>} />} />}
        {(template?.skills.length ?? 0) > 0 && <Property label="Skills" value={<Bounded items={template!.skills} render={name => <span>{name}</span>} />} />}
      </PropertyList>
      <p className="mt-3 text-xs text-theme-text-tertiary">Declared by the current spec. Which tools an MCP server offers is on the server’s own page, from kagent’s discovery.</p>
    </Section>}

    {sessions}

    <ConditionsSection conditions={data?.status?.conditions} />
  </>
}
