import { AlertTriangle, ChevronDown, ChevronRight, CircleAlert, Clock3, GitBranch, GitCommit, Info, ListChecks, RotateCw } from 'lucide-react'
import { clsx } from 'clsx'
import { useState, type ReactNode } from 'react'
import type { GitOpsChange, GitOpsHistoryItem, GitOpsInsight, GitOpsIssue, GitOpsPlanItem } from '../../../types'
import { HealthStatusBadge, SyncStatusBadge } from '../GitOpsStatusBadge'
import { SEVERITY_BADGE, SEVERITY_TEXT } from '../../../utils/badge-colors'
import { compactSource, entryTone, gitopsToSeverity } from './insights-helpers'

interface GitOpsStatusStripProps {
  insight?: GitOpsInsight | null
  loading?: boolean
}

// Status strip is a flat chrome bar (bg-theme-base, no surface card) that
// lives between the page header and the tab content. Sync/Health badges are
// intentionally NOT here — they live next to the title in the page header,
// where they pair with identity. Showing them again here was duplication.
//
// Strip carries only what the header doesn't: the active operation chip
// (when there's actually something running or failed) plus reference
// metadata (Source / Revision / Last reconcile).
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
          <span
            className={clsx('badge badge-sm font-medium uppercase tracking-wide', SEVERITY_BADGE[gitopsToSeverity(operation)])}
            title={summary.operationMessage ? `${operation}: ${summary.operationMessage}` : `Last sync operation: ${operation}`}
          >
            {operation}
          </span>
        )}
        {/* When an operation is in flight or just failed, surface the message
            inline so the user doesn't have to switch to Activity to see why. */}
        {operation && summary.operationMessage && (
          <span className="min-w-0 max-w-[60ch] truncate text-[11px] text-theme-text-secondary" title={summary.operationMessage}>
            {summary.operationMessage}
          </span>
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
  return (
    <span className="inline-flex min-w-0 max-w-[40ch] items-baseline gap-1">
      <span className="shrink-0">{label}:</span>
      <span className={clsx('min-w-0 truncate text-theme-text-secondary', mono && 'font-mono')} title={value}>{value}</span>
    </span>
  )
}

// Single severity-tinted row with a "+N more" expand affordance. The
// alternative — three issue cards across — stacked another colored stripe
// directly under the status strip and split attention.
export function GitOpsIssuesBand({ issues }: { issues?: GitOpsIssue[] | null }) {
  const list = issues ?? []
  const [expanded, setExpanded] = useState(false)
  if (list.length === 0) return null
  const headline = list[0]!
  const remaining = list.length - 1
  const tone = severityTone(headline.severity)
  return (
    <div className={clsx('border-b border-theme-border', tone.band)}>
      <button
        type="button"
        onClick={() => list.length > 1 && setExpanded((v) => !v)}
        className={clsx(
          'flex w-full items-center gap-2 px-4 py-2 text-left text-xs transition-colors',
          list.length > 1 ? 'hover:bg-theme-hover/50' : 'cursor-default'
        )}
        disabled={list.length <= 1}
      >
        {tone.icon}
        <span className={clsx('font-semibold', tone.text)}>{headline.reason}</span>
        <span className="min-w-0 flex-1 truncate text-theme-text-secondary">{headline.message}</span>
        {remaining > 0 && (
          <span className="shrink-0 text-[11px] text-theme-text-tertiary">
            +{remaining} more
          </span>
        )}
        {list.length > 1 && (expanded ? <ChevronDown className="h-3.5 w-3.5 text-theme-text-tertiary" /> : <ChevronRight className="h-3.5 w-3.5 text-theme-text-tertiary" />)}
      </button>
      {expanded && list.length > 1 && (
        <div className="border-t border-theme-border bg-theme-base/40 divide-y divide-theme-border">
          {list.slice(1).map((issue: GitOpsIssue, index: number) => {
            const t = severityTone(issue.severity)
            return (
              <div key={`${issue.reason}-${index}`} className="flex items-start gap-2 px-4 py-2 text-xs">
                {t.icon}
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className={clsx('font-semibold', t.text)}>{issue.reason}</span>
                    <span className="text-[10px] uppercase tracking-wide text-theme-text-tertiary">{issue.scope}</span>
                  </div>
                  <p className="mt-0.5 text-theme-text-secondary">{issue.message}</p>
                  {issue.action && <p className="mt-0.5 text-[11px] text-theme-text-tertiary">{issue.action}</p>}
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
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
}

export function GitOpsChangesView({ insight, error, onOpenResource }: GitOpsChangesViewProps) {
  const changes = insight?.changes ?? []
  const plan = insight?.plan ?? []
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
          {changes.length === 0 ? (
            <div className="p-4 text-sm text-theme-text-secondary">No changed resources reported by the GitOps controller.</div>
          ) : (
            <div className="divide-y divide-theme-border">
              {changes.map((change) => {
                const step = planOrderByRef.get(refKey(change.ref))
                return (
                  <button
                    key={`${change.ref.group}/${change.ref.kind}/${change.ref.namespace}/${change.ref.name}`}
                    type="button"
                    onClick={() => onOpenResource?.(change.ref)}
                    className="grid w-full grid-cols-[minmax(0,1fr)_120px_120px] gap-3 px-4 py-3 text-left text-sm hover:bg-theme-hover"
                  >
                    <div className="min-w-0">
                      <div className="flex items-baseline gap-2">
                        <div className="min-w-0 truncate font-medium text-theme-text-primary">{change.ref.kind} / {change.ref.name}</div>
                        {change.hookPhase && (
                          <span
                            className="shrink-0 rounded border border-violet-400/40 bg-violet-500/10 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-violet-700 dark:text-violet-400"
                            title={`Sync hook: ${change.hookPhase}`}
                          >
                            {change.hookPhase}
                          </span>
                        )}
                        {step !== undefined && (
                          <span
                            className="shrink-0 rounded border border-theme-border bg-theme-elevated px-1.5 py-0.5 font-mono text-[10px] text-theme-text-tertiary"
                            title={`Sync plan step ${step}`}
                          >
                            step {step}
                          </span>
                        )}
                      </div>
                      <div className="truncate text-xs text-theme-text-tertiary">{change.ref.namespace || '(cluster)'} {change.ref.group ? `· ${change.ref.group}` : ''}</div>
                      {/* Per-resource sync error gets emphasis (red text) over the
                          live health message — operators chasing a broken sync
                          want the failure reason on the same row, not in a drawer. */}
                      {change.syncError && (
                        <div className="mt-1 line-clamp-3 text-xs text-red-600 dark:text-red-400" title={change.syncError}>{change.syncError}</div>
                      )}
                      {change.message && !change.syncError && <div className="mt-1 line-clamp-2 text-xs text-theme-text-secondary">{change.message}</div>}
                      {change.partial && <div className="mt-1 text-[11px] text-theme-text-tertiary">{change.partialNote}</div>}
                    </div>
                    <div><SyncStatusBadge sync={(change.sync || change.category || 'Unknown') as any} /></div>
                    <div><HealthStatusBadge health={(change.health || 'Unknown') as any} /></div>
                  </button>
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
                          <span
                            className="shrink-0 rounded border border-violet-400/40 bg-violet-500/10 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-violet-700 dark:text-violet-400"
                            title={`Sync hook: ${item.hook}`}
                          >
                            {item.hook}
                          </span>
                        )}
                      </div>
                      <div className="mt-1 flex flex-wrap gap-1">
                        {item.phase && <Chip label="phase" value={item.phase} />}
                        {item.relationship && <Chip value={item.relationship} />}
                        {item.status && <Chip value={item.status} />}
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
  const operation = insight.history?.find((item) => item.phase && item.message)
  const canRollback = !!insight.capabilities?.rollback && !!onRollback
  return (
    <div className="h-full overflow-auto bg-theme-base p-4">
      <div className="grid gap-4 xl:grid-cols-[420px_minmax(0,1fr)]">
        <section className="rounded-md border border-theme-border bg-theme-surface">
          <SectionHeader icon={RotateCw} title="Current Operation" />
          <div className="space-y-2 p-4 text-sm">
            <InfoRow label="Phase" value={operation?.phase || insight.summary.operationPhase || 'Idle'} />
            <InfoRow label="Revision" value={operation?.revision || insight.summary.lastRevision || insight.summary.targetRevision || '-'} mono />
            <InfoRow label="Message" value={operation?.message || '-'} />
            <InfoRow label="Last reconcile" value={formatRelative(insight.summary.lastReconcile)} />
          </div>
        </section>
        <section className="rounded-md border border-theme-border bg-theme-surface">
          <SectionHeader
            icon={Clock3}
            title="History"
            hint={canRollback ? 'Each revision can be rolled back to.' : undefined}
          />
          <HistoryRows items={insight.history ?? []} canRollback={canRollback} onRollback={onRollback} />
        </section>
      </div>
      <section className="mt-4 rounded-md border border-theme-border bg-theme-surface">
        <SectionHeader icon={AlertTriangle} title="Diagnosis" />
        {(insight.issues ?? []).length === 0 ? (
          <div className="p-4 text-sm text-theme-text-secondary">No active GitOps issues detected.</div>
        ) : (
          <div className="divide-y divide-theme-border">
            {(insight.issues ?? []).map((issue, index) => (
              <div key={`${issue.reason}-${index}`} className="px-4 py-3 text-sm">
                <div className="flex flex-wrap items-center gap-2">
                  <span className={clsx('h-2 w-2 rounded-full', issue.severity === 'critical' ? 'bg-red-500' : issue.severity === 'warning' ? 'bg-amber-500' : 'bg-sky-500')} />
                  <span className="font-medium text-theme-text-primary">{issue.reason}</span>
                  <span className="badge status-neutral">{issue.scope}</span>
                </div>
                <p className="mt-1 text-theme-text-secondary">{issue.message}</p>
                {issue.action && <p className="mt-1 text-xs text-theme-text-tertiary">{issue.action}</p>}
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  )
}

// Vertical timeline; left-gutter dot color encodes outcome at a glance.
function HistoryRows({
  items,
  canRollback = false,
  onRollback,
}: {
  items: GitOpsHistoryItem[]
  canRollback?: boolean
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
          <li key={`${item.id}-${item.revision}-${index}`} className="relative grid grid-cols-[16px_minmax(0,1fr)] gap-3 pb-4 last:pb-0">
            <div className="relative flex justify-center">
              {!isLast && <span className="absolute left-1/2 top-3 h-full w-px -translate-x-1/2 bg-theme-border" />}
              <span
                className={clsx('relative mt-1 h-2.5 w-2.5 rounded-full ring-2 ring-theme-surface', tone.dot)}
                title={item.phase || tone.inferredFrom || 'unknown'}
              />
            </div>
            <div className="min-w-0 text-sm">
              <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
                <span className="font-mono text-xs text-theme-text-primary">{item.revision || item.phase || '-'}</span>
                <span className="text-[11px] text-theme-text-tertiary">{formatRelative(item.deployedAt)}</span>
                {item.initiatedBy && (
                  <span className="text-[11px] text-theme-text-tertiary">by {item.initiatedBy}</span>
                )}
                {showRollback && (
                  <button
                    type="button"
                    onClick={() => onRollback?.(item)}
                    className="ml-auto rounded border border-theme-border bg-theme-elevated px-1.5 py-0.5 text-[10px] text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
                    title={`Roll back to revision ${item.revision || `#${item.id}`}`}
                  >
                    Rollback
                  </button>
                )}
              </div>
              {sourceDisplay && (
                <div className="mt-0.5 truncate text-xs text-theme-text-secondary" title={item.source}>{sourceDisplay}</div>
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
        <span title={hint} className="cursor-help text-theme-text-tertiary hover:text-theme-text-secondary">
          <Info className="h-3.5 w-3.5" />
        </span>
      )}
    </div>
  )
}

function InfoRow({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="grid grid-cols-[120px_minmax(0,1fr)] gap-3">
      <span className="text-theme-text-tertiary">{label}</span>
      <span className={clsx('min-w-0 truncate text-theme-text-primary', mono && 'font-mono text-xs')}>{value}</span>
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
