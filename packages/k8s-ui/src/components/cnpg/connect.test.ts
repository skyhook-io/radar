import { cnpgEndpointAvailability } from './connect'
import type { CNPGClusterHA } from './ha'
import { describe, expect, it } from 'vitest'
import { cnpgConnectInfo, cnpgConnectionURI, cnpgPortForwardCommand, cnpgPsqlCommand } from './connect'

const cluster = (spec: any = {}) => ({ apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', metadata: { name: 'pg', namespace: 'db' }, spec: { instances: 3, ...spec } })

describe('cnpgConnectInfo', () => {
  it('forwards the actual Service port to the local template port', () => {
    const endpoint = { ...cnpgConnectInfo(cluster()).endpoints[0], port: 6543 }
    expect(cnpgPortForwardCommand(endpoint, 'db')).toBe('kubectl -n db port-forward service/pg-rw 5432:6543')
    expect(cnpgPortForwardCommand(endpoint, 'db', 15432)).toBe('kubectl -n db port-forward service/pg-rw 15432:6543')
  })
  it('lists the default Services and names the Secret by convention when the spec does not', () => {
    const info = cnpgConnectInfo(cluster())
    expect(info.endpoints.map((e) => `${e.role} ${e.host}:${e.port}`)).toEqual(['rw pg-rw.db.svc:5432', 'ro pg-ro.db.svc:5432', 'r pg-r.db.svc:5432'])
    expect(info.database).toEqual({ value: 'app', source: "CloudNativePG's default" })
    expect(info.owner.value).toBe('app')
    expect(info.secret).toEqual({ name: 'pg-app', source: 'name by convention (<cluster>-app)', byConvention: true })
    expect(info.endpoints.find((e) => e.role === 'r')?.selects).toBe('any instance (may reach the primary; not read-only)')
    expect(info.endpoints.find((e) => e.role === 'ro')?.selects).toBe('standbys only (read-only)')
    const extra = cnpgConnectInfo(cluster({ managed: { services: { additional: [{ selectorType: 'r', serviceTemplate: { metadata: { name: 'pg-any' } } }] } } }))
    expect(extra.endpoints.find((e) => e.name === 'pg-any')?.selects).toContain('not read-only')
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

  it('treats a distributed-topology replica (replica.primary names another cluster) as a replica, like the operator', () => {
    expect(cnpgConnectInfo(cluster({ replica: { primary: 'pg-east', source: 'pg-east' } })).replicaCluster).toBe(true)
    expect(cnpgConnectInfo(cluster({ replica: { primary: 'pg', source: 'pg-east' } })).replicaCluster).toBe(false)
    expect(cnpgConnectInfo(cluster({ replica: { enabled: true, source: 'pg-east' } })).replicaCluster).toBe(true)
    expect(cnpgConnectInfo(cluster()).replicaCluster).toBe(false)
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
    expect(info.endpoints[1].selects).toBe('the primary (read-write)')
  })

  it('builds templates with a password placeholder only', () => {
    const info = cnpgConnectInfo(cluster({ bootstrap: { initdb: { database: 'orders', owner: 'o w' } } }))
    expect(cnpgConnectionURI(info.endpoints[0], info)).toBe('postgresql://o%20w:<password>@pg-rw.db.svc:5432/orders')
    expect(cnpgPsqlCommand(info.endpoints[0], info)).toBe("psql -h pg-rw.db.svc -p 5432 -U 'o w' -d orders")
    const quoted = cnpgConnectInfo(cluster({ bootstrap: { initdb: { database: "it's" } } }))
    expect(cnpgPsqlCommand(quoted.endpoints[0], quoted)).toBe(`psql -h pg-rw.db.svc -p 5432 -U 'it'\\''s' -d 'it'\\''s'`)
  })
})

it('pins port-forward to a shell-quoted kubeconfig context when supplied', () => {
  const ep = cnpgConnectInfo({ metadata: { name: 'orders', namespace: 'prod' } }).endpoints[0]
  expect(cnpgPortForwardCommand(ep, 'prod', 5432, 'kind-cnpg')).toBe('kubectl --context kind-cnpg -n prod port-forward service/orders-rw 5432:5432')
  expect(cnpgPortForwardCommand(ep, 'prod', 5432, 'test context')).toContain("--context 'test context'")
})

it('reads rw endpoints separately from standby and any-instance Pod readiness', () => {
  const eps = cnpgConnectInfo({ metadata: { name: 'orders', namespace: 'prod' } }).endpoints
  const ha = { rwEndpoints: { state: 'ok', pods: ['orders-1'] }, pods: { state: 'ok' }, instances: [{ pod: 'orders-1', role: 'primary', ready: true }, { pod: 'orders-2', role: 'replica', ready: false }] } as CNPGClusterHA
  expect(eps.map((ep) => cnpgEndpointAvailability(ep, ha).text)).toEqual(['Ready endpoints', 'Unavailable: no ready standby', 'Ready instance observed'])
  expect(cnpgEndpointAvailability(eps[1], { ...ha, instances: [] }).text).toBe('Unavailable: no ready standby')
  expect(eps.map((ep) => cnpgEndpointAvailability(ep).text)).toEqual(['Not checked', 'Not checked', 'Not checked'])
  expect(cnpgEndpointAvailability(eps[0], { ...ha, rwEndpoints: { ...ha.rwEndpoints, state: 'denied' } }).text).toBe('Not checked')
  expect(cnpgEndpointAvailability(eps[1], { ...ha, pods: { state: 'denied' } }).text).toBe('Not checked')
  expect(cnpgEndpointAvailability(eps[1], { ...ha, instances: [{ ...ha.instances[0], role: 'unknown' }] }).text).toBe('Not checked')
})

it('keeps Pooler routing faithful for any-instance and unrecognized selectors', () => {
  for (const [type, selects] of [['rw', 'the primary'], ['ro', 'the standbys'], ['r', 'any instance (may reach the primary; not read-only)'], ['future', 'selector future']]) {
    const ep = cnpgConnectInfo(cluster(), [{ metadata: { name: 'pooler', namespace: 'db' }, spec: { cluster: { name: 'pg' }, type } }]).endpoints.find((e) => e.role === 'pooler')!
    expect(ep.poolerType).toBe(type)
    expect(ep.selects).toBe(`PgBouncer in front of ${selects}`)
    expect(cnpgEndpointAvailability(ep).source).toBe('This Service’s availability has not been read')
  }
})
it('names the denied grant, failed read, unknown role and unread source alongside Not checked', () => {
  const eps = cnpgConnectInfo(cluster()).endpoints
  const ha = { rwEndpoints: { state: 'denied', grant: { verb: 'list', group: 'discovery.k8s.io', resource: 'endpointslices', namespace: 'db' } }, pods: { state: 'error', reason: 'API timeout' }, instances: [] } as unknown as CNPGClusterHA
  expect(cnpgEndpointAvailability(eps[0], ha)).toEqual({ text: 'Not checked', source: 'No access to Service EndpointSlices (needs list endpointslices (discovery.k8s.io) in namespace db)' })
  expect(cnpgEndpointAvailability(eps[1], ha)).toEqual({ text: 'Not checked', source: 'instance Pods could not be read: API timeout' })
  expect(cnpgEndpointAvailability(eps[0], undefined, 'Reading availability…').source).toBe('Reading availability…')
  expect(cnpgEndpointAvailability(eps[0], undefined, 'Availability could not be read: timeout').source).toBe('Availability could not be read: timeout')
  expect(cnpgEndpointAvailability(eps[1], { ...ha, pods: { state: 'ok' }, instances: [{ role: 'unknown', ready: true }] } as CNPGClusterHA).source).toBe('Ready instance roles were not reported')
})
