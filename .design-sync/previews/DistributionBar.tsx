import { DistributionBar } from '@skyhook-io/k8s-ui'

const wrap = { width: 300, padding: '12px 4px' }

export function HealthRollup() {
  return (
    <div style={wrap}>
      <DistributionBar
        ariaLabel="Workload health"
        segments={[
          { key: 'healthy', count: 42, fillClass: 'bg-emerald-500' },
          { key: 'warning', count: 8, fillClass: 'bg-amber-500' },
          { key: 'error', count: 3, fillClass: 'bg-red-500' },
        ]}
      />
    </div>
  )
}

export function MostlyHealthy() {
  return (
    <div style={wrap}>
      <DistributionBar
        ariaLabel="Sync status"
        segments={[
          { key: 'synced', count: 58, fillClass: 'bg-emerald-500' },
          { key: 'outofsync', count: 2, fillClass: 'bg-sky-500' },
        ]}
      />
    </div>
  )
}
