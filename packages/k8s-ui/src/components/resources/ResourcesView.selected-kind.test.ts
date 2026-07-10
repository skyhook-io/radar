import { describe, expect, it } from 'vitest'
import type { APIResource } from '../../types'
import {
  canonicalizeSelectedKind,
  deriveSidebarResourceCounts,
  LOADED_RESOURCE_COUNT_TTL_MS,
  resourceQueryMatchesSelectedKind,
} from './ResourcesView'

const endpoints: APIResource = {
  group: '',
  version: 'v1',
  kind: 'Endpoints',
  name: 'endpoints',
  namespaced: true,
  isCrd: false,
  verbs: ['list', 'get', 'watch'],
}

const httpRoute: APIResource = {
  group: 'gateway.networking.k8s.io',
  version: 'v1',
  kind: 'HTTPRoute',
  name: 'httproutes',
  namespaced: true,
  isCrd: true,
  verbs: ['list', 'get', 'watch'],
}

describe('canonicalizeSelectedKind', () => {
  it('restores the canonical Kind when URL hydration only has the plural resource name', () => {
    expect(
      canonicalizeSelectedKind(
        { name: 'endpoints', kind: 'endpoints', group: '' },
        [endpoints],
        [endpoints]
      )
    ).toEqual({ name: 'endpoints', kind: 'Endpoints', group: '' })
  })

  it('leaves an already canonical selection alone', () => {
    expect(
      canonicalizeSelectedKind(
        { name: 'endpoints', kind: 'Endpoints', group: '' },
        [endpoints],
        [endpoints]
      )
    ).toBeNull()
  })

  it('resolves CRD deep links from Kind to plural resource name', () => {
    expect(
      canonicalizeSelectedKind(
        { name: 'HTTPRoute', kind: 'HTTPRoute', group: 'gateway.networking.k8s.io' },
        [],
        [httpRoute]
      )
    ).toEqual({ name: 'httproutes', kind: 'HTTPRoute', group: 'gateway.networking.k8s.io' })
  })
})

describe('deriveSidebarResourceCounts', () => {
  it('keeps recently loaded counts for resources omitted from resource-counts', () => {
    const now = 10_000

    expect(
      deriveSidebarResourceCounts(
        [endpoints],
        {},
        undefined,
        { Endpoints: { count: 132, expiresAt: now + LOADED_RESOURCE_COUNT_TTL_MS } },
        now
      )
    ).toEqual({ Endpoints: 132 })
  })

  it('expires loaded counts after the TTL', () => {
    const now = 10_000

    expect(
      deriveSidebarResourceCounts(
        [endpoints],
        {},
        undefined,
        { Endpoints: { count: 132, expiresAt: now } },
        now
      )
    ).toEqual({ Endpoints: null })
  })

  it('prefers authoritative resource-counts over cached loaded counts', () => {
    const now = 10_000

    expect(
      deriveSidebarResourceCounts(
        [endpoints],
        { Endpoints: 7 },
        undefined,
        { Endpoints: { count: 132, expiresAt: now + LOADED_RESOURCE_COUNT_TTL_MS } },
        now
      )
    ).toEqual({ Endpoints: 7 })
  })

  it('lets a fresh loaded count override an unavailable count marker', () => {
    const now = 10_000

    expect(
      deriveSidebarResourceCounts(
        [endpoints],
        {},
        ['Endpoints'],
        { Endpoints: { count: 132, expiresAt: now + LOADED_RESOURCE_COUNT_TTL_MS } },
        now
      )
    ).toEqual({ Endpoints: 132 })
  })
})

describe('resourceQueryMatchesSelectedKind', () => {
  it('accepts untagged legacy query results', () => {
    expect(resourceQueryMatchesSelectedKind(undefined, { name: 'endpoints', kind: 'Endpoints', group: '' })).toBe(true)
    expect(resourceQueryMatchesSelectedKind({}, { name: 'endpoints', kind: 'Endpoints', group: '' })).toBe(true)
  })

  it('rejects stale selected-kind query results from the previous kind', () => {
    expect(
      resourceQueryMatchesSelectedKind(
        { resourceName: 'pods', group: '' },
        { name: 'endpoints', kind: 'Endpoints', group: '' }
      )
    ).toBe(false)
  })

  it('matches grouped built-in resources by plural name and group', () => {
    expect(
      resourceQueryMatchesSelectedKind(
        { resourceName: 'endpointslices', group: 'discovery.k8s.io' },
        { name: 'endpointslices', kind: 'EndpointSlice', group: 'discovery.k8s.io' }
      )
    ).toBe(true)
  })
})
