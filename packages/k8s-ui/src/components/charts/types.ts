// Shared time-series chart types. The shape originated from Prometheus query
// results but is generic — any source emitting time-stamped numeric samples
// can feed AreaChart.

export interface TimeSeriesPoint {
  timestamp: number
  value?: number | null
}

export interface TimeSeries {
  labels: Record<string, string>
  /** Sorted ascending by timestamp. Each point's `value` is either finite or
   *  null/absent (a gap). Consumers must skip gap points in arithmetic and
   *  break the line/area path across them rather than bridging or dropping to
   *  0. */
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

/**
 * Vertical marker on a chart at one instant. `kind` names what Radar recorded
 * there; the chart draws the marker and its label and never implies that the
 * marked instant explains the series around it.
 */
export interface ChartAnnotation {
  /** Unix seconds, the same clock as {@link TimeSeriesPoint.timestamp}. */
  timestamp: number
  label: string
  kind: 'change'
}
