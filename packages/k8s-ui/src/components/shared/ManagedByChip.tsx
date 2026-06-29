import { GitBranch, Package } from 'lucide-react'
import { clsx } from 'clsx'
import type { GitOpsOwnerRef } from '../../utils/gitops-owner'
import type { GitOpsStatus } from '../../types/gitops'
import { StatusDot, type StatusTone } from '../ui/status-tone'
import { Tooltip } from '../ui/Tooltip'

export type HelmOwnerRef = {
  namespace: string
  name: string
}

// ManagedByChip renders the "Managed by <ArgoCD/FluxCD app>" affordance for
// resources detected (via labels/annotations) to be GitOps-managed. The chip
// is clickable when the host wires `onOpen`; integrators that don't surface
// a GitOps tab can omit the handler and the chip degrades to a passive badge
// so the relationship is still visible.
//
// Variant:
//   - inline (default): compact pill suitable for header rows and resource list rows
//   - block: starts a new line with mt-1 spacing, used in WorkloadView title strip
export function ManagedByChip({
  owner,
  status,
  onOpen,
  verified = true,
  pending = false,
  source,
  variant = 'inline',
}: {
  owner: GitOpsOwnerRef
  status?: GitOpsStatus | null
  onOpen?: (ref: GitOpsOwnerRef) => void
  verified?: boolean
  pending?: boolean
  source?: string | null
  variant?: 'inline' | 'block'
}) {
  const toolLabel = owner.tool === 'argocd' ? 'ArgoCD' : 'FluxCD'
  const ownerKindLabel = gitOpsOwnerKindLabel(owner)
  const label = owner.namespace ? `${owner.namespace}/${owner.name}` : owner.name
  const statusLabel = status ? gitOpsStatusLabel(status) : null
  const prefix = pending ? 'Resolving' : verified ? 'Managed by' : 'Tracked by'
  const tooltipContent = (
    <span className="flex flex-col gap-1">
      <span>
        {pending ? (
          <>
            Looking up matching {ownerKindLabel} <TooltipCode>{label}</TooltipCode> in this cluster.
          </>
        ) : verified ? (
          <>
            Managed by {toolLabel} · <TooltipCode>{label}</TooltipCode>
            {statusLabel && <> · {statusLabel}{status?.message ? `: ${status.message}` : ''}</>}
          </>
        ) : (
          <>
            {toolLabel} tracking metadata references <TooltipCode>{label}</TooltipCode>, but the matching {ownerKindLabel} is not visible in this cluster.
          </>
        )}
      </span>
      {source && (
        <span>
          Inferred from <TooltipCode>{source}</TooltipCode>.
        </span>
      )}
    </span>
  )
  const interactive = !!onOpen
  const Wrapper = interactive ? 'button' : 'span'
  return (
    <Tooltip content={tooltipContent} delay={500} position="bottom" wrapperClassName={variant === 'block' ? 'mt-1' : undefined}>
      <Wrapper
        {...(interactive
          ? { type: 'button' as const, onClick: () => onOpen?.(owner) }
          : {})}
        className={clsx(
          'inline-flex min-w-0 items-center gap-1 rounded border border-theme-border bg-theme-elevated px-1.5 py-0.5 text-[11px] text-theme-text-secondary',
          interactive && 'hover:border-skyhook-500/60 hover:text-skyhook-500 transition-colors',
        )}
      >
        <GitBranch className="h-3 w-3 shrink-0" />
        <span className="shrink-0 text-theme-text-tertiary">{prefix}</span>
        <span className="truncate max-w-[180px]">{label}</span>
        {statusLabel && (
          <>
            <span className="mx-0.5 h-3 w-px shrink-0 bg-theme-border" aria-hidden />
            <span className="inline-flex shrink-0 items-center gap-1 text-theme-text-tertiary">
              <StatusDot tone={gitOpsStatusTone(status!)} size="xs" />
              <span>{statusLabel}</span>
            </span>
          </>
        )}
      </Wrapper>
    </Tooltip>
  )
}

export function HelmManagedByChip({
  owner,
  onOpen,
  source,
  variant = 'inline',
}: {
  owner: HelmOwnerRef
  onOpen?: (ref: HelmOwnerRef) => void
  source?: string | null
  variant?: 'inline' | 'block'
}) {
  const label = owner.namespace ? `${owner.namespace}/${owner.name}` : owner.name
  const tooltipContent = (
    <span className="flex flex-col gap-1">
      <span>
        Managed by Helm release <TooltipCode>{label}</TooltipCode>.
      </span>
      {source && (
        <span>
          Inferred from <TooltipCode>{source}</TooltipCode>.
        </span>
      )}
    </span>
  )
  const interactive = !!onOpen
  const Wrapper = interactive ? 'button' : 'span'
  return (
    <Tooltip content={tooltipContent} delay={500} position="bottom" wrapperClassName={variant === 'block' ? 'mt-1' : undefined}>
      <Wrapper
        {...(interactive
          ? { type: 'button' as const, onClick: () => onOpen?.(owner) }
          : {})}
        className={clsx(
          'inline-flex min-w-0 items-center gap-1 rounded border border-theme-border bg-theme-elevated px-1.5 py-0.5 text-[11px] text-theme-text-secondary',
          interactive && 'hover:border-skyhook-500/60 hover:text-skyhook-500 transition-colors',
        )}
      >
        <Package className="h-3 w-3 shrink-0" />
        <span className="shrink-0 text-theme-text-tertiary">Managed by Helm</span>
        <span className="truncate max-w-[180px]">{label}</span>
      </Wrapper>
    </Tooltip>
  )
}

function gitOpsOwnerKindLabel(owner: GitOpsOwnerRef): string {
  if (owner.tool === 'argocd') return 'Application'
  if (owner.kind === 'helmreleases') return 'HelmRelease'
  if (owner.kind === 'kustomizations') return 'Kustomization'
  return 'GitOps resource'
}

function TooltipCode({ children }: { children: string }) {
  return (
    <code className="inline-code break-all">
      {children}
    </code>
  )
}

function gitOpsStatusLabel(status: GitOpsStatus): string {
  if (status.suspended) return 'Suspended'
  if (status.sync === 'Reconciling') return 'Syncing'
  if (status.health === 'Degraded') return 'Degraded'
  if (status.health === 'Missing') return 'Missing'
  if (status.sync === 'OutOfSync') return 'OutOfSync'
  if (status.health === 'Progressing') return 'Progressing'
  if (status.sync === 'Synced' && status.health === 'Healthy') return 'Synced'
  if (status.sync !== 'Unknown') return status.sync
  return status.health
}

function gitOpsStatusTone(status: GitOpsStatus): StatusTone {
  if (status.suspended) return 'degraded'
  if (status.sync === 'Synced' && status.health === 'Healthy') return 'healthy'
  if (status.health === 'Degraded' || status.health === 'Missing') return 'unhealthy'
  if (status.sync === 'OutOfSync') return 'degraded'
  if (status.sync === 'Reconciling' || status.health === 'Progressing') return 'neutral'
  return 'unknown'
}
