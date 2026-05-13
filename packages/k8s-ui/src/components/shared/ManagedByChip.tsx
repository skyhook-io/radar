import { GitBranch } from 'lucide-react'
import { clsx } from 'clsx'
import type { GitOpsOwnerRef } from '../../utils/gitops-owner'

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
  onOpen,
  variant = 'inline',
}: {
  owner: GitOpsOwnerRef
  onOpen?: (ref: GitOpsOwnerRef) => void
  variant?: 'inline' | 'block'
}) {
  const toolLabel = owner.tool === 'argocd' ? 'ArgoCD' : 'FluxCD'
  const label = owner.namespace ? `${owner.namespace}/${owner.name}` : owner.name
  const title = `Managed by ${toolLabel} · ${label}`
  const interactive = !!onOpen
  const Wrapper = interactive ? 'button' : 'span'
  return (
    <Wrapper
      {...(interactive
        ? { type: 'button' as const, onClick: () => onOpen?.(owner) }
        : {})}
      title={title}
      className={clsx(
        'inline-flex items-center gap-1 rounded border border-theme-border bg-theme-elevated px-1.5 py-0.5 text-[11px] text-theme-text-secondary',
        variant === 'block' && 'mt-1',
        interactive && 'hover:border-skyhook-500/60 hover:text-skyhook-500 transition-colors',
      )}
    >
      <GitBranch className="h-3 w-3 shrink-0" />
      <span className="shrink-0 text-theme-text-tertiary">Managed by</span>
      <span className="truncate max-w-[180px]">{label}</span>
    </Wrapper>
  )
}
