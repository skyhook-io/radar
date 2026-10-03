import { describe, expect, it } from 'vitest'
import { capabilityReason } from './actions'

describe('capabilityReason', () => {
  it('says nothing for an allowed action, and the server reason when there is one', () => {
    expect(capabilityReason({ allowed: true, permission: 'allowed' })).toBeUndefined()
    expect(capabilityReason({ allowed: false, permission: 'allowed', reason: 'The Cluster is hibernated' })).toBe('The Cluster is hibernated')
  })
  it('names the missing grant when the reason is absent', () => {
    expect(capabilityReason({ allowed: false, permission: 'denied', grant: { verb: 'create', group: 'postgresql.cnpg.io', resource: 'backups', namespace: 'db' } })).toBe(
      'Your account may not do this (create backups (postgresql.cnpg.io) in namespace db)',
    )
    expect(capabilityReason({ allowed: false, permission: 'unknown' })).toBe('Not available')
  })
})
