import { useIssues } from '../../api/client'
import type { SelectedResource } from '../../types'
import { IssuesView, PaneLoader, PageHeader, type IssueResourceRef } from '@skyhook-io/k8s-ui'
import { AlertTriangle } from 'lucide-react'

interface IssuesPaneProps {
  namespaces: string[]
  onNavigateToResource: (resource: SelectedResource) => void
}

// The per-cluster Issues surface. Renders the same shared triage queue
// (IssuesView) the Hub fleet view uses — single cluster here, so no cluster
// label and in-app (client-side) resource navigation. Classification +
// owner-grouping come pre-computed from radar's /api/issues
// (internal/issues.Compose → Classify → Group).
export function IssuesPane({ namespaces, onNavigateToResource }: IssuesPaneProps) {
  const { data, isLoading, error } = useIssues(namespaces)

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
      <IssuesView issues={data?.issues ?? []} anyData={!!data} onResourceClick={onResourceClick} />
    </div>
  )
}
