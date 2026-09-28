import { describe, expect, it } from 'vitest'
import { decodeDrawerTrail, encodeDrawerTrail, parseCNPGRoute, sameResource } from './routes'

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
    expect(sameResource(cnpg, capi)).toBe(false)
    expect(sameResource(cnpg, { ...cnpg })).toBe(true)
  })
})
