import { useMemo, useState } from 'react'
import { useIssues } from '../../api/client'
import type { SelectedResource } from '../../types'
import {
  IssuesView,
  PaneLoader,
  PageHeader,
  SummaryTile,
  ISSUE_SEVERITIES,
  ISSUE_SEVERITY_LABEL,
  type IssueResourceRef,
  type IssueSeverity,
  type SummaryTone,
} from '@skyhook-io/k8s-ui'
import { AlertTriangle } from 'lucide-react'

const SEVERITY_TONE: Record<IssueSeverity, SummaryTone> = { critical: 'error', warning: 'warning' }

interface IssuesPaneProps {
  namespaces: string[]
  onNavigateToResource: (resource: SelectedResource) => void
}

// The per-cluster Issues surface. Renders the same shared triage queue
// (IssuesView) the Hub fleet view uses — single cluster here, so no cluster
// label and in-app (client-side) resource navigation. Classification +
// owner-grouping come pre-computed from radar's /api/issues
// (internal/issues.Compose → Classify → Group). Filtering is the host's job
// (IssuesView is a pure list); single-cluster gets a light severity filter via
// the header status tiles (clickable → filter), matching the Applications /
// GitOps header-tile pattern rather than Hub's fleet facet sidebar.
export function IssuesPane({ namespaces, onNavigateToResource }: IssuesPaneProps) {
  const { data, isLoading, error } = useIssues(namespaces)
  const [severityFilter, setSeverityFilter] = useState<Set<IssueSeverity>>(new Set())

  const allIssues = useMemo(() => data?.issues ?? [], [data])
  const totals = useMemo(() => {
    const t: Record<IssueSeverity, number> = { critical: 0, warning: 0 }
    for (const i of allIssues) t[i.severity] = (t[i.severity] ?? 0) + 1
    return t
  }, [allIssues])
  const shown = severityFilter.size ? allIssues.filter((i) => severityFilter.has(i.severity)) : allIssues

  const toggleSeverity = (s: IssueSeverity) =>
    setSeverityFilter((prev) => {
      const next = new Set(prev)
      if (next.has(s)) next.delete(s); else next.add(s)
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
        actions={
          allIssues.length > 0 ? (
            <>
              <SummaryTile label={allIssues.length === 1 ? 'issue' : 'issues'} value={allIssues.length} />
              {ISSUE_SEVERITIES.map((s) =>
                totals[s] > 0 || severityFilter.has(s) ? (
                  <SummaryTile
                    key={s}
                    label={ISSUE_SEVERITY_LABEL[s]}
                    value={totals[s]}
                    tone={SEVERITY_TONE[s]}
                    active={severityFilter.has(s)}
                    onClick={() => toggleSeverity(s)}
                  />
                ) : null,
              )}
            </>
          ) : undefined
        }
      />

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

      {/* Filtered-empty is NOT the healthy empty state: when a severity filter
          hides every row but issues still exist, say "no matches" rather than
          letting IssuesView render its "nothing broken" terminal state. */}
      {severityFilter.size > 0 && allIssues.length > 0 && shown.length === 0 ? (
        <div className="flex flex-col items-center gap-2 py-12 text-center text-sm text-theme-text-secondary">
          <p>No issues match the selected severity.</p>
          <button
            type="button"
            onClick={() => setSeverityFilter(new Set())}
            className="text-xs text-skyhook-600 hover:text-skyhook-500 dark:text-skyhook-400"
          >
            Clear filter
          </button>
        </div>
      ) : (
        /* anyData = the query resolved, i.e. the cluster is reachable; an empty
           list then means "nothing broken" rather than "not connected". */
        <IssuesView issues={shown} anyData={!!data} onResourceClick={onResourceClick} />
      )}
    </div>
  )
}
