import { Info } from 'lucide-react'
import { Tooltip } from '../../ui/Tooltip'

interface MetricsUnavailableNoticeProps {
  rawError?: string
}

export function MetricsUnavailableNotice({ rawError }: MetricsUnavailableNoticeProps) {
  return (
    <div className="card-inner-lg text-xs text-theme-text-tertiary">
      <div className="flex items-start gap-1.5">
        <span className="min-w-0">
          Metrics unavailable. Radar could not read metrics.k8s.io. Install or repair metrics-server and its APIService to show live CPU and memory usage.
        </span>
        {rawError && (
          <Tooltip
            content={(
              <span className="space-y-1">
                <span className="block">
                  This state only means Radar could not read Kubernetes metrics.k8s.io. Check the v1beta1.metrics.k8s.io APIService and metrics-server health.
                </span>
                <span className="block">
                  On EKS or self-managed clusters, metrics-server may need to be installed. On managed platforms where it is bundled, check that it was not disabled or unhealthy.
                </span>
                <span className="block">
                  Prometheus is a separate metrics source; its availability does not make metrics.k8s.io available.
                </span>
                <span className="block">
                  Raw metrics error: <span className="font-mono break-all">{rawError}</span>
                </span>
              </span>
            )}
            delay={150}
            position="left"
          >
            <button
              type="button"
              className="mt-0.5 inline-flex shrink-0 cursor-help items-center gap-1 text-[11px] font-medium text-theme-text-tertiary hover:text-theme-text-secondary"
              aria-label="Metrics error details"
            >
              <Info className="h-3.5 w-3.5" />
              <span>Details</span>
            </button>
          </Tooltip>
        )}
      </div>
    </div>
  )
}
