import { Fragment, useMemo, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { useSearchParams } from 'react-router-dom'
import {
  DimensionValue,
  FactGrid,
  FactRow,
  OpenIssueContext,
  RefLink,
  SectionHeading,
  StatusDot,
  Tooltip,
  WorkspaceCard,
  WorkspaceProblemCallout,
  formatAge,
  toneTextClass,
  type HealthLevel,
  type NavigateToResource,
  type ResourceRef,
  type StatusBadge,
  type WorkspaceProblem,
} from '@skyhook-io/k8s-ui'
import { genericConditionTone } from '@skyhook-io/k8s-ui/components/resources/renderers/GenericRenderer'
import { FATAL_WAITING_REASONS } from '@skyhook-io/k8s-ui/components/resources/resource-utils'
import {
  getAgentRuntimeStatus,
  getSandboxWarmPoolReadyOfDesired,
  getSandboxClaimStatus,
  getSandboxClaimWarmPool,
  getSandboxOperatingMode,
  getSandboxStatus,
  getSandboxWarmPoolStatus,
  getSandboxWarmPoolTemplate,
  sandboxPodImagePullNextStep,
  sandboxPodName,
  podOwnersCommand,
} from '@skyhook-io/k8s-ui/components/resources/resource-utils-agent-runtimes'
import type { AgentSandboxAttention, AgentSandboxWorkspaceResponse } from '../../api/agentSandbox'
import { useAgentSandboxWorkspace } from '../../api/agentSandbox'
import { useRadarFeature, useResource, useResourceEvents, useResourceWithRelationships } from '../../api/client'
import { useNavCustomization } from '../../context/NavCustomization'
import { useNamespacedCapabilities } from '../../contexts/CapabilitiesContext'
import { buildWorkloadPath, refToSelectedResource } from '../../utils/navigation'
import { issuesPathForSubject } from '../../utils/page-links'
import { CommandChip } from '../substrate/shared'
import { SectionTable } from '../workspace/layout'
import { WorkspaceConditions } from '../workspace/WorkspaceConditions'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { poolClaimsPath, poolNotReadyPath, stateMessage } from './AgentSandboxTables'
import { readInNamespace, readItems, useAgentSandboxLists } from './data'
import { objectKey, readingLabel, schedulerReason, serverReadings, warmPoolBadge } from './labels'
import { claimSandboxName, poolClaims, sandboxController, sandboxPod, type PodAnswer } from './lists'
import { AGENT_SANDBOX_EXTENSIONS_GROUP, AGENT_SANDBOX_GROUP, SANDBOXES_PATH, SANDBOX_WARM_POOLS_PATH } from './routes'
import { Absent, ExpiryCell, KIND_LIST_PERMISSION, ReadingBadge, SourceText } from './shared'
import { attentionProblem } from './workspace'

// A Sandbox, SandboxClaim or SandboxWarmPool's composed Overview, wherever its
// drawer or full view opens: one problem callout, explicit drawer actions,
// At a glance, About, and folded Controller conditions. Framed cards on full
// pages, flat sections in drawers (never cards inside cards).

export type AgentSandboxSummaryKind = 'Sandbox' | 'SandboxClaim' | 'SandboxWarmPool'

type Subject = { kind: string; namespace: string; name: string }

const sameRef = (r: { kind: string; namespace?: string; name: string }, s: Subject) =>
  r.kind === s.kind && (r.namespace ?? '') === s.namespace && r.name === s.name

const RANK: Record<HealthLevel, number> = { unhealthy: 0, alert: 1, degraded: 2, unknown: 3, healthy: 4, neutral: 5 }
const LOG_FAILURE_REASONS = new Set(['PodFailed', 'CrashLoopBackOff'])

function findCondition(resource: any, type: string): any | undefined {
  const conditions = resource?.status?.conditions
  return Array.isArray(conditions) ? conditions.find((c: any) => c?.type === type) : undefined
}

/** First non-zero terminated container exit summary from a read Pod, if any. */
export function podExitSummary(pod: any): string | undefined {
  if (!pod) return undefined
  const statuses: any[] = [
    ...(Array.isArray(pod?.status?.initContainerStatuses) ? pod.status.initContainerStatuses : []),
    ...(Array.isArray(pod?.status?.containerStatuses) ? pod.status.containerStatuses : []),
  ]
  for (const cs of statuses) {
    const t = cs?.state?.terminated ?? cs?.lastState?.terminated
    if (t && typeof t.exitCode === 'number' && t.exitCode !== 0) {
      const container = typeof cs?.name === 'string' && cs.name ? `Container "${cs.name}"` : 'Container'
      return `${container} exited with code ${t.exitCode}${t.reason ? ` (${t.reason})` : ''}.`
    }
  }
  return undefined
}

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
 * A row as the drawer/page callout shows it:
 * - Drops the server's name-guessed Pod ref (the Pod row links the real owned Pod),
 *   except for suspensionBlocked where the conflicting Pod is foreign by definition.
 * - Strips inline parenthetical kubectl commands from nextStep so the command renders
 *   as a clean CommandChip in the Conflicting Pod row rather than prose.
 * - Adds the container exit code to PodFailed / CrashLoopBackOff callouts when the Pod was read.
 */
export function drawerProblem(a: AgentSandboxAttention, pod?: any): WorkspaceProblem {
  const p = attentionProblem(a)
  if (a.rule === 'suspensionBlocked') {
    const quoted = a.command ? ` (${a.command})` : ''
    const nextStep = quoted && p.nextStep?.includes(quoted) ? p.nextStep.replace(quoted, '') : p.nextStep
    return { ...p, nextStep }
  }
  const refs = p.related?.refs.filter((r) => r.kind !== 'Pod') ?? []
  const nextStep = (a.subject.kind === 'Sandbox' && a.rule === 'failed' && sandboxPodImagePullNextStep(a.reason, pod)) || p.nextStep
  const exit = a.subject.kind === 'Sandbox' && a.rule === 'failed' && a.reason && LOG_FAILURE_REASONS.has(a.reason) ? podExitSummary(pod) : undefined
  const detail = [exit, p.detail].filter(Boolean).join(' ') || undefined
  return { ...p, detail, nextStep, related: p.related && refs.length > 0 ? { ...p.related, refs } : undefined }
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

function FlatSection({ title, children, className }: { title?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={clsx('border-t border-theme-border-subtle pt-3 first:border-t-0 first:pt-0', className)}>
      {title && <SectionHeading>{title}</SectionHeading>}
      {children}
    </section>
  )
}

function PodFact({
  answer,
  phase,
  suspendedConfirmed,
  canViewLogs,
  onNavigate,
  onOpenLogs,
}: {
  answer: PodAnswer
  phase?: string
  suspendedConfirmed: boolean
  canViewLogs: boolean
  onNavigate: (ref: ResourceRef) => void
  onOpenLogs?: (pod: ResourceRef) => void
}) {
  if (suspendedConfirmed && answer.state === 'unknown') {
    return (
      <div>
        <div className="text-theme-text-secondary">Terminated (reported by controller)</div>
        <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">
          Pod visibility unknown — Radar couldn’t read every Pod that could belong to it
        </div>
      </div>
    )
  }
  switch (answer.state) {
    case 'loading':
      return <span className="text-theme-text-tertiary">Reading…</span>
    case 'none':
      return <span className="text-theme-text-secondary">none: no Pod Radar can read names this Sandbox as its owner</span>
    case 'unknown':
      return <span className="text-theme-text-tertiary">unknown: Radar couldn’t read every Pod that could belong to it</span>
  }
  const single = answer.pods.length === 1 ? answer.pods[0] : undefined
  return (
    <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-0.5">
      <span>
        {answer.pods.map((p, i) => (
          <Fragment key={p.name}>
            {i > 0 && ', '}
            <RefLink refTo={p} onNavigate={onNavigate} mono />
          </Fragment>
        ))}
        {phase && <span className="text-theme-text-secondary"> · {phase}</span>}
      </span>
      {single && canViewLogs && onOpenLogs && (
        <button type="button" onClick={() => onOpenLogs(single)} className="text-xs text-accent-text hover:underline">
          Logs →
        </button>
      )}
    </span>
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
  const bad = s.state === 'waiting' ? !!s.reason && FATAL_WAITING_REASONS.has(s.reason) : s.exitCode !== undefined && s.exitCode !== 0
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

function ContainersFact({ containers }: { containers: TemplateContainer[] }) {
  const one = containers.length === 1
  return (
    <ul className="space-y-1.5">
      {containers.map((c) => {
        return (
          <li key={`${c.init ? 'init/' : ''}${c.name}`} className="min-w-0">
            {!one && (
              <div className="text-xs text-theme-text-tertiary">
                {c.name}
                {c.init && ' (init)'}
              </div>
            )}
            {c.image ? <div className="break-all font-mono text-[12.5px]">{c.image}</div> : <Absent>no image set</Absent>}
          </li>
        )
      })}
    </ul>
  )
}

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

function CreatedValue({ timestamp }: { timestamp?: string }) {
  if (!timestamp) return <Absent>unknown</Absent>
  return (
    <Tooltip content={new Date(timestamp).toLocaleString()}>
      <time dateTime={timestamp} className="text-theme-text-secondary">
        {formatAge(timestamp)} ago
      </time>
    </Tooltip>
  )
}

interface SummaryAction {
  label: string
  onClick: () => void
  primary?: boolean
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
  framed = false,
}: {
  kind: AgentSandboxSummaryKind
  apiKind: string
  group?: string
  namespace: string
  name: string
  resource: any
  onNavigate?: NavigateToResource
  leadContent?: ReactNode
  /** Framed cards on full pages; flat sections in drawers (never cards inside cards). */
  framed?: boolean
}) {
  const { support } = useRadarFeature('agentSandboxWorkspace')
  const query = useAgentSandboxWorkspace([namespace], { enabled: support !== 'unsupported' })
  const detail = useResourceWithRelationships<any>(apiKind, namespace, name, group)
  const podAnswer = sandboxPod(detail.data?.relationships, detail.isLoading)
  const onePod = kind === 'Sandbox' && podAnswer.state === 'found' && podAnswer.pods.length === 1 ? podAnswer.pods[0] : undefined
  const pod = useResource<any>('pods', namespace, onePod?.name ?? '', undefined, { enabled: !!onePod })
  const podRead = onePod && pod.data?.metadata?.name === onePod.name ? pod.data : undefined
  // Warm pools fetch namespace Sandboxes & Claims to list members (failures first) and referencing claims.
  const lists = useAgentSandboxLists({ namespaces: [namespace], apiResources: undefined, enabled: kind === 'SandboxWarmPool' })
  const events = useResourceEvents(apiKind, namespace, name, group)
  const logs = useNamespacedCapabilities(onePod?.namespace)
  const canViewLogs = logs.canViewLogs && !logs.logsUnknown
  const navigateTo = useWorkspaceNavigate()
  const [params] = useSearchParams()
  const issuesTakenOver = !!useNavCustomization().fleetTakeoverHref?.('issues')
  const navigate = (ref: ResourceRef) => onNavigate?.(refToSelectedResource(ref))
  const openPodLogs = (p: ResourceRef) => navigateTo(buildWorkloadPath({ kind: 'pods', namespace: p.namespace || namespace, name: p.name, tab: 'logs' }))

  const containers = kind === 'Sandbox' ? templateContainers(resource) : []
  const subject = { kind, namespace, name }
  const status = statusOf(kind, resource)
  const server = useMemo(() => serverReadings(query.data), [query.data])
  const readings = query.data ? objectReadings(query.data, subject) : undefined
  const own = readings?.own ?? []
  const folded = readings?.folded ?? []
  const pending = support !== 'unsupported' && query.isLoading
  const showIssues = own.length === 0 && !pending

  const waitingContainers = containers.flatMap((container) => {
    const state = containerState(podRead, container.name, container.init)
    return state?.state === 'waiting' && state.message ? [{ container, state }] : []
  })
  const effectiveLevel: HealthLevel = server.tones.get(objectKey(kind, namespace, name)) ?? status.level
  const suspensionBlocked = own.some((a) => a.rule === 'suspensionBlocked') || (own.length === 0 && status.text === 'Suspension blocked')
  const suspendedCond = kind === 'Sandbox' ? findCondition(resource, 'Suspended') : undefined
  const suspendedConfirmed = kind === 'Sandbox' && status.text === 'Suspended' && suspendedCond?.status === 'True'
  const conflictingPodName = suspensionBlocked ? sandboxPodName(resource) : ''
  const conflictingCommand =
    own.find((a) => a.rule === 'suspensionBlocked')?.command ??
    (conflictingPodName ? podOwnersCommand(namespace, conflictingPodName) : '')

  const callout = (a: AgentSandboxAttention) => {
    const isSelf = sameRef(a.subject, subject)
    const showLogsBtn = framed && isSelf && kind === 'Sandbox' && a.rule === 'failed' && !!a.reason && LOG_FAILURE_REASONS.has(a.reason) && !!onePod && canViewLogs
    return (
      <WorkspaceProblemCallout
        key={a.id}
        problem={drawerProblem(a, isSelf ? podRead : undefined)}
        rootKind={kind}
        subjectIsSelf={isSelf}
        onNavigate={navigate}
        action={
          showLogsBtn && onePod ? (
            <button type="button" onClick={() => openPodLogs(onePod)} className="whitespace-nowrap text-accent-text hover:underline">
              Pod logs →
            </button>
          ) : undefined
        }
      />
    )
  }

  const others = otherConditions(resource, status, [...own, ...folded])
  const conditions: any[] = Array.isArray(resource?.status?.conditions) ? resource.status.conditions : []
  const ns = (n: string) => ({ namespace, name: n })

  // Warm pool members (failures first) and referencing claims.
  const poolMembers = useMemo(() => {
    if (kind !== 'SandboxWarmPool') return [] as { name: string; tone: HealthLevel; label: string }[]
    const fromList = readItems(lists.data.sandboxes)
      .filter((s) => {
        const owner = sandboxController(s)
        return owner?.kind === 'SandboxWarmPool' && owner.name === name && (s?.metadata?.namespace ?? '') === namespace
      })
      .map((s) => {
        const st = getSandboxStatus(s)
        const sName = s?.metadata?.name ?? ''
        const tone = server.tones.get(objectKey('Sandbox', namespace, sName)) ?? st.level
        return { name: sName, tone, label: readingLabel(st).label }
      })
    if (fromList.length > 0) {
      return fromList.sort((a, b) => RANK[a.tone] - RANK[b.tone] || a.name.localeCompare(b.name))
    }
    // Fallback to owner-ref children while list is loading or if list was denied.
    const children = (detail.data?.relationships?.children ?? []).filter((r) => r.kind === 'Sandbox')
    const notReadyNames = new Set(own.flatMap((a) => a.related?.refs.filter((r) => r.kind === 'Sandbox').map((r) => r.name) ?? []))
    return children
      .map((c) => {
        const isNotReady = notReadyNames.has(c.name)
        return { name: c.name, tone: (isNotReady ? 'alert' : 'unknown') as HealthLevel, label: isNotReady ? 'Not ready' : 'Status unread' }
      })
      .sort((a, b) => RANK[a.tone] - RANK[b.tone] || a.name.localeCompare(b.name))
  }, [kind, lists.data.sandboxes, detail.data?.relationships?.children, name, namespace, server, own])

  const poolRefClaims = useMemo(() => {
    if (kind !== 'SandboxWarmPool' || !readInNamespace(lists.data.claims, namespace)) return undefined
    const served = poolClaims(resource, readItems(lists.data.claims))
    const bound = served.filter((c) => claimSandboxName(c) && getSandboxClaimStatus(c).text !== 'Expired').length
    const expired = served.filter((c) => getSandboxClaimStatus(c).text === 'Expired').length
    return { items: served, bound, expired }
  }, [kind, lists.data.claims, namespace, resource])

  const latestWarningEvent = useMemo(() => {
    if (kind !== 'SandboxWarmPool') return undefined
    return events.k8sEvents.find((e) => e.reason === 'WarmPoolNotProgressing' || e.eventType === 'Warning')
  }, [kind, events.k8sEvents])

  // Explicit action bar in drawer mode (matches CNPG's cluster drawer actions).
  const drawerActions: SummaryAction[] = useMemo(() => {
    if (framed) return []
    const openLabel = kind === 'Sandbox' ? 'Open sandbox' : kind === 'SandboxClaim' ? 'Open claim' : 'Open warm pool'
    const actions: SummaryAction[] = [
      {
        label: openLabel,
        primary: true,
        onClick: () => navigateTo(buildWorkloadPath({ kind: apiKind, group, namespace, name })),
      },
    ]
    if (kind === 'Sandbox') {
      if (suspensionBlocked && conflictingPodName) {
        actions.push({
          label: 'Open conflicting Pod',
          onClick: () => navigate({ kind: 'Pod', group: '', namespace, name: conflictingPodName }),
        })
      } else if (onePod) {
        if (canViewLogs) {
          actions.push({ label: 'Pod logs', onClick: () => openPodLogs(onePod) })
        }
        actions.push({ label: 'Open Pod', onClick: () => navigate(onePod) })
      }
    } else if (kind === 'SandboxClaim') {
      const boundSb = claimSandboxName(resource)
      if (boundSb) {
        actions.push({ label: 'Open Sandbox', onClick: () => navigate({ kind: 'Sandbox', group: AGENT_SANDBOX_GROUP, namespace, name: boundSb }) })
      }
      actions.push({
        label: 'Browse warm pools',
        onClick: () => navigateTo(`${SANDBOX_WARM_POOLS_PATH}&namespaces=${encodeURIComponent(namespace)}`),
      })
    } else if (kind === 'SandboxWarmPool') {
      actions.push({
        label: 'View sandboxes',
        onClick: () => navigateTo(`${SANDBOXES_PATH}&pool=${encodeURIComponent(`${namespace}/${name}`)}`),
      })
      actions.push({
        label: 'View claims',
        onClick: () => navigateTo(poolClaimsPath(namespace, name)),
      })
    }
    return actions
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [framed, kind, apiKind, group, namespace, name, suspensionBlocked, conflictingPodName, onePod, canViewLogs, resource])

  const Frame = framed ? WorkspaceCard : FlatSection

  const claimPoolInfo = useMemo(() => {
    if (kind !== 'SandboxClaim') return undefined
    const pool = getSandboxClaimWarmPool(resource)
    if (pool === '-') return { pool: undefined as string | undefined }
    const fromAttention = own.flatMap((a) => a.relations ?? []).find((r) => r.role === 'warmPool')
    const poolRow = query.data?.warmPools.find((w) => w.namespace === namespace && w.name === pool)
    let presence: 'present' | 'absent' | 'unread' | 'unknown' = 'unknown'
    if (poolRow) presence = 'present'
    else if (fromAttention?.ref.name === pool) presence = fromAttention.presence
    else if (query.data) {
      const cov = query.data.coverage.warmPools
      const covered = cov.state === 'notInstalled' || cov.state === 'full' || cov.allowedNamespaces?.includes(namespace)
      presence = covered && query.data.warmPools.length === query.data.warmPoolsTotal ? 'absent' : 'unread'
    }
    return { pool, presence, poolRow }
  }, [kind, resource, own, query.data, namespace])

  const warmPoolSummaryRow = kind === 'SandboxWarmPool' ? query.data?.warmPools.find((w) => w.namespace === namespace && w.name === name) : undefined
  const cleanupMessage = kind === 'SandboxClaim' && status.text === 'Expired' ? findCondition(resource, 'Ready')?.message : undefined
  const cleanupInitiated = typeof cleanupMessage === 'string' && /\bcleanup initiated\b/i.test(cleanupMessage)
  const poolBadge = kind === 'SandboxWarmPool' ? warmPoolBadge(resource, server) : undefined

  return (
    <OpenIssueContext.Provider value={issuesTakenOver ? undefined : (p) => navigateTo(issuesPathForSubject(p.subject, params.get('namespaces')))}>
      <div className="space-y-4 p-4">
        {own.length + folded.length > 0 && <div className="space-y-3">{[...own, ...folded].map(callout)}</div>}
        {showIssues && leadContent}

        {drawerActions.length > 0 && (
          <div className="flex flex-wrap gap-2">
            {drawerActions.map((a) => (
              <button
                key={a.label}
                type="button"
                onClick={a.onClick}
                className={clsx(
                  a.primary ? 'btn-brand' : 'btn-secondary',
                  'inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap px-3 py-1.5 text-xs font-medium',
                )}
              >
                {a.label}
              </button>
            ))}
          </div>
        )}

        <Frame title="At a glance">
          <FactGrid>
            {kind === 'Sandbox' && (
              <>
                <FactRow label="State">
                  {suspendedConfirmed ? (
                    <DimensionValue tone="neutral" source="Suspended=True (PodTerminated) · Sandbox status">
                      Controller confirms suspension
                    </DimensionValue>
                  ) : suspensionBlocked ? (
                    <DimensionValue tone={effectiveLevel} source="Requested: Suspended · Observed: refused (PodNotOwned)">
                      Suspension blocked
                    </DimensionValue>
                  ) : (
                    <DimensionValue
                      tone={effectiveLevel}
                      source="Ready condition · Sandbox status"
                    >
                      {readingLabel(status).label}
                    </DimensionValue>
                  )}
                </FactRow>

                {suspensionBlocked ? (
                  <FactRow label="Conflicting Pod">
                    <div className="space-y-1.5">
                      <div>
                        <RefLink refTo={{ kind: 'Pod', group: '', ...ns(conflictingPodName) }} onNavigate={navigate} mono />
                        <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">
                          Not owned by this Sandbox — controller refuses to delete it
                        </div>
                      </div>
                      {conflictingCommand && <CommandChip command={conflictingCommand} />}
                    </div>
                  </FactRow>
                ) : (
                  <FactRow label="Pod">
                    <PodFact
                      answer={podAnswer}
                      phase={typeof podRead?.status?.phase === 'string' ? podRead.status.phase : undefined}
                      suspendedConfirmed={suspendedConfirmed}
                      canViewLogs={canViewLogs}
                      onNavigate={navigate}
                      onOpenLogs={openPodLogs}
                    />
                    {waitingContainers.map(({ container, state }) => (
                      <StateLine key={`${container.init ? 'init/' : ''}${container.name}`} s={state} />
                    ))}
                  </FactRow>
                )}

                {(() => {
                  const owner = sandboxController(resource)
                  return (
                    <>
                      <FactRow label={owner?.kind === 'SandboxClaim' ? 'Claim' : owner?.kind === 'SandboxWarmPool' ? 'Warm pool' : 'Owner'}>
                        <OwnerFact owner={owner} resource={resource} onNavigate={navigate} />
                      </FactRow>
                      <FactRow label="Expiry">
                        <ExpiryCell resource={resource} claimed={owner?.kind === 'SandboxClaim'} />
                      </FactRow>
                    </>
                  )
                })()}
              </>
            )}

            {kind === 'SandboxClaim' && (
              <>
                <FactRow label="State">
                  {status.text === 'Expired' ? (
                    <DimensionValue
                      tone="neutral"
                      source="Ready condition · SandboxClaim status"
                    >
                      Expired
                    </DimensionValue>
                  ) : (
                    <DimensionValue
                      tone={effectiveLevel}
                      source={stateMessage(resource, status) || 'Ready condition · SandboxClaim status'}
                    >
                      {readingLabel(status).label}
                    </DimensionValue>
                  )}
                </FactRow>

                <FactRow label="Requested pool">
                  {!claimPoolInfo?.pool ? (
                    <Absent>no warm pool</Absent>
                  ) : (
                    <div>
                      <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                        {claimPoolInfo.presence !== 'present' ? (
                          <>
                            <span className="font-mono">{claimPoolInfo.pool}</span>
                            <span className={clsx('text-xs', claimPoolInfo.presence === 'absent' ? toneTextClass('unhealthy') : 'text-theme-text-tertiary')}>
                              {claimPoolInfo.presence === 'absent' ? 'not found' : 'not read'}
                            </span>
                          </>
                        ) : (
                          <>
                            <RefLink refTo={{ kind: 'SandboxWarmPool', group: AGENT_SANDBOX_EXTENSIONS_GROUP, ...ns(claimPoolInfo.pool) }} onNavigate={navigate} mono />
                            {claimPoolInfo.poolRow && (
                              <span className="text-xs text-theme-text-secondary">
                                · {claimPoolInfo.poolRow.ready ?? '?'}/{claimPoolInfo.poolRow.desired ?? '?'} ready
                              </span>
                            )}
                          </>
                        )}
                      </div>
                      <div className="mt-0.5">
                        <button
                          type="button"
                          onClick={() => navigateTo(`${SANDBOX_WARM_POOLS_PATH}&namespaces=${encodeURIComponent(namespace)}`)}
                          className="text-xs text-accent-text hover:underline"
                        >
                          Browse warm pools in this namespace →
                        </button>
                      </div>
                    </div>
                  )}
                </FactRow>

                <FactRow label="Bound sandbox">
                  {(() => {
                    const sandbox = claimSandboxName(resource)
                    if (sandbox) return <RefLink refTo={{ kind: 'Sandbox', group: AGENT_SANDBOX_GROUP, ...ns(sandbox) }} onNavigate={navigate} mono />
                    if (status.text === 'Expired') return <Absent>not bound (expired)</Absent>
                    return <Absent title="The claim’s status names no Sandbox now.">not bound</Absent>
                  })()}
                </FactRow>

                <FactRow label="Expiry & cleanup">
                  <div>
                    <ExpiryCell resource={resource} />
                    {status.text === 'Expired' && (
                      <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">
                        {cleanupInitiated ? 'Controller initiated cleanup. ' : ''}Cleanup outcome unknown.
                      </div>
                    )}
                  </div>
                </FactRow>
              </>
            )}

            {kind === 'SandboxWarmPool' && (
              <>
                <FactRow label="Warm reserve">
                  {(() => {
                    const label = poolBadge?.label ?? readingLabel(status).label
                    const ownRow = own[0]
                    const sched = schedulerReason(ownRow)
                    const sourceParts = [
                      sched ? `Can’t schedule · ${sched.plain}` : ownRow?.reading,
                      'status.readyReplicas / spec.replicas',
                    ].filter(Boolean)
                    return (
                      <DimensionValue tone={effectiveLevel} source={sourceParts.join(' · ')}>
                        {label} · {getSandboxWarmPoolReadyOfDesired(resource)} ready
                      </DimensionValue>
                    )
                  })()}
                </FactRow>

                <FactRow label="Template">
                  {(() => {
                    const t = getSandboxWarmPoolTemplate(resource)
                    if (t === '-') return <Absent>no template</Absent>
                    const presence = (warmPoolSummaryRow?.template === t ? warmPoolSummaryRow.templatePresence : undefined) ?? own.flatMap((a) => a.relations ?? []).find((r) => r.role === 'template' && r.ref.name === t)?.presence
                    if (presence !== 'present') {
                      return (
                        <span className="inline-flex items-center gap-1.5">
                          <span className="font-mono">{t}</span>
                          <span className={clsx('text-xs', presence === 'absent' ? toneTextClass('unhealthy') : 'text-theme-text-tertiary')}>
                            {presence === 'absent' ? 'not found' : 'not read'}
                          </span>
                        </span>
                      )
                    }
                    return <RefLink refTo={{ kind: 'SandboxTemplate', group: AGENT_SANDBOX_EXTENSIONS_GROUP, ...ns(t) }} onNavigate={navigate} mono />
                  })()}
                </FactRow>

                <FactRow label="Members">
                  <div>
                    {lists.data.sandboxes.state === 'loading' && poolMembers.length === 0 ? (
                      <span className="text-theme-text-tertiary">Reading…</span>
                    ) : lists.data.sandboxes.state === 'failed' && poolMembers.length === 0 ? (
                      <span className="text-theme-text-tertiary">
                        {lists.data.sandboxes.denied
                          ? `Unknown — no access (missing ${KIND_LIST_PERMISSION.sandboxes})`
                          : `Unknown — Sandboxes couldn’t be read (${lists.data.sandboxes.message})`}
                      </span>
                    ) : poolMembers.length === 0 ? (
                      <span className="text-theme-text-secondary">
                        {readInNamespace(lists.data.sandboxes, namespace) ? 'No member sandboxes' : 'Unknown — Sandboxes in this namespace were not read in full'}
                      </span>
                    ) : (
                      <>
                        <SectionTable
                          framed={false}
                          minWidth={0}
                          rows={poolMembers.slice(0, 12)}
                          rowKey={(m) => m.name}
                          empty="No members"
                          columns={[
                            {
                              header: 'Sandbox',
                              cell: (m) => <RefLink refTo={{ kind: 'Sandbox', group: AGENT_SANDBOX_GROUP, ...ns(m.name) }} onNavigate={navigate} mono />,
                            },
                            {
                              header: 'State',
                              width: '40%',
                              cell: (m) => <DimensionValue tone={m.tone}>{m.label}</DimensionValue>,
                            },
                          ]}
                        />
                        <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs">
                          {poolMembers.some((m) => m.tone !== 'healthy') && (
                            <button type="button" onClick={() => navigateTo(poolNotReadyPath(namespace, name))} className="text-accent-text hover:underline">
                              Show not ready →
                            </button>
                          )}
                          <button
                            type="button"
                            onClick={() => navigateTo(`${SANDBOXES_PATH}&pool=${encodeURIComponent(`${namespace}/${name}`)}`)}
                            className="text-accent-text hover:underline"
                          >
                            {poolMembers.length > 12 ? 'View all members →' : 'View in Sandboxes →'}
                          </button>
                        </div>
                        {!readInNamespace(lists.data.sandboxes, namespace) && (
                          <div className="mt-1 text-[11.5px] text-theme-text-tertiary">
                            Partial view — Sandboxes in this namespace were not read in full
                          </div>
                        )}
                      </>
                    )}
                  </div>
                </FactRow>

                <FactRow label="Claims referencing">
                  <div>
                    {lists.data.claims.state === 'loading' ? (
                      <span className="text-theme-text-tertiary">Reading…</span>
                    ) : lists.data.claims.state === 'failed' ? (
                      <span className="text-theme-text-tertiary">
                        {lists.data.claims.denied
                          ? `Unknown — no access (missing ${KIND_LIST_PERMISSION.claims})`
                          : `Unknown — SandboxClaims couldn’t be read (${lists.data.claims.message})`}
                      </span>
                    ) : !poolRefClaims ? (
                      <span className="text-theme-text-tertiary">Unknown — claims in this namespace were not read in full</span>
                    ) : poolRefClaims.items.length === 0 ? (
                      <Absent>none</Absent>
                    ) : (
                      <>
                        <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                          <span>
                            {poolRefClaims.items.length} referencing · {poolRefClaims.bound} currently bound
                            {poolRefClaims.expired > 0 ? ` · ${poolRefClaims.expired} expired` : ''}
                          </span>
                          <button type="button" onClick={() => navigateTo(poolClaimsPath(namespace, name))} className="text-xs text-accent-text hover:underline">
                            View claims →
                          </button>
                        </div>
                        <div className="mt-1.5 flex flex-wrap gap-1.5">
                          {poolRefClaims.items.slice(0, 6).map((c) => {
                            const cName = c?.metadata?.name ?? ''
                            const cStatus = getSandboxClaimStatus(c)
                            const cTone = server.tones.get(objectKey('SandboxClaim', namespace, cName)) ?? cStatus.level
                            return (
                              <button
                                key={cName}
                                type="button"
                                onClick={() => navigate({ kind: 'SandboxClaim', group: AGENT_SANDBOX_EXTENSIONS_GROUP, namespace, name: cName })}
                                className="inline-flex items-center gap-1.5 rounded-md border border-theme-border bg-theme-base px-2 py-0.5 text-xs hover:border-accent"
                              >
                                <StatusDot tone={cTone} />
                                <span className="font-mono text-theme-text-primary">{cName}</span>
                                <span className="text-theme-text-tertiary">{readingLabel(cStatus).label}</span>
                              </button>
                            )
                          })}
                        </div>
                      </>
                    )}
                  </div>
                </FactRow>

                {latestWarningEvent && (
                  <FactRow label="Controller event">
                    <div>
                      <div className="flex flex-wrap items-center gap-x-2">
                        <span className={clsx('font-medium', toneTextClass('degraded'))}>{latestWarningEvent.reason || 'Warning'}</span>
                        {latestWarningEvent.timestamp && (
                          <span className="text-xs text-theme-text-tertiary">{formatAge(latestWarningEvent.timestamp)} ago</span>
                        )}
                      </div>
                      {latestWarningEvent.message && (
                        <div className="mt-0.5 text-xs text-theme-text-secondary break-words">{latestWarningEvent.message}</div>
                      )}
                    </div>
                  </FactRow>
                )}
              </>
            )}
          </FactGrid>
        </Frame>

        <Frame title="About">
          <FactGrid>
            {kind === 'Sandbox' && (
              <>
                {containers.length > 0 && (
                  <FactRow label={containers.length === 1 ? 'Image' : 'Images'}>
                    <ContainersFact containers={containers} />
                  </FactRow>
                )}
                <FactRow label="Operating mode">{getSandboxOperatingMode(resource)}</FactRow>
                <FactRow label="Shutdown policy">{resource?.spec?.shutdownPolicy || <Absent>not set</Absent>}</FactRow>
                <FactRow label="Failed pod policy">{resource?.spec?.failedPodPolicy || <Absent>not set</Absent>}</FactRow>
                <FactRow label="Node">
                  {resource?.status?.nodeName ? (
                    <RefLink refTo={{ kind: 'Node', group: '', namespace: '', name: resource.status.nodeName }} onNavigate={navigate} mono />
                  ) : (
                    <Absent>not reported</Absent>
                  )}
                </FactRow>
                <FactRow label="Created">
                  <CreatedValue timestamp={resource?.metadata?.creationTimestamp} />
                </FactRow>
              </>
            )}

            {kind === 'SandboxClaim' && (
              <>
                <FactRow label="Shutdown policy">{resource?.spec?.lifecycle?.shutdownPolicy || <Absent>not set</Absent>}</FactRow>
                <FactRow label="Created">
                  <CreatedValue timestamp={resource?.metadata?.creationTimestamp} />
                </FactRow>
              </>
            )}

            {kind === 'SandboxWarmPool' && (
              <>
                <FactRow label="Desired replicas">
                  {resource?.spec?.replicas !== undefined ? <span className="font-mono">{resource.spec.replicas}</span> : <Absent>not set</Absent>}
                </FactRow>
                <FactRow label="Observed generation">
                  {resource?.status?.observedGeneration !== undefined ? (
                    <span className="font-mono">
                      {resource.status.observedGeneration}
                      {resource?.metadata?.generation !== undefined && resource.status.observedGeneration < resource.metadata.generation && (
                        <span className="ml-1.5 font-sans text-xs text-theme-text-tertiary">(spec is {resource.metadata.generation})</span>
                      )}
                    </span>
                  ) : (
                    <Absent>not reported</Absent>
                  )}
                </FactRow>
                <FactRow label="Created">
                  <CreatedValue timestamp={resource?.metadata?.creationTimestamp} />
                </FactRow>
              </>
            )}
          </FactGrid>
        </Frame>

        {others.length > 0 && (
          <Frame title="Also reported">
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
          </Frame>
        )}

        {(conditions.length > 0 || kind !== 'SandboxWarmPool') &&
          <WorkspaceConditions
            title="Controller conditions"
            conditions={conditions}
            emptyMessage="The controller has not reported status.conditions."
            framed={framed}
            className={framed ? undefined : 'border-t border-theme-border-subtle pt-3'}
          />}
      </div>
    </OpenIssueContext.Provider>
  )
}

/**
 * Header status badge for Sandbox, SandboxClaim, and SandboxWarmPool. Reads
 * the same workspace summary as the lists so the drawer/page header pill
 * always agrees with the table badge (e.g. "Reserve below target", never "Warming").
 */
export function AgentSandboxStatusBadge({ data }: { data: any }) {
  const namespace = data?.metadata?.namespace ?? ''
  const name = data?.metadata?.name ?? ''
  const kind = data?.kind ?? ''
  const { support } = useRadarFeature('agentSandboxWorkspace')
  const query = useAgentSandboxWorkspace(namespace ? [namespace] : [], { enabled: !!namespace && support !== 'unsupported' })
  const server = useMemo(() => serverReadings(query.data), [query.data])
  if (kind === 'SandboxWarmPool') {
    const b = warmPoolBadge(data, server)
    return <ReadingBadge status={b.status} label={b.label} level={b.level} title={b.title} />
  }
  const status = getAgentRuntimeStatus(data)
  if (!status) return null
  const level = server.tones.get(objectKey(kind, namespace, name))
  return <ReadingBadge status={status} level={level} />
}
