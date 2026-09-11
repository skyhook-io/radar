import { useMemo } from 'react'
import { Loader2, ArrowRight } from 'lucide-react'
import { StatusDot } from '@skyhook-io/k8s-ui'
import type { SyncStatusSnapshot } from '../api/client'

// Shown in place of views that need the full cluster dataset while the
// initial informer sync is still running. Resource views for already-synced
// kinds work during this window — the per-kind list doubles as the map of
// what's usable right now.
export function SyncProgressPanel({
  syncStatus,
  onNavigateToKind,
}: {
  syncStatus: SyncStatusSnapshot
  onNavigateToKind: (key: string) => void
}) {
  const total = syncStatus.criticalTotal + syncStatus.deferredTotal
  const synced = syncStatus.criticalSynced + syncStatus.deferredSynced
  const kinds = useMemo(
    () => [...syncStatus.kinds].sort((a, b) => {
      // Ready first, failed last, both alphabetical within their group.
      const rank = (k: typeof a) => (k.synced ? 0 : k.failed ? 2 : 1)
      if (rank(a) !== rank(b)) return rank(a) - rank(b)
      return a.kind.localeCompare(b.kind)
    }),
    [syncStatus.kinds],
  )
  const failedCount = useMemo(
    () => syncStatus.kinds.filter((k) => k.failed).length,
    [syncStatus.kinds],
  )

  return (
    <div className="flex-1 flex items-center justify-center overflow-y-auto bg-theme-base">
      <div className="max-w-xl w-full px-8 py-10">
        <div className="flex items-center gap-3 mb-1">
          <Loader2 className="w-5 h-5 animate-spin text-theme-text-tertiary" />
          <h2 className="text-lg font-semibold text-theme-text-primary">Loading cluster data</h2>
        </div>
        <p className="text-sm text-theme-text-secondary mb-6">
          {synced} of {total} resource types ready — views open as their data finishes loading.
          This view needs the full dataset and will appear automatically.
          {failedCount > 0 && (
            <span className="block mt-1 text-red-400">
              {failedCount} resource {failedCount === 1 ? 'type' : 'types'} failed to load within the
              sync deadline — affected views will show an error. Retry the connection to try again.
            </span>
          )}
        </p>
        <div className="card-inner grid grid-cols-2 gap-x-6 gap-y-1.5 max-h-80 overflow-y-auto">
          {kinds.map((k) => (
            <div key={k.key} className="flex items-center gap-2 min-w-0 text-sm">
              <StatusDot tone={k.synced ? 'healthy' : k.failed ? 'unhealthy' : 'neutral'} size="sm" className="shrink-0" />
              {k.synced ? (
                <button
                  onClick={() => onNavigateToKind(k.key)}
                  className="group flex items-center gap-1 min-w-0 text-theme-text-primary hover:text-theme-brand transition-colors"
                >
                  <span className="truncate">{k.kind}</span>
                  <ArrowRight className="w-3 h-3 opacity-0 group-hover:opacity-100 transition-opacity shrink-0" />
                </button>
              ) : k.failed ? (
                <span className="truncate text-red-400" title="Failed to load within the sync deadline">
                  {k.kind}
                </span>
              ) : (
                <span className="truncate text-theme-text-tertiary animate-pulse">{k.kind}</span>
              )}
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
