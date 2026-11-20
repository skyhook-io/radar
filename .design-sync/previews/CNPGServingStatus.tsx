import { CNPGServingStatus } from '@skyhook-io/k8s-ui'

// The serving headline on a CNPG Cluster's title line.
const wrap = { padding: 12, display: 'flex', flexDirection: 'column', gap: 10, alignItems: 'flex-start' } as const

export function States() {
  return (
    <div style={wrap}>
      <CNPGServingStatus dimension={{ id: 'serving', label: 'Serving', tone: 'healthy', text: 'primary accepting writes', source: 'instance status of payments-db-1' }} />
      <CNPGServingStatus dimension={{ id: 'serving', label: 'Serving', tone: 'unhealthy', text: 'no primary — failover in progress', source: 'status.currentPrimary is empty' }} />
      <CNPGServingStatus dimension={{ id: 'serving', label: 'Serving', tone: 'unknown', text: 'not assessed', source: 'instance status endpoint unreachable' }} />
    </div>
  )
}

export function Selectable() {
  return (
    <div style={wrap}>
      <CNPGServingStatus onSelect={() => {}} dimension={{ id: 'serving', label: 'Serving', tone: 'degraded', text: 'read-only: replica cluster', source: 'spec.replica.enabled' }} />
    </div>
  )
}
