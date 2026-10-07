import { afterEach, expect, it } from 'vitest'
import { initNavigationMap, objectReferenceToResourceRef, resetNavigationMap } from './navigation'

afterEach(resetNavigationMap)

it('preserves the reference namespace and explicit core identity', () => {
  expect(objectReferenceToResourceRef({ apiVersion: 'v1', kind: 'Pod', namespace: 'other', name: 'backend' })).toEqual({ kind: 'Pod', group: '', namespace: 'other', name: 'backend' })
  expect(objectReferenceToResourceRef({ apiVersion: 'v1', kind: 'Node', namespace: 'irrelevant', name: 'worker' })).toEqual({ kind: 'Node', group: '', namespace: '', name: 'worker' })
})

it('uses exact discovered scope for colliding API kinds', () => {
  initNavigationMap([
    { group: 'cluster.example.io', version: 'v1', kind: 'Node', name: 'nodes', namespaced: false, isCrd: true, verbs: ['get'] },
    { group: 'team.example.io', version: 'v1', kind: 'Node', name: 'nodes', namespaced: true, isCrd: true, verbs: ['get'] },
  ])
  expect(objectReferenceToResourceRef({ apiVersion: 'cluster.example.io/v1', kind: 'Node', name: 'worker' })).toEqual({ kind: 'Node', group: 'cluster.example.io', namespace: '', name: 'worker' })
  expect(objectReferenceToResourceRef({ apiVersion: 'team.example.io/v1', kind: 'Node', namespace: 'team', name: 'worker' })).toEqual({ kind: 'Node', group: 'team.example.io', namespace: 'team', name: 'worker' })
  expect(objectReferenceToResourceRef({ apiVersion: 'team.example.io/v1', kind: 'Node', name: 'worker' })).toBeNull()
})

it('does not invent missing API identity, namespace or unknown cluster scope', () => {
  expect(objectReferenceToResourceRef({ kind: 'Pod', namespace: 'team', name: 'backend' })).toBeNull()
  expect(objectReferenceToResourceRef({ apiVersion: 'v1', kind: 'Pod', name: 'backend' })).toBeNull()
  expect(objectReferenceToResourceRef({ apiVersion: 'unknown.example.io/v1', kind: 'Node', name: 'worker' })).toBeNull()
  expect(objectReferenceToResourceRef({ apiVersion: 'unknown.example.io/v1', kind: 'Widget', namespace: 'team', name: 'backend' })).toEqual({ kind: 'Widget', group: 'unknown.example.io', namespace: 'team', name: 'backend' })
  resetNavigationMap()
  expect(objectReferenceToResourceRef({ apiVersion: 'cluster.example.io/v1', kind: 'Node', name: 'worker' })).toBeNull()
})
