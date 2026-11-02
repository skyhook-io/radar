import { CNPGDimensionVerdict } from '@skyhook-io/k8s-ui'

const wrap = { width: 520, padding: 12, display: 'flex', flexDirection: 'column', gap: 8 } as const

export function Verdicts() {
  return (
    <div style={wrap}>
      <CNPGDimensionVerdict dimension={{ id: 'replication', label: 'Replication', tone: 'degraded', text: '1 of 2 standbys streaming', source: 'pg_stat_replication on the primary' }} />
      <CNPGDimensionVerdict dimension={{ id: 'protection', label: 'Protection', tone: 'unhealthy', text: 'WAL archiving failing for 3h', source: 'ContinuousArchiving condition' }} />
      <CNPGDimensionVerdict dimension={{ id: 'storage', label: 'Storage', tone: 'unknown', text: 'not assessed', source: 'volume stats need metrics-server' }} />
    </div>
  )
}

export function HealthyAlwaysShown() {
  return (
    <div style={wrap}>
      <CNPGDimensionVerdict alwaysShow dimension={{ id: 'serving', label: 'Serving', tone: 'healthy', text: 'primary accepting writes', source: 'instance status of payments-db-1' }} />
    </div>
  )
}
