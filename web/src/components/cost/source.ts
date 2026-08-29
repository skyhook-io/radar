import type { CostDataSource, CostUnavailableReason } from '../../api/client'

export function costSourceLabel(source?: CostDataSource): string {
  return source === 'kubecost' ? 'Kubecost Aggregator' : 'OpenCost via Prometheus'
}

export function isCostDiscoveryPending(reason?: string): boolean {
  return reason === 'no_prometheus' || reason === 'no_cost_source'
}

export function costConfigurationAction(reason?: CostUnavailableReason): {
  section: 'prometheus' | 'cost'
  label: string
} {
  return reason === 'no_prometheus'
    ? { section: 'prometheus', label: 'Configure metrics' }
    : { section: 'cost', label: 'Configure cost source' }
}

export function costRateLabels(window?: string): {
  hourly: string
  rate: string
  allocationTitle: string
  allocationPeriod: string
} {
  if (window === '1d') {
    return {
      hourly: 'Hourly (1-day average)',
      rate: '1-day average hourly rate',
      allocationTitle: '1-day average allocation and use',
      allocationPeriod: 'Allocation and observed use: 1-day average',
    }
  }
  if (window === '1h') {
    return {
      hourly: 'Hourly (1-hour average)',
      rate: '1-hour average hourly rate',
      allocationTitle: '1-hour average allocation and use',
      allocationPeriod: 'Allocation and observed use: 1-hour average',
    }
  }
  return {
    hourly: 'Current hourly',
    rate: 'current hourly rate',
    allocationTitle: 'Current allocation and use',
    allocationPeriod: 'Allocation and CPU use: 1h average · Memory use: current',
  }
}

export function costFreshnessLabel(
  source?: CostDataSource,
  window?: string,
  dataThrough?: string,
): string {
  if (source !== 'kubecost') return window ? `last ${window} average` : 'current allocation average'
  const average = window === '1d'
    ? '1-day allocation average'
    : window === '1h'
      ? '1-hour allocation average'
      : 'current allocation average'
  if (!dataThrough) return `latest Kubecost ${average}`
  const timestamp = new Date(dataThrough)
  if (Number.isNaN(timestamp.getTime())) return `latest Kubecost ${average}`
  return `Kubecost ${average} · data through ${timestamp.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })}`
}

export function costIntegrationUnavailableMessage(
  reason?: CostUnavailableReason | 'load_error',
  settingsAvailable = true,
): string | undefined {
  switch (reason) {
    case 'no_cost_source':
      return settingsAvailable
        ? 'No compatible cost source was found. Connect OpenCost metrics in Settings → Metrics or configure Kubecost in Settings → Cost.'
        : 'No compatible cost source is configured for this cluster. Configure OpenCost metrics or Kubecost in the host application or Radar deployment.'
    case 'source_unavailable':
      return settingsAvailable
        ? 'Kubecost Aggregator is unavailable. Check the URL, network path, and cluster ID in Settings → Cost.'
        : 'Kubecost Aggregator is unavailable. Update this cluster’s cost-source configuration in the host application or Radar deployment.'
    case 'deployment_configuration_error':
      return 'Cost collection is misconfigured by this Radar deployment. Update its environment variables or Helm cost values, then restart Radar.'
    case 'authentication_error':
      return settingsAvailable
        ? 'Kubecost rejected the configured API key. Update it in Settings → Cost.'
        : 'Kubecost rejected the configured API key. Update this cluster’s credential in the host application or Radar deployment.'
    case 'configuration_mismatch':
      return settingsAvailable
        ? 'Saved Kubecost settings are not valid for this cluster. Update the cluster ID or local API key in Settings → Cost.'
        : 'Kubecost settings are not valid for this cluster. Update the cluster ID or credential in the host application or Radar deployment.'
    default:
      return undefined
  }
}
