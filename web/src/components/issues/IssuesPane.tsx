import { useMemo, useState } from 'react'
import { useIssues } from '../../api/client'
import type { SelectedResource } from '../../types'
import {
  IssuesView,
  PaneLoader,
  PageHeader,
  DistributionBar,
  DistributionLegendChip,
  ISSUE_SEVERITIES,
  ISSUE_SEVERITY_LABEL,
  ISSUE_SEVERITY_FILL_CLASS,
  ISSUE_SEVERITY_TEXT_CLASS,
  type IssueResourceRef,
  type IssueSeverity,
} from '@skyhook-io/k8s-ui'
import { AlertTriangle } from 'lucide-react'

interface IssuesPaneProps {
  namespaces: string[]
  onNavigateToResource: (resource: SelectedResource) => void
}

// The per-cluster Issues surface. Renders the same shared triage queue
// (IssuesView) the Hub fleet view uses — single cluster here, so no cluster
// label and in-app (client-side) resource navigation. Classification +
// owner-grouping come pre-computed from radar's /api/issues
// (internal/issues.Compose → Classify → Group). Filtering is the host's job
// (IssuesView is a pure list); single-cluster gets a light severity filter
// fused with the rollup (the same count + bar + clickable-legend pattern as the
// Checks queue) rather than Hub's fleet facet sidebar.
export function IssuesPane({ namespaces, onNavigateToResource }: IssuesPaneProps) {
  const { data, isLoading, error } = useIssues(namespaces)
  const [severityFilter, setSeverityFilter] = useState<Set<IssueSeverity>>(new Set())

  const allIssues = data?.issues ?? []
  const totals = useMemo(() => {
    const t: Record<IssueSeverity, number> = { critical: 0, warning: 0 }
    for (const i of allIssues) t[i.severity] = (t[i.severity] ?? 0) + 1
    return t
  }, [allIssues])
  const shown = severityFilter.size ? allIssues.filter((i) => severityFilter.has(i.severity)) : allIssues

  const toggleSeverity = (s: IssueSeverity) =>
    setSeverityFilter((prev) => {
      const next = new Set(prev)
      next.has(s) ? next.delete(s) : next.add(s)
      return next
    })

  const onResourceClick = (ref: IssueResourceRef) =>
    onNavigateToResource({ kind: ref.kind, namespace: ref.namespace ?? '', name: ref.name, group: ref.group ?? '' })

  if (isLoading) {
    return <PaneLoader label="Loading issues…" className="flex-1" />
  }

  if (error) {
    return (
      <div className="flex-1 flex items-center justify-center text-theme-text-secondary">
        <p>Failed to load issues</p>
      </div>
    )
  }

  return (
    <div className="flex-1 flex flex-col min-h-0 p-4 gap-4 overflow-auto">
      <PageHeader
        icon={AlertTriangle}
        title="Issues"
        description="Live cluster problems — crashes, scheduling failures, bad references — grouped by the resource they affect."
      />

      {/* Rollup + light severity filter — count, the shared distribution bar,
          and a clickable severity legend that doubles as the filter (the Checks
          queue pattern). Hidden when there's nothing to summarize. */}
      {allIssues.length > 0 && (
        <div className="flex flex-col gap-2.5">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex items-baseline gap-2">
              <span className="text-lg font-semibold text-theme-text-primary tabular-nums">{allIssues.length}</span>
              <span className="text-sm text-theme-text-tertiary">{allIssues.length === 1 ? 'issue' : 'issues'}</span>
            </div>
            <div className="flex flex-wrap items-center gap-1">
              {ISSUE_SEVERITIES.map((s) =>
                totals[s] > 0 || severityFilter.has(s) ? (
                  <DistributionLegendChip
                    key={s}
                    label={ISSUE_SEVERITY_LABEL[s]}
                    count={totals[s]}
                    fillClass={ISSUE_SEVERITY_FILL_CLASS[s]}
                    textClass={ISSUE_SEVERITY_TEXT_CLASS[s]}
                    active={severityFilter.has(s)}
                    onClick={() => toggleSeverity(s)}
                  />
                ) : null,
              )}
            </div>
          </div>
          <DistributionBar
            ariaLabel="Severity distribution"
            segments={ISSUE_SEVERITIES.map((s) => ({ key: s, count: totals[s], fillClass: ISSUE_SEVERITY_FILL_CLASS[s] }))}
          />
        </div>
      )}

      {/* Visibility honesty: when RBAC reads are incomplete, an empty queue may
          mean "can't see" rather than "nothing broken" — say so up front so the
          empty state isn't mistaken for a clean bill of health. */}
      {data?.visibility?.impact && (
        <div className="flex items-start gap-2 rounded-lg border border-theme-border bg-theme-elevated px-3 py-2 text-xs text-theme-text-secondary">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
          <span>Limited visibility — {data.visibility.impact} Results may be incomplete.</span>
        </div>
      )}

      {/* Truncation honesty: when more issues matched than were returned, say
          so — don't present a capped list as the complete picture. */}
      {data?.total_matched != null && data.total_matched > (data.issues?.length ?? 0) && (
        <p className="text-xs text-theme-text-tertiary">
          Showing {data.issues?.length ?? 0} of {data.total_matched} issues (capped) — narrow by namespace to see the rest.
        </p>
      )}

      {/* anyData = the query resolved, i.e. the cluster is reachable; an empty
          list then means "nothing broken" rather than "not connected". */}
      <IssuesView issues={shown} anyData={!!data} onResourceClick={onResourceClick} />
    </div>
  )
}
