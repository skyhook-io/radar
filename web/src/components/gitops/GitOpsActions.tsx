import { RefreshCw, Pause, Play, Loader2 } from 'lucide-react'
import { clsx } from 'clsx'

interface GitOpsActionsProps {
  /** The GitOps tool type */
  tool: 'flux' | 'argo'
  /** Whether the resource is currently suspended */
  suspended: boolean
  /** Sync/Reconcile handler */
  onSync?: () => void
  /** Suspend handler */
  onSuspend?: () => void
  /** Resume handler */
  onResume?: () => void
  /** Whether a sync operation is in progress */
  isSyncing?: boolean
  /** Whether a suspend/resume operation is in progress */
  isSuspending?: boolean
  /** Layout direction */
  direction?: 'row' | 'column'
  /** Button size */
  size?: 'sm' | 'md'
}

/**
 * Action buttons for GitOps resources (Sync, Suspend, Resume)
 */
export function GitOpsActions({
  tool,
  suspended,
  onSync,
  onSuspend,
  onResume,
  isSyncing,
  isSuspending,
  direction = 'row',
  size = 'md',
}: GitOpsActionsProps) {
  const syncLabel = tool === 'flux' ? 'Reconcile' : 'Sync'
  const buttonClass = size === 'sm'
    ? 'px-2 py-1 text-xs gap-1'
    : 'px-3 py-1.5 text-sm gap-1.5'
  const iconClass = size === 'sm' ? 'w-3 h-3' : 'w-3.5 h-3.5'

  return (
    <div className={clsx('flex gap-2', direction === 'column' ? 'flex-col' : 'flex-row')}>
      {/* Sync/Reconcile button */}
      {onSync && (
        <button
          onClick={onSync}
          disabled={isSyncing || suspended}
          className={clsx(
            'flex items-center rounded font-medium transition-colors',
            buttonClass,
            isSyncing
              ? 'bg-blue-500/20 text-blue-400 cursor-wait'
              : suspended
              ? 'bg-theme-elevated text-theme-text-tertiary cursor-not-allowed'
              : 'bg-blue-500/20 text-blue-400 hover:bg-blue-500/30'
          )}
          title={suspended ? 'Cannot sync while suspended' : syncLabel}
        >
          {isSyncing ? (
            <Loader2 className={clsx(iconClass, 'animate-spin')} />
          ) : (
            <RefreshCw className={iconClass} />
          )}
          {syncLabel}
        </button>
      )}

      {/* Suspend/Resume button */}
      {suspended ? (
        onResume && (
          <button
            onClick={onResume}
            disabled={isSuspending}
            className={clsx(
              'flex items-center rounded font-medium transition-colors',
              buttonClass,
              isSuspending
                ? 'bg-green-500/20 text-green-400 cursor-wait'
                : 'bg-green-500/20 text-green-400 hover:bg-green-500/30'
            )}
          >
            {isSuspending ? (
              <Loader2 className={clsx(iconClass, 'animate-spin')} />
            ) : (
              <Play className={iconClass} />
            )}
            Resume
          </button>
        )
      ) : (
        onSuspend && (
          <button
            onClick={onSuspend}
            disabled={isSuspending}
            className={clsx(
              'flex items-center rounded font-medium transition-colors',
              buttonClass,
              isSuspending
                ? 'bg-yellow-500/20 text-yellow-400 cursor-wait'
                : 'bg-yellow-500/20 text-yellow-400 hover:bg-yellow-500/30'
            )}
          >
            {isSuspending ? (
              <Loader2 className={clsx(iconClass, 'animate-spin')} />
            ) : (
              <Pause className={iconClass} />
            )}
            Suspend
          </button>
        )
      )}
    </div>
  )
}

/**
 * Minimal sync-only button for compact UIs
 */
export function SyncButton({
  onClick,
  loading,
  disabled,
  label = 'Sync',
  size = 'md',
}: {
  onClick: () => void
  loading?: boolean
  disabled?: boolean
  label?: string
  size?: 'sm' | 'md'
}) {
  const buttonClass = size === 'sm'
    ? 'px-2 py-1 text-xs gap-1'
    : 'px-3 py-1.5 text-sm gap-1.5'
  const iconClass = size === 'sm' ? 'w-3 h-3' : 'w-3.5 h-3.5'

  return (
    <button
      onClick={onClick}
      disabled={loading || disabled}
      className={clsx(
        'flex items-center rounded font-medium transition-colors',
        buttonClass,
        loading || disabled
          ? 'bg-theme-elevated text-theme-text-tertiary cursor-not-allowed'
          : 'bg-blue-500/20 text-blue-400 hover:bg-blue-500/30'
      )}
    >
      {loading ? (
        <Loader2 className={clsx(iconClass, 'animate-spin')} />
      ) : (
        <RefreshCw className={iconClass} />
      )}
      {label}
    </button>
  )
}

/**
 * Suspend/Resume toggle button
 */
export function SuspendToggle({
  suspended,
  onSuspend,
  onResume,
  loading,
  size = 'md',
}: {
  suspended: boolean
  onSuspend: () => void
  onResume: () => void
  loading?: boolean
  size?: 'sm' | 'md'
}) {
  const buttonClass = size === 'sm'
    ? 'px-2 py-1 text-xs gap-1'
    : 'px-3 py-1.5 text-sm gap-1.5'
  const iconClass = size === 'sm' ? 'w-3 h-3' : 'w-3.5 h-3.5'

  if (suspended) {
    return (
      <button
        onClick={onResume}
        disabled={loading}
        className={clsx(
          'flex items-center rounded font-medium transition-colors',
          buttonClass,
          loading
            ? 'bg-green-500/20 text-green-400 cursor-wait'
            : 'bg-green-500/20 text-green-400 hover:bg-green-500/30'
        )}
      >
        {loading ? (
          <Loader2 className={clsx(iconClass, 'animate-spin')} />
        ) : (
          <Play className={iconClass} />
        )}
        Resume
      </button>
    )
  }

  return (
    <button
      onClick={onSuspend}
      disabled={loading}
      className={clsx(
        'flex items-center rounded font-medium transition-colors',
        buttonClass,
        loading
          ? 'bg-yellow-500/20 text-yellow-400 cursor-wait'
          : 'bg-yellow-500/20 text-yellow-400 hover:bg-yellow-500/30'
      )}
    >
      {loading ? (
        <Loader2 className={clsx(iconClass, 'animate-spin')} />
      ) : (
        <Pause className={iconClass} />
      )}
      Suspend
    </button>
  )
}
