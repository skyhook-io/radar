import { useMemo, useState } from 'react'
import { AlertTriangle } from 'lucide-react'
import { IssueRow } from './IssuesView'
import { compareIssues, type Issue, type IssueResourceRef } from './types'

export function ResourceIssuesSection({
  issues,
  onResourceClick,
}: {
  issues: Issue[] | undefined
  /** When provided, related resources in a causal link become clickable. */
  onResourceClick?: (ref: IssueResourceRef) => void
}) {
  const sorted = useMemo(() => [...(issues ?? [])].sort(compareIssues), [issues])
  const [openId, setOpenId] = useState<string | null>(null)

  if (sorted.length === 0) return null

  return (
    <section className="space-y-2">
      <div className="flex items-center gap-2 px-1 text-xs font-semibold uppercase tracking-wide text-theme-text-secondary">
        <AlertTriangle className="h-4 w-4 text-theme-text-tertiary" aria-hidden />
        <span>Operational issues ({sorted.length})</span>
      </div>
      <ol className="flex flex-col gap-1.5">
        {sorted.map((issue) => {
          const key = issueKey(issue)
          return (
            <IssueRow
              key={key}
              issue={issue}
              open={openId === key}
              onToggle={() => setOpenId((cur) => (cur === key ? null : key))}
              onResourceClick={onResourceClick}
            />
          )
        })}
      </ol>
    </section>
  )
}

function issueKey(issue: Issue): string {
  return `${issue.cluster_id ?? ''}:${issue.id}`
}
