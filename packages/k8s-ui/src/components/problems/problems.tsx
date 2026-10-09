import { createContext, useContext, type ReactNode } from 'react'
import { clsx } from 'clsx'
import type { HealthLevel } from '../resources/resource-utils'
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
export interface WorkspaceProblem<C extends string = string> {
  /** Stable identity for keys. */
  id: string
  severity: 'critical' | 'warning' | 'posture'
  category: C
  title: string
  detail?: string
  rawDetail?: string
  /** The object the evidence is about: the workspace's root object or one of its children. */
  subject: { kind: string; group: string; namespace: string; name: string }
  /**
   * issue: the Issues engine. audit: a best-practice check. measurement:
   * derived from a reading only callers holding its grants receive.
   */
  source: 'issue' | 'audit' | 'measurement'
  /** What took the measurement, e.g. "Prometheus" (shown as "Measured by Prometheus"). */
  measuredBy?: string
  /** The measurement's series were matched to the subject by name only (see `measuredBy`). */
  unverifiedMatch?: boolean
  /** How it was measured (queries, metric names), shown on hover over the source. */
  sourceDetail?: string
  /** A shorter headline for tight places (a fleet cell); `title` stays the precise one. */
  shortTitle?: string
  /** Where an issue's evidence comes from. */
  origin?: ProblemOrigin
  /** Other objects the same problem is about, e.g. earlier runs that failed the same way. */
  alsoAbout?: { kind: string; name: string }[]
}

export const PROBLEM_TONE: Record<WorkspaceProblem['severity'], HealthLevel> = {
  critical: 'unhealthy',
  warning: 'degraded',
  posture: 'neutral',
}

const PROBLEM_VARIANT: Record<WorkspaceProblem['severity'], 'error' | 'warning' | 'info'> = {
  critical: 'error',
  warning: 'warning',
  posture: 'info',
}

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

export function ProblemCallout({
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
  return (
    <AlertBanner variant={PROBLEM_VARIANT[problem.severity]} title={problem.title} message={problem.detail}>
      {problem.rawDetail && <FoldSection title="Scheduler message" summary="" attention={false}><div className="break-words text-xs text-theme-text-secondary">{problem.rawDetail}</div></FoldSection>}
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
            <StatusDot tone={PROBLEM_TONE[p.severity]} size="sm" />
          </span>
          <div className="min-w-0">
            <div className={clsx('font-medium break-words', toneTextClass(PROBLEM_TONE[p.severity]))}>{p.title}</div>
            {p.detail && <div className="text-xs text-theme-text-secondary break-words">{p.detail}</div>}
            {p.rawDetail && <FoldSection title="Scheduler message" summary="" attention={false}><div className="break-words text-xs text-theme-text-secondary">{p.rawDetail}</div></FoldSection>}
            <ProblemMeta problem={p} rootKind={rootKind} onNavigate={onNavigate} />
          </div>
        </li>
      ))}
    </ul>
  )
}
