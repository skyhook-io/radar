import { describe, expect, it } from 'vitest'
import { formatGrant } from './grant'

describe('formatGrant', () => {
  it('words a namespaced grant like the server', () => {
    expect(formatGrant({ verb: 'patch', group: 'postgresql.cnpg.io', resource: 'clusters', subresource: 'status', namespace: 'pg' })).toBe(
      'patch clusters/status (postgresql.cnpg.io) in namespace pg',
    )
    expect(formatGrant({ verb: 'get', resource: 'pods', subresource: 'proxy', namespace: 'pg' })).toBe('get pods/proxy in namespace pg')
  })
  it('words a cluster-wide grant with the group as resource.group', () => {
    expect(formatGrant({ verb: 'get', resource: 'nodes' })).toBe('get nodes cluster-wide')
    expect(formatGrant({ verb: 'list', group: 'postgresql.cnpg.io', resource: 'clusters' })).toBe('list clusters.postgresql.cnpg.io cluster-wide')
  })
  it('is undefined without a grant', () => {
    expect(formatGrant(undefined)).toBeUndefined()
  })
})
