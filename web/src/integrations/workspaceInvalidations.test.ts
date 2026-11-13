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
  it('refreshes the logging pipeline on Logging operator and pod changes', () => {
    expect(pendingInvalidations([{ kind: 'ClusterFlow', group: 'logging.banzaicloud.io' }]).map(d => d.id)).toEqual(['logging'])
    expect(pendingInvalidations([{ kind: 'Pod', group: '' }]).map(d => d.id)).toEqual(['logging'])
    expect(pendingInvalidations([{ kind: 'Pod', group: '' }])[0].queryKey).toEqual(['logging', 'pipeline'])
  })
  it('lists two workspaces changed in one batch', () => {
    expect(pendingInvalidations([{ kind: 'Pod' }, { kind: 'Cluster', group: 'postgresql.cnpg.io' }]).map(d => d.id)).toEqual(['cnpg', 'logging'])
  })
})
