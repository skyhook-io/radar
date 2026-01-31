import { clsx } from 'clsx'
import { CheckCircle2, AlertCircle, Loader2, Pause, HelpCircle, XCircle } from 'lucide-react'
import type { GitOpsStatus, SyncStatus, GitOpsHealthStatus } from '../../types/gitops'

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
  if (status.suspended) {
    return 'bg-yellow-500/20 text-yellow-400 border border-yellow-500/30'
  }
  if (status.sync === 'Synced' && status.health === 'Healthy') {
    return 'bg-green-500/20 text-green-400 border border-green-500/30'
  }
  if (status.health === 'Degraded') {
    return 'bg-red-500/20 text-red-400 border border-red-500/30'
  }
  if (status.sync === 'OutOfSync') {
    return 'bg-orange-500/20 text-orange-400 border border-orange-500/30'
  }
  if (status.sync === 'Reconciling' || status.health === 'Progressing') {
    return 'bg-blue-500/20 text-blue-400 border border-blue-500/30'
  }
  return 'bg-gray-500/20 text-gray-400 border border-gray-500/30'
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
      return {
        icon: CheckCircle2,
        color: 'bg-green-500/10 text-green-400',
        label: 'Healthy',
      }
    case 'Progressing':
      return {
        icon: Loader2,
        color: 'bg-blue-500/10 text-blue-400',
        label: 'Progressing',
      }
    case 'Degraded':
      return {
        icon: XCircle,
        color: 'bg-red-500/10 text-red-400',
        label: 'Degraded',
      }
    case 'Suspended':
      return {
        icon: Pause,
        color: 'bg-yellow-500/10 text-yellow-400',
        label: 'Suspended',
      }
    case 'Missing':
      return {
        icon: AlertCircle,
        color: 'bg-orange-500/10 text-orange-400',
        label: 'Missing',
      }
    default:
      return {
        icon: HelpCircle,
        color: 'bg-gray-500/10 text-gray-400',
        label: 'Unknown',
      }
  }
}

/**
 * Simple sync status badge without health indicator
 */
export function SyncStatusBadge({ sync, suspended }: { sync: SyncStatus; suspended?: boolean }) {
  if (suspended) {
    return (
      <span className="px-2 py-0.5 rounded text-xs font-medium bg-yellow-500/20 text-yellow-400 border border-yellow-500/30 inline-flex items-center gap-1">
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
      return {
        icon: CheckCircle2,
        color: 'bg-green-500/20 text-green-400 border border-green-500/30',
        label: 'Synced',
      }
    case 'OutOfSync':
      return {
        icon: AlertCircle,
        color: 'bg-orange-500/20 text-orange-400 border border-orange-500/30',
        label: 'OutOfSync',
      }
    case 'Reconciling':
      return {
        icon: Loader2,
        color: 'bg-blue-500/20 text-blue-400 border border-blue-500/30',
        label: 'Syncing',
      }
    default:
      return {
        icon: HelpCircle,
        color: 'bg-gray-500/20 text-gray-400 border border-gray-500/30',
        label: 'Unknown',
      }
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
