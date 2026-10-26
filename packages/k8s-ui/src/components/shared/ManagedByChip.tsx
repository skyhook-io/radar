import { useEffect, useRef, useState } from 'react'
import { GitBranch, Package } from 'lucide-react'
import { clsx } from 'clsx'
import { gitOpsOwnerFromRef, managerCandidates, managerChipPresentation, managerChoices, managerRefLabel, type GitOpsOwnerRef, type ManagerChoice, type ManagerListing } from '../../utils/gitops-owner'
import { useAnimatedUnmount } from '../../hooks/useAnimatedUnmount'
import { TRANSITION_MENU, overlayExitMs, overlayTransitionStyle } from '../../utils/animation'
import type { ManagerAnswer } from '../../types/core'
import type { GitOpsStatus } from '../../types/gitops'
import { StatusDot, type StatusTone } from '../ui/status-tone'
import { Tooltip } from '../ui/Tooltip'

export type HelmOwnerRef = {
  namespace: string
  name: string
}

// ManagedByChip renders the "Managed by <ArgoCD/FluxCD app>" affordance for
// resources detected (via labels/annotations) to be GitOps-managed. `listing`
// compares that claim with the managers whose status lists the resource: a
// manager that doesn't list it while another does is shown as "Tracked by"
// with a warning, since the claim is all that names it. The chip
// is clickable when the host wires `onOpen`; integrators that don't surface
// a GitOps tab can omit the handler and the chip degrades to a passive badge
// so the relationship is still visible.
//
// Variant:
//   - inline (default): compact pill suitable for header rows and resource list rows
//   - block: starts a new line with mt-1 spacing, used in WorkloadView title strip
export function ManagedByChip({
  owner,
  manager,
  status,
  onOpen,
  verified = true,
  pending = false,
  source,
  listing,
  variant = 'inline',
}: {
  owner?: GitOpsOwnerRef | null
  manager?: ManagerAnswer
  status?: GitOpsStatus | null
  onOpen?: (ref: GitOpsOwnerRef) => void
  verified?: boolean
  pending?: boolean
  source?: string | null
  listing?: ManagerListing
  variant?: 'inline' | 'block'
}) {
  if (manager) return <ManagerAnswerChip manager={manager} status={status} onOpen={onOpen} variant={variant} />
  if (!owner) return null
  const toolLabel = owner.tool === 'argocd' ? 'ArgoCD' : 'FluxCD'
  const ownerKindLabel = gitOpsOwnerKindLabel(owner)
  const label = owner.namespace ? `${owner.namespace}/${owner.name}` : owner.name
  const statusLabel = status ? gitOpsStatusLabel(status) : null
  const conflict = listing?.state === 'conflict' ? listing.listedBy : null
  const prefix = pending ? 'Resolving' : verified && !conflict ? 'Managed by' : 'Tracked by'
  const tooltipContent = (
    <span className="flex flex-col gap-1">
      <span>
        {pending ? (
          <>
            Looking up matching {ownerKindLabel} <TooltipCode>{label}</TooltipCode> in this cluster.
          </>
        ) : conflict ? (
          <>
            This resource's {toolLabel} tracking metadata names <TooltipCode>{label}</TooltipCode>, but that {ownerKindLabel}'s status doesn't list it.{' '}
            {conflict.length === 1 ? 'The status of ' : 'The statuses of '}
            {conflict.map((m, i) => (
              <span key={`${m.namespace}/${m.name}`}>
                {i > 0 && ', '}
                <TooltipCode>{m.namespace ? `${m.namespace}/${m.name}` : m.name}</TooltipCode>
              </span>
            ))}{' '}
            {conflict.length === 1 ? 'does.' : 'do.'}
          </>
        ) : verified ? (
          <>
            Managed by {toolLabel} · <TooltipCode>{label}</TooltipCode>
            {statusLabel && <> · {statusLabel}{status?.message ? `: ${status.message}` : ''}</>}
            {listing?.state === 'confirmed' && <>. Its status lists this resource too.</>}
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
        {conflict && <StatusDot tone="degraded" size="xs" />}
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

function ManagerAnswerChip({ manager, status, onOpen, variant }: {
  manager: ManagerAnswer
  status?: GitOpsStatus | null
  onOpen?: (ref: GitOpsOwnerRef) => void
  variant: 'inline' | 'block'
}) {
  const [open, setOpen] = useState(false)
  const { shouldRender, isOpen } = useAnimatedUnmount(open, overlayExitMs('menu'))
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    // Consumed, so the Escape that closes the menu doesn't also close the drawer.
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.preventDefault()
      e.stopPropagation()
      setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey, true)
    }
  }, [open])

  const presentation = managerChipPresentation(manager)
  if (!presentation) return null
  // Several candidates open a menu naming each, so none is reached only
  // through the one shown first; one opens directly.
  // Every candidate is named; only one Radar found links. A label that names
  // no namespace stays unlinked: Radar doesn't guess it.
  const choices = managerChoices(manager).map(c => {
    const owner = gitOpsOwnerFromRef(c.ref)
    return { ...c, owner: c.found && owner?.namespace ? owner : null }
  })
  const several = managerCandidates(manager).length > 1
  const lead = manager.lead ? gitOpsOwnerFromRef(manager.lead) : null
  const direct = !several ? choices.find(c => c.owner && lead && c.owner.name === lead.name && c.owner.namespace === lead.namespace)?.owner ?? null : null
  const menu = Boolean(onOpen) && several && choices.some(c => c.owner)
  // The manager's own status, shown only when it is confirmed to manage this
  // resource: beside a list alone, Synced reads as this resource's state.
  const statusLabel = lead && status && !several && manager.state === 'agree' ? gitOpsStatusLabel(status) : null
  const chipClass = clsx(
    'inline-flex min-w-0 items-center gap-1 rounded border border-theme-border bg-theme-elevated px-1.5 py-0.5 text-[11px] text-theme-text-secondary',
    (menu || direct && onOpen) && 'hover:border-accent hover:text-accent transition-colors',
  )
  const content = (
    <>
      <GitBranch className="h-3 w-3 shrink-0" />
      {presentation.warning && <StatusDot tone="degraded" size="xs" />}
      <span className="shrink-0 text-theme-text-tertiary">{presentation.prefix}</span>
      <span className="truncate max-w-[180px]">{presentation.label}</span>
      {presentation.via && <span className="shrink-0 text-theme-text-tertiary">via {presentation.via}</span>}
      {statusLabel && <><span className="mx-0.5 text-theme-text-disabled">·</span><StatusDot tone={gitOpsStatusTone(status!)} size="xs" /><span>{statusLabel}</span></>}
    </>
  )
  const tooltip = `${presentation.tooltip}${statusLabel ? ` ${statusLabel}${status?.message ? `: ${status.message}` : ''}.` : ''}`
  const chip = menu ? (
    <button type="button" aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen(v => !v)} className={chipClass}>{content}</button>
  ) : direct && onOpen ? (
    <button type="button" onClick={() => onOpen(direct)} className={chipClass}>{content}</button>
  ) : (
    // Focusable, so keyboard users reach the tooltip that explains it.
    <span tabIndex={0} className={chipClass}>{content}</span>
  )
  return (
    <div ref={ref} className={clsx('relative inline-flex min-w-0', variant === 'block' && 'mt-1')}>
      <Tooltip content={tooltip} delay={500} position="bottom">{chip}</Tooltip>
      {menu && shouldRender && (
        <div
          role="menu"
          inert={!open}
          className={clsx(
            'absolute left-0 top-full z-50 mt-1 min-w-[220px] origin-top-left rounded-lg border border-theme-border bg-theme-surface py-1 shadow-theme-lg',
            TRANSITION_MENU,
            isOpen ? 'opacity-100 translate-y-0 scale-100' : 'opacity-0 -translate-y-1 scale-[0.97]',
            !open && 'pointer-events-none',
          )}
          style={overlayTransitionStyle(isOpen, 'menu')}
        >
          {choices.map(c => (
            <button
              key={`${c.ref.group}/${c.ref.kind}/${c.ref.namespace}/${c.ref.name}`}
              type="button"
              role="menuitem"
              disabled={!c.owner}
              onClick={() => {
                if (!c.owner) return
                setOpen(false)
                onOpen?.(c.owner)
              }}
              className={clsx(
                'flex w-full flex-col items-start px-3 py-1.5 text-left text-xs transition-colors',
                c.owner ? 'text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary' : 'cursor-default text-theme-text-tertiary',
              )}
            >
              <span className="truncate">{managerRefLabel(c.ref)}</span>
              <span className="text-[11px] text-theme-text-tertiary">{choiceRole(c)}{c.owner ? '' : !c.ref.namespace ? " · its label doesn't name its namespace" : !c.found ? ' · Radar found no such manager you can view' : ''}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

function choiceRole(c: Pick<ManagerChoice, 'listed' | 'claimed' | 'found'>): string {
  if (c.listed && c.claimed) return 'Its status lists this, and the metadata names it'
  if (c.listed) return 'Its status lists this'
  return 'This metadata names it'
}

export function HelmManagedByChip({
  owner,
  manager,
  onOpen,
  source,
  variant = 'inline',
}: {
  owner: HelmOwnerRef
  manager?: ManagerAnswer
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
      {manager && <span>{managerChipPresentation(manager)?.tooltip}</span>}
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

export function gitOpsOwnerKindLabel(owner: GitOpsOwnerRef): string {
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
