import { describe, it, expect } from 'vitest'
import { Database, Server, Puzzle } from 'lucide-react'
import { getResourceIcon, DEFAULT_RESOURCE_ICON } from './resource-icons'

describe('getResourceIcon', () => {
  it('disambiguates colliding kinds by API group', () => {
    // `Cluster` is shipped by CNPG, Cluster API, KubeBlocks and others. Without
    // the group there is nothing to pick on, so the browser passes it.
    expect(getResourceIcon('Cluster', 'postgresql.cnpg.io')).toBe(Database)
    expect(getResourceIcon('Cluster', 'cluster.x-k8s.io')).toBe(Server)
  })

  it('falls back to the default for an unmapped group', () => {
    // A third operator's `clusters` CRD must not inherit a Postgres icon.
    expect(getResourceIcon('Cluster', 'apps.kubeblocks.io')).toBe(DEFAULT_RESOURCE_ICON)
    expect(getResourceIcon('Backup', 'kubevirt.io')).toBe(DEFAULT_RESOURCE_ICON)
  })

  it('ignores the group for kinds that do not collide', () => {
    expect(getResourceIcon('Pod')).not.toBe(Puzzle)
    expect(getResourceIcon('Pod', 'postgresql.cnpg.io')).toBe(getResourceIcon('Pod'))
  })

  it('resolves unambiguous CNPG kinds without a group', () => {
    // Pooler is CNPG-only, so it keys directly and works everywhere.
    expect(getResourceIcon('Pooler')).not.toBe(DEFAULT_RESOURCE_ICON)
    expect(getResourceIcon('Pooler')).toBe(getResourceIcon('Pooler', 'postgresql.cnpg.io'))
  })

  it('returns the default for an entirely unknown kind', () => {
    expect(getResourceIcon('Widget')).toBe(DEFAULT_RESOURCE_ICON)
  })
})
