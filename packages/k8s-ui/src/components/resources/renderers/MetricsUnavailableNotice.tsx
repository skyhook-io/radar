import { Info } from 'lucide-react'
import { Tooltip } from '../../ui/Tooltip'

interface MetricsUnavailableNoticeProps {
  rawError?: string
  diagnosis?: string
}

export function MetricsUnavailableNotice({ rawError, diagnosis }: MetricsUnavailableNoticeProps) {
  return (
    <div className="card-inner-lg text-xs text-theme-text-tertiary">
      <div className="flex items-center gap-1.5">
        <span className="min-w-0 leading-5">
          Metrics unavailable. Radar cannot read metrics.k8s.io.
        </span>
        {rawError && (
          <Tooltip
            content={(
              <span className="space-y-1">
                <span className="block">
                  This panel uses Kubernetes metrics.k8s.io for live CPU and memory; Prometheus data does not fill it.
                </span>
                <span className="block">
                  {diagnosis || 'Check that metrics-server is installed and healthy, and that the v1beta1.metrics.k8s.io APIService is Available.'}
                </span>
                <span className="block">
                  Raw error: <span className="font-mono break-words">{rawError}</span>
                </span>
              </span>
            )}
            delay={150}
            position="left"
          >
            <button
              type="button"
              className="inline-flex shrink-0 cursor-help items-center gap-1 text-xs font-medium leading-5 text-theme-text-tertiary hover:text-theme-text-secondary"
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
