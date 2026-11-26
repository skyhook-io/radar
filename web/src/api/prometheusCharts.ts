import type { PrometheusStatus } from './client'

/**
 * Whether the metrics source in use is an OpenObserve connection that can't
 * answer right now. Older Radar reports such a relay as connected, so the
 * source, not `connected`, is what says so.
 */
export function metricsSourceUnavailable(status: PrometheusStatus | null | undefined): boolean {
  return status?.source?.kind === 'openobserve' && status.source.unavailable === true
}

export function hasPrometheusCharts(status: PrometheusStatus | null | undefined): boolean {
  if (metricsSourceUnavailable(status)) return false
  return status?.connected === true || status?.gkeSystemMetrics === true
}

/** The one-word state Settings shows for the metrics connection. */
export function metricsConnectionLabel(status: PrometheusStatus | null | undefined): string {
  if (!status) return 'Checking…'
  if (metricsSourceUnavailable(status)) return 'OpenObserve unavailable'
  if (status.connected) return 'Connected'
  if (status.gkeSystemMetrics) return 'GKE system metrics'
  if (status.discovering) return 'Discovering…'
  return 'Not connected'
}
