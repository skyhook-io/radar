import { MetricsSparkline } from '@skyhook-io/k8s-ui'
import type { MetricsDataPoint } from '@skyhook-io/k8s-ui'

const cell = {
  display: 'flex',
  alignItems: 'center',
  gap: 12,
  padding: '10px 14px',
  fontSize: 13,
  color: 'var(--text-secondary)',
} as const
const BASE_MS = Date.UTC(2024, 6, 5, 12, 0, 0)
const N = 20
const MiB = 1024 * 1024

function points(pick: (i: number) => Omit<MetricsDataPoint, 'timestamp'>): MetricsDataPoint[] {
  return Array.from({ length: N }, (_, i) => ({
    timestamp: new Date(BASE_MS + i * 60_000).toISOString(),
    ...pick(i),
  }))
}

export function CpuSparkline() {
  const data = points((i) => ({ cpu: (140 + 120 * (0.5 + 0.5 * Math.sin(i / 2.5))) * 1e6, memory: 0 }))
  return (
    <div style={cell}>
      <span style={{ minWidth: 160 }}>checkout-7d9f8c-4tzkg</span>
      <MetricsSparkline dataPoints={data} type="cpu" width={130} height={32} />
    </div>
  )
}

export function MemorySparkline() {
  const data = points((i) => ({ cpu: 0, memory: (300 + 170 * (0.5 + 0.5 * Math.sin(i / 3 + 1))) * MiB }))
  return (
    <div style={cell}>
      <span style={{ minWidth: 160 }}>api-6b4c9d-7m4kf</span>
      <MetricsSparkline dataPoints={data} type="memory" width={130} height={32} />
    </div>
  )
}
