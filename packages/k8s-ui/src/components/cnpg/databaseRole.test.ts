import { describe, expect, it } from 'vitest'
import { cnpgDatabaseRoleFacts, cnpgDatabaseRoleMeta } from './databaseRole'

const role = (spec: any, status: any = {}) => ({
  apiVersion: 'postgresql.cnpg.io/v1',
  kind: 'DatabaseRole',
  metadata: { name: 'app-reader', namespace: 'db' },
  spec: { cluster: { name: 'pg' }, name: 'reader', ...spec },
  status,
})

describe('cnpgDatabaseRoleFacts', () => {
  it('reports the Cluster’s managed.roles precedence as three-valued', () => {
    const cluster = { spec: { managed: { roles: [{ name: 'reader' }] } } }
    expect(cnpgDatabaseRoleFacts(role({}), cluster).overriddenByCluster).toBe(true)
    expect(cnpgDatabaseRoleFacts(role({}), { spec: {} }).overriddenByCluster).toBe(false)
    expect(cnpgDatabaseRoleFacts(role({}), null).overriddenByCluster).toBeNull()
  })

  it('reads applied three ways and the operator message', () => {
    expect(cnpgDatabaseRoleFacts(role({}), null).state).toBe('pending')
    const failed = cnpgDatabaseRoleFacts(role({}, { applied: false, message: 'database role is already managed by the CNPG cluster' }), null)
    expect(failed.state).toBe('failed')
    expect(failed.message).toContain('already managed')
  })

  it('treats an omitted login as false and names the client certificate Secret', () => {
    const f = cnpgDatabaseRoleFacts(role({ clientCertificate: {}, validUntil: '2027-01-01T00:00:00Z' }, { clientCertificate: { expiration: '2026-12-01T00:00:00Z' } }), null)
    expect(f.login).toBe(false)
    expect(f.clientCertificate?.secret).toBe('app-reader-client-cert')
    expect(cnpgDatabaseRoleMeta(f)).toBe('no login · password valid until 2027-01-01T00:00:00Z · client cert until 2026-12-01T00:00:00Z')
  })
})
