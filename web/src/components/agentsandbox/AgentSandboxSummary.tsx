import { Fragment, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import { OpenIssueContext, RefLink, WorkspaceProblemCallout, toneTextClass, type NavigateToResource, type ResourceRef, type StatusBadge, type WorkspaceProblem } from '@skyhook-io/k8s-ui'
import type { Relationships } from '@skyhook-io/k8s-ui/types/core'
import { genericConditionTone } from '@skyhook-io/k8s-ui/components/resources/renderers/GenericRenderer'
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
import { useRadarFeature, useResourceWithRelationships } from '../../api/client'
import { useNavCustomization } from '../../context/NavCustomization'
import { refToSelectedResource } from '../../utils/navigation'
import { issuesPathForSubject } from '../../utils/page-links'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { stateMessage } from './AgentSandboxTables'
import { claimSandboxName, sandboxController } from './lists'
import { AGENT_SANDBOX_EXTENSIONS_GROUP, AGENT_SANDBOX_GROUP } from './routes'
import { Dash, ExpiryCell, ReadingBadge } from './shared'
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

function PodFact({ answer, onNavigate }: { answer: PodAnswer; onNavigate: (ref: ResourceRef) => void }) {
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
    </>
  )
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
                  <PodFact answer={sandboxPod(detail.data?.relationships, detail.isLoading)} onNavigate={navigate} />
                </Fact>
              )}
              {(() => {
                const owner = sandboxController(resource)
                return (
                  <Fact label={owner?.kind === 'SandboxClaim' ? 'Claim' : 'Warm pool'}>
                    {owner ? <RefLink refTo={owner} onNavigate={navigate} mono /> : <Dash />}
                  </Fact>
                )
              })()}
              <Fact label="Expiry">
                <ExpiryCell resource={resource} />
              </Fact>
            </>
          )}
          {kind === 'SandboxClaim' && (
            <>
              <Fact label="Warm pool">
                {(() => {
                  const pool = getSandboxClaimWarmPool(resource)
                  if (pool === '-') return <Dash />
                  const relation = own.flatMap((a) => a.relations ?? []).find((r) => r.role === 'warmPool')
                  // The reading already says a missing pool is missing; its name is enough here.
                  if (relation && relation.presence !== 'present') return <span className="font-mono">{pool}</span>
                  return <RefLink refTo={{ kind: 'SandboxWarmPool', group: AGENT_SANDBOX_EXTENSIONS_GROUP, ...ns(pool) }} onNavigate={navigate} mono />
                })()}
              </Fact>
              <Fact label="Sandbox">
                {(() => {
                  const sandbox = claimSandboxName(resource)
                  return sandbox ? <RefLink refTo={{ kind: 'Sandbox', group: AGENT_SANDBOX_GROUP, ...ns(sandbox) }} onNavigate={navigate} mono /> : <Dash />
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
                  return t === '-' ? <Dash /> : <RefLink refTo={{ kind: 'SandboxTemplate', group: AGENT_SANDBOX_EXTENSIONS_GROUP, ...ns(t) }} onNavigate={navigate} mono />
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
