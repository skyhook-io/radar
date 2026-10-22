import { createContext, useContext, type ReactNode } from 'react'
import { clsx } from 'clsx'
import type { HealthLevel } from '../resources/resource-utils'
import type { ResourceRef } from '../../types/core'
import { healthToSeverity } from '../../utils/badge-colors'
import { StatusDot, toneTextClass } from '../ui/status-tone'
import { Tooltip } from '../ui/Tooltip'
import { AlertBanner } from '../ui/drawer-components'
import { FoldSection } from '../ui/FoldSection'
import { RefLink, type NavigateToRef } from '../ui/RefLink'

/** Where a problem's evidence comes from, in user terms. */
export interface ProblemOrigin {
  label: string
  /** The exact field or condition, shown on hover. */
  detail?: string
}

/**
 * Something about a workspace object that needs a look. `C` is the
 * integration's own category set.
 */
export interface ProblemDetails<C extends string = string> {
  /** Stable identity for keys. */
  id: string
  category: C
  title: string
  detail?: string
  rawDetail?: string
  rawDetailLabel?: string
  /** The object the evidence is about: the workspace's root object or one of its children. */
  subject: ResourceRef & { group: string }
  /** A shorter headline for tight places (a fleet cell); `title` stays the precise one. */
  shortTitle?: string
  /** Other objects the same problem is about, e.g. earlier runs that failed the same way. */
  alsoAbout?: Pick<ResourceRef, 'kind' | 'name'>[]
}

export type ProblemProvenance =
  | { source: 'issue'; origin?: ProblemOrigin; measuredBy?: never; sourceDetail?: never }
  | { source: 'audit'; sourceDetail?: string; origin?: never; measuredBy?: never }
  | { source: 'measurement'; measuredBy?: string; sourceDetail?: string; origin?: never }

export type WorkspaceProblem<C extends string = string> = ProblemDetails<C> & ProblemProvenance & { tone: Exclude<HealthLevel, 'healthy'> }

/** A problem's provenance label: where its evidence comes from, never a generic "Radar issue". */
export function problemOriginLabel(problem: WorkspaceProblem): ProblemOrigin {
  switch (problem.source) {
    case 'audit':
      return { label: 'Best-practice check', detail: problem.sourceDetail }
    case 'measurement':
      return { label: problem.measuredBy ? `Measured by ${problem.measuredBy}` : 'Measured', detail: problem.sourceDetail }
  }
  return problem.origin ?? { label: 'Detected by Radar' }
}

/**
 * How a host opens a problem on its Issues page. Supplied by context so every
 * summary and drawer gets the link without threading a prop through each.
 */
export const OpenIssueContext = createContext<((problem: WorkspaceProblem) => void) | undefined>(undefined)

export function ProblemMeta({
  problem,
  rootKind,
  onNavigate,
  subjectIsSelf,
  children,
}: {
  problem: WorkspaceProblem
  /** The workspace's root kind: a problem about another kind names its subject. */
  rootKind: string
  onNavigate?: NavigateToRef
  subjectIsSelf?: boolean
  children?: ReactNode
}) {
  const openIssue = useContext(OpenIssueContext)
  const origin = problemOriginLabel(problem)
  const aboutChild = !subjectIsSelf && problem.subject.kind !== rootKind
  return (
    <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-theme-text-tertiary">
      {aboutChild && (
        <span>
          {problem.subject.kind}{' '}
          <RefLink refTo={problem.subject} onNavigate={onNavigate} mono />
          {problem.alsoAbout && problem.alsoAbout.length > 0 && ' '}
          {problem.alsoAbout && problem.alsoAbout.length > 0 && (
            <Tooltip
              content={
                <ul>
                  {problem.alsoAbout.map((o) => (
                    <li key={`${o.kind}/${o.name}`} className="font-mono">
                      {o.kind} {o.name}
                    </li>
                  ))}
                </ul>
              }
            >
              <span>and {problem.alsoAbout.length} more</span>
            </Tooltip>
          )}
        </span>
      )}
      <Tooltip content={origin.detail} disabled={!origin.detail}>
        <span>{origin.label}</span>
      </Tooltip>
      {openIssue && problem.source === 'issue' && (
        <button type="button" onClick={() => openIssue(problem)} className="whitespace-nowrap text-accent-text hover:underline">
          See in Issues →
        </button>
      )}
      {children}
    </div>
  )
}

export function WorkspaceProblemCallout({
  problem,
  rootKind,
  more,
  onNavigate,
  action,
  subjectIsSelf,
}: {
  problem: WorkspaceProblem
  rootKind: string
  more?: ReactNode
  onNavigate?: NavigateToRef
  action?: ReactNode
  /** The callout sits on the subject's own page, so linking to it would loop. */
  subjectIsSelf?: boolean
}) {
  const severity = healthToSeverity(problem.tone)
  const variant = severity === 'neutral' ? 'unknown' : severity
  return (
    <AlertBanner variant={variant} title={problem.title} message={problem.detail}>
      {problem.rawDetail && <FoldSection title={problem.rawDetailLabel ?? "Original evidence"} summary="" attention={false}><div className="break-words text-xs text-theme-text-secondary">{problem.rawDetail}</div></FoldSection>}
      <ProblemMeta problem={problem} rootKind={rootKind} onNavigate={onNavigate} subjectIsSelf={subjectIsSelf}>
        {action}
        {more}
      </ProblemMeta>
    </AlertBanner>
  )
}

/** The problems a callout does not show, as a compact list with the callout's tone, title and source. */
export function ProblemList({ problems, rootKind, onNavigate }: { problems: WorkspaceProblem[]; rootKind: string; onNavigate?: NavigateToRef }) {
  return (
    <ul className="space-y-2">
      {problems.map((p) => (
        <li key={p.id} className="flex items-start gap-2 text-sm">
          <span className="mt-1.5 shrink-0">
            <StatusDot tone={p.tone} size="sm" />
          </span>
          <div className="min-w-0">
            <div className={clsx('font-medium break-words', toneTextClass(p.tone))}>{p.title}</div>
            {p.detail && <div className="text-xs text-theme-text-secondary break-words">{p.detail}</div>}
            {p.rawDetail && <FoldSection title={p.rawDetailLabel ?? "Original evidence"} summary="" attention={false}><div className="break-words text-xs text-theme-text-secondary">{p.rawDetail}</div></FoldSection>}
            <ProblemMeta problem={p} rootKind={rootKind} onNavigate={onNavigate} />
          </div>
        </li>
      ))}
    </ul>
  )
}
