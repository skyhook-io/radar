import { useMemo, useState, type ReactNode } from 'react'
import { AlertTriangle } from 'lucide-react'
import { kindToPluralWithGroup } from '../../utils/navigation'
import { IssueRow, issueRowKey, useOpenIssueNotice, useVisibleIssues, type IssueRowSlotContext } from './IssuesView'
import type { IssueExpansion, IssueTimelineLinks } from './IssueRecentChanges'
import { compareIssues, subjectRef, type Issue, type IssueResourceRef, type IssueRowChange } from './types'

export function ResourceIssuesSection({
  issues,
  onResourceClick,
  subjectResource,
  compact,
  renderDetailSection,
  heading = "Operational issues",
  onOpenIssueChange,
  expandedIssue,
  rowChanges,
  onVisibleIssuesChange,
  timelineLinks,
}: {
  compact?: boolean
  renderDetailSection?: (ctx: IssueRowSlotContext) => ReactNode
  issues: Issue[] | undefined
  heading?: string
  /** When provided, related resources in a causal link become clickable. */
  onResourceClick?: (ref: IssueResourceRef) => void
  /** The resource this section is embedded under. When an issue's subject IS
   *  this resource, its redundant "Subject" deep-link is suppressed — the drawer
   *  header already names it. Omit (e.g. the standalone fleet queue) to always
   *  show the subject. */
  subjectResource?: IssueResourceRef
  /** See IssuesViewProps.onOpenIssueChange. */
  onOpenIssueChange?: (issue: Issue | null) => void
  /** See IssuesViewProps.expandedIssue. */
  expandedIssue?: (issue: Issue) => IssueExpansion | undefined
  /** See IssuesViewProps.rowChanges. */
  rowChanges?: ReadonlyMap<string, IssueRowChange>
  /** See IssuesViewProps.onVisibleIssuesChange. */
  onVisibleIssuesChange?: (issues: Issue[]) => void
  /** See IssuesViewProps.timelineLinks. */
  timelineLinks?: IssueTimelineLinks
}) {
  const sorted = useMemo(() => [...(issues ?? [])].sort(compareIssues), [issues])
  const [openId, setOpenId] = useState<string | null>(null)
  useOpenIssueNotice(sorted, openId, issueRowKey, onOpenIssueChange)
  const listRef = useVisibleIssues(sorted, onVisibleIssuesChange)

  if (sorted.length === 0) return null

  return (
    <section className="space-y-2">
      <div className="flex items-center gap-2 px-1 text-xs font-semibold uppercase tracking-wide text-theme-text-secondary">
        <AlertTriangle className="h-4 w-4 text-theme-text-tertiary" aria-hidden />
        <span>{heading} ({sorted.length})</span>
      </div>
      <ol ref={listRef} className="flex flex-col gap-1.5">
        {sorted.map((issue) => {
          const key = issueRowKey(issue)
          const open = openId === key
          return (
            <IssueRow
              key={key}
              issue={issue}
              expandedIssue={open ? expandedIssue?.(issue) : undefined}
              compact={compact}
              renderDetailSection={renderDetailSection}
              open={open}
              onToggle={() => setOpenId((cur) => (cur === key ? null : key))}
              onResourceClick={onResourceClick}
              hideSubject={subjectResource ? sameResource(subjectRef(issue), subjectResource) : false}
              rowChange={rowChanges?.get(key)}
              timelineLinks={timelineLinks}
            />
          )
        })}
      </ol>
    </section>
  )
}

// Identity match tolerant of singular/plural + casing on kind (the subject ref
// carries the wire kind, the host typically passes the plural API name). A
// match hides the card's kind and name, and a resource's feed also holds issues
// about its owners, so the API group must match exactly: a missing group is the
// core group. An issue that carries no group for a non-core kind (an older
// Radar) then keeps its name, which repeats the header rather than hiding
// another resource.
export function sameResource(a: IssueResourceRef, b: IssueResourceRef): boolean {
  return (
    (a.group ?? '') === (b.group ?? '') &&
    a.name === b.name &&
    (a.namespace ?? '') === (b.namespace ?? '') &&
    kindToPluralWithGroup(a.kind, a.group ?? '').toLowerCase() ===
      kindToPluralWithGroup(b.kind, b.group ?? '').toLowerCase()
  )
}
