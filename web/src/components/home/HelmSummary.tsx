import type { DashboardHelmSummary } from '../../api/client'
import { Package, ArrowRight, Shield } from 'lucide-react'
import { clsx } from 'clsx'
import { Tooltip } from '../ui/Tooltip'

interface HelmSummaryProps {
  data?: DashboardHelmSummary
  onNavigate: () => void
}

function getStatusBadgeClass(status: string): string {
  switch (status.toLowerCase()) {
    case 'deployed':
      return 'bg-green-500/10 text-green-500'
    case 'failed':
      return 'bg-red-500/10 text-red-500'
    case 'pending-install':
    case 'pending-upgrade':
    case 'pending-rollback':
      return 'bg-yellow-500/10 text-yellow-500'
    case 'superseded':
      return 'bg-theme-elevated text-theme-text-tertiary'
    default:
      return 'bg-theme-elevated text-theme-text-secondary'
  }
}

function getHealthDot(health?: string): string {
  switch (health) {
    case 'healthy':
      return 'bg-green-500'
    case 'degraded':
      return 'bg-yellow-500'
    case 'unhealthy':
      return 'bg-red-500'
    default:
      return 'bg-theme-text-tertiary'
  }
}

export function HelmSummary({ data, onNavigate }: HelmSummaryProps) {
  return (
    <button
      onClick={onNavigate}
      className="group h-[260px] rounded-lg border-[3px] border-blue-500/30 bg-theme-surface/50 hover:-translate-y-1 hover:shadow-[0_12px_24px_rgba(0,0,0,0.12)] hover:border-blue-500/60 transition-all duration-200 text-left cursor-pointer"
    >
      <div className="flex flex-col h-full w-full">
      <div className="flex items-center justify-between px-4 py-2 border-b border-theme-border">
        <div className="flex items-center gap-2">
          <Package className="w-4 h-4 text-blue-500" />
          <span className="text-sm font-semibold text-blue-500">Helm Releases</span>
          {data && data.total > 0 && (
            <span className="text-[11px] bg-blue-500/10 px-1.5 py-0.5 rounded text-blue-500">
              {data.total}
            </span>
          )}
        </div>
      </div>

      <div className="flex-1 min-h-0">
        {!data ? (
          <div className="divide-y divide-theme-border">
            {[...Array(3)].map((_, i) => (
              <div key={i} className="flex items-center justify-between px-3 py-1.5">
                <div className="flex items-center gap-2 min-w-0">
                  <span className="w-1.5 h-1.5 rounded-full shrink-0 bg-theme-text-tertiary/30 animate-pulse" />
                  <span className="h-3 w-24 rounded bg-theme-text-tertiary/20 animate-pulse" />
                  <span className="h-3 w-14 rounded bg-theme-text-tertiary/10 animate-pulse" />
                </div>
                <div className="flex items-center gap-1.5 ml-2">
                  <span className="h-3 w-20 rounded bg-theme-text-tertiary/10 animate-pulse hidden sm:inline-block" />
                  <span className="h-4 w-14 rounded bg-theme-text-tertiary/15 animate-pulse" />
                </div>
              </div>
            ))}
          </div>
        ) : data.restricted ? (
          <div className="flex flex-col items-center justify-center h-full py-4 text-theme-text-tertiary">
            <Shield className="w-8 h-8 text-amber-400 mb-2" />
            <span className="text-xs font-medium text-theme-text-secondary">Access Restricted</span>
            <span className="text-[11px] mt-1">Insufficient permissions to list Helm releases</span>
          </div>
        ) : data.releases.length === 0 ? (
          <div className="flex items-center justify-center h-full py-4 text-xs text-theme-text-tertiary">
            No Helm releases found
          </div>
        ) : (
          <div className="divide-y divide-theme-border">
            {data.releases.map((release) => (
              <div
                key={`${release.namespace}/${release.name}`}
                className="flex items-center justify-between px-3 py-1.5"
              >
                <div className="flex items-center gap-2 min-w-0">
                  <span className={clsx('w-1.5 h-1.5 rounded-full shrink-0', getHealthDot(release.resourceHealth))} />
                  <span className="text-xs text-theme-text-primary truncate">{release.name}</span>
                  <span className="text-[10px] text-theme-text-tertiary">{release.namespace}</span>
                </div>
                <div className="flex items-center gap-1.5 ml-2 min-w-0">
                  <Tooltip content={`${release.chart} ${release.chartVersion}`} delay={100}>
                    <span className="text-[10px] text-theme-text-tertiary hidden sm:inline truncate max-w-[150px]">
                      {release.chart} {release.chartVersion}
                    </span>
                  </Tooltip>
                  <span className={clsx('text-[10px] px-1 py-0.5 rounded shrink-0', getStatusBadgeClass(release.status))}>
                    {release.status}
                  </span>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="px-4 py-1.5 border-t border-theme-border flex items-center justify-between">
        <span className="text-[10px] text-theme-text-tertiary">
          {data && data.total > data.releases.length ? `+${data.total - data.releases.length} more` : ''}
        </span>
        <span className="flex items-center gap-1.5 text-xs font-medium text-blue-500 group-hover:text-blue-400 transition-colors">
          Open Helm
          <ArrowRight className="w-3.5 h-3.5 transition-transform group-hover:translate-x-0.5" />
        </span>
      </div>
      </div>
    </button>
  )
}
