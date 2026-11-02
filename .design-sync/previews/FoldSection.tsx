import { FoldSection, FactGrid, FactRow } from '@skyhook-io/k8s-ui'

const wrap = { width: 520, padding: 12 } as const

export function NeedsAttention() {
  return (
    <div style={wrap}>
      <FoldSection title="Backups" hint="Barman to s3://acme-pg-backups" summary="WAL archiving failing for 3h" attention>
        <FactGrid>
          <FactRow label="Last base backup">Completed 23h ago</FactRow>
          <FactRow label="WAL archiving">Failing — AccessDenied on PutObject</FactRow>
        </FactGrid>
      </FoldSection>
    </div>
  )
}

export function Folded() {
  return (
    <div style={wrap}>
      <FoldSection title="Certificates" summary="Server and replication certs valid for 81 days" attention={false}>
        <FactGrid>
          <FactRow label="Server CA">payments-db-ca</FactRow>
        </FactGrid>
      </FoldSection>
    </div>
  )
}
