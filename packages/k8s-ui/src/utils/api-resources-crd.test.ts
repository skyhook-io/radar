import { expect, it } from 'vitest'
import { categorizeResources, resourceSearchAliases } from './api-resources'
import type { APIResource } from '../types'
const crd: APIResource = { group: 'apiextensions.k8s.io', version: 'v1', kind: 'CustomResourceDefinition', name: 'customresourcedefinitions', namespaced: false, isCrd: false, verbs: ['get', 'list'] }
it('places discovered definitions in Cluster without a fallback row', () => {
  expect(categorizeResources([crd]).find(c => c.name === 'Cluster')?.resources).toContainEqual(crd)
  expect(categorizeResources([]).flatMap(c => c.resources).some(r => r.kind === crd.kind)).toBe(false)
  expect(categorizeResources([{ ...crd, verbs: ['get'] }]).flatMap(c => c.resources).some(r => r.kind === crd.kind)).toBe(false)
  expect(resourceSearchAliases(crd)).toEqual(['crd', 'crds'])
  expect(resourceSearchAliases({ group: 'other.io', kind: crd.kind })).toEqual([])
})
