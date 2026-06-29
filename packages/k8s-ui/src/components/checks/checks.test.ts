import { describe, it, expect } from 'vitest'
import { resourceKey, resourceRefKey, checkFindingKey, mapRadarSeverity, SOURCE_RADAR_BUILTIN, type CheckResourceRef } from './types'

describe('resourceKey', () => {
  // Same kind/ns/name across two API groups must not collide. Mirrors
  // radar/pkg/audit helpers_test.go's group-aware key.
  it('disambiguates the same kind/ns/name across API groups', () => {
    expect(resourceKey('', 'Service', 'prod', 'api')).not.toBe(resourceKey('serving.knative.dev', 'Service', 'prod', 'api'))
  })
})

describe('checkFindingKey', () => {
  const ref = (cluster_id: string): CheckResourceRef => ({ cluster_id, group: 'apps', kind: 'Deployment', namespace: 'prod', name: 'api' })

  // The same resource identity + check on two clusters must produce distinct
  // keys — even when the clusters' display names collapse to the same label.
  it('disambiguates identical findings across cluster IDs', () => {
    const a = checkFindingKey('cl_aaa', SOURCE_RADAR_BUILTIN, resourceRefKey(ref('cl_aaa')), 'run-as-root')
    const b = checkFindingKey('cl_bbb', SOURCE_RADAR_BUILTIN, resourceRefKey(ref('cl_bbb')), 'run-as-root')
    expect(a).not.toBe(b)
  })

  it('disambiguates by the optional detail discriminator', () => {
    const rk = resourceRefKey(ref('cl_aaa'))
    const noDetail = checkFindingKey('cl_aaa', SOURCE_RADAR_BUILTIN, rk, 'container-checks')
    const a = checkFindingKey('cl_aaa', SOURCE_RADAR_BUILTIN, rk, 'container-checks', 'sidecar')
    const b = checkFindingKey('cl_aaa', SOURCE_RADAR_BUILTIN, rk, 'container-checks', 'app')
    expect(new Set([noDetail, a, b]).size).toBe(3)
  })
})

describe('mapRadarSeverity', () => {
  it('maps danger→high and warning→medium', () => {
    expect(mapRadarSeverity('danger')).toBe('high')
    expect(mapRadarSeverity('warning')).toBe('medium')
  })
  it('falls back to medium for an unrecognized severity', () => {
    expect(mapRadarSeverity('unknown')).toBe('medium')
  })
})
