import type { CostDataSource } from '../../api/client'

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
