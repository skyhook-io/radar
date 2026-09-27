import { MetricsSummary } from '@skyhook-io/k8s-ui'
import type { TimeSeries } from '@skyhook-io/k8s-ui'

const box = { width: 480, padding: '14px 8px' }
const BASE = 1720180800
const N = 30
const MiB = 1024 * 1024

export function CpuStats() {
  const series: TimeSeries[] = [
    {
      labels: { pod: 'checkout-7d9f8c-4tzkg' },
      dataPoints: Array.from({ length: N }, (_, i) => ({
        timestamp: BASE + i * 60,
        value: 0.24 + 0.14 * (0.5 + 0.5 * Math.sin(i / 3.5)),
      })),
    },
  ]
  return (
    <div style={box}>
      <MetricsSummary series={series} unit="cores" currentColorClass="text-blue-400" />
    </div>
  )
}

export function MemoryAcrossPods() {
  const series: TimeSeries[] = [220, 285, 190].map((base, s) => ({
    labels: { pod: `api-6b4c9d-${['2xq7v', '7m4kf', '9plzc'][s]}` },
    dataPoints: Array.from({ length: N }, (_, i) => ({
      timestamp: BASE + i * 60,
      value: (base + 45 * (0.5 + 0.5 * Math.sin(i / 5 + s))) * MiB,
    })),
  }))
  return (
    <div style={box}>
      <MetricsSummary series={series} unit="bytes" currentColorClass="text-emerald-400" />
    </div>
  )
}
