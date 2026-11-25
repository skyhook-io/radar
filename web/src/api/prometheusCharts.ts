import type { PrometheusStatus } from './client'

export function hasPrometheusCharts(status: PrometheusStatus | null | undefined): boolean {
  return status?.connected === true || status?.gkeSystemMetrics === true
}
