import { describe, expect, it } from 'vitest'
import { collectionGroupParts, type NamespaceCollectionGroup } from './namespace-collection'

const group = (kinds: string[], namespaces: NamespaceCollectionGroup['namespaces']): NamespaceCollectionGroup => ({
  kinds: kinds.map(kind => ({ kind })),
  namespaces,
})

describe('collectionGroupParts', () => {
  it('names a few kinds in the plural and the namespaces Radar collects them in', () => {
    expect(collectionGroupParts(group(['Pod'], { count: 1, names: ['team-a'] }))).toEqual({
      kinds: 'Pods',
      kindNames: ['Pods'],
      summarized: false,
      namespaces: 'team-a',
    })
    const two = collectionGroupParts(group(['ConfigMap', 'Pod'], { count: 2, names: ['team-a', 'team-b'] }))
    expect(two.kinds).toBe('ConfigMaps and Pods')
    expect(two.namespaces).toBe('team-a and team-b')
  })

  it('pluralizes kinds as the rest of the UI does', () => {
    const parts = collectionGroupParts(group(['Ingress', 'NetworkPolicy'], { count: 1, names: ['team-a'] }))
    expect(parts.kinds).toBe('Ingresses and NetworkPolicies')
  })

  it('counts the kinds past three and keeps every one for a list', () => {
    const parts = collectionGroupParts(group(['Pod', 'Service', 'Job', 'CronJob', 'Secret'], { count: 1, names: ['a'] }))
    expect(parts.kinds).toBe('5 kinds')
    expect(parts.summarized).toBe(true)
    expect(parts.kindNames).toEqual(['Pods', 'Services', 'Jobs', 'CronJobs', 'Secrets'])
  })

  it('counts the namespaces past three', () => {
    const parts = collectionGroupParts(group(['Pod'], { count: 4, names: ['a', 'b', 'c', 'd'] }))
    expect(parts.namespaces).toBe('a, b, c and 1 more namespace')
  })

  it('counts namespaces when there are too many to name', () => {
    expect(collectionGroupParts(group(['Pod'], { count: 120 })).namespaces).toBe('120 namespaces')
  })

  it('says when no namespace the caller can see is collected', () => {
    expect(collectionGroupParts(group(['Pod'], { count: 0 })).namespaces).toBe('no namespace you can see')
  })
})
