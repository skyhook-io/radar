// @skyhook-io/k8s-ui/components/charts — shared time-series chart primitives.
// Used by Radar's PrometheusCharts / PrometheusChartsGrid and available to
// library consumers (e.g. Radar Hub custom dashboards) that want the same
// chart aesthetic without inheriting Radar's full Prom data layer.

export { AreaChart } from './AreaChart'
export { MetricsSummary } from './MetricsSummary'
export { SeriesLegend } from './SeriesLegend'
export { SERIES_COLORS, seriesColor, seriesFill, computeShortLabels } from './colors'
export { formatMetricValue, formatTimestamp } from './format'
export type { PrometheusDataPoint, PrometheusSeries, ReferenceLine } from './types'
