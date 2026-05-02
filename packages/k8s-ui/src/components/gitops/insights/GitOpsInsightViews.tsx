import { AlertTriangle, ChevronDown, ChevronRight, CircleAlert, Clock3, GitBranch, GitCommit, Info, ListChecks } from 'lucide-react'
import { clsx } from 'clsx'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import type { GitOpsChange, GitOpsHistoryItem, GitOpsInsight, GitOpsIssue, GitOpsPlanItem } from '../../../types'
import { HealthStatusBadge, SyncStatusBadge } from '../GitOpsStatusBadge'
import { SEVERITY_BADGE, SEVERITY_TEXT } from '../../../utils/badge-colors'
import { Tooltip } from '../../ui/Tooltip'
import { compactSource, entryTone, gitopsToSeverity } from './insights-helpers'

interface GitOpsStatusStripProps {
  insight?: GitOpsInsight | null
  loading?: boolean
}

// Status strip carries the operation chip (when a sync is in flight or
// failed) plus reference metadata (Source / Revision / Last reconcile /
// Sync mode). Health and Sync badges live next to the title in the page
// header — pair them there with identity, not here.
export function GitOpsStatusStrip({ insight, loading }: GitOpsStatusStripProps) {
  const summary = insight?.summary
  if (loading) {
    return <div className="h-8 animate-pulse border-b border-theme-border bg-theme-base" />
  }
  if (!summary) return null
  const operation = liveOperationPhase(summary.operationPhase)
  return (
    <div className="border-b border-theme-border bg-theme-base px-4 py-2">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5">
        {operation && (
          <Tooltip
            content={`Last sync operation: ${operation}`}
            delay={200}
          >
            <span
              // Pulse only while the operation is actively progressing.
              className={clsx(
                'badge badge-sm font-medium uppercase tracking-wide',
                SEVERITY_BADGE[gitopsToSeverity(operation)],
                isInFlightPhase(operation) && 'animate-pulse',
              )}
            >
              {operation}
            </span>
          </Tooltip>
        )}
        {/* When a sync is in flight, surface the live progress message inline
            so the operator sees what's happening without opening Activity.
            For *failed* operations the message is intentionally NOT shown
            here — the GitOpsIssuesBand below owns the failure narrative
            (parsed cause, retry count, raw message) so the strip stays a
            calm orientation row instead of duplicating the error three times. */}
        {operation && summary.operationMessage && isInFlightPhase(operation) && (
          <Tooltip content={summary.operationMessage} delay={400} wrapperClassName="min-w-0 max-w-[60ch]">
            <span className="block truncate text-[11px] text-theme-text-secondary">
              {summary.operationMessage}
            </span>
          </Tooltip>
        )}
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-theme-text-tertiary">
          <MetaFact label="Source" value={summary.source || '-'} />
          <MetaFact label="Revision" value={summary.lastRevision || summary.targetRevision || '-'} mono />
          <MetaFact label="Last reconcile" value={formatRelative(summary.lastReconcile)} />
          {summary.autoSyncMode && <MetaFact label="Sync mode" value={summary.autoSyncMode} />}
        </div>
      </div>
    </div>
  )
}

function isInFlightPhase(phase: string): boolean {
  const p = phase.toLowerCase()
  return p.includes('running') || p.includes('progress') || p.includes('reconcil')
}

// Show the operation chip only for phases the operator needs to *act on*.
// "Succeeded" + "Idle" are calm steady states — surfacing them in always-on
// chrome adds noise (and reads contradictorily when the app is OutOfSync but
// the *last* sync technically succeeded). Failure + in-flight phases get
// surfaced because they imply work happening or stuck.
function liveOperationPhase(phase?: string): string | null {
  if (!phase) return null
  const p = phase.toLowerCase()
  if (p === 'succeeded' || p === 'idle' || p === '') return null
  return phase
}

function MetaFact({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  // Each fact sizes to its content. The parent flex-wrap row breaks when
  // facts don't all fit, so on wide screens long values like the full source
  // URL or full revision SHA show in their entirety — no premature ellipsis
  // when there's screen real estate to spare. max-w-full is a safety net for
  // the pathological case of one value being wider than the viewport itself,
  // in which case truncate + tooltip kick in.
  return (
    <span className="inline-flex min-w-0 max-w-full items-baseline gap-1">
      <span className="shrink-0">{label}:</span>
      <Tooltip content={value} delay={400} wrapperClassName="min-w-0">
        <span className={clsx('block truncate font-medium text-theme-text-primary', mono && 'font-mono')}>{value}</span>
      </Tooltip>
    </span>
  )
}

// Plan items often surface 'Unknown' as a placeholder when status info isn't
// available — a row of `OutOfSync · Unknown · unknown` chips reads as broken.
// Skip the chip when the value carries no signal.
function isUnknownChipValue(v: string): boolean {
  const lower = v.toLowerCase()
  return lower === 'unknown' || lower === ''
}

// GitOpsIssuesBand renders the issue list at the top of the page. Two paths:
//
//   1. A critical operation failure (Argo's "the sync attempt itself broke")
//      gets the rich GitOpsFailureCard treatment — structured cause/retry/
//      affected-resource fields, raw error collapsed by default. This is the
//      common operator question of "what's broken and is it stuck?".
//   2. Anything else (warnings, info, per-resource issues without a parent
//      operation failure) renders as a compact stacked alert row with the
//      same expand-for-more behavior as before.
//
// The split exists because operation failures are causally *upstream* of
// the resource issues that follow — treating them as peers in the same
// alert stack made one root cause look like three problems.
export function GitOpsIssuesBand({ issues, onSelectIssue }: { issues?: GitOpsIssue[] | null; onSelectIssue?: (issue: GitOpsIssue) => void }) {
  const list = issues ?? []
  if (list.length === 0) return null
  const operationFailure = list.find((i) => i.severity === 'critical' && i.scope === 'operation')
  const others = operationFailure ? list.filter((i) => i !== operationFailure) : list
  return (
    <div className="border-b border-theme-border">
      {operationFailure && <GitOpsFailureCard issue={operationFailure} onSelect={onSelectIssue} />}
      {others.length > 0 && <GitOpsCompactIssueStack issues={others} onSelectIssue={onSelectIssue} />}
    </div>
  )
}

// Structured failure card. One unit, owns the failure narrative end-to-end:
// title (parsed cause when recognized, falls back to raw reason), affected
// resource, retry posture, raw controller error in a collapsed details.
function GitOpsFailureCard({ issue, onSelect }: { issue: GitOpsIssue; onSelect?: (issue: GitOpsIssue) => void }) {
  const [showRaw, setShowRaw] = useState(false)
  const stuck = !!issue.stuck
  const ref = issue.refs?.[0]
  // Title prioritizes the parsed cause's first sentence. Without parsing we
  // get the bare phase ("Failed") which alone tells the user nothing — fall
  // back to the first sentence of the raw message in that case so something
  // useful is always at title weight.
  const title = issue.cause
    ? firstSentence(issue.cause)
    : firstSentence(issue.message) || issue.reason
  // The body sentence is the parsed cause's full text minus the first
  // sentence (which is in the title), or the rest of the message if we
  // didn't recognize the pattern. Either way the operator gets one
  // meaningful sentence at body weight, not a tempfile path prefix.
  const body = issue.cause ? remainderAfterFirstSentence(issue.cause) : remainderAfterFirstSentence(issue.message)
  return (
    <div
      className={clsx(
        'border-b border-theme-border px-4 py-3',
        stuck ? 'bg-red-500/15 dark:bg-red-500/15' : 'bg-red-500/[0.06]',
      )}
    >
      <div className="flex items-start gap-3">
        <CircleAlert className={clsx('mt-0.5 h-4 w-4 shrink-0', stuck ? 'text-red-700 dark:text-red-300' : 'text-red-600 dark:text-red-400')} />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
            <h3 className={clsx('text-sm font-semibold', stuck ? 'text-red-700 dark:text-red-200' : 'text-red-600 dark:text-red-300')}>{title}</h3>
            {/* Persistence pip: distinguishes "first failure, may resolve" from
                "stuck after retries, human action required". Argo's controller
                won't auto-recover past the retry ceiling so this is the single
                most operationally important signal on the card. */}
            {stuck && (
              <span className="rounded-sm bg-red-600/90 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-white">
                Stuck · retried {issue.retryCount}×
              </span>
            )}
            {!stuck && issue.retryCount && issue.retryCount > 0 && (
              <span className="text-[11px] text-theme-text-tertiary">retried {issue.retryCount}×</span>
            )}
          </div>
          {body && <p className="mt-1 text-[13px] text-theme-text-secondary">{body}</p>}
          {ref && (
            <dl className="mt-2 flex flex-wrap gap-x-5 gap-y-1 text-[12px]">
              <div className="flex gap-1.5">
                <dt className="text-theme-text-tertiary">Affected</dt>
                <dd className="font-medium text-theme-text-primary">{ref.kind} · <span className="font-mono">{ref.name}</span></dd>
              </div>
            </dl>
          )}
          <div className="mt-2 flex flex-wrap items-center gap-3">
            {onSelect && ref && (
              <button
                type="button"
                onClick={() => onSelect(issue)}
                className="inline-flex items-center gap-1 rounded border border-red-500/40 bg-theme-base px-2 py-1 text-[11px] font-medium text-red-700 hover:bg-red-500/10 dark:text-red-300"
              >
                View affected resource <ChevronRight className="h-3 w-3" />
              </button>
            )}
            <button
              type="button"
              onClick={() => setShowRaw((v) => !v)}
              className="inline-flex items-center gap-1 text-[11px] text-theme-text-tertiary transition-colors hover:text-theme-text-secondary"
            >
              {showRaw ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
              {showRaw ? 'Hide raw controller error' : 'Show raw controller error'}
            </button>
          </div>
          {showRaw && (
            <pre className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-all rounded border border-theme-border bg-theme-base px-3 py-2 font-mono text-[11px] text-theme-text-secondary">
              {issue.message}
            </pre>
          )}
        </div>
      </div>
    </div>
  )
}

// Compact stack for non-failure issues. Same expand-for-more behavior as the
// pre-redesign band, just split out so the failure card can own the top slot
// without inheriting the "+N more" mechanic that doesn't fit a rich card.
//
// Headline behavior is context-dependent: if the headline issue carries a
// resource ref AND a select callback is wired, clicking the headline jumps
// straight to that resource in Changes — the expand affordance is useful
// only when there's metadata "behind" the headline, not when the headline
// itself is the actionable thing.
function GitOpsCompactIssueStack({ issues, onSelectIssue }: { issues: GitOpsIssue[]; onSelectIssue?: (issue: GitOpsIssue) => void }) {
  const [expanded, setExpanded] = useState(false)
  if (issues.length === 0) return null
  const headline = issues[0]!
  const remaining = issues.length - 1
  const tone = severityTone(headline.severity)
  const headlineRef = headline.refs?.[0]
  const headlineActionable = !!(onSelectIssue && headlineRef)
  const canExpand = issues.length > 1
  return (
    <div className={tone.band}>
      <div className="flex items-stretch">
        <button
          type="button"
          onClick={() => {
            if (headlineActionable) {
              onSelectIssue?.(headline)
            } else if (canExpand) {
              setExpanded((v) => !v)
            }
          }}
          className={clsx(
            'flex flex-1 items-center gap-2 px-4 py-2 text-left text-xs transition-colors',
            (headlineActionable || canExpand) ? 'hover:bg-theme-hover/50' : 'cursor-default',
          )}
          disabled={!headlineActionable && !canExpand}
        >
          {tone.icon}
          <span className={clsx('font-semibold', tone.text)}>{headline.reason}</span>
          <span className="min-w-0 flex-1 truncate text-theme-text-secondary">{headline.message}</span>
          {headlineActionable && (
            <span className="shrink-0 text-[11px] font-medium text-theme-text-secondary">View →</span>
          )}
          {!headlineActionable && remaining > 0 && (
            <span className="shrink-0 text-[11px] text-theme-text-tertiary">+{remaining} more</span>
          )}
          {!headlineActionable && canExpand && (expanded ? <ChevronDown className="h-3.5 w-3.5 text-theme-text-tertiary" /> : <ChevronRight className="h-3.5 w-3.5 text-theme-text-tertiary" />)}
        </button>
        {/* When the headline is actionable AND there are more issues behind
            it, give the expand affordance its own button so neither action
            steals from the other. */}
        {headlineActionable && canExpand && (
          <button
            type="button"
            onClick={() => setExpanded((v) => !v)}
            aria-label={expanded ? 'Hide other issues' : `Show ${remaining} more issue${remaining === 1 ? '' : 's'}`}
            className="flex shrink-0 items-center gap-1 border-l border-theme-border/60 px-3 text-[11px] text-theme-text-tertiary hover:bg-theme-hover/50"
          >
            +{remaining}
            {expanded ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
          </button>
        )}
      </div>
      {expanded && canExpand && (
        <div className="divide-y divide-theme-border border-t border-theme-border bg-theme-base/40">
          {issues.slice(1).map((issue: GitOpsIssue, index: number) => {
            const t = severityTone(issue.severity)
            const ref = issue.refs?.[0]
            const actionable = !!(onSelectIssue && ref)
            return (
              <button
                key={`${issue.reason}-${index}`}
                type="button"
                onClick={() => actionable && onSelectIssue?.(issue)}
                disabled={!actionable}
                className={clsx(
                  'flex w-full items-start gap-2 px-4 py-2 text-left text-xs transition-colors',
                  actionable ? 'hover:bg-theme-hover/50' : 'cursor-default',
                )}
              >
                {t.icon}
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className={clsx('font-semibold', t.text)}>{issue.reason}</span>
                    <span className="text-[10px] uppercase tracking-wide text-theme-text-tertiary">{issue.scope}</span>
                  </div>
                  <p className="mt-0.5 text-theme-text-secondary">{issue.message}</p>
                  {issue.action && <p className="mt-0.5 text-[11px] text-theme-text-tertiary">{issue.action}</p>}
                </div>
                {actionable && (
                  <span className="shrink-0 self-center text-[11px] font-medium text-theme-text-secondary">View →</span>
                )}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}

// firstSentence/remainderAfterFirstSentence split a string at the first
// sentence boundary so the failure card can render "headline + body" from a
// single source string. Falls back to the whole string when there's no
// terminator — better than truncating mid-thought.
function firstSentence(s: string): string {
  if (!s) return ''
  const i = s.search(/[.!?](\s|$)/)
  if (i < 0) return s.trim()
  return s.slice(0, i + 1).trim()
}

function remainderAfterFirstSentence(s: string): string {
  if (!s) return ''
  const i = s.search(/[.!?](\s|$)/)
  if (i < 0) return ''
  return s.slice(i + 1).trim()
}

// Map an Issue severity to its visual elements via the canonical Severity
// tokens. The full SEVERITY_BADGE classes are used for the band (theme-aware
// background + text + border) instead of hand-rolled `bg-red-500/10` literals
// so dark-mode + the `alert` (orange) intermediate tier work consistently.
function severityTone(severity: string): { band: string; icon: ReactNode; text: string } {
  const sev = gitopsToSeverity(severity)
  const Icon = sev === 'error' ? CircleAlert : sev === 'warning' ? AlertTriangle : Info
  return {
    band: SEVERITY_BADGE[sev],
    icon: <Icon className={clsx('h-3.5 w-3.5 shrink-0', SEVERITY_TEXT[sev])} />,
    text: SEVERITY_TEXT[sev],
  }
}

interface GitOpsChangesViewProps {
  insight?: GitOpsInsight | null
  error?: Error | null
  onOpenResource?: (ref: GitOpsChange['ref']) => void
  // When set, the matching change row scrolls into view and gets a transient
  // highlight ring. Used when the user clicks "View →" on an issue alert in
  // the band above. Key shape: `${kind}/${namespace||''}/${name}` (group is
  // intentionally not part of the key — issue refs may not carry it).
  focusKey?: string | null
}

export function GitOpsChangesView({ insight, error, onOpenResource, focusKey }: GitOpsChangesViewProps) {
  const changes = insight?.changes ?? []
  const plan = insight?.plan ?? []
  // refs[focusKey] holds the DOM node of the row to scroll into view; the
  // map persists across renders so the effect can find the node even when
  // changes re-render (e.g. polling). Cleared per-render is fine — the next
  // map of row callbacks rebuilds on the same render that consumes focusKey.
  const rowRefs = useRef<Map<string, HTMLDivElement>>(new Map())
  useEffect(() => {
    if (!focusKey) return
    const node = rowRefs.current.get(focusKey)
    if (node) {
      node.scrollIntoView({ behavior: 'smooth', block: 'center' })
    }
  }, [focusKey])
  // Distinguish "still loading" from "fetch failed"; previously both fell
  // through to a permanent "Loading…" message that hid backend 5xxs.
  if (error && !insight) {
    return <InsightErrorState error={error} />
  }
  if (!insight) {
    return <CenteredText>Loading GitOps changes...</CenteredText>
  }
  // Cross-reference: build a map from refKey → plan order so each Change row
  // can advertise where it lands in the upcoming sync. Answers "if I sync
  // now, in what order will my drifted resources be reconciled?" without
  // forcing the user to scan both panels and mentally join them.
  const planOrderByRef = new Map<string, number>()
  for (const item of plan) {
    const key = refKey(item.ref)
    if (!planOrderByRef.has(key)) planOrderByRef.set(key, item.order)
  }
  return (
    <div className="h-full overflow-auto bg-theme-base p-4">
      <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_420px]">
        <section className="rounded-md border border-theme-border bg-theme-surface">
          <SectionHeader icon={GitCommit} title="Changes" hint={insight.summary.partialReason} />
          {/* Honest disclaimer about diff scope. Argo's CRD doesn't expose
              per-resource desired-vs-live diffs — those are computed on
              demand by the Argo API server, which Radar doesn't call. The
              user could otherwise stare at this list expecting to see what
              changed; instead they get drift status + per-resource sync
              messages + a clear pointer to the tools that can show the
              line-by-line diff. */}
          {changes.length > 0 && (
            <div className="border-b border-theme-border bg-theme-base/40 px-4 py-2 text-[11px] text-theme-text-tertiary">
              Radar reads each resource's drift status from the controller. For a line-by-line diff, use the Argo CD UI or run <code className="rounded bg-theme-elevated px-1 py-0.5 font-mono text-[10px]">argocd app diff {insight.summary.name}</code>.
            </div>
          )}
          {changes.length === 0 ? (
            <div className="p-4 text-sm text-theme-text-secondary">No changed resources reported by the GitOps controller.</div>
          ) : (
            <div className="divide-y divide-theme-border">
              {changes.map((change) => {
                const step = planOrderByRef.get(refKey(change.ref))
                const rowKey = refKey(change.ref)
                const focused = focusKey === rowKey
                const explanation = !change.syncError && !change.message
                  ? explainChangeStatus(change.sync, change.health, insight.summary)
                  : ''
                const hasInlineDetail = !!(
                  (change.drift && change.drift.entries.length > 0) ||
                  (change.recentEvents && change.recentEvents.length > 0)
                )
                return (
                  <ChangeRow
                    key={`${change.ref.group}/${change.ref.kind}/${change.ref.namespace}/${change.ref.name}`}
                    change={change}
                    step={step}
                    explanation={explanation}
                    focused={focused}
                    autoExpand={focused}
                    hasInlineDetail={hasInlineDetail}
                    onOpenResource={onOpenResource}
                    registerRef={(el) => {
                      // Map registry: register on mount, clean up on unmount
                      // so we don't hold references to detached nodes when
                      // changes re-flow.
                      if (el) {
                        rowRefs.current.set(rowKey, el)
                      } else {
                        rowRefs.current.delete(rowKey)
                      }
                    }}
                  />
                )
              })}
            </div>
          )}
        </section>
        <GitOpsPlanPanel plan={plan} tool={insight.summary.tool} />
      </div>
    </div>
  )
}

// Keys must match between Plan items and Change items for the cross-ref to
// work. Group can be omitted in either source, so we don't require it for
// equality — kind+namespace+name is the practical identifier here.
function refKey(ref: { kind: string; namespace?: string; name: string }): string {
  return `${ref.kind}/${ref.namespace || ''}/${ref.name}`
}

// explainChangeStatus turns a (sync, health) tuple into a one-sentence
// explanation that's contextual to the parent app's posture (auto-sync,
// in-flight operation). Returned only for cases where neither a sync error
// nor a health message is available — otherwise those carry the truth and
// this would just add noise. Empty string falls through to no row content.
//
// The "what to do" framing is intentional: badges already communicate state
// ("OutOfSync"); the row should communicate what the operator should do
// about it. With auto-sync on the answer is usually "wait, Argo will fix
// it"; with manual mode the answer is "click Sync".
function explainChangeStatus(
  sync: string | undefined,
  health: string | undefined,
  summary: GitOpsInsight['summary'],
): string {
  const isAuto = (summary.autoSyncMode ?? '').toLowerCase().startsWith('auto')
  const inFlight = (summary.operationPhase ?? '').toLowerCase() === 'running'
  if (sync === 'OutOfSync') {
    if (inFlight) return 'Live state differs from Git. A sync is in progress — wait for it to finish.'
    if (isAuto) return 'Live state differs from Git. Auto-sync should reconcile this within a few minutes; click Sync to force it.'
    return 'Live state differs from Git. Click Sync to apply the desired state.'
  }
  if (health === 'Degraded') return 'Resource reports an unhealthy state. Open the resource for events and logs.'
  if (health === 'Missing') return 'Declared in Git but not present in the cluster. Sync to create it.'
  if (health === 'Progressing') return 'Resource is mid-rollout (e.g. pods coming up). Should converge shortly.'
  if (health === 'Suspended') return 'Resource is paused (e.g. CronJob suspended, HPA disabled). Intentional unless surprising.'
  return ''
}

// ChangeRow: one resource in the Changes list. Two-zone interaction model:
//   - Whole row click → toggle inline expand (when there's inline detail
//     to show; otherwise it's a no-op so we don't tease an empty panel)
//   - "Open" pill on the right → open the standard resource drawer
//
// Inline expand pulls together the two new signals that turn "OutOfSync"
// from a label into an answer:
//   - Drift: per-field diff between desired (last-applied annotation) and
//     live spec — answers "what's actually different?"
//   - Recent events: ImagePullBackOff/FailedScheduling/etc. — answers
//     "what's the underlying cluster reason?"
function ChangeRow({
  change,
  step,
  explanation,
  focused,
  autoExpand,
  hasInlineDetail,
  onOpenResource,
  registerRef,
}: {
  change: GitOpsChange
  step: number | undefined
  explanation: string
  focused: boolean
  autoExpand: boolean
  hasInlineDetail: boolean
  onOpenResource?: (ref: GitOpsChange['ref']) => void
  registerRef: (el: HTMLDivElement | null) => void
}) {
  const [expanded, setExpanded] = useState(autoExpand && hasInlineDetail)
  // Auto-expand when an issue alert deep-links to this row — the user just
  // clicked "View →" on an issue, so they want to see the detail
  // immediately, not have to expand again.
  useEffect(() => {
    if (autoExpand && hasInlineDetail) setExpanded(true)
  }, [autoExpand, hasInlineDetail])
  const driftEntries = change.drift?.entries ?? []
  const events = change.recentEvents ?? []
  return (
    <div
      ref={registerRef}
      className={clsx(
        'transition-colors',
        focused && 'bg-amber-500/10 ring-2 ring-inset ring-amber-500/60',
      )}
    >
      <div className="grid w-full grid-cols-[minmax(0,1fr)_120px_120px_auto] gap-3 px-4 py-3 text-sm">
        <button
          type="button"
          onClick={() => hasInlineDetail && setExpanded((v) => !v)}
          disabled={!hasInlineDetail}
          className={clsx(
            'min-w-0 text-left',
            hasInlineDetail ? 'cursor-pointer hover:text-theme-text-primary' : 'cursor-default',
          )}
        >
          <div className="flex items-baseline gap-2">
            {hasInlineDetail && (
              expanded
                ? <ChevronDown className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
                : <ChevronRight className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
            )}
            <div className="min-w-0 truncate font-medium text-theme-text-primary">{change.ref.kind} / {change.ref.name}</div>
            {change.hookPhase && (
              <Tooltip content={`Sync hook: ${change.hookPhase}`} delay={200} wrapperClassName="shrink-0">
                <span className="rounded border border-violet-400/40 bg-violet-500/10 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-violet-700 dark:text-violet-400">
                  {change.hookPhase}
                </span>
              </Tooltip>
            )}
            {step !== undefined && (
              <Tooltip content={`Sync plan step ${step}`} delay={200} wrapperClassName="shrink-0">
                <span className="rounded border border-theme-border bg-theme-elevated px-1.5 py-0.5 font-mono text-[10px] text-theme-text-tertiary">
                  step {step}
                </span>
              </Tooltip>
            )}
            {/* Detail badges: surface that there's something to see in
                the expanded panel. Without these the user has no signal
                that clicking will reveal anything useful. */}
            {driftEntries.length > 0 && (
              <Tooltip content={`${driftEntries.length} field${driftEntries.length === 1 ? '' : 's'} differ from Git`} delay={200} wrapperClassName="shrink-0">
                <span className="rounded border border-amber-500/40 bg-amber-500/10 px-1.5 py-0.5 text-[10px] font-medium text-amber-700 dark:text-amber-400">
                  {driftEntries.length} diff
                </span>
              </Tooltip>
            )}
            {events.length > 0 && (
              <Tooltip content={`${events.length} recent event${events.length === 1 ? '' : 's'}`} delay={200} wrapperClassName="shrink-0">
                <span className={clsx(
                  'rounded border px-1.5 py-0.5 text-[10px] font-medium',
                  events.some((e) => e.type === 'Warning')
                    ? 'border-red-500/40 bg-red-500/10 text-red-700 dark:text-red-400'
                    : 'border-theme-border bg-theme-elevated text-theme-text-tertiary',
                )}>
                  {events.length} event{events.length === 1 ? '' : 's'}
                </span>
              </Tooltip>
            )}
          </div>
          <div className="ml-[18px] truncate text-xs text-theme-text-tertiary">{change.ref.namespace || '(cluster)'} {change.ref.group ? `· ${change.ref.group}` : ''}</div>
          {/* Per-resource sync error gets emphasis (red text) over the
              live health message — operators chasing a broken sync want
              the failure reason on the same row, not in a drawer. */}
          {change.syncError && (
            <Tooltip content={change.syncError} delay={400} wrapperClassName="ml-[18px] mt-1 block max-w-full">
              <span className="line-clamp-3 text-xs text-red-600 dark:text-red-400">{change.syncError}</span>
            </Tooltip>
          )}
          {change.message && !change.syncError && <div className="ml-[18px] mt-1 line-clamp-2 text-xs text-theme-text-secondary">{change.message}</div>}
          {!change.syncError && !change.message && explanation && (
            <div className="ml-[18px] mt-1 text-xs text-theme-text-tertiary">{explanation}</div>
          )}
        </button>
        <div className="self-start"><SyncStatusBadge sync={(change.sync || change.category || 'Unknown') as any} /></div>
        <div className="self-start"><HealthStatusBadge health={(change.health || 'Unknown') as any} /></div>
        <div className="self-start">
          {onOpenResource && (
            <button
              type="button"
              onClick={() => onOpenResource(change.ref)}
              className="rounded border border-theme-border bg-theme-base px-2 py-0.5 text-[11px] text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
            >
              Open
            </button>
          )}
        </div>
      </div>
      {expanded && hasInlineDetail && (
        <div className="border-t border-theme-border bg-theme-base/40 px-4 py-3">
          {driftEntries.length > 0 && <DriftPanel drift={change.drift!} />}
          {events.length > 0 && <RecentEventsPanel events={events} />}
        </div>
      )}
    </div>
  )
}

// DriftPanel renders the structured per-field diff. Format mimics a
// `diff -u` summary: removed paths in red, added in green, changed shown
// inline as "old → new". Path is monospace; values are JSON-encoded and
// pre-wrapped so structured values (objects, arrays) render readably.
function DriftPanel({ drift }: { drift: NonNullable<GitOpsChange['drift']> }) {
  return (
    <div>
      <div className="mb-2 flex items-baseline justify-between gap-2">
        <h4 className="text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">Field diff</h4>
        <span className="text-[10px] text-theme-text-tertiary">
          desired (Git) → live ·
          {drift.truncated ? ' showing first 50 entries' : ` ${drift.entries.length} field${drift.entries.length === 1 ? '' : 's'}`}
        </span>
      </div>
      <div className="space-y-1 font-mono text-[11px]">
        {drift.entries.map((entry, i) => (
          <DriftEntryRow key={`${entry.path}-${i}`} entry={entry} />
        ))}
      </div>
    </div>
  )
}

function DriftEntryRow({ entry }: { entry: NonNullable<GitOpsChange['drift']>['entries'][number] }) {
  if (entry.op === 'removed') {
    return (
      <div>
        <span className="text-red-600 dark:text-red-400">- {entry.path}</span>
        {entry.desired && <span className="ml-2 text-theme-text-secondary">{entry.desired}</span>}
      </div>
    )
  }
  if (entry.op === 'added') {
    return (
      <div>
        <span className="text-emerald-700 dark:text-emerald-400">+ {entry.path}</span>
        {entry.live && <span className="ml-2 text-theme-text-secondary">{entry.live}</span>}
      </div>
    )
  }
  return (
    <div>
      <span className="text-amber-700 dark:text-amber-400">~ {entry.path}</span>
      <span className="ml-2 text-theme-text-tertiary">{entry.desired}</span>
      <span className="mx-1 text-theme-text-tertiary">→</span>
      <span className="text-theme-text-primary">{entry.live}</span>
    </div>
  )
}

// RecentEventsPanel surfaces the last few events involving this resource.
// Warning events get a red bar so the eye lands on them; normals are
// muted. Aggregation count (when present) is a critical signal — "this
// failed 47 times" is very different from "this failed once".
function RecentEventsPanel({ events }: { events: NonNullable<GitOpsChange['recentEvents']> }) {
  return (
    <div className="mt-3 first:mt-0">
      <h4 className="mb-2 text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">Recent events</h4>
      <div className="space-y-1">
        {events.map((e, i) => {
          const isWarning = e.type === 'Warning'
          return (
            <div
              key={`${e.reason}-${e.lastTimestamp}-${i}`}
              className={clsx(
                'rounded border px-2 py-1.5 text-[11px]',
                isWarning ? 'border-red-500/40 bg-red-500/5' : 'border-theme-border bg-theme-base',
              )}
            >
              <div className="flex items-baseline gap-2">
                <span className={clsx('font-semibold', isWarning ? 'text-red-700 dark:text-red-400' : 'text-theme-text-primary')}>
                  {e.reason}
                </span>
                {e.count && e.count > 1 && (
                  <span className="text-[10px] text-theme-text-tertiary">×{e.count}</span>
                )}
                <span className="ml-auto text-[10px] text-theme-text-tertiary">{formatRelativeTime(e.lastTimestamp)}</span>
              </div>
              <p className="mt-0.5 text-theme-text-secondary">{e.message}</p>
            </div>
          )
        })}
      </div>
    </div>
  )
}

function formatRelativeTime(value: string): string {
  if (!value) return ''
  const t = Date.parse(value)
  if (!Number.isFinite(t)) return value
  const diff = Date.now() - t
  if (diff < 60_000) return 'just now'
  const m = Math.floor(diff / 60_000)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  return `${Math.floor(h / 24)}d ago`
}

function GitOpsPlanPanel({ plan, tool }: { plan?: GitOpsPlanItem[] | null; tool?: string }) {
  const items = plan ?? []
  // Group consecutive items by wave when at least one item declares a wave —
  // makes "what runs in what order" visually obvious for multi-wave apps.
  // When no items have waves (the common case for single-app syncs), fall
  // through to a flat list to avoid an awkward "Wave (none)" header.
  const hasAnyWave = items.some((i) => i.waveSet)
  return (
    <section className="rounded-md border border-theme-border bg-theme-surface">
      <SectionHeader icon={ListChecks} title="Sync Plan" hint={tool === 'argocd' ? 'Argo order: phase, wave, kind, then name.' : 'Flux order follows source and dependency relationships.'} />
      <div className="max-h-[640px] overflow-auto">
        {items.length === 0 ? (
          <div className="p-4 text-sm text-theme-text-secondary">No plan data available.</div>
        ) : (
          <div className="divide-y divide-theme-border">
            {items.map((item, index) => {
              const prev = items[index - 1]
              const showWaveHeader = hasAnyWave && (index === 0 || (prev?.wave ?? null) !== (item.wave ?? null) || prev?.waveSet !== item.waveSet)
              return (
                <div key={`${item.order}-${item.ref.kind}-${item.ref.name}`}>
                  {showWaveHeader && (
                    <div className="bg-theme-base/50 px-4 py-1 text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
                      {item.waveSet ? `Wave ${item.wave}` : 'Default wave'}
                    </div>
                  )}
                  <div className="grid grid-cols-[60px_minmax(0,1fr)] gap-2 px-4 py-3 text-sm">
                    <div className="text-right font-mono text-[11px] text-theme-text-tertiary">step {item.order}</div>
                    <div className="min-w-0">
                      <div className="flex items-baseline gap-2">
                        <span className="min-w-0 truncate font-medium text-theme-text-primary">{item.ref.kind} / {item.ref.name}</span>
                        {item.hook && (
                          <Tooltip content={`Sync hook: ${item.hook}`} delay={200} wrapperClassName="shrink-0">
                            <span className="rounded border border-violet-400/40 bg-violet-500/10 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-violet-700 dark:text-violet-400">
                              {item.hook}
                            </span>
                          </Tooltip>
                        )}
                      </div>
                      <div className="mt-1 flex flex-wrap gap-1">
                        {item.phase && <Chip label="phase" value={item.phase} />}
                        {item.relationship && !isUnknownChipValue(item.relationship) && <Chip value={item.relationship} />}
                        {item.status && !isUnknownChipValue(item.status) && <Chip value={item.status} />}
                      </div>
                    </div>
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </div>
    </section>
  )
}

interface GitOpsActivityInsightViewProps {
  insight?: GitOpsInsight | null
  error?: Error | null
  // Optional rollback callback. When provided AND insight.capabilities.rollback
  // is true, history rows with an ID expose a Rollback button that fires this
  // with the target entry. The consumer is responsible for the confirmation
  // dialog + the actual mutation.
  onRollback?: (item: GitOpsHistoryItem) => void
}

export function GitOpsActivityInsightView({ insight, error, onRollback }: GitOpsActivityInsightViewProps) {
  if (error && !insight) return <InsightErrorState error={error} />
  if (!insight) return <CenteredText>Loading GitOps activity...</CenteredText>
  const canRollback = !!insight.capabilities?.rollback && !!onRollback
  // Auto-sync makes rollback futile — the controller would re-sync to HEAD
  // immediately. Argo's own Web UI disables the button in this state. Detect
  // by autoSyncMode prefix so "Auto · prune", "Auto · self-heal", etc. all match.
  const autoSyncBlocksRollback = (insight.summary?.autoSyncMode ?? '').toLowerCase().startsWith('auto')
  return (
    <div className="h-full overflow-auto bg-theme-base p-4">
      {/* History is the only section here. The current operation surfaces as
          the top history row (phase + message + finishedAt come from
          operationState). Issues live in GitOpsIssuesBand at page top. */}
      <section className="rounded-md border border-theme-border bg-theme-surface">
        <SectionHeader
          icon={Clock3}
          title="History"
          hint={canRollback ? 'Each revision can be rolled back to.' : undefined}
        />
        <HistoryRows
          items={insight.history ?? []}
          canRollback={canRollback}
          rollbackBlockedReason={autoSyncBlocksRollback ? 'Auto-sync is enabled. Disable it to enable rollback — otherwise the controller will sync forward to HEAD again.' : undefined}
          onRollback={onRollback}
        />
      </section>
    </div>
  )
}

// Vertical timeline; left-gutter dot color encodes outcome at a glance.
function HistoryRows({
  items,
  canRollback = false,
  rollbackBlockedReason,
  onRollback,
}: {
  items: GitOpsHistoryItem[]
  canRollback?: boolean
  // When set, the Rollback button renders disabled with this string as the
  // tooltip explaining why. Null/undefined means rollback is enabled normally.
  rollbackBlockedReason?: string
  onRollback?: (item: GitOpsHistoryItem) => void
}) {
  if (items.length === 0) {
    return (
      <div className="flex items-center gap-3 px-4 py-6 text-sm text-theme-text-tertiary">
        <span className="h-2 w-2 rounded-full border border-dashed border-theme-text-tertiary" />
        <span>No deployments yet.</span>
      </div>
    )
  }
  return (
    <ol className="px-4 py-3">
      {items.map((item, index) => {
        const tone = entryTone(item)
        const isLast = index === items.length - 1
        const sourceDisplay = compactSource(item.source)
        // Only history entries with a numeric ID can be rolled back to —
        // the in-flight current operation row has no ID and rolling "back"
        // to it is meaningless.
        const showRollback = canRollback && !!item.id && !!onRollback
        return (
          // `group` enables the Rollback button's hover-reveal; baseline
          // opacity-40 keeps it touch-discoverable.
          <li key={`${item.id}-${item.revision}-${index}`} className="group relative grid grid-cols-[16px_minmax(0,1fr)] gap-3 pb-4 last:pb-0">
            <div className="relative flex justify-center">
              {!isLast && <span className="absolute left-1/2 top-3 h-full w-[2px] -translate-x-1/2 bg-theme-text-tertiary/30" />}
              <Tooltip content={item.phase || tone.inferredFrom || 'unknown'} delay={120}>
                <span className={clsx('relative mt-1 h-2.5 w-2.5 rounded-full ring-2 ring-theme-surface', tone.dot)} />
              </Tooltip>
            </div>
            <div className="min-w-0 text-sm">
              <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
                <span className="font-mono text-xs text-theme-text-primary">{item.revision || item.phase || '-'}</span>
                <span className="text-[11px] text-theme-text-tertiary">{formatRelative(item.deployedAt)}</span>
                {item.initiatedBy && (
                  <span className="text-[11px] text-theme-text-tertiary">by {item.initiatedBy}</span>
                )}
                {showRollback && (
                  <Tooltip
                    content={rollbackBlockedReason || `Roll back to revision ${item.revision || `#${item.id}`}`}
                    delay={200}
                    wrapperClassName="ml-auto"
                  >
                    {/* Two visually distinct states:
                        - Enabled: low-emphasis baseline (opacity-40), brightens on row hover
                        - Disabled (auto-sync on): full opacity but desaturated, cursor-not-allowed,
                          no hover affordance — reads unambiguously as "not actionable, here's why" */}
                    <button
                      type="button"
                      onClick={() => !rollbackBlockedReason && onRollback?.(item)}
                      disabled={!!rollbackBlockedReason}
                      aria-disabled={!!rollbackBlockedReason}
                      className={clsx(
                        'rounded border px-1.5 py-0.5 text-[10px] transition-opacity',
                        rollbackBlockedReason
                          ? 'cursor-not-allowed border-theme-border bg-theme-base text-theme-text-tertiary'
                          : 'border-theme-border bg-theme-elevated text-theme-text-secondary opacity-40 hover:bg-theme-hover hover:text-theme-text-primary hover:opacity-100 focus-visible:opacity-100 group-hover:opacity-100'
                      )}
                    >
                      Rollback
                    </button>
                  </Tooltip>
                )}
              </div>
              {sourceDisplay && (
                <Tooltip content={item.source} delay={400} wrapperClassName="mt-0.5 block max-w-full">
                  <span className="block truncate text-xs text-theme-text-secondary">{sourceDisplay}</span>
                </Tooltip>
              )}
              {item.message && (
                <div className={clsx('mt-0.5 line-clamp-2 text-[11px]', sourceDisplay ? 'text-theme-text-tertiary' : 'text-theme-text-secondary')}>{item.message}</div>
              )}
            </div>
          </li>
        )
      })}
    </ol>
  )
}


function SectionHeader({ icon: Icon, title, hint }: { icon: typeof GitBranch; title: string; hint?: string }) {
  return (
    <div className="flex items-center gap-2 border-b border-theme-border px-4 py-2.5">
      <Icon className="h-4 w-4 text-theme-text-tertiary" />
      <h2 className="text-sm font-semibold text-theme-text-primary">{title}</h2>
      {hint && (
        <Tooltip content={hint} delay={120}>
          <span className="cursor-help text-theme-text-tertiary hover:text-theme-text-secondary">
            <Info className="h-3.5 w-3.5" />
          </span>
        </Tooltip>
      )}
    </div>
  )
}

function Chip({ label, value }: { label?: string; value: string }) {
  return <span className="rounded border border-theme-border bg-theme-elevated px-1.5 py-0.5 text-[10px] text-theme-text-secondary">{label ? `${label}: ` : ''}{value}</span>
}

function CenteredText({ children }: { children: ReactNode }) {
  return <div className="flex h-full items-center justify-center text-sm text-theme-text-secondary">{children}</div>
}

// Surfaced when the insights endpoint errors. Without this the subviews
// would render their "Loading…" placeholder forever, hiding the failure
// from the user and from the operator looking at logs.
function InsightErrorState({ error }: { error: Error }) {
  return (
    <div className="flex h-full items-start justify-center bg-theme-base p-6">
      <div className={clsx('max-w-2xl rounded-md p-4 text-sm', SEVERITY_BADGE.error)}>
        <div className="flex items-start gap-2">
          <CircleAlert className="mt-0.5 h-4 w-4 shrink-0" />
          <div className="min-w-0">
            <div className="font-semibold">Failed to load GitOps insights</div>
            <p className="mt-1 break-words opacity-90">{error.message || 'Unknown error'}</p>
          </div>
        </div>
      </div>
    </div>
  )
}

function formatRelative(value?: string) {
  if (!value) return '-'
  const time = new Date(value).getTime()
  if (!Number.isFinite(time)) return value
  const seconds = Math.max(0, Math.floor((Date.now() - time) / 1000))
  if (seconds < 60) return `${seconds}s ago`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  return `${days}d ago`
}
