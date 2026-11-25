import { Fragment, useContext, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import { clsx } from 'clsx'
import {
  Collapse,
  CollapseChevron,
  DimensionValue,
  FactGrid,
  FactRow,
  OpenIssueContext,
  RefLink,
  RowDetails,
  StatusDot,
  Tooltip,
  WorkspaceCard,
  WorkspaceProblemCallout,
  toneTextClass,
  type ResourceRef,
  type StatusTone,
} from '@skyhook-io/k8s-ui'
import type { AgentSandboxAttention, AgentSandboxRef, AgentSandboxWorkspaceResponse } from '../../api/agentSandbox'
import { useResourceWithRelationships } from '../../api/client'
import { sandboxPod } from './lists'
import { useNamespacedCapabilities } from '../../contexts/CapabilitiesContext'
import type { SelectedResource } from '../../types'
import { buildWorkloadPath, refToSelectedResource } from '../../utils/navigation'
import { issuesPathForSubject } from '../../utils/page-links'
import { ScreenBody } from '../workspace/layout'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { CommandChip } from '../substrate/shared'
import { poolNotReadyPath } from './AgentSandboxTables'
import { notReadyMembers, schedulerReason } from './labels'
import { SANDBOXES_PATH, SANDBOX_CLAIMS_PATH, SANDBOX_WARM_POOLS_PATH } from './routes'
import { AGENT_SANDBOX_WHAT_IT_IS, AgentSandboxHeader, SourceText } from './shared'
import { PRESENCE_TEXT, WorkspaceCoverageNotice, attentionAge, attentionDetail, attentionProblem, controllerValue, leftOutText, relationLabel, severityTone } from './workspace'

export const NOTHING_NEEDS_ATTENTION = 'Nothing needs attention.'
/** The same, when part of Agent Sandbox could not be read: an empty list there proves less. */
export const NOTHING_IN_WHAT_WAS_READ = 'Nothing in what Radar can read needs attention.'

// Rows shown before folding the rest. The server orders rows (worst first);
// the list keeps that order and does not group or re-sort them.
const ATTENTION_SHOWN = 8

export interface AgentSandboxOverviewProps {
  data: AgentSandboxWorkspaceResponse
  onClearNamespaces: () => void
  onInspect: (resource: SelectedResource) => void
  inspected: SelectedResource | null
}

export interface SummaryFact {
  label: string
  value: ReactNode
  source?: ReactNode
  tone: StatusTone
  linkLabel?: string
  listPath?: string
  refTo?: ResourceRef & { group: string }
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`
const at = (lowerBound: boolean, n: number) => `${lowerBound ? '≥' : ''}${n}`
const toRef = (r: AgentSandboxRef): ResourceRef & { group: string } => ({ kind: r.kind, group: r.group, namespace: r.namespace ?? '', name: r.name })

/** The At a glance dimensions: Controller, Sandboxes, Claims, Warm reserve. Unknown never reads as 0. */
export function overviewFacts(data: AgentSandboxWorkspaceResponse): SummaryFact[] {
  const { counts } = data
  const controller = controllerValue(data.controller)
  const controllerReadable = data.controller.state === 'running' || data.controller.state === 'notReady' || data.controller.state === 'scaledToZero'
  const controllerRef = controllerReadable
    ? { kind: 'Deployment', group: 'apps', namespace: data.controller.namespace, name: data.controller.name }
    : undefined
  const facts: SummaryFact[] = [
    {
      label: 'Controller',
      value: controller.value,
      tone: controller.tone,
      refTo: controllerRef,
      linkLabel: controllerRef ? 'Deployment' : undefined,
      source:
        data.controller.state === 'notInstalled'
          ? 'No Agent Sandbox CRDs served'
          : `Deployment ${data.controller.namespace}/${data.controller.name} · cluster-level check, outside namespace filter`,
    },
  ]

  const s = counts.sandboxes
  if (!s) {
    facts.push({
      label: 'Sandboxes',
      value: leftOutText(data, 'sandboxes'),
      tone: data.coverage.sandboxes?.state === 'notInstalled' ? 'neutral' : 'unknown',
      linkLabel: 'Sandboxes',
      listPath: SANDBOXES_PATH,
      source: 'agents.x-k8s.io',
    })
  } else if (s.total === 0) {
    facts.push({
      label: 'Sandboxes',
      value: s.lowerBound ? 'None Radar can read' : 'None',
      tone: s.lowerBound ? 'unknown' : 'neutral',
      linkLabel: 'Sandboxes',
      listPath: SANDBOXES_PATH,
      source: 'agents.x-k8s.io',
    })
  } else {
    const lb = s.lowerBound
    const problemParts = [
      s.failed > 0 ? `${at(lb, s.failed)} failed` : null,
      s.unschedulable > 0 ? `${at(lb, s.unschedulable)} can’t schedule` : null,
      s.suspensionBlocked > 0 ? `${at(lb, s.suspensionBlocked)} suspension blocked` : null,
      s.unknown > 0 ? `${at(lb, s.unknown)} unknown` : null,
    ].filter((x): x is string => x !== null)
    const otherParts = [
      s.running > 0 ? `${at(lb, s.running)} running` : null,
      s.starting > 0 ? `${at(lb, s.starting)} starting` : null,
      s.suspended > 0 ? `${at(lb, s.suspended)} suspended` : null,
      s.expired > 0 ? `${at(lb, s.expired)} expired` : null,
      s.finished > 0 ? `${at(lb, s.finished)} finished` : null,
      s.terminating > 0 ? `${at(lb, s.terminating)} terminating` : null,
    ].filter((x): x is string => x !== null)
    const headline =
      problemParts.length > 0
        ? problemParts.join(' · ')
        : s.running > 0
          ? [`${at(lb, s.running)} running`, s.starting > 0 ? `${at(lb, s.starting)} starting` : null].filter(Boolean).join(' · ')
          : otherParts.join(' · ')
    const subParts = problemParts.length > 0 ? otherParts : s.running > 0 ? otherParts.filter((p) => !p.endsWith(' running') && !p.endsWith(' starting')) : []
    const tone: StatusTone =
      s.failed > 0 ? 'unhealthy' : s.suspensionBlocked > 0 || s.unschedulable > 0 ? 'alert' : s.starting > 0 || s.unknown > 0 ? 'degraded' : lb ? 'unknown' : s.running > 0 ? 'healthy' : 'neutral'
    facts.push({
      label: 'Sandboxes',
      value: headline,
      tone,
      linkLabel: 'Sandboxes',
      listPath: SANDBOXES_PATH,
      source: [`${at(lb, s.total)} total`, ...subParts, 'agents.x-k8s.io'].join(' · '),
    })
  }

  const c = counts.claims
  if (!c) {
    facts.push({
      label: 'Claims',
      value: leftOutText(data, 'claims'),
      tone: data.coverage.claims?.state === 'notInstalled' ? 'neutral' : 'unknown',
      linkLabel: 'Claims',
      listPath: SANDBOX_CLAIMS_PATH,
      source: 'extensions.agents.x-k8s.io',
    })
  } else if (c.total === 0) {
    facts.push({
      label: 'Claims',
      value: c.lowerBound ? 'None Radar can read' : 'None',
      tone: c.lowerBound ? 'unknown' : 'neutral',
      linkLabel: 'Claims',
      listPath: SANDBOX_CLAIMS_PATH,
      source: 'extensions.agents.x-k8s.io',
    })
  } else {
    const lb = c.lowerBound
    const headline =
      c.waiting > 0
        ? `${at(lb, c.waiting)} waiting · ${at(lb, c.bound)} bound`
        : c.bound > 0
          ? `${at(lb, c.bound)} bound`
          : `${at(lb, c.expired)} expired`
    const subParts = [`${at(lb, c.total)} total`, c.expired > 0 && (c.waiting > 0 || c.bound > 0) ? `${at(lb, c.expired)} expired` : null, 'extensions.agents.x-k8s.io'].filter(Boolean)
    facts.push({
      label: 'Claims',
      value: headline,
      tone: c.waiting > 0 ? 'degraded' : lb ? 'unknown' : c.bound > 0 ? 'healthy' : 'neutral',
      linkLabel: 'Claims',
      listPath: SANDBOX_CLAIMS_PATH,
      source: subParts.join(' · '),
    })
  }

  const w = counts.warmPools
  if (!w) {
    facts.push({
      label: 'Warm reserve',
      value: leftOutText(data, 'warmPools'),
      tone: data.coverage.warmPools?.state === 'notInstalled' ? 'neutral' : 'unknown',
      linkLabel: 'Warm pools',
      listPath: SANDBOX_WARM_POOLS_PATH,
      source: 'extensions.agents.x-k8s.io',
    })
  } else if (w.total === 0) {
    facts.push({
      label: 'Warm reserve',
      value: w.lowerBound ? 'None Radar can read' : 'None',
      tone: w.lowerBound ? 'unknown' : 'neutral',
      linkLabel: 'Warm pools',
      listPath: SANDBOX_WARM_POOLS_PATH,
      source: 'extensions.agents.x-k8s.io',
    })
  } else {
    const lb = w.lowerBound
    const readyLowerBound = lb || !!w.readyLowerBound
    const desiredLowerBound = lb || !!w.desiredLowerBound
    const ready = readyLowerBound && w.ready === 0 ? 'unknown' : at(readyLowerBound, w.ready)
    const target = desiredLowerBound && w.desired === 0 ? 'unknown' : at(desiredLowerBound, w.desired)
    const pools = lb ? `≥${w.total} pools` : plural(w.total, 'pool', 'pools')
    const poolAttention = data.attention.filter((a) => a.category === 'warmPool')
    const worstPoolTone = poolAttention.length > 0 ? severityTone(poolAttention[0].severity) : undefined
    const topPoolRow = poolAttention[0]
    const topScheduler = schedulerReason(topPoolRow)
    const causeNote = topPoolRow
      ? `${topPoolRow.subject.name}: ${topScheduler ? `can’t schedule (${topScheduler.plain.toLowerCase()})` : topPoolRow.title.toLowerCase()}`
      : 'status.readyReplicas / spec.replicas'
    facts.push({
      label: 'Warm reserve',
      value: ready === 'unknown' || target === 'unknown' ? `${ready} ready · target ${target} in ${pools}` : `${ready} of ${target} ready in ${pools}`,
      tone: worstPoolTone ?? (readyLowerBound || desiredLowerBound ? 'unknown' : w.ready >= w.desired ? 'healthy' : 'degraded'),
      linkLabel: 'Warm pools',
      listPath: SANDBOX_WARM_POOLS_PATH,
      source: `${causeNote} · extensions.agents.x-k8s.io`,
    })
  }

  return facts
}

function RefsLine({ lead, refs, onNavigate }: { lead: string; refs: AgentSandboxRef[]; onNavigate: (ref: ResourceRef) => void }) {
  return (
    <div className="break-words text-xs text-theme-text-secondary">
      {lead}{' '}
      {refs.map((r, i) => (
        <Fragment key={`${r.kind}/${r.namespace}/${r.name}`}>
          {i > 0 && (i === refs.length - 1 ? ' and ' : ', ')}
          <RefLink refTo={toRef(r)} onNavigate={onNavigate} mono />
        </Fragment>
      ))}
      .
    </div>
  )
}

/**
 * The objects a row names, each with what Radar could say about it: a link
 * when it is there, "not found" only where its kind was read in full in that
 * namespace, "not read" otherwise. Objects the row already links are left out.
 */
function shownRelations(a: AgentSandboxAttention) {
  const linked = new Set([a.subject, ...(a.related?.refs ?? []), ...(a.emptyPool ? [a.emptyPool] : [])].map((r) => `${r.kind}/${r.namespace ?? ''}/${r.name}`))
  return (a.relations ?? []).filter((r) => !linked.has(`${r.ref.kind}/${r.ref.namespace ?? ''}/${r.ref.name}`))
}

function RelationsLine({ a, onNavigate }: { a: AgentSandboxAttention; onNavigate: (ref: ResourceRef) => void }) {
  const shown = shownRelations(a)
  if (shown.length === 0) return null
  return (
    <div className="break-words text-xs text-theme-text-secondary">
      {shown.map((r, i) => (
        <Fragment key={`${r.role}/${r.ref.name}`}>
          {i > 0 && ' · '}
          {relationLabel(r)}{' '}
          {r.presence === 'present' ? (
            <RefLink refTo={toRef(r.ref)} onNavigate={onNavigate} mono />
          ) : (
            <>
              <span className="font-mono">{r.ref.name}</span>{' '}
              <span className={clsx(r.presence === 'absent' ? toneTextClass('unhealthy') : 'text-theme-text-tertiary')}>{PRESENCE_TEXT[r.presence]}</span>
            </>
          )}
        </Fragment>
      ))}
    </div>
  )
}

function Impact({ a, onNavigate }: { a: AgentSandboxAttention; onNavigate: (ref: ResourceRef) => void }) {
  if (a.rule === 'controllerNotRunning' && a.related) return <span>{a.related.lead.replace(/:$/, '')}</span>
  const w = a.waitingClaims
  if (w && w.count !== null) {
    if (w.count === 0) return <span>no claim waits on it</span>
    return (
      <span>
        {w.lowerBound ? 'at least ' : ''}
        {plural(w.count, 'claim', 'claims')} waiting with no warm sandbox ready
      </span>
    )
  }
  if (a.emptyPool) {
    return (
      <span>
        waiting: warm pool <RefLink refTo={toRef(a.emptyPool)} onNavigate={onNavigate} mono /> has no sandbox ready
      </span>
    )
  }
  return null
}

/** Strips an inline parenthetical command so the next step reads cleanly; the chip lives in Details on Overview. */
export function cleanNextStepText(a: AgentSandboxAttention): string {
  if (!a.command) return a.nextStep
  const quoted = ` (${a.command})`
  return a.nextStep.includes(quoted) ? a.nextStep.replace(quoted, '') : a.nextStep
}

const LOG_FAILURE_REASONS = new Set(['PodFailed', 'CrashLoopBackOff'])

function RowLinkOut({ a }: { a: AgentSandboxAttention }) {
  const openIssue = useContext(OpenIssueContext)
  const navigateTo = useWorkspaceNavigate()
  const members = notReadyMembers(a)
  if (members.length > 0) {
    return (
      <button type="button" onClick={() => navigateTo(poolNotReadyPath(a.subject.namespace ?? '', a.subject.name))} className="whitespace-nowrap text-accent-text hover:underline">
        Show all members that aren’t ready →
      </button>
    )
  }
  if (!openIssue || !a.issue) return null
  return (
    <button type="button" onClick={() => openIssue(attentionProblem(a))} className="whitespace-nowrap text-accent-text hover:underline">
      See in Issues →
    </button>
  )
}

/**
 * Compact Needs attention row: severity dot + title + age, one evidence line
 * with subject + direct Pod/Logs link + honest Issues/members link, optional
 * caveat, one-line next step. Full reading, kubectl commands, and raw source
 * messages fold under Details.
 */
function AttentionRow({ a, onNavigate }: { a: AgentSandboxAttention; onNavigate: (ref: ResourceRef) => void }) {
  const navigateTo = useWorkspaceNavigate()
  const age = attentionAge(a)
  const detail = attentionDetail(a)
  const nextStep = cleanNextStepText(a)
  const evidence = detail || nextStep
  const podRef = a.related?.refs.find((r) => r.kind === 'Pod' && !r.group)
  const sandboxName = a.subject.kind === 'Sandbox' && a.rule !== 'suspensionBlocked' && podRef ? a.subject.name : ''
  const detailQuery = useResourceWithRelationships('sandboxes', a.subject.namespace ?? '', sandboxName, a.subject.group)
  const owned = sandboxPod(detailQuery.data?.relationships, detailQuery.isLoading)
  const verified = owned.state === 'found' && owned.pods.length === 1 ? owned.pods[0] : undefined
  const inlinePod = a.rule === 'suspensionBlocked' ? (podRef && toRef(podRef)) : verified
  const { canViewLogs, logsUnknown } = useNamespacedCapabilities(verified?.namespace)

  const showPodLogs = !!verified && canViewLogs && !logsUnknown && a.rule === 'failed' && !!a.reason && LOG_FAILURE_REASONS.has(a.reason)
  const extraRelated = a.related?.refs.some((r) => r.kind !== 'Pod') ? a.related : undefined
  const hasDetails = !!detail || !!a.command || !!extraRelated || shownRelations(a).length > 0 || !!a.rawDetail
  const [open, setOpen] = useState(false)
  const tone = severityTone(a.severity)

  return (
    <li className="min-w-0 py-2.5 first:pt-0 last:pb-0 text-sm" data-rule={a.rule}>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
        <StatusDot tone={tone} />
        <span className={clsx('break-words font-medium', toneTextClass(tone))}>{a.title}</span>
        {age && (
          <Tooltip content={age.tip}>
            <span className="whitespace-nowrap text-xs text-theme-text-tertiary">
              {a.rule === 'controllerNotRunning' ? `for ${age.text}` : `${age.text} ago`}
            </span>
          </Tooltip>
        )}
      </div>
      <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-theme-text-tertiary">
        <span className="min-w-0 break-words">
          {a.subject.kind} <RefLink refTo={toRef(a.subject)} onNavigate={onNavigate} mono />
        </span>
        {inlinePod && (
          <span className="min-w-0 break-words">
            {a.rule === 'suspensionBlocked' ? 'Conflicting Pod' : 'Pod'}{' '}
            <RefLink refTo={inlinePod} onNavigate={onNavigate} mono />
          </span>
        )}
        {showPodLogs && inlinePod && (
          <button
            type="button"
            onClick={() => navigateTo(buildWorkloadPath({ kind: 'pods', namespace: inlinePod.namespace ?? '', name: inlinePod.name, tab: 'logs' }))}
            className="whitespace-nowrap text-accent-text hover:underline"
          >
            Pod logs →
          </button>
        )}
        <Impact a={a} onNavigate={onNavigate} />
        <RowLinkOut a={a} />
      </div>
      {a.caveat && <div className="mt-0.5 break-words text-xs text-theme-text-secondary">{a.caveat}</div>}
      <div className="mt-0.5 break-words text-xs text-theme-text-primary">
        {evidence}
      </div>
      {hasDetails && (
        <RowDetails open={open} onToggle={() => setOpen((v) => !v)}>
          {detail && <div className="break-words text-xs text-theme-text-secondary">Next step: {nextStep}</div>}
          {a.command && <CommandChip command={a.command} />}
          {extraRelated && <RefsLine lead={extraRelated.lead} refs={extraRelated.refs} onNavigate={onNavigate} />}
          <RelationsLine a={a} onNavigate={onNavigate} />
          {a.rawDetail && <SourceText label={a.rawDetailLabel ?? 'Source’s message'} text={a.rawDetail} />}
        </RowDetails>
      )}
    </li>
  )
}

function foldedSeveritySummary(rows: AgentSandboxAttention[]): string {
  const counts: Partial<Record<AgentSandboxAttention['severity'], number>> = {}
  for (const r of rows) counts[r.severity] = (counts[r.severity] ?? 0) + 1
  const parts = (['critical', 'alert', 'warning'] as const)
    .filter((s) => (counts[s] ?? 0) > 0)
    .map((s) => `${counts[s]} ${s}`)
  return parts.length > 0 ? ` — ${parts.join(', ')}` : ''
}

function AttentionList({ rows, onNavigate }: { rows: AgentSandboxAttention[]; onNavigate: (ref: ResourceRef) => void }) {
  const [showAll, setShowAll] = useState(false)
  const folded = rows.slice(ATTENTION_SHOWN)
  const foldId = 'agent-sandbox-attention-more'
  return (
    <div>
      <ul className="divide-y divide-theme-border-subtle">
        {rows.slice(0, ATTENTION_SHOWN).map((a) => (
          <AttentionRow key={a.id} a={a} onNavigate={onNavigate} />
        ))}
      </ul>
      {folded.length > 0 && (
        <>
          <Collapse open={showAll} id={foldId}>
            <ul className="divide-y divide-theme-border-subtle border-t border-theme-border-subtle pt-2.5">
              {folded.map((a) => (
                <AttentionRow key={a.id} a={a} onNavigate={onNavigate} />
              ))}
            </ul>
          </Collapse>
          <button
            type="button"
            aria-expanded={showAll}
            aria-controls={foldId}
            onClick={() => setShowAll((v) => !v)}
            className="mt-2.5 inline-flex items-center gap-1 text-xs font-medium text-accent-text hover:underline"
          >
            <CollapseChevron open={showAll} />
            {showAll ? 'Show fewer' : `Show ${folded.length} more${foldedSeveritySummary(folded)}`}
          </button>
        </>
      )}
    </div>
  )
}

export function AgentSandboxOverview({ data, onClearNamespaces, onInspect }: AgentSandboxOverviewProps) {
  const navigateTo = useWorkspaceNavigate()
  const [params] = useSearchParams()
  const navigate = (ref: ResourceRef) => onInspect(refToSelectedResource(ref))

  const controllerProblem = data.attention.find((a) => a.rule === 'controllerNotRunning')
  const attentionRows = controllerProblem ? data.attention.filter((a) => a.id !== controllerProblem.id) : data.attention
  const facts = overviewFacts(data)
  const totalObjects = (data.counts.sandboxes?.total ?? 0) + (data.counts.claims?.total ?? 0) + (data.counts.warmPools?.total ?? 0)
  const scopeSubtitle =
    data.namespaces.length > 0
      ? `Namespace: ${data.namespaces.join(', ')}`
      : 'All namespaces'

  const expiredTotal = data.expired.recentTotal
  const expiredNote = `Expired in the last 7 days. Sandboxes with shutdownPolicy Retain stay until deleted; Delete removes them on expiry.${data.expired.lowerBound ? ' Some namespaces weren’t read, so there may be more.' : ''}`

  return (
    <OpenIssueContext.Provider value={(p) => navigateTo(issuesPathForSubject(p.subject, params.get('namespaces')))}>
      <div className="flex min-h-0 flex-1 flex-col">
        <AgentSandboxHeader
          title="Overview"
          subtitle={scopeSubtitle}
          intro={totalObjects === 0 ? AGENT_SANDBOX_WHAT_IT_IS : undefined}
          namespaces={data.namespaces}
          onClearNamespaces={onClearNamespaces}
        />
        <ScreenBody>
          <WorkspaceCoverageNotice data={data} />

          {controllerProblem && (
            <WorkspaceProblemCallout
              problem={attentionProblem(controllerProblem)}
              rootKind="Deployment"
              onNavigate={navigate}
            />
          )}

          <WorkspaceCard title="Needs attention">
            {attentionRows.length === 0 ? (
              <div className="flex items-center gap-2 text-sm text-theme-text-secondary">
                {data.attentionComplete && !controllerProblem && <StatusDot tone="healthy" />}
                <span className="text-theme-text-tertiary">{controllerProblem ? 'No other findings in the objects Radar read.' : data.attentionComplete ? NOTHING_NEEDS_ATTENTION : NOTHING_IN_WHAT_WAS_READ}</span>
              </div>
            ) : (
              <AttentionList rows={attentionRows} onNavigate={navigate} />
            )}
            {data.attentionTotal > data.attention.length && (
              <p className="mt-2.5 text-xs text-theme-text-tertiary">Showing {data.attention.length} of {data.attentionTotal}.</p>
            )}
          </WorkspaceCard>

          <WorkspaceCard title="At a glance">
            <FactGrid>
              {facts.map((f) => (
                <FactRow key={f.label} label={f.label}>
                  <DimensionValue
                    tone={f.tone}
                    linkLabel={f.linkLabel}
                    onOpen={
                      f.listPath
                        ? () => navigateTo(f.listPath!)
                        : f.refTo
                          ? () => navigate(f.refTo!)
                          : undefined
                    }
                    source={
                      f.label === 'Sandboxes' && expiredTotal > 0 ? (
                        <span className="inline-flex flex-wrap items-center gap-x-2">
                          <span>{f.source}</span>
                          <Tooltip content={expiredNote}>
                            <span>{at(data.expired.lowerBound, expiredTotal)} sandboxes and claims expired in the last 7 days</span>
                          </Tooltip>
                          <button
                            type="button"
                            onClick={() => navigateTo(`${SANDBOXES_PATH}&state=recentlyExpired`)}
                            className="text-accent-text hover:underline"
                          >
                            Recently expired sandboxes →
                          </button>
                          <button
                            type="button"
                            onClick={() => navigateTo(`${SANDBOX_CLAIMS_PATH}&segment=expired&state=recentlyExpired`)}
                            className="text-accent-text hover:underline"
                          >
                            Recently expired claims →
                          </button>
                        </span>
                      ) : (
                        f.source
                      )
                    }
                  >
                    {f.value}
                  </DimensionValue>
                </FactRow>
              ))}
            </FactGrid>
          </WorkspaceCard>
        </ScreenBody>
      </div>
    </OpenIssueContext.Provider>
  )
}
