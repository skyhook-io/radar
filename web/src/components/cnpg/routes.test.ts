import { describe, expect, it } from 'vitest'
import { cnpgDetailKindFor, cnpgDetailPath, cnpgViewNamedForKind, parseCNPGRoute } from './routes'
import { decodeDrawerTrail, encodeDrawerTrail, sameSelectedResource } from '../../utils/drawer-trail'

describe('CNPG routes', () => {
  it('parses workspace screens and falls back to Overview for unknown or unavailable ones', () => {
    expect(parseCNPGRoute('/cnpg').screen).toBe('overview')
    expect(parseCNPGRoute('/cnpg/').screen).toBe('overview')
    expect(parseCNPGRoute('/cnpg/nope').screen).toBe('overview')
    expect(parseCNPGRoute('/cnpg/protection').screen).toBe('protection')
    expect(parseCNPGRoute('/cnpg/operator').screen).toBe('operator')
  })

  it('round-trips a drawer trail and keeps the API group', () => {
    const trail = [
      { kind: 'backups', group: 'postgresql.cnpg.io', namespace: 'payments', name: 'pg-billing-20260928020000' },
      { kind: 'objectstores', group: 'barmancloud.cnpg.io', namespace: 'payments', name: 's3-billing' },
      { kind: 'clusterimagecatalogs', group: 'postgresql.cnpg.io', namespace: '', name: 'postgresql-standard' },
    ]
    const encoded = encodeDrawerTrail(trail)
    expect(decodeDrawerTrail(encoded)).toEqual(trail)
  })

  it('drops malformed entries instead of opening a guessed object', () => {
    expect(decodeDrawerTrail('clusters:postgresql.cnpg.io:payments:pg-orders~garbage')).toEqual([
      { kind: 'clusters', group: 'postgresql.cnpg.io', namespace: 'payments', name: 'pg-orders' },
    ])
    expect(decodeDrawerTrail(null)).toEqual([])
  })

  it('distinguishes same-named kinds from different groups', () => {
    const cnpg = { kind: 'clusters', group: 'postgresql.cnpg.io', namespace: 'a', name: 'x' }
    const capi = { kind: 'clusters', group: 'cluster.x-k8s.io', namespace: 'a', name: 'x' }
    expect(sameSelectedResource(cnpg, capi)).toBe(false)
    expect(sameSelectedResource(cnpg, { ...cnpg })).toBe(true)
  })

  it('parses full-detail routes for every CNPG kind and the cluster-scoped placeholder', () => {
    expect(parseCNPGRoute('/cnpg/clusters/payments/pg-orders')).toEqual({
      screen: 'overview',
      detail: { plural: 'clusters', group: 'postgresql.cnpg.io', namespace: 'payments', name: 'pg-orders' },
    })
    expect(parseCNPGRoute('/cnpg/objectstores/payments/s3-billing').screen).toBe('protection')
    expect(parseCNPGRoute('/cnpg/objectstores/payments/s3-billing').detail?.group).toBe('barmancloud.cnpg.io')
    expect(parseCNPGRoute('/cnpg/clusterimagecatalogs/_/postgresql-standard').detail?.namespace).toBe('')
    expect(parseCNPGRoute('/cnpg/clusters/payments').detail).toBeUndefined()
  })

  it('builds detail paths carrying the context', () => {
    expect(cnpgDetailPath({ plural: 'clusters', namespace: 'payments', name: 'pg-orders' }, 'prod', 'logs')).toBe(
      '/cnpg/clusters/payments/pg-orders?ctx=prod&tab=logs',
    )
    expect(cnpgDetailPath({ plural: 'clusterimagecatalogs', namespace: '', name: 'std' })).toBe('/cnpg/clusterimagecatalogs/_/std')
  })

  it('only claims CNPG kinds from the CNPG groups', () => {
    expect(cnpgDetailKindFor('clusters', 'postgresql.cnpg.io')).toBe('clusters')
    expect(cnpgDetailKindFor('clusters', 'cluster.x-k8s.io')).toBeNull()
    expect(cnpgDetailKindFor('backups', 'velero.io')).toBeNull()
  })
})

describe('cnpgViewNamedForKind', () => {
  it('is true only for the kind the crumb’s view is named after', () => {
    expect(cnpgViewNamedForKind('clusters')).toBe(true)
    expect(cnpgViewNamedForKind('backups')).toBe(true)
    expect(cnpgViewNamedForKind('poolers')).toBe(true)
    // Also under Backups, but not what the view is named after.
    expect(cnpgViewNamedForKind('scheduledbackups')).toBe(false)
    expect(cnpgViewNamedForKind('objectstores')).toBe(false)
    expect(cnpgViewNamedForKind('databases')).toBe(false)
    expect(cnpgViewNamedForKind('pods')).toBe(false)
  })
})
