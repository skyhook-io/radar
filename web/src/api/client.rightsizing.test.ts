import { describe, expect, it } from 'vitest'
import { getRightsizingScanCacheConfig } from './client'

describe('rightsizing scan cache config', () => {
  it('retains result lookups briefly', () => {
    const config = getRightsizingScanCacheConfig(['staging', 'default'], 'cluster-a')

    expect(config.gcTime).toBe(5 * 60 * 1000)
  })

  it('isolates identities', () => {
    expect(getRightsizingScanCacheConfig([], 'a', 'alice').queryKey).not.toEqual(getRightsizingScanCacheConfig([], 'a', 'bob').queryKey)
  })

  it('normalizes namespace order and isolates cluster and namespace scopes', () => {
    const config = getRightsizingScanCacheConfig(['staging', 'default'], 'cluster-a')

    expect(config.namespaceKey).toBe('default,staging')
    expect(config.queryKey).toEqual([
      'prometheus-rightsizing-scan',
      '/api',
      '',
      'cluster-a',
      'default,staging',
    ])
    expect(getRightsizingScanCacheConfig(['default', 'staging'], 'cluster-a').queryKey).toEqual(
      config.queryKey,
    )
    expect(getRightsizingScanCacheConfig(['default'], 'cluster-a').queryKey).not.toEqual(
      config.queryKey,
    )
    expect(getRightsizingScanCacheConfig(['staging', 'default'], 'cluster-b').queryKey).not.toEqual(
      config.queryKey,
    )
  })
})
