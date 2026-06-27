import { AlertTriangle } from 'lucide-react'
import { Section } from '../ui/drawer-components'
import { Badge } from '../ui/Badge'
import type { Issue } from './types'

/**
 * ResourceIssuesSection — the compact "Operational Issues" block for the resource
 * detail. Renders the live, diagnosed Issues that touch one resource (its own +,
 * for a workload, its owned pods' issues, server-rolled-up via RelatedIssues).
 * The diagnosis sibling of IssuesView's queue row — same Cause/Action vocabulary,
 * stripped of the queue chrome (accordion, scope copy, fan-out expansion).
 */
export function ResourceIssuesSection({ issues }: { issues: Issue[] | undefined }) {
  if (!issues || issues.length === 0) return null
  return (
    <Section title={`Operational Issues (${issues.length})`} icon={AlertTriangle} defaultExpanded>
      <div className="space-y-3">
        {issues.map((issue) => (
          <div key={issue.id} className="card-inner">
            <div className="mb-1 flex items-center gap-2">
              <Badge severity={issue.severity === 'critical' ? 'error' : 'warning'} size="sm">
                {issue.severity}
              </Badge>
              <span className="text-sm font-medium text-theme-text-primary">{issue.reason}</span>
              {issue.count ? (
                <span className="text-xs text-theme-text-tertiary tabular-nums">· {issue.count} affected</span>
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
        ))}
      </div>
    </Section>
  )
}
