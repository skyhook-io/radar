import { AlertTriangle, ArrowUpDown, ChevronDown, ChevronRight, ChevronUp, CircleAlert, Clock3, GitBranch, GitCommit, Info, Loader2, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { PaneLoader } from '../../ui/PaneLoader'
import { clsx } from 'clsx'
import { Fragment, useEffect, useRef, useState, type ReactNode } from 'react'
import type { GitOpsChange, GitOpsHistoryItem, GitOpsInsight, GitOpsInsightRef, GitOpsIssue, GitOpsPlanItem, GitOpsRemediation, GitOpsResourceTree, GitOpsTreeNode } from '../../../types'
import { HealthStatusBadge, SyncStatusBadge } from '../GitOpsStatusBadge'
import { SEVERITY_BADGE, SEVERITY_TEXT } from '../../../utils/badge-colors'
import { formatRelativeAgeTime } from '../../../utils/format'
import { Tooltip } from '../../ui/Tooltip'
import { SearchBox } from '../../ui/SearchBox'
import { FilterPill, type FilterPillTone } from '../../ui/FilterPill'
import { Collapse, CollapseChevron } from '../../ui/Collapse'
import type { SortDir } from '../../ui/SortableTh'
import {
  changeHealth,
  changeMatchesFacets,
  changeMatchesSearch,
  changeSync,
  compactSource,
  entryTone,
  gitopsToSeverity,
  healthStatusRank,
  isArgoResourceSyncEligible,
  messageToPhase,
  normalizeHealthStatus,
  normalizeSyncStatus,
  resourceStatusCounts,
  syncStatusRank,
  type ResourceSortKey,
  type ResourceStatusFacet,
} from './insights-helpers'

interface GitOpsStatusStripProps {
  insight?: GitOpsInsight | null
  loading?: boolean
}

// Status strip carries the operation chip (when a sync is in flight or
// failed) plus reference metadata. The exact field set depends on the
// resource's lifecycle phase:
//
//   - Healthy / steady states: Source / Revision / Last reconcile / Sync mode
//     answer "what is this app pointing at and when did it last reconcile".
//   - Terminating: those fields become operationally meaningless (the
//     controller has stopped reconciling and "Sync mode: Auto" is a lie
//     during cleanup). Replace with deletion-relevant facts: pending
//     duration, finalizers, and a hint about which controller owns
//     cleanup. Source/Revision still exist on the resource if the user
//     wants to dig — they're available in the YAML view of the standard
//     resource drawer; promoting them here when they don't apply just
//     creates contradictory state on the page.
//
// Health and Sync badges live next to the title in the page header —
// pair them there with identity, not here.
export function GitOpsStatusStrip({ insight, loading }: GitOpsStatusStripProps) {
  const summary = insight?.summary
  if (loading) {
    return <div className="h-8 animate-pulse border-b border-theme-border bg-theme-base" />
  }
  if (!summary) return null

  if (summary.terminating) {
    return <TerminatingStatusStrip summary={summary} />
  }

  const operation = liveOperationPhase(summary.operationPhase)
  const revision = summary.lastRevision || summary.targetRevision || ''
  const shortRev = shortRevisionForCommit(revision)
  const commitUrl = revision ? commitURLForRepo(summary.source, revision) : null
  const reconcileAge = formatRelative(summary.lastReconcile)
  const healthSummary = buildHealthSummary(insight.changes ?? [])
  // STUCK belongs with the operation chip — it's a property of the same
  // state ("failed AND won't self-recover"). Co-locating FAILED + STUCK ·
  // RETRIED N× lets the operator scan the whole operational verdict in
  // one glance instead of reading the failure card body to learn whether
  // the controller has given up.
  const operationFailure = (insight.issues ?? []).find(
    (i) => i.severity === 'critical' && i.scope === 'operation' && i.stuck,
  )
  const operationTooltipMessage = summary.rawOperationMessage || summary.operationMessage
  return (
    <div className="border-b border-theme-border bg-theme-base px-4 py-2">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5">
        {operation && (
          <Tooltip content={`Last sync operation: ${operation}`} delay={200}>
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
        {operationFailure && (
          <Tooltip content="Argo's retry budget is exhausted — the operation won't self-recover, action is required." delay={200}>
            <span className="rounded-sm bg-red-600/90 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-white">
              Stuck · retried {operationFailure.retryCount}×
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
          <Tooltip content={operationTooltipMessage} delay={400} wrapperClassName="min-w-0 max-w-[60ch]">
            <span className="block truncate text-[11px] text-theme-text-secondary">
              {summary.operationMessage}
            </span>
          </Tooltip>
        )}
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-theme-text-tertiary">
          {shortRev && (
            <span className="inline-flex items-baseline gap-1">
              <span className="shrink-0">Latest revision:</span>
              {commitUrl ? (
                <Tooltip content={`Open commit ${revision} on the remote`} delay={400}>
                  <a
                    href={commitUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="font-mono font-medium text-theme-text-primary underline-offset-2 hover:underline"
                  >
                    {shortRev}
                  </a>
                </Tooltip>
              ) : (
                <Tooltip content={revision} delay={400}>
                  <span className="font-mono font-medium text-theme-text-primary">{shortRev}</span>
                </Tooltip>
              )}
              {reconcileAge && <span className="text-theme-text-tertiary">· {reconcileAge}</span>}
            </span>
          )}
          {!shortRev && reconcileAge && <MetaFact label="Last reconcile" value={reconcileAge} />}
          {healthSummary && (
            <span className={clsx('inline-flex items-baseline gap-1 font-medium', healthSummary.tone)}>
              {healthSummary.text}
            </span>
          )}
        </div>
      </div>
    </div>
  )
}

// shortRevisionForCommit normalizes a controller-reported revision to a
// commit-ish short form. Git SHAs collapse to 7 chars; revisions that
// already look short (tags, semver, "master@sha1:..." truncated) pass
// through. Returns empty when the input is empty/whitespace.
function shortRevisionForCommit(revision: string): string {
  const trimmed = revision.trim()
  if (!trimmed) return ''
  // Flux records "master@sha1:9f4969..." — strip the prefix and shorten.
  const sha1Match = trimmed.match(/sha1:([0-9a-f]{7,40})/i)
  if (sha1Match) return sha1Match[1].slice(0, 7)
  // Pure 40-char SHA → 7 chars.
  if (/^[0-9a-f]{40}$/i.test(trimmed)) return trimmed.slice(0, 7)
  return trimmed
}

// gitTreeURL derives a "browse the source directory" URL from a joined
// summary.source ("repoURL · path · chart") plus a revision. Used when a
// resource is Missing — we can't show its live state, but we CAN point at
// where it's declared in Git so the operator can read the source. Returns
// null when the host isn't recognized; caller hides the affordance then.
function gitTreeURL(source: string | undefined, revision: string): string | null {
  if (!source) return null
  const parts = source.split(' · ').map((s) => s.trim()).filter(Boolean)
  if (parts.length === 0) return null
  const repoOnly = parts[0]!
  const subPath = parts[1] || ''
  const clean = repoOnly.replace(/\.git$/, '').replace(/\/$/, '')
  let sha = revision || 'HEAD'
  const m = (revision || '').match(/sha1:([0-9a-f]{7,40})/i)
  if (m) sha = m[1]
  const pathSegment = subPath ? `/${encodeURI(subPath)}` : ''
  if (/^https?:\/\/github\.com\//.test(clean)) return `${clean}/tree/${sha}${pathSegment}`
  if (/^https?:\/\/gitlab\.com\//.test(clean)) return `${clean}/-/tree/${sha}${pathSegment}`
  if (/^https?:\/\/bitbucket\.org\//.test(clean)) return `${clean}/src/${sha}${pathSegment}`
  return null
}

// commitURLForRepo derives the remote URL for a commit when we recognize
// the source host. Returns null for unrecognized hosts (private gitea,
// self-hosted, etc.) — the caller renders the SHA as plain text in that
// case rather than a wrong-looking link.
//
// `source` arrives joined as "repoURL · path · chart" (see Summary.Source
// builder server-side). The repo URL is always the first segment; the rest
// are path/chart suffixes that would confuse the commit URL constructor.
function commitURLForRepo(source: string | undefined, revision: string): string | null {
  if (!source || !revision) return null
  const repoOnly = source.split(' · ')[0].trim()
  const clean = repoOnly.replace(/\.git$/, '').replace(/\/$/, '')
  // Pull out a usable SHA: full or short SHA; or the sha1:... form Flux uses.
  let sha = revision
  const m = revision.match(/sha1:([0-9a-f]{7,40})/i)
  if (m) sha = m[1]
  if (!/^[0-9a-f]{7,40}$/i.test(sha)) return null
  // Recognized hosts. Self-hosted GitLab/Gitea variants would need the
  // server to surface a `commitUrlTemplate` annotation — out of scope here.
  if (/^https?:\/\/github\.com\//.test(clean)) return `${clean}/commit/${sha}`
  if (/^https?:\/\/gitlab\.com\//.test(clean)) return `${clean}/-/commit/${sha}`
  if (/^https?:\/\/bitbucket\.org\//.test(clean)) return `${clean}/commits/${sha}`
  return null
}

// buildHealthSummary turns the per-resource Changes list into a one-line
// "did everything come up?" summary. All-healthy reads green and explicit
// ("4/4 resources healthy"); mixed states list the unhealthy buckets so
// the user sees at a glance what didn't make it.
function buildHealthSummary(changes: GitOpsChange[]): { text: string; tone: string } | null {
  if (changes.length === 0) return null
  let healthy = 0
  let degraded = 0
  let missing = 0
  let outOfSync = 0
  let other = 0
  for (const c of changes) {
    const cat = c.category
    if (cat === 'Synced' || c.health === 'Healthy') healthy++
    else if (cat === 'Degraded') degraded++
    else if (cat === 'Missing') missing++
    else if (cat === 'OutOfSync') outOfSync++
    else other++
  }
  const total = changes.length
  if (healthy === total) {
    return { text: `✓ ${total}/${total} resources healthy`, tone: 'text-emerald-500' }
  }
  const parts: string[] = []
  if (degraded > 0) parts.push(`${degraded} Degraded`)
  if (missing > 0) parts.push(`${missing} Missing`)
  if (outOfSync > 0) parts.push(`${outOfSync} OutOfSync`)
  if (healthy > 0) parts.push(`${healthy} healthy`)
  if (other > 0) parts.push(`${other} other`)
  return { text: parts.join(' · '), tone: 'text-amber-500' }
}

// TerminatingStatusStrip swaps the regular metadata for deletion-relevant
// facts (pending duration, finalizers). Source/Revision move behind a
// disclosure — still available for forensics but not noise during deletion.
function TerminatingStatusStrip({ summary }: { summary: NonNullable<GitOpsInsight['summary']> }) {
  const [showHistorical, setShowHistorical] = useState(false)
  const pending = formatRelative(summary.terminationStartedAt) || 'recently'
  const finalizers = summary.finalizers ?? []
  return (
    <div className="border-b border-orange-500/20 bg-orange-500/[0.04] px-4 py-2">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5">
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-theme-text-tertiary">
          <MetaFact label="Pending deletion" value={pending} />
          {finalizers.length > 0 && (
            <MetaFact label="Finalizers" value={finalizers.join(', ')} mono />
          )}
        </div>
        <button
          type="button"
          onClick={() => setShowHistorical((v) => !v)}
          className="shrink-0 text-[11px] text-theme-text-tertiary transition-colors hover:text-theme-text-secondary"
        >
          {showHistorical ? '− Hide pre-deletion metadata' : '+ Show pre-deletion metadata'}
        </button>
      </div>
      <Collapse open={showHistorical}>
        <div className="mt-2 flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1 border-t border-theme-border/40 pt-2 text-[11px] text-theme-text-tertiary">
          {summary.source && <MetaFact label="Source" value={summary.source} />}
          {(summary.lastRevision || summary.targetRevision) && (
            <MetaFact label="Revision" value={summary.lastRevision || summary.targetRevision || '-'} mono />
          )}
          {summary.lastReconcile && <MetaFact label="Last reconcile" value={formatRelative(summary.lastReconcile)} />}
          {summary.autoSyncMode && <MetaFact label="Sync mode" value={summary.autoSyncMode} />}
        </div>
      </Collapse>
    </div>
  )
}

function isInFlightPhase(phase: string): boolean {
  const p = phase.toLowerCase()
  return p.includes('running') || p.includes('terminat') || p.includes('progress') || p.includes('reconcil')
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
  // inline-flex so each fact sizes to content; flex-wrap on the parent
  // handles row breaks. max-w-full guards the pathological "value wider
  // than viewport" case where truncate + tooltip take over.
  return (
    <span className="inline-flex min-w-0 max-w-full items-baseline gap-1">
      <span className="shrink-0">{label}:</span>
      <Tooltip content={value} delay={400} wrapperClassName="min-w-0">
        <span className={clsx('block truncate font-medium text-theme-text-primary', mono && 'font-mono')}>{value}</span>
      </Tooltip>
    </span>
  )
}

// GitOpsIssuesBand renders top-of-page issues with three render paths:
// lifecycle banner > critical operation failure (GitOpsFailureCard) >
// stacked alert rows for everything else. When `terminating` is true,
// the non-lifecycle paths fold into a default-collapsed disclosure since
// pre-deletion operation failures are forensic-only — the controller
// stopped reconciling once deletion started.
export function GitOpsIssuesBand({
  issues,
  onSelectIssue,
  onRemediate,
  remediationPending,
  terminating,
}: {
  issues?: GitOpsIssue[] | null
  onSelectIssue?: (issue: GitOpsIssue) => void
  onRemediate?: (remediation: GitOpsRemediation) => void
  remediationPending?: boolean
  terminating?: boolean
}) {
  const list = issues ?? []
  if (list.length === 0) return null
  const lifecycle = list.find((i) => i.scope === 'lifecycle')
  const nonLifecycle = lifecycle ? list.filter((i) => i !== lifecycle) : list
  const operationFailure = nonLifecycle.find((i) => i.severity === 'critical' && i.scope === 'operation')
  const others = operationFailure ? nonLifecycle.filter((i) => i !== operationFailure) : nonLifecycle
  const showHistoricalCollapsed = terminating && (operationFailure || others.length > 0)
  return (
    <div className="border-b border-theme-border">
      {lifecycle && <GitOpsLifecycleBanner issue={lifecycle} />}
      {showHistoricalCollapsed ? (
        <GitOpsHistoricalIssuesDisclosure operationFailure={operationFailure} others={others} onSelectIssue={onSelectIssue} onRemediate={onRemediate} remediationPending={remediationPending} />
      ) : (
        <>
          {operationFailure && <GitOpsFailureCard issue={operationFailure} onSelect={onSelectIssue} onRemediate={onRemediate} remediationPending={remediationPending} />}
          {others.length > 0 && <GitOpsCompactIssueStack issues={others} onSelectIssue={onSelectIssue} />}
        </>
      )}
    </div>
  )
}

// GitOpsLifecycleBanner promotes the lifecycle Issue (resource pending
// deletion) above all other issues with a distinct orange treatment that
// matches the [Terminating] chip in the title row. Nothing else on the
// page should dominate when the resource is being deleted.
function GitOpsLifecycleBanner({ issue }: { issue: GitOpsIssue }) {
  return (
    <div className="border-b border-orange-500/30 bg-orange-500/[0.08] px-4 py-3">
      <div className="flex items-start gap-3">
        <Trash2 className="mt-0.5 h-4 w-4 shrink-0 text-orange-400" />
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-2">
            <h3 className="text-sm font-semibold text-orange-300">{issue.reason}</h3>
            <span className="text-[10px] uppercase tracking-wide text-orange-400/70">Lifecycle</span>
          </div>
          <p className="mt-1 text-[13px] text-theme-text-secondary">{issue.message}</p>
          {issue.cause && <p className="mt-1 text-[12px] text-orange-300/90">{issue.cause}</p>}
          {issue.action && <p className="mt-1 text-[11px] text-theme-text-tertiary">{issue.action}</p>}
        </div>
      </div>
    </div>
  )
}

// GitOpsHistoricalIssuesDisclosure wraps the regular issue rendering in a
// collapsible disclosure when the resource is Terminating. Default
// collapsed because pre-deletion failures are forensic context, not
// actionable. Counts the issues so the operator can see at a glance how
// many were active before deletion was initiated.
function GitOpsHistoricalIssuesDisclosure({
  operationFailure,
  others,
  onSelectIssue,
  onRemediate,
  remediationPending,
}: {
  operationFailure?: GitOpsIssue
  others: GitOpsIssue[]
  onSelectIssue?: (issue: GitOpsIssue) => void
  onRemediate?: (remediation: GitOpsRemediation) => void
  remediationPending?: boolean
}) {
  const [expanded, setExpanded] = useState(false)
  const total = (operationFailure ? 1 : 0) + others.length
  if (total === 0) return null
  return (
    <div className="border-b border-theme-border bg-theme-surface/40">
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="flex w-full items-center justify-between px-4 py-2 text-left transition-colors hover:bg-theme-hover/40"
        aria-expanded={expanded}
      >
        <div className="flex items-center gap-2">
          <CollapseChevron open={expanded} className="h-3.5 w-3.5" />
          <span className="text-[12px] font-medium text-theme-text-secondary">
            Pre-deletion issues ({total})
          </span>
          <span className="text-[11px] text-theme-text-tertiary">
            captured before deletion was initiated — forensic context, not actionable
          </span>
        </div>
      </button>
      <Collapse open={expanded}>
        <div className="border-t border-theme-border">
          {operationFailure && <GitOpsFailureCard issue={operationFailure} onSelect={onSelectIssue} onRemediate={onRemediate} remediationPending={remediationPending} />}
          {others.length > 0 && <GitOpsCompactIssueStack issues={others} onSelectIssue={onSelectIssue} />}
        </div>
      </Collapse>
    </div>
  )
}

// Structured failure card. One unit, owns the failure narrative end-to-end:
// title (parsed cause when recognized, falls back to raw reason), affected
// resource, retry posture, raw controller error in a collapsed details.
function GitOpsFailureCard({
  issue,
  onSelect,
  onRemediate,
  remediationPending,
}: {
  issue: GitOpsIssue
  onSelect?: (issue: GitOpsIssue) => void
  // onRemediate fires when the contextual fix button is clicked. The host
  // app (web/) dispatches on issue.remediation.kind to run the right
  // mutation (create namespace, etc.). Undefined → button is hidden.
  onRemediate?: (remediation: GitOpsRemediation) => void
  remediationPending?: boolean
}) {
  const [showRaw, setShowRaw] = useState(false)
  const stuck = !!issue.stuck
  const ref = issue.refs?.[0]
  const rawControllerMessage = issue.rawMessage || issue.message
  const rawControllerLabel = issue.rawMessage ? 'raw controller error' : 'controller message'
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
            {/* Low-key retry count for non-stuck failures so the operator
                knows whether to wait or dig. Stuck failures get the
                prominent FAILED · STUCK chip in the status strip instead. */}
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
            {/* Remediation button — primary contextual action when the parser
                recognized a fix pattern. Placed first so the operator's eye
                lands on the action, not the diagnostic affordances below. */}
            {issue.remediation && onRemediate && (
              <RemediationButton remediation={issue.remediation} pending={!!remediationPending} onClick={() => onRemediate(issue.remediation!)} />
            )}
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
              <CollapseChevron open={showRaw} className="h-3 w-3" />
              {showRaw ? `Hide ${rawControllerLabel}` : `Show ${rawControllerLabel}`}
            </button>
          </div>
          <Collapse open={showRaw}>
            <pre className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-all rounded border border-theme-border bg-theme-base px-3 py-2 font-mono text-[11px] text-theme-text-secondary">
              {rawControllerMessage}
            </pre>
          </Collapse>
        </div>
      </div>
    </div>
  )
}

// RemediationButton renders the right copy + icon for a known remediation
// kind. New kinds added to vocab.RemediationKind must add a case here or
// the button silently falls through to a generic "Apply suggested fix"
// label that the operator can't trust — we want every contextual action
// to read specifically.
function RemediationButton({
  remediation,
  pending,
  onClick,
}: {
  remediation: GitOpsRemediation
  pending: boolean
  onClick: () => void
}) {
  let label = 'Apply suggested fix'
  let Icon: typeof Plus = Plus
  if (remediation.kind === 'create-namespace' && remediation.target) {
    label = `Create namespace ${remediation.target}`
    Icon = Plus
  }
  const button = (
    // Primary-blue, not red: the failure card itself is the "diagnosis red"
    // surface; the action button is *constructive* (creating a namespace,
    // applying a fix). Red on a button reads as destructive ("Terminate",
    // "Delete") and would make operators hesitate before clicking a safe fix.
    <button
      type="button"
      onClick={onClick}
      disabled={pending}
      className="btn-brand inline-flex items-center gap-1.5 rounded-md px-2.5 py-1 text-[11px] font-semibold shadow-sm transition-colors disabled:cursor-not-allowed disabled:opacity-60"
    >
      {pending ? <Loader2 className="h-3 w-3 animate-spin" /> : <Icon className="h-3 w-3" />}
      {pending ? 'Applying…' : label}
    </button>
  )
  return remediation.hint ? <Tooltip content={remediation.hint} delay={300}>{button}</Tooltip> : button
}

// Compact stack for non-failure issues. Split out from the failure-card
// path so the rich card can own the top slot without inheriting the
// "+N more" expand mechanic.
//
// If the headline issue has a resource ref and onSelectIssue is wired,
// clicking jumps directly to the resource in Changes — the expand
// affordance is only useful when there's metadata behind the headline.
function refText(ref: GitOpsInsightRef | undefined): string {
  if (!ref) return ''
  return `${ref.kind} ${ref.name}`
}

function GitOpsCompactIssueStack({ issues, onSelectIssue }: { issues: GitOpsIssue[]; onSelectIssue?: (issue: GitOpsIssue) => void }) {
  const [expanded, setExpanded] = useState(false)
  if (issues.length === 0) return null
  const headline = issues[0]!
  const remaining = issues.length - 1
  const tone = severityTone(headline.severity)
  const headlineRef = headline.refs?.[0]
  const headlineActionable = !!(onSelectIssue && headlineRef)
  const canExpand = issues.length > 1
  // Single click target per row: row toggles when there's a stack to expand,
  // otherwise it opens the resource.
  const headlineAction: 'expand' | 'open' | 'none' =
    canExpand ? 'expand' : headlineActionable ? 'open' : 'none'
  return (
    <div className={tone.band}>
      <button
        type="button"
        onClick={() => {
          if (headlineAction === 'expand') setExpanded((v) => !v)
          else if (headlineAction === 'open') onSelectIssue?.(headline)
        }}
        disabled={headlineAction === 'none'}
        className={clsx(
          'group flex w-full items-center gap-2 px-4 py-2 text-left text-xs transition-colors',
          headlineAction !== 'none' ? 'hover:bg-theme-hover/50' : 'cursor-default',
        )}
        aria-expanded={canExpand ? expanded : undefined}
      >
        {tone.icon}
        <span className={clsx('shrink-0 font-semibold', tone.text)}>{headline.reason}</span>
        <span className="min-w-0 flex-1 truncate text-theme-text-secondary">{headline.message}</span>
        {/* Inline count when there are more issues behind the headline.
            Lightweight text — pairs with the chevron as a single disclosure
            unit instead of a separator-bordered count button. */}
        {remaining > 0 && (
          <span className="shrink-0 text-[11px] text-theme-text-tertiary">
            +{remaining} more
          </span>
        )}
        {/* Open-resource pill: only shown when the row's action IS to open
            (single-issue case). When the row expands, the per-row Open
            pills live inside the expanded section, scoped to each item. */}
        {headlineAction === 'open' && headlineRef && (
          <span className="shrink-0 text-[11px] font-medium text-theme-text-secondary opacity-70 transition-opacity group-hover:opacity-100">
            Open {refText(headlineRef)} →
          </span>
        )}
        {canExpand && <CollapseChevron open={expanded} className="h-3.5 w-3.5" />}
      </button>
      {canExpand && (
        <Collapse open={expanded}>
        <div className="divide-y divide-theme-border border-t border-theme-border bg-theme-base/40">
          {issues.slice(1).map((issue: GitOpsIssue, index: number) => {
            const t = severityTone(issue.severity)
            const ref = issue.refs?.[0]
            const actionable = !!(onSelectIssue && ref)
            const rawMessage = issue.rawMessage && issue.rawMessage !== issue.message ? issue.rawMessage : ''
            return (
              <button
                key={`${issue.reason}-${index}`}
                type="button"
                onClick={() => actionable && onSelectIssue?.(issue)}
                disabled={!actionable}
                className={clsx(
                  'group flex w-full items-start gap-2 px-4 py-2 text-left text-xs transition-colors',
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
                  {issue.cause && <p className="mt-0.5 text-[11px] text-theme-text-tertiary">{issue.cause}</p>}
                  {rawMessage && <p className="mt-0.5 break-words font-mono text-[11px] text-theme-text-tertiary">{rawMessage}</p>}
                  {issue.action && <p className="mt-0.5 text-[11px] text-theme-text-tertiary">{issue.action}</p>}
                </div>
                {actionable && ref && (
                  <span className="shrink-0 self-center text-[11px] font-medium text-theme-text-secondary opacity-70 transition-opacity group-hover:opacity-100">
                    Open {refText(ref)} →
                  </span>
                )}
              </button>
            )
          })}
        </div>
        </Collapse>
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
  const Icon = sev === 'error' ? CircleAlert : (sev === 'warning' || sev === 'alert') ? AlertTriangle : Info
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
  onSyncResource?: (ref: GitOpsChange['ref']) => void
  syncResourceDisabledReason?: string
  // When set, the matching change row scrolls into view and gets a transient
  // highlight ring. Used when the user clicks "View →" on an issue alert in
  // the band above. Key shape: `${kind}/${namespace||''}/${name}` (group is
  // intentionally not part of the key — issue refs may not carry it).
  focusKey?: string | null
  // Optional topology tree for the "All resources" toggle. When supplied,
  // generated descendants (Pods, ReplicaSets, etc.) that aren't in the
  // controller's declared inventory can be unioned into the list, matching
  // Argo's default list-view behavior. Default mode still shows declared
  // resources only — the diagnostic data (drift, events) lives there.
  tree?: GitOpsResourceTree | null
}

// Status facets for the Resources list. OutOfSync is a sync-status concern
// (amber/warn); Degraded + Missing are health failures (red/danger).
const STATUS_FACETS: { key: ResourceStatusFacet; label: string; tone: FilterPillTone }[] = [
  { key: 'outOfSync', label: 'Out of sync', tone: 'warn' },
  { key: 'degraded', label: 'Degraded', tone: 'danger' },
  { key: 'missing', label: 'Missing', tone: 'danger' },
]

export function GitOpsChangesView({ insight, error, onOpenResource, onSyncResource, syncResourceDisabledReason, focusKey, tree }: GitOpsChangesViewProps) {
  // "All resources" toggle: when on, render generated descendants alongside
  // the controller's declared inventory. Argo's UI defaults to "all" — we
  // default to "declared" because the diagnostic data (drift, events) lives
  // on declared resources only and the triage flow stays cleaner without
  // 30+ Pod rows in the way. Operators who want the full picture flip it.
  const [showAll, setShowAll] = useState(false)
  // Resources list controls: free-text search (name/kind/namespace), status
  // facets (the three states worth isolating on a drifted app — they replaced
  // the per-resource OutOfSync "issues" that used to restate the whole table),
  // and column sort. Default sort is 'order' — the controller's apply sequence
  // — because it's the only sort under which the wave grouping headers read
  // correctly.
  const [search, setSearch] = useState('')
  const [statusFilters, setStatusFilters] = useState<Set<ResourceStatusFacet>>(new Set())
  const [sort, setSort] = useState<{ key: ResourceSortKey; dir: SortDir }>({ key: 'order', dir: 'asc' })
  const changes = insight?.changes ?? []
  // Keep object identity here: tree extras are newly allocated, while ordered
  // controller changes retain their original references.
  const declaredChanges = new Set(changes)
  const plan = insight?.plan ?? []
  // Synthesize Change rows for generated tree nodes that aren't already in
  // the declared inventory. These rows carry less diagnostic data — no
  // drift, no recent events, no syncResult — but enough to match Argo's
  // "all resources" mental model: kind/name/namespace + live sync/health.
  const extraFromTree: GitOpsChange[] = showAll && tree
    ? buildTreeExtras(tree.nodes ?? [], changes)
    : []
  // refs[focusKey] holds the DOM node of the row to scroll into view; the
  // map persists across renders so the effect can find the node even when
  // changes re-render (e.g. polling).
  const rowRefs = useRef<Map<string, HTMLDivElement>>(new Map())
  // A deep-link (an issue's "View →") is an explicit "take me to this resource"
  // intent. Clear any active search/facet filters so the target can't be
  // filtered out of the list before we scroll to it. Functional updates keep
  // this a no-op when no filter is active.
  useEffect(() => {
    if (!focusKey) return
    setSearch((s) => (s ? '' : s))
    setStatusFilters((f) => (f.size ? new Set<ResourceStatusFacet>() : f))
  }, [focusKey])
  // Scroll to the focused row once it has mounted. Re-runs when the filter
  // state changes so that after the reset above remounts a previously
  // filtered-out row, the scroll fires; the ref guard keeps it to one scroll
  // per focusKey instead of re-scrolling on every poll re-render.
  const scrolledFor = useRef<string | null>(null)
  useEffect(() => {
    if (!focusKey) {
      scrolledFor.current = null
      return
    }
    if (scrolledFor.current === focusKey) return
    const node = rowRefs.current.get(focusKey)
    if (node) {
      node.scrollIntoView({ behavior: 'smooth', block: 'center' })
      scrolledFor.current = focusKey
    }
  }, [focusKey, search, statusFilters])
  // Distinguish "still loading" from "fetch failed" so a backend 5xx
  // doesn't render as a stuck "Loading…".
  if (error && !insight) {
    return <InsightErrorState error={error} />
  }
  if (!insight) {
    return <PaneLoader label="Loading GitOps resources…" className="h-full" />
  }
  // Build plan metadata maps keyed by ref so each Change row can advertise
  // its hook phase and wave assignment. The plan and changes lists are the
  // same resources from different angles — we fold the plan metadata onto
  // each change row and order the list by the plan's apply sequence.
  const planByRef = new Map<string, GitOpsPlanItem>()
  for (const item of plan) {
    const key = refKey(item.ref)
    if (!planByRef.has(key)) planByRef.set(key, item)
  }
  // Apply-order baseline: the list in the sequence the controller reconciles.
  // Changes without a plan entry land at the end in name order — managed
  // resources the controller saw but didn't sequence (completed hooks,
  // status-only entries). Extras-from-tree land after all declared rows.
  // The column-sort/search/facet controls derive off this ordered baseline.
  const applyOrdered = [...changes, ...extraFromTree].sort((a, b) => {
    const ap = planByRef.get(refKey(a.ref))?.order
    const bp = planByRef.get(refKey(b.ref))?.order
    if (ap == null && bp == null) return refKey(a.ref).localeCompare(refKey(b.ref))
    if (ap == null) return 1
    if (bp == null) return -1
    return ap - bp
  })
  const totalCount = applyOrdered.length
  const counts = resourceStatusCounts(applyOrdered)
  const filtered = applyOrdered.filter((c) => changeMatchesSearch(c, search) && changeMatchesFacets(c, statusFilters))
  const displayChanges = sortChanges(filtered, sort)
  const filtersActive = search.trim() !== '' || statusFilters.size > 0
  // Wave grouping only reads correctly in apply order (ascending). Any column
  // sort or a descending flip breaks the sequence, so drop the headers and
  // render a flat list. Also skip them for single-wave / no-wave apps — an
  // "always wave 0" label is noise.
  const groupByWave = sort.key === 'order' && sort.dir === 'asc' && plan.some((i) => i.waveSet)
  // Source URL for Missing rows. We can't show their live state (resource
  // doesn't exist), but we CAN point at where they're declared in Git —
  // which is the most useful thing to do when there's no drawer to open.
  const sourceTreeURL = gitTreeURL(insight.summary.source, insight.summary.lastRevision || insight.summary.targetRevision || '')
  const toggleFacet = (facet: ResourceStatusFacet) => {
    setStatusFilters((prev) => {
      const next = new Set(prev)
      if (next.has(facet)) next.delete(facet)
      else next.add(facet)
      return next
    })
  }
  const onSort = (key: ResourceSortKey) => {
    setSort((prev) => (prev.key === key ? { key, dir: prev.dir === 'asc' ? 'desc' : 'asc' } : { key, dir: 'asc' }))
  }
  return (
    <div className="h-full overflow-auto bg-theme-base p-4">
      <section className="rounded-md border border-theme-border bg-theme-surface">
        <div className="flex items-center justify-between border-b border-theme-border px-4 py-2.5">
          <div className="flex items-center gap-2">
            <GitCommit className="h-4 w-4 text-theme-text-tertiary" />
            <h2 className="text-sm font-semibold text-theme-text-primary">Resources</h2>
            {insight.summary.partialReason && (
              <Tooltip content={insight.summary.partialReason} delay={120}>
                <span className="cursor-help text-theme-text-tertiary hover:text-theme-text-secondary">
                  <Info className="h-3.5 w-3.5" />
                </span>
              </Tooltip>
            )}
            <span className="text-[11px] tabular-nums text-theme-text-tertiary">
              {filtersActive && displayChanges.length !== totalCount
                ? `${displayChanges.length} of ${totalCount}`
                : `${totalCount} ${totalCount === 1 ? 'resource' : 'resources'}`}
            </span>
          </div>
          {tree && (
            <div className="flex items-center gap-0.5 rounded-md border border-theme-border bg-theme-base p-0.5 text-[11px]">
              <Tooltip content="Resources the GitOps controller declares — drift and events are computed only for these." delay={300}>
                <button
                  type="button"
                  onClick={() => setShowAll(false)}
                  className={clsx(
                    'rounded px-2 py-0.5 font-medium transition-colors',
                    !showAll
                      ? 'bg-theme-elevated text-theme-text-primary'
                      : 'text-theme-text-secondary hover:text-theme-text-primary',
                  )}
                >
                  Declared
                </button>
              </Tooltip>
              <Tooltip content="Includes generated descendants like Pods and ReplicaSets (matches Argo's default list)." delay={300}>
                <button
                  type="button"
                  onClick={() => setShowAll(true)}
                  className={clsx(
                    'rounded px-2 py-0.5 font-medium transition-colors',
                    showAll
                      ? 'bg-theme-elevated text-theme-text-primary'
                      : 'text-theme-text-secondary hover:text-theme-text-primary',
                  )}
                >
                  All
                </button>
              </Tooltip>
            </div>
          )}
        </div>
        {/* Filter/sort toolbar. The status facets are the primary way to
            answer "which resources are the problem?" now that the issues band
            no longer restates every OutOfSync resource above. */}
        {totalCount > 0 && (
          <div className="flex flex-wrap items-center gap-2 border-b border-theme-border px-4 py-2">
            <SearchBox
              value={search}
              onChange={setSearch}
              scope="gitops"
              shortcutId="gitops-resources-search"
              placeholder="Filter resources… (press /)"
              className="w-full min-w-[180px] sm:w-64"
            />
            {/* Status facets appear only when the app actually has that state
                (or the operator has it toggled on) — a healthy app shouldn't
                render three "(0)" pills. */}
            <div className="flex flex-wrap items-center gap-1.5">
              {STATUS_FACETS.map(({ key, label, tone }) =>
                counts[key] > 0 || statusFilters.has(key) ? (
                  <FilterPill
                    key={key}
                    label={label}
                    tone={tone}
                    active={statusFilters.has(key)}
                    count={counts[key]}
                    onClick={() => toggleFacet(key)}
                  />
                ) : null,
              )}
            </div>
            {sort.key !== 'order' && (
              <button
                type="button"
                onClick={() => setSort({ key: 'order', dir: 'asc' })}
                className="ml-auto inline-flex items-center gap-1 text-[11px] text-theme-text-tertiary transition-colors hover:text-theme-text-secondary"
              >
                <ArrowUpDown className="h-3 w-3" /> Apply order
              </button>
            )}
          </div>
        )}
        {/* Honest disclaimer about diff scope. Neither Argo nor Flux exposes
            per-resource desired-vs-live diffs on the CRD — they're computed
            on demand by their respective servers/CLIs, which Radar doesn't
            call. */}
        {totalCount > 0 && (
          <div className="border-b border-theme-border bg-theme-base/40 px-4 py-2 text-[11px] text-theme-text-tertiary">
            Radar reads each resource's drift status from the controller. For a line-by-line diff, {insight.summary.tool === 'fluxcd' ? (
              insight.summary.kind === 'HelmRelease' ? (
                <>run <code className="inline-code text-[10px]">helm diff upgrade {insight.summary.name} &lt;chart&gt;</code> (requires the helm-diff plugin).</>
              ) : (
                <>run <code className="inline-code text-[10px]">flux diff kustomization {insight.summary.name} --path &lt;local-manifests&gt;</code>.</>
              )
            ) : (
              <>use the Argo CD UI or run <code className="inline-code text-[10px]">argocd app diff {insight.summary.name}</code>.</>
            )}
          </div>
        )}
        {totalCount === 0 ? (
          <div className="p-4 text-sm text-theme-text-secondary">No managed resources reported by the GitOps controller.</div>
        ) : displayChanges.length === 0 ? (
          <div className="flex flex-col items-start gap-2 p-4 text-sm text-theme-text-secondary">
            <span>No resources match the current filters.</span>
            <button
              type="button"
              onClick={() => {
                setSearch('')
                setStatusFilters(new Set())
              }}
              className="text-[12px] font-medium text-skyhook-500 hover:underline"
            >
              Clear filters
            </button>
          </div>
        ) : (
          <>
            <div className="grid grid-cols-[minmax(0,1fr)_120px_120px_220px] gap-3 border-b border-theme-border bg-theme-base/40 px-4 py-2">
              <ResSortHeader label="Resource" sortKey="name" sort={sort} onSort={onSort} />
              <ResSortHeader label="Sync" sortKey="sync" sort={sort} onSort={onSort} />
              <ResSortHeader label="Health" sortKey="health" sort={sort} onSort={onSort} />
              <div />
            </div>
            <div className="divide-y divide-theme-border">
              {displayChanges.map((change, idx) => {
                const planItem = planByRef.get(refKey(change.ref))
                const hook = planItem?.hook
                const wave = planItem?.wave
                const waveSet = !!planItem?.waveSet
                const rowKey = refKey(change.ref)
                const focused = focusKey === rowKey
                const explanation = !change.syncError && !change.message
                  ? explainChangeStatus(change.sync, change.health, insight.summary)
                  : ''
                const hasInlineDetail = !!(
                  (change.drift && change.drift.entries.length > 0) ||
                  (change.recentEvents && change.recentEvents.length > 0)
                )
                // Render a wave separator above this row when the wave value
                // changed from the previous one. waveSet=false rows under a
                // waved plan get a "Default wave" header — matches how Argo's
                // UI separates explicitly-waved from default. Only in apply
                // order (groupByWave); a column sort renders a flat list.
                const prevPlan = idx > 0 ? planByRef.get(refKey(displayChanges[idx - 1]!.ref)) : undefined
                const showWaveHeader = groupByWave && (idx === 0 || prevPlan?.wave !== wave || prevPlan?.waveSet !== waveSet)
                return (
                  <Fragment key={`${change.ref.group}/${change.ref.kind}/${change.ref.namespace}/${change.ref.name}`}>
                    {showWaveHeader && (
                      <div className="bg-theme-base/50 px-4 py-1 text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
                        {waveSet ? `Wave ${wave}` : 'Default wave'}
                      </div>
                    )}
                    <ChangeRow
                      change={change}
                      hook={hook}
                      explanation={explanation}
                      focused={focused}
                      autoExpand={focused}
                      hasInlineDetail={hasInlineDetail}
                      onOpenResource={onOpenResource}
                      onSyncResource={onSyncResource}
                      syncResourceDisabledReason={syncResourceDisabledReason}
                      declared={declaredChanges.has(change)}
                      sourceTreeURL={sourceTreeURL}
                      registerRef={(el) => {
                        if (el) {
                          rowRefs.current.set(rowKey, el)
                        } else {
                          rowRefs.current.delete(rowKey)
                        }
                      }}
                    />
                  </Fragment>
                )
              })}
            </div>
          </>
        )}
      </section>
    </div>
  )
}

// sortChanges applies the active column sort to the already apply-ordered
// list. 'order' is the identity ordering (the list arrives in apply sequence),
// so ascending returns it untouched and descending reverses. Column sorts
// tie-break on kind/name so equal-status rows stay stable and readable.
function sortChanges(list: GitOpsChange[], sort: { key: ResourceSortKey; dir: SortDir }): GitOpsChange[] {
  if (sort.key === 'order') return sort.dir === 'asc' ? list : [...list].reverse()
  const dir = sort.dir === 'asc' ? 1 : -1
  const nameKey = (c: GitOpsChange) => `${c.ref.kind}/${c.ref.name}`
  return [...list].sort((a, b) => {
    let cmp = 0
    if (sort.key === 'name') cmp = nameKey(a).localeCompare(nameKey(b))
    else if (sort.key === 'sync') cmp = syncStatusRank(changeSync(a)) - syncStatusRank(changeSync(b))
    else if (sort.key === 'health') cmp = healthStatusRank(changeHealth(a)) - healthStatusRank(changeHealth(b))
    if (cmp === 0) cmp = nameKey(a).localeCompare(nameKey(b))
    return cmp * dir
  })
}

// ResSortHeader is the div-grid equivalent of ui/SortableTh (which renders a
// <th>): the Resources list is a grid of expandable rows, not a <table>, so it
// can't use the table header cell. Same chevron affordance for consistency.
function ResSortHeader({
  label,
  sortKey,
  sort,
  onSort,
}: {
  label: string
  sortKey: ResourceSortKey
  sort: { key: ResourceSortKey; dir: SortDir }
  onSort: (key: ResourceSortKey) => void
}) {
  const active = sort.key === sortKey
  return (
    <button
      type="button"
      onClick={() => onSort(sortKey)}
      aria-label={`Sort by ${label}`}
      className="inline-flex items-center gap-1 select-none text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary transition-colors hover:text-theme-text-secondary focus-visible:text-theme-text-secondary focus-visible:outline-none"
    >
      {label}
      {active ? (
        sort.dir === 'asc' ? <ChevronUp className="h-3 w-3" /> : <ChevronDown className="h-3 w-3" />
      ) : (
        <ArrowUpDown className="h-3 w-3 opacity-40" />
      )}
    </button>
  )
}

// Keys must match between Plan items and Change items for the cross-ref to
// work. Group can be omitted in either source, so we don't require it for
// equality — kind+namespace+name is the practical identifier here.
// buildTreeExtras turns generated tree nodes into synthetic GitOpsChange rows
// so the unified Resources list can render them without a parallel row
// component. The declared set is excluded (already rendered from
// insight.changes); root + group nodes are skipped — root is the GitOps CR
// itself (rendered as the page header) and groups are collapse-only fixtures.
function buildTreeExtras(nodes: GitOpsTreeNode[], declared: GitOpsChange[]): GitOpsChange[] {
  const declaredKeys = new Set(declared.map((c) => refKey(c.ref)))
  const out: GitOpsChange[] = []
  for (const n of nodes) {
    if (n.role === 'root' || n.role === 'group') continue
    // Defensive guard: well-formed trees shouldn't produce empty
    // kind/name nodes, but the TypeScript type allows ""; a row with empty
    // identifiers renders as "Open  →" and routes to a broken URL on click.
    if (!n.ref.kind || !n.ref.name) continue
    const key = refKey(n.ref)
    if (declaredKeys.has(key)) continue
    out.push({
      ref: {
        group: n.ref.group,
        kind: n.ref.kind,
        namespace: n.ref.namespace,
        name: n.ref.name,
      },
      // Synthetic category: tree nodes don't carry the controller's per-
      // resource sync category. Default to Unknown — the row renders with
      // the live sync/health badges from the topology, which is the same
      // signal at a different vocabulary.
      category: 'Unknown',
      sync: n.sync,
      health: n.health,
      hasDesired: false,
      hasLive: true,
      partial: true,
      partialNote: 'Generated resource — not directly tracked by the GitOps controller (no drift / event diagnostics).',
    })
  }
  return out
}

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
  const phase = (summary.operationPhase ?? '').toLowerCase()
  const inFlight = phase === 'running'
  // When the parent operation has Failed/Errored, "click Sync to force it"
  // misleads — sync has already been tried (and likely retried). The top
  // banner owns the cause; per-row copy should defer to it instead of
  // suggesting an action that won't help.
  const parentFailed = phase === 'failed' || phase === 'error'
  if (sync === 'OutOfSync') {
    if (parentFailed) return 'Sync failed for this resource — see the operation error above for the cause.'
    if (inFlight) return 'Live state differs from Git. A sync is in progress — wait for it to finish.'
    if (isAuto) return 'Live state differs from Git. Auto-sync should reconcile this within a few minutes; click Sync to force it.'
    return 'Live state differs from Git. Click Sync to apply the desired state.'
  }
  if (health === 'Missing' && parentFailed) return 'Resource was not created — see the operation error above for the cause.'
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
  hook,
  explanation,
  focused,
  autoExpand,
  hasInlineDetail,
  onOpenResource,
  onSyncResource,
  syncResourceDisabledReason,
  declared,
  sourceTreeURL,
  registerRef,
}: {
  change: GitOpsChange
  // Hook phase from the plan item (the controller-declared annotation).
  // Falls back to change.hookPhase (the executed phase) for visibility on
  // resources that already ran their hook.
  hook: string | undefined
  explanation: string
  focused: boolean
  autoExpand: boolean
  hasInlineDetail: boolean
  onOpenResource?: (ref: GitOpsChange['ref']) => void
  onSyncResource?: (ref: GitOpsChange['ref']) => void
  syncResourceDisabledReason?: string
  declared: boolean
  // Constructed URL pointing at the source directory in the remote Git
  // host (github / gitlab / bitbucket). Used as the "where this would be
  // declared" affordance on Missing rows, since opening the drawer for a
  // resource that doesn't exist just shows "Resource not found".
  sourceTreeURL?: string | null
  registerRef: (el: HTMLDivElement | null) => void
}) {
  const [expanded, setExpanded] = useState(autoExpand && hasInlineDetail)
  // Auto-expand when an issue alert deep-links to this row — the user just
  // clicked the issue, so they want to see the detail immediately.
  useEffect(() => {
    if (autoExpand && hasInlineDetail) setExpanded(true)
  }, [autoExpand, hasInlineDetail])
  const driftEntries = change.drift?.entries ?? []
  const events = change.recentEvents ?? []
  // Missing resources have no live state to drill into — opening the drawer
  // just shows "Resource not found", which is a wasted click. Treat them
  // differently: hide the Open pill, suppress the drawer-open click path,
  // and offer "View in Git →" instead so the operator can read the
  // declared source instead of a non-existent live object.
  const isAbsent = change.health === 'Missing' && !change.hasLive
  const handleRowClick = () => {
    if (hasInlineDetail) {
      setExpanded((v) => !v)
    } else if (!isAbsent && onOpenResource) {
      onOpenResource(change.ref)
    }
  }
  // Row stays click-affordant when there's inline detail to expand OR a
  // live resource to drill into. Missing rows without inline detail are
  // intentionally non-interactive — there's nowhere useful to go.
  const rowInteractive = hasInlineDetail || (!isAbsent && !!onOpenResource)
  const hookLabel = change.hookPhase || hook
  const canSyncResource = !!onSyncResource && isArgoResourceSyncEligible(change, declared, hookLabel)
  const syncResourceButton = canSyncResource ? (
    <button
      type="button"
      onClick={() => onSyncResource(change.ref)}
      disabled={!!syncResourceDisabledReason}
      className="inline-flex w-full items-center justify-center gap-1.5 rounded border border-theme-border bg-theme-base px-2 py-1 text-[11px] font-medium text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary disabled:cursor-not-allowed disabled:opacity-50"
    >
      <RefreshCw className="h-3 w-3" />
      Sync resource
    </button>
  ) : null
  return (
    <div
      ref={registerRef}
      className={clsx(
        'group transition-colors',
        focused && 'bg-amber-500/10 ring-2 ring-inset ring-amber-500/60',
      )}
    >
      {/* Fixed action-column width so the Sync/Health badges line up
          across rows regardless of how long "Open <Kind> <name> →" is. With
          an `auto` last column, each row's 1fr column got a different
          residual width, pushing the badges to inconsistent offsets. */}
      <div className="grid w-full grid-cols-[minmax(0,1fr)_120px_120px_220px] gap-3 px-4 py-3 text-sm">
        <button
          type="button"
          onClick={handleRowClick}
          disabled={!rowInteractive}
          className={clsx(
            'min-w-0 text-left',
            rowInteractive ? 'cursor-pointer hover:text-theme-text-primary' : 'cursor-default',
          )}
        >
          <div className="flex items-baseline gap-2">
            {hasInlineDetail ? (
              <CollapseChevron open={expanded} className="h-3.5 w-3.5 self-center" />
            ) : (
              <span aria-hidden="true" className="h-3.5 w-3.5 shrink-0" />
            )}
            <div className="min-w-0 truncate font-medium text-theme-text-primary">{change.ref.kind} / {change.ref.name}</div>
            {hookLabel && (
              <Tooltip content={`Sync hook: ${hookLabel}`} delay={200} wrapperClassName="shrink-0">
                <span className="rounded border border-violet-400/40 bg-violet-500/10 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-violet-700 dark:text-violet-400">
                  {hookLabel}
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
            <Tooltip content={change.rawSyncError || change.syncError} delay={400} wrapperClassName="ml-[18px] mt-1 block max-w-full">
              <span className="line-clamp-3 text-xs text-red-600 dark:text-red-400">{change.syncError}</span>
            </Tooltip>
          )}
          {change.message && !change.syncError && <div className="ml-[18px] mt-1 line-clamp-2 text-xs text-theme-text-secondary">{change.message}</div>}
          {!change.syncError && !change.message && explanation && (
            <div className="ml-[18px] mt-1 text-xs text-theme-text-tertiary">{explanation}</div>
          )}
        </button>
        <div className="self-start"><SyncStatusBadge sync={normalizeSyncStatus(change.sync ?? change.category)} /></div>
        <div className="self-start"><HealthStatusBadge health={normalizeHealthStatus(change.health)} /></div>
        <div className="self-start space-y-1.5">
          {syncResourceButton && syncResourceDisabledReason ? (
            <Tooltip content={syncResourceDisabledReason} delay={200} wrapperClassName="block">
              {syncResourceButton}
            </Tooltip>
          ) : syncResourceButton}
          {/* Three affordance states:
              - Live resource (not Missing): "Open <kind> <name> →" opens the
                K8s drawer.
              - Missing resource WITH a recognized Git host: "View in Git →"
                opens the source directory in a new tab.
              - Missing resource without recognized host: nothing — the
                row's own explanation copy is the surface; we don't fake
                an action that wouldn't help. */}
          {!isAbsent && onOpenResource && (
            <button
              type="button"
              onClick={() => onOpenResource(change.ref)}
              title={`Open ${change.ref.kind} ${change.ref.name}`}
              className="block w-full truncate rounded border border-theme-border bg-theme-base px-2 py-0.5 text-left text-[11px] text-theme-text-secondary opacity-70 transition-all hover:bg-theme-hover hover:text-theme-text-primary group-hover:opacity-100"
            >
              Open {change.ref.kind} {change.ref.name} →
            </button>
          )}
          {isAbsent && sourceTreeURL && (
            <Tooltip content="Open the source directory in Git — the live resource doesn't exist yet" delay={300}>
              <a
                href={sourceTreeURL}
                target="_blank"
                rel="noopener noreferrer"
                onClick={(e) => e.stopPropagation()}
                className="inline-block rounded border border-theme-border bg-theme-base px-2 py-0.5 text-[11px] text-theme-text-secondary opacity-70 transition-all hover:bg-theme-hover hover:text-theme-text-primary group-hover:opacity-100"
              >
                View in Git →
              </a>
            </Tooltip>
          )}
          {isAbsent && !sourceTreeURL && (
            <span className="block text-[11px] italic text-theme-text-tertiary">
              No live resource
            </span>
          )}
        </div>
      </div>
      {hasInlineDetail && (
        <Collapse open={expanded} mountLazily>
          <div className="border-t border-theme-border bg-theme-base/40 px-4 py-3">
            {driftEntries.length > 0 && <DriftPanel drift={change.drift!} />}
            {events.length > 0 && <RecentEventsPanel events={events} />}
          </div>
        </Collapse>
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
  return formatRelativeAgeTime(value, '')
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
  if (!insight) return <PaneLoader label="Loading GitOps activity…" className="h-full" />
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
                <PhaseChip phase={item.phase} message={item.message} />
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
                <Tooltip content={item.rawMessage || item.message} delay={400} wrapperClassName="mt-0.5 block max-w-full">
                  <div className={clsx('line-clamp-2 text-[11px]', sourceDisplay ? 'text-theme-text-tertiary' : 'text-theme-text-secondary')}>{item.message}</div>
                </Tooltip>
              )}
            </div>
          </li>
        )
      })}
    </ol>
  )
}


// Outcome chip on each history row. The dot in the gutter encodes outcome by
// color, but a textual chip makes the result legible at a glance for users who
// don't immediately decode the color palette. Falls back to message-derived
// phase when the controller didn't populate phase explicitly (Argo only fills
// phase on the most recent revision; older entries lose it without inference).
function PhaseChip({ phase, message }: { phase?: string; message?: string }) {
  const effective = phase || messageToPhase(message)
  if (!effective) return null
  const severity = gitopsToSeverity(effective)
  // Don't render a neutral chip — it adds visual noise without information.
  if (severity === 'neutral') return null
  const label = effective.charAt(0).toUpperCase() + effective.slice(1).toLowerCase()
  return (
    <span className={clsx('badge-sm', SEVERITY_BADGE[severity])}>{label}</span>
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
  return formatRelativeAgeTime(value)
}
