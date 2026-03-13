import { clsx } from 'clsx'
import { CheckCircle2, AlertCircle, Loader2, Pause, HelpCircle, XCircle } from 'lucide-react'
import type { GitOpsStatus, SyncStatus, GitOpsHealthStatus } from '../../types/gitops'
import { SEVERITY_BADGE_BORDERED, SEVERITY_BADGE } from '../../utils/badge-colors'

interface GitOpsStatusBadgeProps {
  status: GitOpsStatus
  showHealth?: boolean
  compact?: boolean
}

/**
 * Unified status badge for GitOps resources (FluxCD and ArgoCD)
 * Shows sync status with optional health indicator
 */
export function GitOpsStatusBadge({ status, showHealth = true, compact = false }: GitOpsStatusBadgeProps) {
  const Icon = getStatusIcon(status)
  const colorClass = getStatusColorClass(status)
  const label = getStatusLabel(status)

  if (compact) {
    return (
      <span
        className={clsx('px-2 py-0.5 rounded text-xs font-medium inline-flex items-center gap-1', colorClass)}
        title={status.message}
      >
        <Icon className="w-3 h-3" />
        {label}
      </span>
    )
  }

  // Don't show health indicator if the main badge already shows health status
  // (i.e., when the main label is 'Degraded', 'Progressing', or 'Suspended')
  const mainLabelShowsHealth = label === 'Degraded' || label === 'Progressing' || label === 'Suspended'

  return (
    <div className="flex items-center gap-2">
      <span
        className={clsx('px-2 py-0.5 rounded text-xs font-medium inline-flex items-center gap-1', colorClass)}
        title={status.message}
      >
        <Icon className="w-3.5 h-3.5" />
        {label}
      </span>
      {showHealth && !mainLabelShowsHealth && status.health !== 'Unknown' && status.health !== getHealthFromSync(status.sync) && (
        <HealthIndicator health={status.health} />
      )}
    </div>
  )
}

function getHealthFromSync(sync: SyncStatus): GitOpsHealthStatus {
  if (sync === 'Synced') return 'Healthy'
  if (sync === 'Reconciling') return 'Progressing'
  return 'Unknown'
}

function getStatusIcon(status: GitOpsStatus) {
  if (status.suspended) return Pause
  if (status.sync === 'Reconciling') return Loader2
  if (status.sync === 'Synced' && status.health === 'Healthy') return CheckCircle2
  if (status.health === 'Degraded') return XCircle
  if (status.sync === 'OutOfSync') return AlertCircle
  if (status.health === 'Progressing') return Loader2
  return HelpCircle
}

function getStatusColorClass(status: GitOpsStatus): string {
  if (status.suspended) return SEVERITY_BADGE_BORDERED.warning
  if (status.sync === 'Synced' && status.health === 'Healthy') return SEVERITY_BADGE_BORDERED.success
  if (status.health === 'Degraded') return SEVERITY_BADGE_BORDERED.error
  if (status.sync === 'OutOfSync') return SEVERITY_BADGE_BORDERED.warning
  if (status.sync === 'Reconciling' || status.health === 'Progressing') return SEVERITY_BADGE_BORDERED.info
  return SEVERITY_BADGE_BORDERED.neutral
}

function getStatusLabel(status: GitOpsStatus): string {
  if (status.suspended) return 'Suspended'
  if (status.sync === 'Reconciling') return 'Syncing'
  if (status.sync === 'Synced' && status.health === 'Healthy') return 'Synced'
  if (status.health === 'Degraded') return 'Degraded'
  if (status.sync === 'OutOfSync') return 'OutOfSync'
  if (status.health === 'Progressing') return 'Progressing'
  return 'Unknown'
}

interface HealthIndicatorProps {
  health: GitOpsHealthStatus
}

function HealthIndicator({ health }: HealthIndicatorProps) {
  const { icon: Icon, color, label } = getHealthInfo(health)

  return (
    <span
      className={clsx('px-1.5 py-0.5 rounded text-xs inline-flex items-center gap-1', color)}
      title={`Health: ${label}`}
    >
      <Icon className="w-3 h-3" />
      {label}
    </span>
  )
}

function getHealthInfo(health: GitOpsHealthStatus) {
  switch (health) {
    case 'Healthy':
      return { icon: CheckCircle2, color: SEVERITY_BADGE.success, label: 'Healthy' }
    case 'Progressing':
      return { icon: Loader2, color: SEVERITY_BADGE.info, label: 'Progressing' }
    case 'Degraded':
      return { icon: XCircle, color: SEVERITY_BADGE.error, label: 'Degraded' }
    case 'Suspended':
      return { icon: Pause, color: SEVERITY_BADGE.warning, label: 'Suspended' }
    case 'Missing':
      return { icon: AlertCircle, color: SEVERITY_BADGE.warning, label: 'Missing' }
    default:
      return { icon: HelpCircle, color: SEVERITY_BADGE.neutral, label: 'Unknown' }
  }
}

/**
 * Simple sync status badge without health indicator
 */
export function SyncStatusBadge({ sync, suspended }: { sync: SyncStatus; suspended?: boolean }) {
  if (suspended) {
    return (
      <span className={clsx('px-2 py-0.5 rounded text-xs font-medium inline-flex items-center gap-1', SEVERITY_BADGE_BORDERED.warning)}>
        <Pause className="w-3 h-3" />
        Suspended
      </span>
    )
  }

  const config = getSyncConfig(sync)
  const Icon = config.icon

  return (
    <span className={clsx('px-2 py-0.5 rounded text-xs font-medium inline-flex items-center gap-1', config.color)}>
      <Icon className={clsx('w-3 h-3', sync === 'Reconciling' && 'animate-spin')} />
      {config.label}
    </span>
  )
}

function getSyncConfig(sync: SyncStatus) {
  switch (sync) {
    case 'Synced':
      return { icon: CheckCircle2, color: SEVERITY_BADGE_BORDERED.success, label: 'Synced' }
    case 'OutOfSync':
      return { icon: AlertCircle, color: SEVERITY_BADGE_BORDERED.warning, label: 'OutOfSync' }
    case 'Reconciling':
      return { icon: Loader2, color: SEVERITY_BADGE_BORDERED.info, label: 'Syncing' }
    default:
      return { icon: HelpCircle, color: SEVERITY_BADGE_BORDERED.neutral, label: 'Unknown' }
  }
}

/**
 * Simple health status badge
 */
export function HealthStatusBadge({ health }: { health: GitOpsHealthStatus }) {
  const { icon: Icon, color, label } = getHealthInfo(health)

  return (
    <span className={clsx('px-2 py-0.5 rounded text-xs font-medium inline-flex items-center gap-1', color)}>
      <Icon className={clsx('w-3 h-3', health === 'Progressing' && 'animate-spin')} />
      {label}
    </span>
  )
}
