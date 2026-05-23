// Shared time-series chart types. Named with the "Prometheus" prefix because
// the shape originated from Prometheus query results, but the structure is
// generic — any source emitting time-stamped numeric samples can feed AreaChart.

export interface PrometheusDataPoint {
  timestamp: number
  value: number
}

export interface PrometheusSeries {
  labels: Record<string, string>
  dataPoints: PrometheusDataPoint[]
}

/**
 * Horizontal reference line overlaid on a chart (e.g. request / limit).
 * - tone='request' renders in muted gray
 * - tone='limit' renders in amber
 *
 * Neither tone is alarming on its own — the chart auto-extends its Y axis
 * to include reference lines, so they're always visible without clipping.
 */
export interface ReferenceLine {
  value: number
  label: string
  tone: 'request' | 'limit'
}
