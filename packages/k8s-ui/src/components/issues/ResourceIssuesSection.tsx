import { AlertTriangle, ExternalLink } from 'lucide-react'
import { Section } from '../ui/drawer-components'
import { Badge } from '../ui/Badge'
import type { Issue, IssueResourceRef } from './types'
import { categoryLabel } from './severity'
import { diagnosticFactLabel, confidenceTitle, incidentParentLabel } from './diagnostic'

/**
 * ResourceIssuesSection — the compact "Operational Issues" block for the resource
 * detail. Renders the live, diagnosed Issues that touch one resource (its own +,
 * for a workload, its owned pods' issues, server-rolled-up via RelatedIssues).
 * The diagnosis sibling of IssuesView's queue row — same Cause/Action vocabulary,
 * stripped of the queue chrome (accordion, scope copy, fan-out expansion).
 *
 * Header mirrors the queue: the plain `categoryLabel` is the operator-facing
 * headline and the raw `reason` rides alongside as a muted signal (so the K8s
 * jargon is available but not the lead). Body is primarily the plain
 * `cause` (which names the offending object) + the `Next step` — the diagnosis
 * and the fix. The raw `message`/evidence stays in the queue + MCP where the
 * locator detail is wanted; inline it mostly restated the cause or the category,
 * so it's omitted to keep the card scannable. (`message` is the body fallback
 * only for categories that don't yet emit a `cause`.)
 */
export function ResourceIssuesSection({
  issues,
  onResourceClick,
}: {
  issues: Issue[] | undefined
  /** When provided, related resources in a causal link become clickable. */
  onResourceClick?: (ref: IssueResourceRef) => void
}) {
  if (!issues || issues.length === 0) return null
  return (
    <Section title={`Operational Issues (${issues.length})`} icon={AlertTriangle} defaultExpanded>
      <div className="space-y-3">
        {issues.map((issue) => {
          const parent = issue.incident_parent
          return (
            <div key={issue.id} className="card-inner">
              <div className="mb-1 flex min-w-0 items-baseline gap-2">
                <Badge severity={issue.severity === 'critical' ? 'error' : 'warning'} size="sm">
                  {issue.severity}
                </Badge>
                <span className="shrink-0 text-sm font-medium text-theme-text-primary">{categoryLabel(issue.category)}</span>
                {issue.reason ? (
                  <span className="min-w-0 flex-1 truncate text-xs text-theme-text-tertiary">{issue.reason}</span>
                ) : null}
                {issue.count ? (
                  <span className="shrink-0 text-xs text-theme-text-tertiary tabular-nums">· {issue.count} affected</span>
                ) : null}
              </div>
              {issue.cause ? (
                <p className="text-sm leading-relaxed text-theme-text-secondary">{issue.cause}</p>
              ) : issue.message ? (
                <p className="text-sm leading-relaxed text-theme-text-secondary">{issue.message}</p>
              ) : null}
              {issue.action ? (
                <p className="mt-1 text-sm leading-relaxed text-theme-text-secondary">
                  <span className="font-medium text-theme-text-primary">Next step: </span>
                  {issue.action}
                </p>
              ) : null}
              {issue.remediation_kind === 'create-namespace' && issue.remediation_target ? (
                <p className="mt-1 text-xs text-theme-text-tertiary">
                  Suggested fix: create namespace{' '}
                  <code className="rounded bg-theme-elevated px-1 font-mono">{issue.remediation_target}</code> — apply it from the GitOps detail page.
                </p>
              ) : null}
              {parent ? (
                <p className="mt-1 text-xs text-theme-text-tertiary">
                  {incidentParentLabel(parent.fact_type, parent.confidence)}:{' '}
                  {onResourceClick ? (
                    <button
                      type="button"
                      onClick={() => onResourceClick(parent.ref)}
                      className="group inline-flex items-center gap-1 text-left font-mono hover:text-theme-text-secondary"
                      title={confidenceTitle(parent.confidence ?? '')}
                    >
                      {parent.ref.kind} / {parent.ref.name}
                      <ExternalLink className="h-3 w-3 opacity-0 group-hover:opacity-100" />
                    </button>
                  ) : (
                    <span className="font-mono">{parent.ref.kind} / {parent.ref.name}</span>
                  )}
                </p>
              ) : null}
              <CausalContext issue={issue} onResourceClick={onResourceClick} />
            </div>
          )
        })}
      </div>
    </Section>
  )
}

/**
 * CausalContext — the compact, drawer-density rendering of an issue's
 * cross-subject causal links (DiagnosticContext). Shows only the linking facts
 * (those carrying a confidence tier or related issues) — the queue's IssuesView
 * renders the fuller context with clickable resource navigation; here the related
 * resources are shown as plain identifiers to keep the resource panel scannable.
 */
function CausalContext({ issue, onResourceClick }: { issue: Issue; onResourceClick?: (ref: IssueResourceRef) => void }) {
  const ctx = issue.diagnostic_context
  const links = ctx?.facts?.filter((f) => f.confidence || (f.related_issues && f.related_issues.length > 0)) ?? []
  if (!ctx || links.length === 0) return null
  return (
    <div className="mt-2 border-t border-theme-border/60 pt-2">
      <div className="mb-1 flex items-center gap-2">
        <span className="text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">Context</span>
      </div>
      <ul className="space-y-1.5">
        {links.map((fact, idx) => (
          <li key={`${fact.type}-${idx}`} className="text-xs">
            <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
              <span className="font-medium text-theme-text-secondary">{diagnosticFactLabel(fact.type)}</span>
              {fact.confidence ? (
                <span className="badge-sm text-[10px] text-theme-text-tertiary" title={confidenceTitle(fact.confidence)}>
                  {fact.confidence} confidence
                </span>
              ) : null}
              {fact.message ? <span className="text-theme-text-tertiary">{fact.message}</span> : null}
            </div>
            {fact.related_issues && fact.related_issues.length > 0 ? (
              <ul className="mt-0.5 space-y-0.5 pl-3">
                {fact.related_issues.map((rel, ri) => {
                  const label = (
                    <>
                      <span className="text-[10px] uppercase tracking-wide text-theme-text-tertiary">{rel.ref.kind}</span>{' '}
                      <span className="font-mono">
                        {rel.ref.namespace ? `${rel.ref.namespace} / ` : ''}
                        {rel.ref.name}
                      </span>
                      {rel.count && rel.count > 1 ? (
                        <span className="ml-1 tabular-nums" title={`${rel.count} affected resources grouped under this issue`}>· {rel.count} affected</span>
                      ) : null}
                    </>
                  )
                  return (
                    <li key={ri} className="text-theme-text-tertiary">
                      {onResourceClick ? (
                        <button
                          type="button"
                          onClick={() => onResourceClick(rel.ref)}
                          className="group inline-flex items-center gap-1 text-left hover:text-theme-text-secondary"
                        >
                          {label}
                          <ExternalLink className="h-3 w-3 opacity-0 group-hover:opacity-100" />
                        </button>
                      ) : (
                        label
                      )}
                    </li>
                  )
                })}
              </ul>
            ) : null}
          </li>
        ))}
      </ul>
    </div>
  )
}
