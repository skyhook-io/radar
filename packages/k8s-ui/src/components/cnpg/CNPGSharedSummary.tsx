import type { ReactNode } from 'react'
import { Badge } from '../ui/Badge'
import type { StatusBadge as StatusBadgeValue } from '../resources/resource-utils'
import { CNPG_GROUP } from '../resources/resource-utils-cnpg'
import type { CNPGWorkspaceIssue, CNPGWorkspaceResponse } from './workspace'
import { healthToSeverity } from '../../utils/badge-colors'
import { clustersIn, problemsForObject, relationUnavailable, targetCluster, type CNPGObjectRef } from './relations'
import { type NavigateToRef, RefLink } from '../ui/RefLink'
import { FactValue, ProblemCallout } from '../workspace'

const MAX_PROBLEMS = 3

export function SummaryShell({ children }: { children: ReactNode }) {
  return <div className="px-4 py-4">{children}</div>
}

/** The object's own Radar issues, most severe first. */
export function ObjectProblems({
  issues,
  subject,
  onNavigate,
}: {
  issues: CNPGWorkspaceIssue[] | undefined
  subject: CNPGObjectRef
  onNavigate?: NavigateToRef
}) {
  const problems = problemsForObject(issues, subject)
  if (problems.length === 0) return null
  const shown = problems.slice(0, MAX_PROBLEMS)
  const rest = problems.length - shown.length
  return (
    <div className="mb-4 space-y-2">
      {shown.map((p, i) => (
        <ProblemCallout
          rootKind="Cluster"
          key={p.id}
          problem={p}
          onNavigate={onNavigate}
          subjectIsSelf
          more={i === shown.length - 1 && rest > 0 ? <span>+{rest} more</span> : null}
        />
      ))}
    </div>
  )
}

export function PhaseBadge({ status }: { status: StatusBadgeValue }) {
  return (
    <Badge severity={healthToSeverity(status.level)} size="sm">
      {status.text}
    </Badge>
  )
}

export function NotReported({ text = 'Not reported' }: { text?: string }) {
  return <span className="text-theme-text-tertiary">{text}</span>
}

/** A timestamp as an age, with the absolute time on hover. */
export function TimeAgo({ at, missing }: { at: unknown; missing?: string }) {
  if (typeof at !== 'string' || !at || !Number.isFinite(Date.parse(at))) return <NotReported text={missing} />
  return <FactValue fact={{ text: '', tone: 'neutral', at }} />
}

export function Note({ children }: { children: ReactNode }) {
  return <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">{children}</div>
}

/** The Cluster an object declares itself against, linked. */
export function ClusterLink({
  resource,
  workspace,
  onNavigate,
}: {
  resource: any
  workspace: CNPGWorkspaceResponse | null
  onNavigate?: NavigateToRef
}) {
  const name = resource?.spec?.cluster?.name
  if (!name) return <NotReported text="Not set" />
  const ns = resource?.metadata?.namespace ?? ''
  const visible = !!targetCluster(resource, clustersIn(workspace))
  return (
    <span>
      <RefLink refTo={{ kind: 'Cluster', group: CNPG_GROUP, namespace: ns, name }} onNavigate={onNavigate} mono />
      {workspace && !visible && !relationUnavailable(workspace, 'clusters', ns, 'Clusters') && (
        <span className="text-theme-text-tertiary"> · not found in this namespace</span>
      )}
    </span>
  )
}
