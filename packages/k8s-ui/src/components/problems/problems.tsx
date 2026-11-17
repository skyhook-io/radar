import { Fragment, createContext, useContext, type ReactNode } from 'react'
import { clsx } from 'clsx'
import type { HealthLevel } from '../resources/resource-utils'
import type { ResourceRef } from '../../types/core'
import { healthToSeverity } from '../../utils/badge-colors'
import { toneTextClass } from '../ui/status-tone'
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
  /** What to do about it, in one plain line; a callout shows it under the detail. */
  nextStep?: string
  /** The object the evidence is about: the workspace's root object or one of its children. */
  subject: ResourceRef & { group: string }
  /** A shorter headline for tight places (a fleet cell); `title` stays the precise one. */
  shortTitle?: string
  /** Other objects the same problem is about, e.g. earlier runs that failed the same way. */
  alsoAbout?: Pick<ResourceRef, 'kind' | 'name'>[]
  /** Objects the detail goes on to name, each a link: "Velero rejected 2 runs because of it: a and b." */
  related?: { lead: string; refs: (ResourceRef & { group: string })[] }
}

// Links listed before "and N more".
const RELATED_SHOWN = 3

function RelatedRefs({ related, onNavigate, className }: { related: NonNullable<ProblemDetails['related']>; onNavigate?: NavigateToRef; className?: string }) {
  const shown = related.refs.slice(0, RELATED_SHOWN)
  const rest = related.refs.length - shown.length
  return (
    <div className={clsx('break-words text-xs', className)}>
      {related.lead}{' '}
      {shown.map((ref, i) => (
        <Fragment key={`${ref.kind}/${ref.namespace}/${ref.name}`}>
          {i > 0 && (i === shown.length - 1 && rest === 0 ? ' and ' : ', ')}
          <RefLink refTo={ref} onNavigate={onNavigate} mono />
        </Fragment>
      ))}
      {rest > 0 && ` and ${rest} more`}.
    </div>
  )
}

export type ProblemProvenance =
  | { source: 'issue'; origin?: ProblemOrigin; measuredBy?: never; sourceDetail?: never }
  | { source: 'audit'; sourceDetail?: string; origin?: never; measuredBy?: never }
  | { source: 'measurement'; measuredBy?: string; sourceDetail?: string; origin?: never }
  /** A workspace's own reading of cached objects. */
  | { source: 'reading'; origin?: ProblemOrigin; measuredBy?: never; sourceDetail?: never }

export type WorkspaceProblem<C extends string = string> = ProblemDetails<C> & ProblemProvenance & { tone: Exclude<HealthLevel, 'healthy'> }

/** A problem's provenance label: where its evidence comes from, never a generic "Radar issue". */
export function problemOriginLabel(problem: WorkspaceProblem): ProblemOrigin {
  switch (problem.source) {
    case 'audit':
      return { label: 'Best-practice check', detail: problem.sourceDetail }
    case 'measurement':
      return { label: problem.measuredBy ? `Measured by ${problem.measuredBy}` : 'Measured', detail: problem.sourceDetail }
    case 'reading':
      return problem.origin ?? { label: 'Detected by Radar' }
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
  hideOrigin,
  className = 'mt-1.5',
  children,
}: {
  problem: WorkspaceProblem
  /** The workspace's root kind: a problem about another kind names its subject. */
  rootKind: string
  onNavigate?: NavigateToRef
  subjectIsSelf?: boolean
  /** Leave out where the problem came from: when every row on a screen comes from the same place, the label says nothing. */
  hideOrigin?: boolean
  /** Spacing above the line. */
  className?: string
  children?: ReactNode
}) {
  const openIssue = useContext(OpenIssueContext)
  const origin = problemOriginLabel(problem)
  const aboutChild = !subjectIsSelf && problem.subject.kind !== rootKind
  return (
    <div className={clsx(className, 'flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-theme-text-tertiary')}>
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
      {!hideOrigin && (
        <Tooltip content={origin.detail} disabled={!origin.detail}>
          <span>{origin.label}</span>
        </Tooltip>
      )}
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
      {problem.related && <RelatedRefs related={problem.related} onNavigate={onNavigate} className="mt-1 text-theme-text-secondary" />}
      {problem.nextStep && (
        <div className="mt-1 break-words text-xs text-theme-text-primary">
          <span className="font-medium">Next step: </span>
          {problem.nextStep}
        </div>
      )}
      {problem.rawDetail && <FoldSection title={problem.rawDetailLabel ?? "Original evidence"} summary="" attention={false}><div className="break-words text-xs text-theme-text-secondary">{problem.rawDetail}</div></FoldSection>}
      <ProblemMeta problem={problem} rootKind={rootKind} onNavigate={onNavigate} subjectIsSelf={subjectIsSelf}>
        {action}
        {more}
      </ProblemMeta>
    </AlertBanner>
  )
}

/**
 * The problems a callout does not show, as a compact list with the callout's
 * tone, title and source. `subjectFirst` names each row's subject under its
 * title, before the detail, for a list whose rows are about many objects.
 */
export function ProblemList({ problems, rootKind, onNavigate, hideOrigin, subjectFirst }: { problems: WorkspaceProblem[]; rootKind: string; onNavigate?: NavigateToRef; hideOrigin?: boolean; subjectFirst?: boolean }) {
  return (
    <ul className="space-y-2">
      {problems.map((p) => {
        const meta = <ProblemMeta problem={p} rootKind={rootKind} onNavigate={onNavigate} hideOrigin={hideOrigin} className={subjectFirst ? 'mt-0.5' : undefined} />
        return (
          <li key={p.id} className="min-w-0 text-sm">
            <div className={clsx('font-medium break-words', toneTextClass(p.tone))}>{p.title}</div>
            {subjectFirst && meta}
            {p.detail && <div className="text-xs text-theme-text-secondary break-words">{p.detail}</div>}
            {p.related && <RelatedRefs related={p.related} onNavigate={onNavigate} className="text-theme-text-secondary" />}
            {p.rawDetail && <FoldSection title={p.rawDetailLabel ?? "Original evidence"} summary="" attention={false}><div className="break-words text-xs text-theme-text-secondary">{p.rawDetail}</div></FoldSection>}
            {!subjectFirst && meta}
          </li>
        )
      })}
    </ul>
  )
}
