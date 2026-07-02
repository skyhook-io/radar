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
          Metrics unavailable. Radar cannot read metrics.k8s.io.
        </span>
        {rawError && (
          <Tooltip
            content={(
              <span className="space-y-1">
                <span className="block">
                  Check metrics-server and the metrics.k8s.io APIService.
                </span>
                <span className="block">
                  Prometheus is separate.
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
