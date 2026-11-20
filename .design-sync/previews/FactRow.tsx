import { FactGrid, FactRow, FactValue, FactSource } from '@skyhook-io/k8s-ui'

const wrap = { width: 520, padding: 12 } as const

export function InFactGrid() {
  const archiving = { text: 'Failing', tone: 'unhealthy' as const, source: 'ContinuousArchiving condition', at: new Date(Date.now() - 3 * 3600_000).toISOString(), atMeaning: 'since' as const }
  return (
    <div style={wrap}>
      <FactGrid>
        <FactRow label="Primary">payments-db-1</FactRow>
        <FactRow label="WAL archiving"><FactValue fact={archiving} /><FactSource fact={archiving} /></FactRow>
        <FactRow label="Last base backup"><FactValue fact={{ text: 'Completed', tone: 'healthy', at: new Date(Date.now() - 23 * 3600_000).toISOString() }} /></FactRow>
        <FactRow label="Replication slots"><FactValue fact={{ text: 'Not read', tone: 'unknown', source: 'needs the CNPG metrics endpoint' }} /></FactRow>
      </FactGrid>
    </div>
  )
}
