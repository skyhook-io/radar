import { useMemo, useState } from 'react'
import { AlertTriangle } from 'lucide-react'
import { kindToPlural } from '../../utils/navigation'
import { IssueRow } from './IssuesView'
import { compareIssues, subjectRef, type Issue, type IssueResourceRef } from './types'

export function ResourceIssuesSection({
  issues,
  onResourceClick,
  subjectResource,
}: {
  issues: Issue[] | undefined
  /** When provided, related resources in a causal link become clickable. */
  onResourceClick?: (ref: IssueResourceRef) => void
  /** The resource this section is embedded under. When an issue's subject IS
   *  this resource, its redundant "Subject" deep-link is suppressed — the drawer
   *  header already names it. Omit (e.g. the standalone fleet queue) to always
   *  show the subject. */
  subjectResource?: IssueResourceRef
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
              hideSubject={subjectResource ? sameResource(subjectRef(issue), subjectResource) : false}
            />
          )
        })}
      </ol>
    </section>
  )
}

// Identity match tolerant of singular/plural + casing on kind (the subject ref
// carries the wire kind, the host typically passes the plural API name) — group
// is deliberately ignored: a per-resource issue feed is already scoped to one
// resource, and kind+namespace+name uniquely identifies it there.
export function sameResource(a: IssueResourceRef, b: IssueResourceRef): boolean {
  return (
    a.name === b.name &&
    (a.namespace ?? '') === (b.namespace ?? '') &&
    kindToPlural(a.kind).toLowerCase() === kindToPlural(b.kind).toLowerCase()
  )
}

function issueKey(issue: Issue): string {
  return `${issue.cluster_id ?? ''}:${issue.id}`
}
