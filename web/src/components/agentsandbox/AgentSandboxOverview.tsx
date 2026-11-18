import { Fragment, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import { clsx } from 'clsx'
import {
  Badge,
  Collapse,
  CollapseChevron,
  FoldSection,
  OpenIssueContext,
  ProblemMeta,
  RefLink,
  StatusDot,
  Tooltip,
  formatRelativeAgeTime,
  healthToSeverity,
  toneTextClass,
  type ResourceRef,
  type StatusTone,
} from '@skyhook-io/k8s-ui'
import { formatAge } from '@skyhook-io/k8s-ui/components/resources/resource-utils'
import type { AgentSandboxAttention, AgentSandboxRef, AgentSandboxWarmPoolRow, AgentSandboxWorkspaceResponse } from '../../api/agentSandbox'
import type { SelectedResource } from '../../types'
import { refToSelectedResource } from '../../utils/navigation'
import { issuesPathForSubject } from '../../utils/page-links'
import { ScreenBody, SectionTable, Sub, type TableColumn } from '../workspace/layout'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { AGENT_SANDBOX_EXTENSIONS_GROUP, SANDBOXES_PATH, SANDBOX_CLAIMS_PATH, SANDBOX_WARM_POOLS_PATH } from './routes'
import { AGENT_SANDBOX_WIDE, AgentSandboxHeader, Dash, ObjectName } from './shared'
import { CANT_SCHEDULE, objectKey, serverReadings } from './labels'
import { useMediaQuery } from '../../hooks/useMediaQuery'
import { PRESENCE_TEXT, WorkspaceCoverageNotice, attentionAge, attentionProblem, controllerValue, leftOutText, relationLabel, severityTone } from './workspace'

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

interface SummaryFact {
  label: string
  value: ReactNode
  tone: StatusTone
  list?: { path: string; title: string }
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`
const at = (lowerBound: boolean, n: number) => `${lowerBound ? '≥' : ''}${n}`

/** The status line: the controller, sandboxes by state, claims, warm pools. A kind the summary left out reads unknown, never 0. */
export function overviewFacts(data: AgentSandboxWorkspaceResponse): SummaryFact[] {
  const { counts } = data
  const controller = controllerValue(data.controller)
  const facts: SummaryFact[] = [{ label: 'Controller', value: controller.value, tone: controller.tone }]

  const sbList = { path: SANDBOXES_PATH, title: 'Every Sandbox, its state, owner and expiry' }
  const s = counts.sandboxes
  if (!s) facts.push({ label: 'Sandboxes', value: leftOutText(data, 'sandboxes'), tone: data.coverage.sandboxes?.state === 'notInstalled' ? 'neutral' : 'unknown', list: sbList })
  else if (s.total === 0) facts.push({ label: 'Sandboxes', value: s.lowerBound ? 'none Radar can read' : 'none', tone: s.lowerBound ? 'unknown' : 'neutral', list: sbList })
  else {
    const words: Partial<Record<keyof typeof s, string>> = { suspensionBlocked: 'suspension blocked', unschedulable: 'can’t schedule' }
    const parts = (['running', 'starting', 'unschedulable', 'suspended', 'suspensionBlocked', 'expired', 'finished', 'failed', 'terminating', 'unknown'] as const)
      .filter((k) => k === 'running' || s[k] > 0)
      .map((k) => `${at(s.lowerBound, s[k])} ${words[k] ?? k}`)
    facts.push({
      label: 'Sandboxes',
      value: `${at(s.lowerBound, s.total)}: ${parts.join(', ')}`,
      tone: s.failed > 0 ? 'unhealthy' : s.suspensionBlocked > 0 || s.unschedulable > 0 ? 'alert' : s.starting > 0 || s.unknown > 0 ? 'degraded' : 'healthy',
      list: sbList,
    })
  }

  const clList = { path: SANDBOX_CLAIMS_PATH, title: 'Every SandboxClaim, its warm pool and the Sandbox it is bound to' }
  const c = counts.claims
  if (!c) facts.push({ label: 'Claims', value: leftOutText(data, 'claims'), tone: data.coverage.claims?.state === 'notInstalled' ? 'neutral' : 'unknown', list: clList })
  else if (c.total === 0) facts.push({ label: 'Claims', value: c.lowerBound ? 'none Radar can read' : 'none', tone: c.lowerBound ? 'unknown' : 'neutral', list: clList })
  else {
    const parts = [`${at(c.lowerBound, c.bound)} bound`, `${at(c.lowerBound, c.waiting)} waiting`, ...(c.expired > 0 ? [`${at(c.lowerBound, c.expired)} expired`] : [])]
    facts.push({ label: 'Claims', value: `${at(c.lowerBound, c.total)}: ${parts.join(', ')}`, tone: c.waiting > 0 ? 'degraded' : 'healthy', list: clList })
  }

  const wpList = { path: SANDBOX_WARM_POOLS_PATH, title: 'Every SandboxWarmPool, its template and the claims it serves' }
  const w = counts.warmPools
  if (!w) facts.push({ label: 'Warm pools', value: leftOutText(data, 'warmPools'), tone: data.coverage.warmPools?.state === 'notInstalled' ? 'neutral' : 'unknown', list: wpList })
  else if (w.total === 0) facts.push({ label: 'Warm pools', value: w.lowerBound ? 'none Radar can read' : 'none', tone: w.lowerBound ? 'unknown' : 'neutral', list: wpList })
  else {
    const pools = w.lowerBound ? `≥${w.total} pools` : plural(w.total, 'pool', 'pools')
    facts.push({
      label: 'Warm pools',
      value: `${at(w.lowerBound, w.ready)} of ${at(w.lowerBound, w.desired)} sandboxes ready in ${pools}`,
      tone: w.ready >= w.desired ? 'healthy' : 'degraded',
      list: wpList,
    })
  }
  return facts
}

/** One line answering "is Agent Sandbox working?" before the detail below it. */
function Summary({ facts, onOpenList }: { facts: SummaryFact[]; onOpenList: (path: string) => void }) {
  return (
    <dl className="flex flex-wrap gap-x-7 gap-y-2 text-sm" aria-label="Agent Sandbox at a glance">
      {facts.map((f) => (
        <div key={f.label} className="flex items-center gap-1.5">
          <StatusDot tone={f.tone} />
          <dt className="text-theme-text-tertiary">
            {f.list ? (
              <Tooltip content={f.list.title}>
                <button type="button" onClick={() => onOpenList(f.list!.path)} className="text-accent-text hover:underline">
                  {f.label}
                </button>
              </Tooltip>
            ) : (
              f.label
            )}
            :
          </dt>
          <dd className="font-medium text-theme-text-primary">{f.value}</dd>
        </div>
      ))}
    </dl>
  )
}

const toRef = (r: AgentSandboxRef): ResourceRef & { group: string } => ({ kind: r.kind, group: r.group, namespace: r.namespace ?? '', name: r.name })

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
function RelationsLine({ a, onNavigate }: { a: AgentSandboxAttention; onNavigate: (ref: ResourceRef) => void }) {
  const linked = new Set([a.subject, ...(a.related?.refs ?? [])].map((r) => `${r.kind}/${r.namespace ?? ''}/${r.name}`))
  const shown = (a.relations ?? []).filter((r) => !linked.has(`${r.ref.kind}/${r.ref.namespace ?? ''}/${r.ref.name}`))
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

/** One row: title, subject, Radar's reading, links, the next step, and the source's own words folded last. */
function AttentionRow({ a, onNavigate }: { a: AgentSandboxAttention; onNavigate: (ref: ResourceRef) => void }) {
  const p = attentionProblem(a)
  const age = attentionAge(a)
  return (
    <li className="min-w-0 text-sm" data-rule={a.rule}>
      <div className="break-words">
        <span className={clsx('font-medium', toneTextClass(severityTone(a.severity)))}>{p.title}</span>
        {age && (
          <Tooltip content={age.tip}>
            <span className="ml-1.5 text-xs text-theme-text-tertiary">{age.text}</span>
          </Tooltip>
        )}
      </div>
      <ProblemMeta problem={p} rootKind="" onNavigate={onNavigate} hideOrigin className="mt-0.5" />
      {p.detail && <div className="break-words text-xs text-theme-text-secondary">{p.detail}</div>}
      {a.related && <RefsLine lead={a.related.lead} refs={a.related.refs} onNavigate={onNavigate} />}
      <RelationsLine a={a} onNavigate={onNavigate} />
      <div className="mt-0.5 break-words text-xs text-theme-text-primary">
        <span className="font-medium">Next step: </span>
        {a.nextStep}
      </div>
      {a.rawDetail && (
        <FoldSection title={a.rawDetailLabel ?? 'Original evidence'} summary="" attention={false}>
          <div className="break-words text-xs text-theme-text-secondary">{a.rawDetail}</div>
        </FoldSection>
      )}
    </li>
  )
}

function AttentionList({ rows, onNavigate }: { rows: AgentSandboxAttention[]; onNavigate: (ref: ResourceRef) => void }) {
  const [showAll, setShowAll] = useState(false)
  const folded = rows.slice(ATTENTION_SHOWN)
  const foldId = 'agent-sandbox-attention-more'
  return (
    <div>
      <ul className="space-y-2.5">
        {rows.slice(0, ATTENTION_SHOWN).map((a) => (
          <AttentionRow key={a.id} a={a} onNavigate={onNavigate} />
        ))}
      </ul>
      {folded.length > 0 && (
        <>
          <Collapse open={showAll} id={foldId}>
            <ul className="space-y-2.5 pt-2.5">
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
            className="mt-2 inline-flex items-center gap-1 text-xs font-medium text-accent-text hover:underline"
          >
            <CollapseChevron open={showAll} />
            {showAll ? 'Show fewer' : `Show ${folded.length} more`}
          </button>
        </>
      )}
    </div>
  )
}

function TemplateCell({ p, onInspect }: { p: AgentSandboxWarmPoolRow; onInspect: (r: SelectedResource) => void }) {
  if (!p.template) return <Dash />
  if (p.templatePresence === 'present') {
    return (
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation()
          onInspect(refToSelectedResource({ kind: 'SandboxTemplate', group: AGENT_SANDBOX_EXTENSIONS_GROUP, namespace: p.namespace, name: p.template! }))
        }}
        onKeyDown={(e) => e.stopPropagation()}
        className="block max-w-full truncate text-left font-mono text-sm text-accent-text hover:underline"
      >
        {p.template}
      </button>
    )
  }
  return (
    <>
      <ObjectName name={p.template} />
      <Sub>{p.templatePresence === 'absent' ? 'not found' : 'not read'}</Sub>
    </>
  )
}

/** The warm pools as the server read them: state, ready of desired, template and the claims each serves. */
function WarmPoolsTable({ data, onInspect, inspected }: Pick<AgentSandboxOverviewProps, 'data' | 'onInspect' | 'inspected'>) {
  const wide = useMediaQuery(AGENT_SANDBOX_WIDE)
  const multi = new Set(data.warmPools.map((p) => p.namespace)).size > 1
  const server = serverReadings(data)
  const readyText = (p: AgentSandboxWarmPoolRow) => (p.desired !== undefined ? `${p.ready}/${p.desired}` : `${p.ready}`)
  // Narrow: ready of desired under the state, the age under the name.
  const columns: TableColumn<AgentSandboxWarmPoolRow>[] = [
    {
      header: 'Warm pool',
      width: wide ? '26%' : '28%',
      cell: (p) => (
        <>
          <ObjectName name={p.name} />
          {multi && <Sub>{p.namespace}</Sub>}
          {!wide && p.created && <Sub>{formatAge(p.created)} old</Sub>}
        </>
      ),
    },
    {
      header: 'State',
      width: wide ? '16%' : '24%',
      cell: (p) => (
        <>
          {(() => {
            // Colored as the server's row about the pool, as in the lists.
            const level = server.tones.get(objectKey('SandboxWarmPool', p.namespace, p.name)) ?? p.state.level
            const badge = (label: string) => (
              <Badge severity={healthToSeverity(level)} size="sm">
                {label}
              </Badge>
            )
            return p.state.text === 'Warming' && server.unschedulable.has(`${p.namespace}/${p.name}`) ? (
              <Tooltip content="Warming, but the scheduler can’t place its sandboxes, so it isn’t refilling">{badge(CANT_SCHEDULE)}</Tooltip>
            ) : (
              badge(p.state.text)
            )
          })()}
          {!wide && <Sub>{p.desired !== undefined ? `${readyText(p)} ready` : `${p.ready} ready, no replicas set`}</Sub>}
        </>
      ),
    },
    ...(wide
      ? [
          {
            header: 'Ready',
            width: '12%',
            cell: (p: AgentSandboxWarmPoolRow) => (
              <>
                <span className="font-mono text-sm">{readyText(p)}</span>
                <Sub>{p.desired !== undefined ? 'of desired' : 'no replicas set'}</Sub>
              </>
            ),
          },
        ]
      : []),
    { header: 'Template', width: wide ? '18%' : '24%', cell: (p) => <TemplateCell p={p} onInspect={onInspect} /> },
    {
      header: 'Claims served',
      width: wide ? '18%' : '24%',
      cell: (p) =>
        !p.claims ? (
          <span className="text-sm text-theme-text-tertiary">unknown</span>
        ) : p.claims.total === 0 ? (
          <span className="text-sm text-theme-text-tertiary">none</span>
        ) : (
          <>
            <span className="text-sm">{p.claims.total}</span>
            <Sub>{p.claims.bound} bound</Sub>
          </>
        ),
    },
    ...(wide
      ? [{ header: 'Age', width: '10%', cell: (p: AgentSandboxWarmPoolRow) => (p.created ? <span className="text-sm text-theme-text-secondary">{formatAge(p.created)}</span> : <Dash />) }]
      : []),
  ]
  const empty = !data.counts.warmPools
    ? `Unknown: ${leftOutText(data, 'warmPools').replace(/^unknown — /, '')}.`
    : data.counts.warmPools.lowerBound
      ? 'No warm pools in the namespaces Radar could read.'
      : 'No warm pools in this scope.'
  return (
    <SectionTable
      title="Warm pools"
      columns={columns}
      rows={data.warmPools}
      rowKey={(p) => `${p.namespace}/${p.name}`}
      rowResource={(p) => refToSelectedResource({ kind: 'SandboxWarmPool', group: AGENT_SANDBOX_EXTENSIONS_GROUP, namespace: p.namespace, name: p.name })}
      onInspect={onInspect}
      inspected={inspected}
      empty={empty}
      minWidth={wide ? 760 : 560}
      footer={data.warmPoolsTotal > data.warmPools.length ? `Showing ${data.warmPools.length} of ${data.warmPoolsTotal} warm pools.` : undefined}
    />
  )
}

const EXPIRED_SHOWN = 5

/** One line naming what reached its shutdown time this week, newest first: the status line counts them, this says which. */
function RecentlyExpired({ data, onNavigate }: { data: AgentSandboxWorkspaceResponse; onNavigate: (ref: ResourceRef) => void }) {
  const { recent, recentTotal, lowerBound } = data.expired
  if (recentTotal === 0) return null
  const shown = recent.slice(0, EXPIRED_SHOWN)
  const more = recentTotal - shown.length
  const note = `Expired in the last 7 days. These stay until deleted; a Sandbox with shutdownPolicy Delete is removed when it expires, so it isn’t listed.${lowerBound ? ' Some namespaces weren’t read, so there may be more.' : ''}`
  return (
    <p className="text-sm text-theme-text-secondary" aria-label="Recently expired">
      <Tooltip content={note}>
        <span className="text-theme-text-tertiary underline decoration-dotted underline-offset-2">Expired this week</span>
      </Tooltip>
      :{' '}
      {shown.map((e, i) => (
        <Fragment key={`${e.ref.kind}/${e.ref.namespace}/${e.ref.name}`}>
          {i > 0 && ', '}
          {e.ref.kind === 'SandboxClaim' && 'claim '}
          <RefLink refTo={toRef(e.ref)} onNavigate={onNavigate} mono />
          {e.at && <span className="text-theme-text-tertiary"> {formatRelativeAgeTime(e.at)}</span>}
        </Fragment>
      ))}
      {more > 0 && ` and ${lowerBound ? '≥' : ''}${more} more`}
    </p>
  )
}

export function AgentSandboxOverview({ data, onClearNamespaces, onInspect, inspected }: AgentSandboxOverviewProps) {
  const navigateTo = useWorkspaceNavigate()
  const [params] = useSearchParams()
  const navigate = (ref: ResourceRef) => onInspect(refToSelectedResource(ref))

  return (
    <OpenIssueContext.Provider value={(p) => navigateTo(issuesPathForSubject(p.subject, params.get('namespaces')))}>
      <div className="flex min-h-0 flex-1 flex-col">
        <AgentSandboxHeader title="Overview" namespaces={data.namespaces} onClearNamespaces={onClearNamespaces} />
        <ScreenBody>
          <WorkspaceCoverageNotice data={data} />
          <div className="space-y-1.5">
            <Summary facts={overviewFacts(data)} onOpenList={(path) => navigateTo(path)} />
            <RecentlyExpired data={data} onNavigate={navigate} />
          </div>
          <section aria-labelledby="agent-sandbox-attention">
            <h2 id="agent-sandbox-attention" className="mb-2 text-sm font-semibold text-theme-text-primary">Needs attention</h2>
            <div className="rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
              {data.attention.length === 0 ? (
                <p className="text-sm text-theme-text-tertiary">{data.attentionComplete ? NOTHING_NEEDS_ATTENTION : NOTHING_IN_WHAT_WAS_READ}</p>
              ) : (
                <AttentionList rows={data.attention} onNavigate={navigate} />
              )}
              {data.attentionTotal > data.attention.length && (
                <p className="mt-2 text-xs text-theme-text-tertiary">Showing {data.attention.length} of {data.attentionTotal}.</p>
              )}
            </div>
          </section>
          {data.coverage.warmPools?.state !== 'notInstalled' && <WarmPoolsTable data={data} onInspect={onInspect} inspected={inspected} />}
        </ScreenBody>
      </div>
    </OpenIssueContext.Provider>
  )
}
