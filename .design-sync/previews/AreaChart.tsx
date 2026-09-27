import { AreaChart } from '@skyhook-io/k8s-ui'

const box = { width: 480, height: 220, padding: 8 }
const BASE = 1720180800
const N = 32

export function CpuWithLimits() {
  const dataPoints = Array.from({ length: N }, (_, i) => ({
    timestamp: BASE + i * 60,
    value: 0.22 + 0.16 * (0.5 + 0.5 * Math.sin(i / 4)) + (i > 20 ? (i - 20) * 0.01 : 0),
  }))
  return (
    <div style={box}>
      <AreaChart
        series={[{ labels: { pod: 'checkout-7d9f8c-4tzkg' }, dataPoints }]}
        color="#3b82f6"
        fillColor="#3b82f622"
        unit="cores"
        referenceLines={[
          { value: 0.1, label: 'request 100m', kind: 'request' },
          { value: 0.5, label: 'limit 500m', kind: 'limit' },
        ]}
      />
    </div>
  )
}

export function MemoryMultiPod() {
  const MiB = 1048576
  const pods = [
    { pod: 'api-6b4c9d-2xq7v', base: 214, amp: 42 },
    { pod: 'api-6b4c9d-7m4kf', base: 268, amp: 56 },
    { pod: 'api-6b4c9d-9plzc', base: 176, amp: 30 },
  ]
  const series = pods.map(({ pod, base, amp }, s) => ({
    labels: { pod },
    dataPoints: Array.from({ length: N }, (_, i) => ({
      timestamp: BASE + i * 60,
      value: (base + amp * (0.5 + 0.5 * Math.sin(i / 5 + s * 1.3))) * MiB,
    })),
  }))
  return (
    <div style={box}>
      <AreaChart series={series} color="#10b981" fillColor="#10b98122" unit="bytes" />
    </div>
  )
}
