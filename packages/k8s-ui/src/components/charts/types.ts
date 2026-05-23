// Shared time-series chart types. The shape originated from Prometheus query
// results but is generic — any source emitting time-stamped numeric samples
// can feed AreaChart.

export interface TimeSeriesPoint {
  timestamp: number
  value: number
}

export interface TimeSeries {
  labels: Record<string, string>
  /** Expected to be sorted ascending by timestamp and finite-valued. The
   *  chart's path math and saturation peak scans assume this; passing
   *  unsorted or NaN samples will produce garbage rendering, not crashes. */
  dataPoints: TimeSeriesPoint[]
}

/** @deprecated Use {@link TimeSeriesPoint}. Kept for one release for callers
 *  still importing the Prom-prefixed name. */
export type PrometheusDataPoint = TimeSeriesPoint

/** @deprecated Use {@link TimeSeries}. Kept for one release for callers still
 *  importing the Prom-prefixed name. */
export type PrometheusSeries = TimeSeries

/**
 * Horizontal reference line overlaid on a chart. `kind` is semantic — it
 * drives which value `computeSaturation` treats as the operational ceiling.
 * The chart auto-extends its Y axis to fit reference lines, so they're
 * never clipped.
 */
export interface ReferenceLine {
  value: number
  label: string
  kind: 'request' | 'limit'
}
