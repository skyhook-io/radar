import { Fragment, type ReactNode } from 'react'
import { ExternalLink, GitBranch, Info, Loader2 } from 'lucide-react'
import { clsx } from 'clsx'

import { AlertBanner } from '../ui/drawer-components'
import {
  canConfirmGitOpsWrite,
  gitOpsWriteHeadline,
  guardToolLabel,
  type GitOpsWriteAssessment,
  type GitOpsWriteGuard,
} from '../../utils/gitops-write-guard'
import { gitOpsOwnerFromRef, managerChoices, managerRefLabel, type GitOpsOwnerRef } from '../../utils/gitops-owner'
import { escapeRegExp } from '../../utils/log-format'

export { canConfirmGitOpsWrite }

export interface GitOpsWriteWarningProps {
  guard: GitOpsWriteGuard
  acknowledged: boolean
  onAcknowledgedChange: (acknowledged: boolean) => void
  /** Opens the GitOps owner (or Helm release) the guard names. */
  onOpenOwner?: () => void
  /** Opens one of several managers the guard names, so a reader can inspect
   *  each before deciding. Without it, they are named but not linked. */
  onOpenManager?: (owner: GitOpsOwnerRef) => void
  disabled?: boolean
  className?: string
}

const OWNER_LINK_LABEL = {
  applications: 'View Argo CD Application',
  kustomizations: 'View Flux Kustomization',
  helmreleases: 'View Flux HelmRelease',
} as const

// managerLinks: the managers a guard naming more than one can open, the lead
// among them, since a lead isn't a winner. One Radar didn't find (missing,
// withheld) or a label naming no namespace isn't linked: the label may name
// either of two Applications of that name.
function managerLinks(guard: GitOpsWriteGuard, onOpenManager: GitOpsWriteWarningProps['onOpenManager']): { label: string; owner: GitOpsOwnerRef }[] {
  if (!onOpenManager || !guard.manager) return []
  const choices = managerChoices(guard.manager)
  if (choices.length < 2) return []
  return choices.flatMap((c) => {
    const owner = c.found && c.ref.namespace ? gitOpsOwnerFromRef(c.ref) : null
    return owner ? [{ label: managerRefLabel(c.ref), owner }] : []
  })
}

type ManagerLink = ReturnType<typeof managerLinks>[number]

// linkManagers turns the first mention of each linked manager, across all the
// lines, into its link, so the reason the reader is already reading is where
// they open it. A name followed by more of a name (argocd/a in argocd/ab) isn't
// a mention.
function linkManagers(lines: string[], links: ManagerLink[], onOpen: (owner: GitOpsOwnerRef) => void): { lines: ReactNode[]; unmentioned: ManagerLink[] } {
  if (!links.length) return { lines, unmentioned: [] }
  const byLabel = new Map(links.map((l) => [l.label, l]))
  const pattern = new RegExp(`(${[...byLabel.keys()].map(escapeRegExp).join('|')})(?![\\w./-])`, 'g')
  const linked = new Set<string>()
  const out = lines.map((line, i) => {
    const parts: ReactNode[] = []
    let last = 0
    for (const match of line.matchAll(pattern)) {
      const link = byLabel.get(match[0])!
      if (linked.has(link.label)) continue
      linked.add(link.label)
      parts.push(line.slice(last, match.index))
      parts.push(
        <button
          key={`${i}:${link.label}`}
          type="button"
          onClick={() => onOpen(link.owner)}
          className="font-medium text-accent underline decoration-dotted underline-offset-2 hover:decoration-solid"
        >
          {link.label}
        </button>,
      )
      last = match.index! + match[0].length
    }
    if (!parts.length) return line
    parts.push(line.slice(last))
    return <Fragment key={i}>{parts}</Fragment>
  })
  return { lines: out, unmentioned: links.filter((l) => !linked.has(l.label)) }
}

function reasonLines(perWrite: GitOpsWriteAssessment[], levels: GitOpsWriteGuard['level'][]): string[] {
  const relevant = perWrite.filter((entry) => levels.includes(entry.level) && entry.reason)
  const distinct = new Set(relevant.map((entry) => entry.reason))
  if (distinct.size <= 1) return [...distinct]
  return relevant.map((entry) =>
    entry.write.description ? `${entry.write.description}: ${entry.reason}` : entry.reason,
  )
}

export function GitOpsWriteWarning({
  guard,
  acknowledged,
  onAcknowledgedChange,
  onOpenOwner,
  onOpenManager,
  disabled = false,
  // Not undefined: AlertBanner would add its own mb-4, and a margin utility
  // overrides the parent's space-y gap.
  className = '',
}: GitOpsWriteWarningProps) {
  if (guard.pending) {
    return (
      <div
        className={clsx(
          'flex items-center gap-2 rounded-lg border border-theme-border bg-theme-base px-3 py-2 text-xs text-theme-text-secondary',
          className,
        )}
      >
        <Loader2 className="h-3.5 w-3.5 animate-spin" />
        Checking GitOps ownership…
      </div>
    )
  }
  if (guard.level === 'none') return null

  const headline = gitOpsWriteHeadline(guard)
  const links = managerLinks(guard, onOpenManager)
  const { lines, unmentioned } = linkManagers(
    reasonLines(guard.perWrite, guard.level === 'info' ? ['info'] : ['will-revert', 'may-revert']),
    links,
    (owner) => onOpenManager?.(owner),
  )
  // Several managers link where the reason names them; one it doesn't name
  // still gets a link of its own.
  const ownerLink = links.length ? (
    unmentioned.length ? (
      <div className="flex flex-wrap gap-x-3 gap-y-1">
        {unmentioned.map(({ label, owner }) => (
          <button
            key={label}
            type="button"
            onClick={() => onOpenManager!(owner)}
            className="inline-flex items-center gap-1 text-xs font-medium text-accent hover:underline"
          >
            View {label}
            <ExternalLink className="h-3 w-3" />
          </button>
        ))}
      </div>
    ) : null
  ) : onOpenOwner ? (
    <button
      type="button"
      onClick={onOpenOwner}
      className="inline-flex items-center gap-1 text-xs font-medium text-accent hover:underline"
    >
      {guard.owner ? OWNER_LINK_LABEL[guard.owner.kind] : 'View Helm release'}
      <ExternalLink className="h-3 w-3" />
    </button>
  ) : null

  if (guard.level === 'info') {
    return (
      <div
        className={clsx(
          'flex items-start gap-2 rounded-lg border border-theme-border bg-theme-base px-3 py-2 text-xs text-theme-text-secondary',
          className,
        )}
      >
        <Info className="mt-0.5 h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
        <div className="min-w-0 space-y-1">
          <p>
            <span className="font-medium text-theme-text-primary">{headline}</span>
            {lines.map((line, i) => <Fragment key={i}> {line}</Fragment>)}
          </p>
          {ownerLink}
        </div>
      </div>
    )
  }

  const tool = guardToolLabel(guard)
  const durableFix = guard.owner
    ? 'For a lasting change, change it in the GitOps source.'
    : guard.helmRelease
      ? 'For a lasting change, change the release’s chart values.'
      : null
  return (
    <AlertBanner
      variant="warning"
      icon={GitBranch}
      title={headline}
      className={className}
      message={
        lines.length === 1 ? (
          <>
            {lines[0]}
            {durableFix ? ` ${durableFix}` : ''}
          </>
        ) : durableFix ?? undefined
      }
      items={lines.length > 1 ? lines : undefined}
    >
      {ownerLink && <div className="mt-2">{ownerLink}</div>}
      <label className="mt-2 flex cursor-pointer items-start gap-2 text-xs text-theme-text-primary">
        <input
          type="checkbox"
          checked={acknowledged}
          onChange={(event) => onAcknowledgedChange(event.target.checked)}
          disabled={disabled}
          className="mt-0.5 h-3.5 w-3.5 accent-skyhook-500"
        />
        <span>
          I understand {tool} {guard.level === 'will-revert' ? 'will' : 'may'} revert this.
        </span>
      </label>
    </AlertBanner>
  )
}
