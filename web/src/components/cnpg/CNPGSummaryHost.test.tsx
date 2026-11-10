import { describe, expect, it } from 'vitest'
import { renderCNPGSummary } from './CNPGSummaryHost'

const ctx = (apiVersion: string, kind: string) => ({
  apiKind: kind,
  namespace: 'pg',
  name: 'x',
  resource: { apiVersion, kind, metadata: { namespace: 'pg', name: 'x' } },
  context: 'drawer' as const,
})

describe('renderCNPGSummary', () => {
  it('returns null for resources without a CNPG summary, so they keep the default Overview', () => {
    expect(renderCNPGSummary(ctx('v1', 'Pod'))).toBeNull()
    expect(renderCNPGSummary(ctx('apps/v1', 'Deployment'))).toBeNull()
    expect(renderCNPGSummary(ctx('velero.io/v1', 'Backup'))).toBeNull()
    expect(renderCNPGSummary(ctx('cluster.x-k8s.io/v1beta1', 'Cluster'))).toBeNull()
  })

  it('returns a summary for a CNPG Cluster', () => {
    expect(renderCNPGSummary(ctx('postgresql.cnpg.io/v1', 'Cluster'))).not.toBeNull()
  })
})
