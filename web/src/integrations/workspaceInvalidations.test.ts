import { describe, expect, it } from 'vitest'
import { pendingInvalidations } from './workspaceInvalidations'

describe('workspace invalidations', () => {
  it('maps a CloudNativePG change to the CNPG workspace query', () => {
    const due = pendingInvalidations([{ kind: 'Cluster', group: 'postgresql.cnpg.io' }])
    expect(due.map(d => d.id)).toEqual(['cnpg'])
    expect(due[0].queryKey).toEqual(['cnpg', 'workspace'])
  })
  it('ignores changes no workspace reads', () => {
    expect(pendingInvalidations([{ kind: 'Deployment', group: 'apps' }])).toEqual([])
  })
  it('lists each workspace once per batch', () => {
    expect(pendingInvalidations([
      { kind: 'Cluster', group: 'postgresql.cnpg.io' },
      { kind: 'Backup', group: 'postgresql.cnpg.io' },
    ]).map(d => d.id)).toEqual(['cnpg'])
  })
  it('refreshes the logging pipeline on Logging operator changes, not pod events', () => {
    expect(pendingInvalidations([{ kind: 'ClusterFlow', group: 'logging.banzaicloud.io' }])[0].queryKey).toEqual(['logging', 'pipeline'])
    // Each refetch rescans every pod, and Job churn deletes pods every few seconds; the page polls instead.
    for (const operation of ['add', 'update', 'delete'] as const) {
      expect(pendingInvalidations([{ kind: 'Pod', group: '', operation }])).toEqual([])
    }
  })
  it('lists two workspaces changed in one batch', () => {
    expect(pendingInvalidations([{ kind: 'Flow', group: 'logging.banzaicloud.io' }, { kind: 'Cluster', group: 'postgresql.cnpg.io' }]).map(d => d.id)).toEqual(['cnpg', 'logging'])
  })
})
