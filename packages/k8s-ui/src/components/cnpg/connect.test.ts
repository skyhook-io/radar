import { describe, expect, it } from 'vitest'
import { cnpgConnectInfo, cnpgConnectionURI, cnpgPsqlCommand } from './connect'

const cluster = (spec: any = {}) => ({ apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', metadata: { name: 'pg', namespace: 'db' }, spec: { instances: 3, ...spec } })

describe('cnpgConnectInfo', () => {
  it('lists the default Services and names the Secret by convention when the spec does not', () => {
    const info = cnpgConnectInfo(cluster())
    expect(info.endpoints.map((e) => `${e.role} ${e.host}:${e.port}`)).toEqual(['rw pg-rw.db.svc:5432', 'ro pg-ro.db.svc:5432', 'r pg-r.db.svc:5432'])
    expect(info.database).toEqual({ value: 'app', source: "CloudNativePG's default" })
    expect(info.owner.value).toBe('app')
    expect(info.secret).toEqual({ name: 'pg-app', source: 'name by convention (<cluster>-app)', byConvention: true })
  })

  it('reads database, owner and Secret the way CloudNativePG resolves them: recovery, pg_basebackup, then initdb', () => {
    const info = cnpgConnectInfo(
      cluster({
        bootstrap: {
          initdb: { database: 'orders', owner: 'orders_owner', secret: { name: 'init-secret' } },
          recovery: { database: 'restored', secret: { name: 'recovery-secret' } },
        },
      }),
    )
    expect(info.database).toEqual({ value: 'restored', source: 'spec.bootstrap.recovery.database' })
    expect(info.owner).toEqual({ value: 'orders_owner', source: 'spec.bootstrap.initdb.owner' })
    expect(info.secret).toEqual({ name: 'recovery-secret', source: 'spec.bootstrap.recovery.secret.name', byConvention: false })
  })

  it('owner defaults to the database name', () => {
    expect(cnpgConnectInfo(cluster({ bootstrap: { initdb: { database: 'orders' } } })).owner).toEqual({
      value: 'orders',
      source: "CloudNativePG's default: the database's name",
    })
  })

  it('does not invent a database for a monolithic import', () => {
    const info = cnpgConnectInfo(cluster({ bootstrap: { initdb: { import: { type: 'monolith' } } } }))
    expect(info.database.value).toBeUndefined()
    expect(info.owner.value).toBeUndefined()
  })

  it('drops disabled default Services and adds managed and Pooler Services with their ports', () => {
    const info = cnpgConnectInfo(
      cluster({
        managed: {
          services: {
            disabledDefaultServices: ['ro', 'r'],
            additional: [{ selectorType: 'rw', serviceTemplate: { metadata: { name: 'pg-lb' }, spec: { type: 'LoadBalancer', ports: [{ port: 6543 }] } } }],
          },
        },
      }),
      [
        { metadata: { name: 'pg-pooler-ro', namespace: 'db' }, spec: { cluster: { name: 'pg' }, type: 'ro' } },
        { metadata: { name: 'other', namespace: 'db' }, spec: { cluster: { name: 'pg2' } } },
        { metadata: { name: 'pg-pooler', namespace: 'elsewhere' }, spec: { cluster: { name: 'pg' } } },
      ],
    )
    expect(info.disabled).toEqual(['ro', 'r'])
    expect(info.endpoints.map((e) => `${e.role} ${e.name}:${e.port}${e.portFromTemplate ? '*' : ''}`)).toEqual(['rw pg-rw:5432', 'additional pg-lb:6543*', 'pooler pg-pooler-ro:5432'])
    expect(info.endpoints[2].poolerType).toBe('ro')
  })

  it('builds templates with a password placeholder only', () => {
    const info = cnpgConnectInfo(cluster({ bootstrap: { initdb: { database: 'orders', owner: 'o w' } } }))
    expect(cnpgConnectionURI(info.endpoints[0], info)).toBe('postgresql://o%20w:<password>@pg-rw.db.svc:5432/orders')
    expect(cnpgPsqlCommand(info.endpoints[0], info)).toBe("psql -h pg-rw.db.svc -p 5432 -U 'o w' -d orders")
    const quoted = cnpgConnectInfo(cluster({ bootstrap: { initdb: { database: "it's" } } }))
    expect(cnpgPsqlCommand(quoted.endpoints[0], quoted)).toBe(`psql -h pg-rw.db.svc -p 5432 -U 'it'\\''s' -d 'it'\\''s'`)
  })
})
