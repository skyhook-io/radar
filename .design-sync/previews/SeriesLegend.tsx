import { SeriesLegend } from '@skyhook-io/k8s-ui'
import type { TimeSeries } from '@skyhook-io/k8s-ui'

const box = { width: 420, padding: '14px 8px' }
const points = [{ timestamp: 1720180800, value: 1 }]

export function PodSeries() {
  const pods = ['checkout-7d9f8c-4tzkg', 'checkout-7d9f8c-5z79f', 'checkout-7d9f8c-8kqm2', 'checkout-7d9f8c-b4xph']
  const series: TimeSeries[] = pods.map((pod) => ({ labels: { pod }, dataPoints: points }))
  return (
    <div style={box}>
      <SeriesLegend series={series} color="#3b82f6" />
    </div>
  )
}

export function CustomLabels() {
  const containers = ['app', 'istio-proxy', 'otel-collector']
  const series: TimeSeries[] = containers.map((container) => ({
    labels: { pod: 'payments-api-5c8b-2xq7v', container },
    dataPoints: points,
  }))
  return (
    <div style={box}>
      <SeriesLegend series={series} color="var(--accent)" seriesLabels={containers} />
    </div>
  )
}

export function OverflowMore() {
  const series: TimeSeries[] = Array.from({ length: 14 }, (_, i) => ({
    labels: { instance: `node-pool-a-${String(i + 1).padStart(2, '0')}:9100` },
    dataPoints: points,
  }))
  return (
    <div style={box}>
      <SeriesLegend series={series} color="#10b981" />
    </div>
  )
}
