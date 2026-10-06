import { describe, expect, it } from 'vitest'
import { cnpgHost } from './host'

const resource = { kind: 'clusters', group: 'postgresql.cnpg.io', namespace: 'db', name: 'pg' }

describe('CNPG host composition', () => {
  it('preserves context and query state when mapping a generic detail route', () => {
    const search = new URLSearchParams('apiGroup=postgresql.cnpg.io&ctx=kind-demo&tab=timeline&drawer=pods%3Apg-1')
    const path = cnpgHost.detailRedirect(resource, search)!
    const url = new URL(path, 'http://radar.test')
    expect(url.pathname).toBe('/cnpg/clusters/db/pg')
    expect(Object.fromEntries(url.searchParams)).toEqual({ ctx: 'kind-demo', tab: 'activity', drawer: 'pods:pg-1' })
    expect(search.get('tab')).toBe('timeline')
    expect(search.get('apiGroup')).toBe('postgresql.cnpg.io')
  })

  it('keeps kind collisions on their own routes and expands CNPG with context', () => {
    const other = { ...resource, group: 'cluster.x-k8s.io' }
    expect(cnpgHost.detailRedirect(other, new URLSearchParams())).toBeNull()
    expect(cnpgHost.expandedPath(other)).toBeNull()
    expect(cnpgHost.expandedPath(resource, 'kind-demo', 'yaml')).toBe('/cnpg/clusters/db/pg?ctx=kind-demo&tab=yaml')
    expect(cnpgHost.logs('Cluster', { apiVersion: 'cluster.x-k8s.io/v1beta1' }, 'db', 'pg')).toBeNull()
  })
})
