import type { DatadogSizingWorkload } from '../../types/rightsizing'
import { RightsizingEvidence } from './RightsizingEvidence'

export function DatadogRightsizingEvidence({ data }: { data: DatadogSizingWorkload }) {
  return (
    <div className="mt-2 space-y-3">
      <p className="text-xs text-theme-text-secondary">
        Datadog · {data.window} lookback · {data.replicas} current{' '}
        {data.replicas === 1 ? 'replica' : 'replicas'}. Measurements cover the current Pods since
        their creation. Rollouts and restarts can reduce coverage.
      </p>
      <RightsizingEvidence data={data} />
    </div>
  )
}
