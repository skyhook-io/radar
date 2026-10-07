import { expect, it } from 'vitest'
import { endpointSliceMatchesService, endpointSliceServiceAssociation } from './endpoint-slices'
const service = { metadata: { namespace: 'team', name: 'web', uid: 'web-now' } }
const owner = (name='web', uid='web-now', controller=false) => ({ apiVersion: 'v1', kind: 'Service', name, uid, controller })
const slice = (metadata: object) => ({ apiVersion: 'discovery.k8s.io/v1', kind: 'EndpointSlice', metadata: { namespace: 'team', ...metadata } })

it('uses the standard label as primary, independently of metadata ownership', () => {
  const value = slice({ labels: { 'kubernetes.io/service-name': 'web' }, ownerReferences: [owner('other', 'old', true)] })
  expect(endpointSliceMatchesService(value, service)).toBe(true)
  expect(endpointSliceServiceAssociation(value)?.source).toBe('label')
  expect(endpointSliceMatchesService(slice({ labels: { 'kubernetes.io/service-name': 'other' }, ownerReferences: [owner()] }), service)).toBe(false)
  expect(endpointSliceMatchesService(slice({ namespace: 'foreign', labels: { 'kubernetes.io/service-name': 'web' } }), service)).toBe(false)
})

it('resolves unambiguous exact core owners and rejects observed replacement UIDs', () => {
  expect(endpointSliceMatchesService(slice({ ownerReferences: [owner()] }), service)).toBe(true)
  expect(endpointSliceMatchesService(slice({ ownerReferences: [owner('web', 'deleted')] }), service)).toBe(false)
  expect(endpointSliceServiceAssociation(slice({ ownerReferences: [{ ...owner(), apiVersion: 'custom.example.io/v1' }] }))).toBeNull()
  expect(endpointSliceServiceAssociation(slice({ ownerReferences: [owner(), owner('other','other-now')] }))).toBeNull()
  expect(endpointSliceMatchesService(slice({ ownerReferences: [owner('other','other-now'), owner('web','web-now',true)] }), service)).toBe(true)
  expect(endpointSliceMatchesService(slice({ ownerReferences: [owner(), owner()] }), service)).toBe(true)
})

it('does not interpret non-EndpointSlice API groups or empty source identity', () => {
  expect(endpointSliceServiceAssociation({ ...slice({ labels: { 'kubernetes.io/service-name': 'web' } }), apiVersion: 'custom.example.io/v1' })).toBeNull()
  expect(endpointSliceServiceAssociation(slice({ namespace: '', ownerReferences: [owner()] }))).toBeNull()
  expect(endpointSliceServiceAssociation(slice({}))).toBeNull()
})
