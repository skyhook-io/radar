import { Fragment, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { useSearchParams } from 'react-router-dom'
import { OpenIssueContext, RefLink, WorkspaceProblemCallout, toneTextClass, type NavigateToResource, type ResourceRef, type StatusBadge, type WorkspaceProblem } from '@skyhook-io/k8s-ui'
import type { Relationships } from '@skyhook-io/k8s-ui/types/core'
import { genericConditionTone } from '@skyhook-io/k8s-ui/components/resources/renderers/GenericRenderer'
import { FATAL_WAITING_REASONS } from '@skyhook-io/k8s-ui/components/resources/resource-utils'
import {
  getReadyOfDesired,
  getSandboxClaimStatus,
  getSandboxClaimWarmPool,
  getSandboxStatus,
  getSandboxWarmPoolStatus,
  getSandboxWarmPoolTemplate,
} from '@skyhook-io/k8s-ui/components/resources/resource-utils-agent-runtimes'
import type { AgentSandboxAttention, AgentSandboxWorkspaceResponse } from '../../api/agentSandbox'
import { useAgentSandboxWorkspace } from '../../api/agentSandbox'
import { useRadarFeature, useResource, useResourceWithRelationships } from '../../api/client'
import { useNavCustomization } from '../../context/NavCustomization'
import { refToSelectedResource } from '../../utils/navigation'
import { issuesPathForSubject } from '../../utils/page-links'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { stateMessage } from './AgentSandboxTables'
import { claimSandboxName, sandboxController } from './lists'
import { AGENT_SANDBOX_EXTENSIONS_GROUP, AGENT_SANDBOX_GROUP } from './routes'
import { Absent, ExpiryCell, ReadingBadge, SourceText } from './shared'
import { attentionProblem } from './workspace'

// A Sandbox, SandboxClaim or SandboxWarmPool's composed Overview, wherever its
// drawer opens (the workspace, Issues, a resource list): the reading the
// workspace and Issues give it, from the same server rows, then the few facts
// that place it. The resource's own renderer, with its conditions and the
// controller's banner, is "Spec & status".

export type AgentSandboxSummaryKind = 'Sandbox' | 'SandboxClaim' | 'SandboxWarmPool'

type Subject = { kind: string; namespace: string; name: string }

const sameRef = (r: { kind: string; namespace?: string; name: string }, s: Subject) => r.kind === s.kind && (r.namespace ?? '') === s.namespace && r.name === s.name

/**
 * The server's rows that read this object. Its own rows when it has any;
 * otherwise the rows that fold it in: a warm pool's reserve row names its
 * sandboxes that aren't ready, the controller's row the Sandboxes waiting on
 * it, and a Sandbox's row stands for the claim it belongs to. `unknown` when
 * the server kept only some of its rows and none is about this object.
 */
export function objectReadings(data: AgentSandboxWorkspaceResponse, subject: Subject): { own: AgentSandboxAttention[]; folded: AgentSandboxAttention[]; unknown: boolean } {
  const own = data.attention.filter((a) => sameRef(a.subject, subject))
  if (own.length > 0) return { own, folded: [], unknown: false }
  const folded = data.attention.filter(
    (a) =>
      a.related?.refs.some((r) => sameRef(r, subject)) ||
      (subject.kind === 'SandboxClaim' && a.relations?.some((r) => r.role === 'owner' && sameRef(r.ref, subject))),
  )
  return { own, folded, unknown: folded.length === 0 && data.attentionTotal > data.attention.length }
}

/**
 * A row as the drawer shows it. The server names a Sandbox's Pod by the name
 * the controller would give it, without reading it; the drawer links the Pod
 * only from what Radar read, on its own line, so those refs are left out.
 * A refused suspension is the exception: its Pod is the one the controller
 * reported it won't delete, which by definition this Sandbox doesn't own, so
 * no owner-reference lookup can find it and the server's ref stays.
 */
export function drawerProblem(a: AgentSandboxAttention): WorkspaceProblem {
  const p = attentionProblem(a)
  if (a.rule === 'suspensionBlocked') return p
  const refs = p.related?.refs.filter((r) => r.kind !== 'Pod') ?? []
  return { ...p, related: p.related && refs.length > 0 ? { ...p.related, refs } : undefined }
}

export type PodAnswer =
  | { state: 'loading' }
  | { state: 'found'; pods: ResourceRef[] }
  /** Every Pod the viewer may read was checked, and none names this Sandbox as its owner. */
  | { state: 'none' }
  | { state: 'unknown' }

/** The Sandbox's Pod from its owner-reference children, never from its name. */
export function sandboxPod(relationships: Relationships | undefined, loading: boolean): PodAnswer {
  if (!relationships) return loading ? { state: 'loading' } : { state: 'unknown' }
  const pods = (relationships.children ?? []).filter((r) => r.kind === 'Pod' && !r.group)
  if (pods.length > 0) return { state: 'found', pods }
  return relationships.ownership?.children.complete ? { state: 'none' } : { state: 'unknown' }
}

function statusOf(kind: AgentSandboxSummaryKind, resource: any): StatusBadge {
  if (kind === 'Sandbox') return getSandboxStatus(resource)
  if (kind === 'SandboxClaim') return getSandboxClaimStatus(resource)
  return getSandboxWarmPoolStatus(resource)
}

/**
 * Conditions that report a problem the reading doesn't already explain. The
 * reading is Ready's; it explains PodScheduled when the scheduler is its
 * source and Suspended when it is a refused suspension.
 */
export function otherConditions(resource: any, status: StatusBadge, rows: AgentSandboxAttention[]): any[] {
  const conditions: any[] = Array.isArray(resource?.status?.conditions) ? resource.status.conditions : []
  const covered = new Set(['Ready'])
  const scheduled = conditions.find((c) => c?.type === 'PodScheduled')
  if (rows.some((a) => a.rawDetailSource === 'scheduler') || (scheduled?.status === 'False' && scheduled.reason === status.text)) covered.add('PodScheduled')
  if (status.text === 'Suspension blocked' || rows.some((a) => a.rule === 'suspensionBlocked')) covered.add('Suspended')
  return conditions.filter((c) => c?.type && !covered.has(c.type) && ['fail', 'warning', 'unclear'].includes(genericConditionTone(resource, c)))
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-theme-text-tertiary">{label}</dt>
      <dd className="min-w-0 break-words text-theme-text-primary">{children}</dd>
    </>
  )
}

function PodFact({ answer, phase, onNavigate }: { answer: PodAnswer; phase?: string; onNavigate: (ref: ResourceRef) => void }) {
  switch (answer.state) {
    case 'loading':
      return <span className="text-theme-text-tertiary">Reading…</span>
    case 'none':
      return <span className="text-theme-text-secondary">none: no Pod Radar can read names this Sandbox as its owner</span>
    case 'unknown':
      return <span className="text-theme-text-tertiary">unknown: Radar couldn’t read every Pod that could belong to it</span>
  }
  return (
    <>
      {answer.pods.map((p, i) => (
        <Fragment key={p.name}>
          {i > 0 && ', '}
          <RefLink refTo={p} onNavigate={onNavigate} mono />
        </Fragment>
      ))}
      {phase && <span className="text-theme-text-secondary"> · {phase}</span>}
    </>
  )
}

export interface TemplateContainer {
  name: string
  image?: string
  init: boolean
}

/** The containers of a Sandbox's Pod template (spec.podTemplate.spec), init containers first. */
export function templateContainers(resource: any): TemplateContainer[] {
  const spec = resource?.spec?.podTemplate?.spec
  const list = (items: unknown, init: boolean): TemplateContainer[] =>
    Array.isArray(items) ? items.filter((c: any) => typeof c?.name === 'string').map((c: any) => ({ name: c.name, image: typeof c.image === 'string' && c.image ? c.image : undefined, init })) : []
  return [...list(spec?.initContainers, true), ...list(spec?.containers, false)]
}

export type ContainerState =
  | { state: 'waiting'; reason?: string; message?: string }
  | { state: 'terminated'; reason?: string; message?: string; exitCode?: number }
  | { state: 'running' }

/** A container's current state as the Pod Radar read reports it; undefined when the Pod reports none for it. */
export function containerState(pod: any, name: string, init: boolean): ContainerState | undefined {
  const statuses: any[] = (init ? pod?.status?.initContainerStatuses : pod?.status?.containerStatuses) ?? []
  const st = Array.isArray(statuses) ? statuses.find((c) => c?.name === name)?.state : undefined
  if (st?.waiting) return { state: 'waiting', reason: st.waiting.reason, message: st.waiting.message }
  if (st?.terminated) return { state: 'terminated', reason: st.terminated.reason, message: st.terminated.message, exitCode: st.terminated.exitCode }
  if (st?.running) return { state: 'running' }
  return undefined
}

function StateLine({ s }: { s: ContainerState }) {
  if (s.state === 'running') return <div className="text-xs text-theme-text-secondary">Running</div>
  const bad = s.state === 'waiting' ? !!s.reason && FATAL_WAITING_REASONS.has(s.reason) : s.exitCode !== 0
  const head =
    s.state === 'waiting'
      ? `Waiting${s.reason ? `: ${s.reason}` : ''}`
      : `Exited${s.exitCode !== undefined ? ` with code ${s.exitCode}` : ''}${s.reason ? ` (${s.reason})` : ''}`
  return (
    <div className="mt-0.5 space-y-0.5">
      <div className={clsx('text-xs', bad ? toneTextClass('unhealthy') : 'text-theme-text-secondary')}>{head}</div>
      {s.message && <SourceText text={s.message} />}
    </div>
  )
}

/**
 * The images the Sandbox's Pod template asks for, each with its container's
 * state where Radar read the Pod. Without the Pod, only the template.
 */
function ContainersFact({ containers, pod }: { containers: TemplateContainer[]; pod?: any }) {
  const one = containers.length === 1
  return (
    <ul className="space-y-1.5">
      {containers.map((c) => {
        const s = pod ? containerState(pod, c.name, c.init) : undefined
        return (
          <li key={`${c.init ? 'init/' : ''}${c.name}`} className="min-w-0">
            {!one && (
              <div className="text-xs text-theme-text-tertiary">
                {c.name}
                {c.init && ' (init)'}
              </div>
            )}
            {c.image ? <div className="break-all font-mono text-[12.5px]">{c.image}</div> : <Absent>no image set</Absent>}
            {s && <StateLine s={s} />}
          </li>
        )
      })}
    </ul>
  )
}

/** A Sandbox's owner, from its owner references: a claim or warm pool linked, another owner named, or none. */
function OwnerFact({ owner, resource, onNavigate }: { owner: ReturnType<typeof sandboxController>; resource: any; onNavigate: (ref: ResourceRef) => void }) {
  if (owner) return <RefLink refTo={owner} onNavigate={onNavigate} mono />
  const refs: any[] = Array.isArray(resource?.metadata?.ownerReferences) ? resource.metadata.ownerReferences : []
  const other = refs.find((r) => r?.controller) ?? refs[0]
  if (other?.kind && other?.name) {
    return (
      <span>
        {other.kind} <span className="font-mono">{other.name}</span>
      </span>
    )
  }
  return <Absent>none: no warm pool or claim owns it</Absent>
}

function StateFact({ resource, status }: { resource: any; status: StatusBadge }) {
  const message = stateMessage(resource, status)
  return (
    <>
      <ReadingBadge status={status} />
      {message && <div className="mt-0.5 text-xs text-theme-text-secondary">{message}</div>}
    </>
  )
}

export function AgentSandboxSummary({
  kind,
  apiKind,
  group,
  namespace,
  name,
  resource,
  onNavigate,
  leadContent,
}: {
  kind: AgentSandboxSummaryKind
  apiKind: string
  group?: string
  namespace: string
  name: string
  resource: any
  onNavigate?: NavigateToResource
  leadContent?: ReactNode
}) {
  const { support } = useRadarFeature('agentSandboxWorkspace')
  const query = useAgentSandboxWorkspace([namespace], { enabled: support !== 'unsupported' })
  // The drawer's own read of this object: its owner-reference children are the Pods Radar read.
  const detail = useResourceWithRelationships<any>(apiKind, namespace, name, group)
  const podAnswer = sandboxPod(detail.data?.relationships, detail.isLoading)
  // The Pod's own state only for the one Pod its owner references name: never a guess among several.
  const onePod = kind === 'Sandbox' && podAnswer.state === 'found' && podAnswer.pods.length === 1 ? podAnswer.pods[0] : undefined
  const pod = useResource<any>('pods', namespace, onePod?.name ?? '', undefined, { enabled: !!onePod })
  const podRead = onePod && pod.data?.metadata?.name === onePod.name ? pod.data : undefined
  const navigateTo = useWorkspaceNavigate()
  const [params] = useSearchParams()
  const issuesTakenOver = !!useNavCustomization().fleetTakeoverHref?.('issues')
  const navigate = (ref: ResourceRef) => onNavigate?.(refToSelectedResource(ref))

  const subject = { kind, namespace, name }
  const status = statusOf(kind, resource)
  const readings = query.data ? objectReadings(query.data, subject) : undefined
  const own = readings?.own ?? []
  const folded = readings?.folded ?? []
  // Radar's operational issues stand in until the server's reading is known,
  // and wherever it has none about this object.
  const pending = support !== 'unsupported' && query.isLoading
  const showIssues = own.length === 0 && !pending

  const callout = (a: AgentSandboxAttention) => (
    <WorkspaceProblemCallout key={a.id} problem={drawerProblem(a)} rootKind={kind} subjectIsSelf={sameRef(a.subject, subject)} onNavigate={navigate} />
  )
  const others = otherConditions(resource, status, [...own, ...folded])
  // The Pod of a refused suspension isn't owned by the Sandbox: an owner-reference "none" would contradict the reading.
  const suspensionBlocked = own.some((a) => a.rule === 'suspensionBlocked') || (own.length === 0 && status.text === 'Suspension blocked')
  const ns = (n: string) => ({ namespace, name: n })

  return (
    <OpenIssueContext.Provider value={issuesTakenOver ? undefined : (p) => navigateTo(issuesPathForSubject(p.subject, params.get('namespaces')))}>
      <div className="space-y-4 p-4">
        {own.length + folded.length > 0 && <div className="space-y-3">{[...own, ...folded].map(callout)}</div>}
        {showIssues && leadContent}
        <dl className="grid grid-cols-[7.5rem_minmax(0,1fr)] items-baseline gap-x-4 gap-y-2 text-sm" aria-label={`${kind} at a glance`}>
          {own.length === 0 && (
            <Fact label="State">
              <StateFact resource={resource} status={status} />
            </Fact>
          )}
          {kind === 'Sandbox' && (
            <>
              {!suspensionBlocked && (
                <Fact label="Pod">
                  <PodFact answer={podAnswer} phase={typeof podRead?.status?.phase === 'string' ? podRead.status.phase : undefined} onNavigate={navigate} />
                </Fact>
              )}
              {(() => {
                const containers = templateContainers(resource)
                if (containers.length === 0) return null
                return (
                  <Fact label={containers.length === 1 ? 'Image' : 'Images'}>
                    <ContainersFact containers={containers} pod={podRead} />
                  </Fact>
                )
              })()}
              {(() => {
                const owner = sandboxController(resource)
                return (
                  <>
                    <Fact label={owner?.kind === 'SandboxClaim' ? 'Claim' : owner?.kind === 'SandboxWarmPool' ? 'Warm pool' : 'Owner'}>
                      <OwnerFact owner={owner} resource={resource} onNavigate={navigate} />
                    </Fact>
                    <Fact label="Expiry">
                      <ExpiryCell resource={resource} claimed={owner?.kind === 'SandboxClaim'} />
                    </Fact>
                  </>
                )
              })()}
            </>
          )}
          {kind === 'SandboxClaim' && (
            <>
              <Fact label="Warm pool">
                {(() => {
                  const pool = getSandboxClaimWarmPool(resource)
                  if (pool === '-') return <Absent>no warm pool</Absent>
                  const relation = own.flatMap((a) => a.relations ?? []).find((r) => r.role === 'warmPool')
                  // The reading already says a missing pool is missing; its name is enough here.
                  if (relation && relation.presence !== 'present') return <span className="font-mono">{pool}</span>
                  return <RefLink refTo={{ kind: 'SandboxWarmPool', group: AGENT_SANDBOX_EXTENSIONS_GROUP, ...ns(pool) }} onNavigate={navigate} mono />
                })()}
              </Fact>
              <Fact label="Sandbox">
                {(() => {
                  const sandbox = claimSandboxName(resource)
                  return sandbox ? <RefLink refTo={{ kind: 'Sandbox', group: AGENT_SANDBOX_GROUP, ...ns(sandbox) }} onNavigate={navigate} mono /> : <Absent title="The claim’s status names no Sandbox now.">not bound</Absent>
                })()}
              </Fact>
              <Fact label="Expiry">
                <ExpiryCell resource={resource} />
              </Fact>
            </>
          )}
          {kind === 'SandboxWarmPool' && (
            <>
              <Fact label="Ready">
                <span className="font-mono">{getReadyOfDesired(resource)}</span>
              </Fact>
              <Fact label="Template">
                {(() => {
                  const t = getSandboxWarmPoolTemplate(resource)
                  return t === '-' ? <Absent>no template</Absent> : <RefLink refTo={{ kind: 'SandboxTemplate', group: AGENT_SANDBOX_EXTENSIONS_GROUP, ...ns(t) }} onNavigate={navigate} mono />
                })()}
              </Fact>
            </>
          )}
        </dl>
        {others.length > 0 && (
          <section aria-label="Other conditions">
            <h3 className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">Also reported</h3>
            <ul className="space-y-1 text-sm">
              {others.map((c) => (
                <li key={c.type} className="break-words">
                  <span className={toneTextClass(genericConditionTone(resource, c) === 'warning' ? 'degraded' : 'unhealthy')}>
                    {c.type}={c.status}
                  </span>
                  {c.reason && <span className="text-theme-text-tertiary"> ({c.reason})</span>}
                  {c.message && <div className="text-xs text-theme-text-secondary">{c.message}</div>}
                </li>
              ))}
            </ul>
          </section>
        )}
      </div>
    </OpenIssueContext.Provider>
  )
}
