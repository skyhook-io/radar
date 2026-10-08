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

export { canConfirmGitOpsWrite }

export interface GitOpsWriteWarningProps {
  guard: GitOpsWriteGuard
  acknowledged: boolean
  onAcknowledgedChange: (acknowledged: boolean) => void
  /** Opens the GitOps owner (or Helm release) the guard names. */
  onOpenOwner?: () => void
  disabled?: boolean
  className?: string
}

const OWNER_LINK_LABEL = {
  applications: 'View Argo CD Application',
  kustomizations: 'View Flux Kustomization',
  helmreleases: 'View Flux HelmRelease',
} as const

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
  const ownerLink = onOpenOwner ? (
    <button
      type="button"
      onClick={onOpenOwner}
      className="inline-flex items-center gap-1 text-xs font-medium text-skyhook-600 hover:underline dark:text-skyhook-400"
    >
      {guard.owner ? OWNER_LINK_LABEL[guard.owner.kind] : 'View Helm release'}
      <ExternalLink className="h-3 w-3" />
    </button>
  ) : null

  if (guard.level === 'info') {
    const lines = reasonLines(guard.perWrite, ['info'])
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
            <span className="font-medium text-theme-text-primary">{headline}</span> {lines.join(' ')}
          </p>
          {ownerLink}
        </div>
      </div>
    )
  }

  const tool = guardToolLabel(guard)
  const lines = reasonLines(guard.perWrite, ['will-revert', 'may-revert'])
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
        <span>I understand {tool} may revert this.</span>
      </label>
    </AlertBanner>
  )
}
