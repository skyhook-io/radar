import { MetricsChart } from '@skyhook-io/k8s-ui'
import type { MetricsDataPoint } from '@skyhook-io/k8s-ui'

const box = { width: 360, padding: 12 }
const BASE_MS = Date.UTC(2024, 6, 5, 12, 0, 0)
const N = 24
const MiB = 1024 * 1024

function points(pick: (i: number) => Omit<MetricsDataPoint, 'timestamp'>): MetricsDataPoint[] {
  return Array.from({ length: N }, (_, i) => ({
    timestamp: new Date(BASE_MS + i * 60_000).toISOString(),
    ...pick(i),
  }))
}

export function CpuWithLimits() {
  // ~150m-280m, in nanocores
  const data = points((i) => ({ cpu: (150 + 130 * (0.5 + 0.5 * Math.sin(i / 3))) * 1e6, memory: 0 }))
  return (
    <div style={box}>
      <MetricsChart dataPoints={data} type="cpu" height={90} limit="500m" request="100m" />
    </div>
  )
}

export function MemoryWithLimits() {
  // ~340-530 MiB
  const data = points((i) => ({ cpu: 0, memory: (340 + 190 * (0.5 + 0.5 * Math.sin(i / 4 + 1))) * MiB }))
  return (
    <div style={box}>
      <MetricsChart dataPoints={data} type="memory" height={90} limit="1Gi" request="256Mi" />
    </div>
  )
}
