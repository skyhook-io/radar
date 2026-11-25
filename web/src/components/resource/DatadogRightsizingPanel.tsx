import { useQuery } from '@tanstack/react-query'
import { DatadogRightsizingEvidence } from '@skyhook-io/k8s-ui/components/rightsizing/DatadogRightsizingEvidence'
import type { DatadogSizingDetail } from '@skyhook-io/k8s-ui/types/rightsizing'
import { fetchJSON, useRadarFeature } from '../../api/client'
import { getApiBase } from '../../api/config'

export function DatadogRightsizingPanel({
  kind,
  namespace,
  name,
}: {
  kind: string
  namespace: string
  name: string
}) {
  const { support, blockedReason } = useRadarFeature('datadogRightsizing')
  const path =
    '/metrics/datadog/rightsizing/' + [kind, namespace, name].map(encodeURIComponent).join('/')
  const queryKey = ['datadog-rightsizing', getApiBase(), path]
  const query = useQuery({
    queryKey,
    enabled: support === 'supported',
    queryFn: ({ signal }) => fetchJSON<DatadogSizingDetail>(path, signal),
    staleTime: 2 * 60_000,
    refetchOnWindowFocus: false,
    retry: false,
  })
  if (support === 'unsupported') {
    return (
      <p className="mt-4 text-sm text-theme-text-secondary">
        Upgrade this cluster’s Radar agent to see Datadog rightsizing.
      </p>
    )
  }
  if (support === 'unknown') {
    return <p role="status" className="mt-4 text-sm text-theme-text-secondary">{blockedReason}</p>
  }
  if (!query.error && query.data?.configured === false) {
    return (
      <p role="status" className="mt-4 text-sm text-theme-text-secondary">
        Datadog rightsizing is unavailable for this cluster.
      </p>
    )
  }
  return (
    <section className="mt-4 rounded-lg border border-theme-border bg-theme-surface p-3">
      <h3 className="text-sm font-medium text-theme-text-primary">Rightsizing</h3>
      {query.error ? (
        <p role="alert" className="mt-2 text-sm text-theme-text-secondary">
          {query.error.message}{' '}
          <button
            type="button"
            className="text-accent hover:underline"
            onClick={() => void query.refetch()}
          >
            Retry
          </button>
        </p>
      ) : !query.data?.configured ? (
        <p role="status" className="mt-2 text-sm text-theme-text-secondary">
          Checking workload demand…
        </p>
      ) : (
        <DatadogRightsizingEvidence data={query.data.rightsizing} />
      )}
    </section>
  )
}
