import { AlertTriangle } from 'lucide-react'
import { Section } from '../ui/drawer-components'
import { Badge } from '../ui/Badge'
import type { Issue } from './types'
import { categoryLabel } from './severity'

/**
 * ResourceIssuesSection — the compact "Operational Issues" block for the resource
 * detail. Renders the live, diagnosed Issues that touch one resource (its own +,
 * for a workload, its owned pods' issues, server-rolled-up via RelatedIssues).
 * The diagnosis sibling of IssuesView's queue row — same Cause/Action vocabulary,
 * stripped of the queue chrome (accordion, scope copy, fan-out expansion).
 *
 * Header mirrors the queue: the plain `categoryLabel` is the operator-facing
 * headline and the raw `reason` rides alongside as a muted signal (so the K8s
 * jargon is available but not the lead). Body is intentionally just the plain
 * `cause` (which names the offending object) + the `Next step` — the diagnosis
 * and the fix. The raw `message`/evidence stays in the queue + MCP where the
 * locator detail is wanted; inline it mostly restated the cause or the category,
 * so it's omitted to keep the card scannable. (`message` is the body fallback
 * only for categories that don't yet emit a `cause`.)
 */
export function ResourceIssuesSection({ issues }: { issues: Issue[] | undefined }) {
  if (!issues || issues.length === 0) return null
  return (
    <Section title={`Operational Issues (${issues.length})`} icon={AlertTriangle} defaultExpanded>
      <div className="space-y-3">
        {issues.map((issue) => {
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
            </div>
          )
        })}
      </div>
    </Section>
  )
}
