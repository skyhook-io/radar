import { CNPGDimensionMark } from '@skyhook-io/k8s-ui'

// The dimension mark beside a CNPG Cluster's tab label: only non-healthy
// dimensions draw a glyph; unknown (unassessed) is a hollow ring.
const row = { display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, padding: '4px 0' } as const

export function Tones() {
  return (
    <div style={{ padding: 12 }}>
      <div style={row}><CNPGDimensionMark dimension={{ id: 'replication', label: 'Replication', tone: 'degraded', text: '1 of 2 standbys streaming', source: 'pg_stat_replication on the primary' }} /> Replication</div>
      <div style={row}><CNPGDimensionMark dimension={{ id: 'protection', label: 'Protection', tone: 'unhealthy', text: 'WAL archiving failing for 3h', source: 'status.conditions ContinuousArchiving' }} /> Protection</div>
      <div style={row}><CNPGDimensionMark dimension={{ id: 'storage', label: 'Storage', tone: 'alert', text: 'pgdata 91% full', source: 'kubelet volume stats' }} /> Storage</div>
      <div style={row}><CNPGDimensionMark dimension={{ id: 'serving', label: 'Serving', tone: 'unknown', text: 'not assessed', source: 'instance status endpoint unreachable' }} /> Serving</div>
    </div>
  )
}
