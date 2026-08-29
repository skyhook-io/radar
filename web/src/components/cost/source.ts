import type { CostDataSource, CostUnavailableReason } from '../../api/client'

export function costSourceLabel(source?: CostDataSource): string {
  return source === 'kubecost' ? 'Kubecost Aggregator' : 'OpenCost via Prometheus'
}

export function costFreshnessLabel(
  source?: CostDataSource,
  window = '1h',
  dataThrough?: string,
): string {
  if (source !== 'kubecost') return `last ${window} average`
  if (!dataThrough) return 'latest Kubecost ETL data'
  const timestamp = new Date(dataThrough)
  if (Number.isNaN(timestamp.getTime())) return 'latest Kubecost ETL data'
  return `Kubecost data through ${timestamp.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })}`
}

export function costIntegrationUnavailableMessage(
  reason?: CostUnavailableReason | 'load_error',
): string | undefined {
  switch (reason) {
    case 'source_unavailable':
      return 'Kubecost Aggregator is unavailable. Check the URL, network path, and cluster ID in Settings → Cost.'
    case 'deployment_configuration_error':
      return 'Cost collection is misconfigured by this Radar deployment. Update its environment variables or Helm cost values, then restart Radar.'
    case 'authentication_error':
      return 'Kubecost rejected the configured API key. Update it in Settings → Cost.'
    case 'configuration_mismatch':
      return 'Saved Kubecost settings are not valid for this cluster. Update the cluster ID or local API key in Settings → Cost.'
    default:
      return undefined
  }
}
