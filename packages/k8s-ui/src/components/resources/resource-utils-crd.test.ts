import { expect, it } from 'vitest'
import { getCRDStatus } from './resource-utils-crd'
import { getGenericResourceStatus } from './generic-status'
import { getResourceStatus } from '../shared/ResourceRendererDispatch'

it.each([
  [{ type: 'Established', status: 'True' }, 'Established', 'healthy'],
  [{ type: 'Established', status: 'False', message: 'waiting' }, 'Not established', 'unhealthy'],
  [{ type: 'NamesAccepted', status: 'False', reason: 'Conflict', message: 'name taken' }, 'Conflict', 'unhealthy'],
  [{ type: 'Terminating', status: 'True' }, 'Terminating', 'degraded'],
  [{ type: 'Established', status: 'Unknown' }, 'Unknown', 'unknown'],
  [undefined, 'Unknown', 'unknown'],
])('shares CRD condition classification: %j', (condition, text, tone) => {
  const data = { kind: 'CustomResourceDefinition', apiVersion: 'apiextensions.k8s.io/v1', status: { conditions: condition ? [condition] : [] } }
  expect(getCRDStatus(data)).toMatchObject({ text, tone })
  expect(getGenericResourceStatus(data)).toEqual(getCRDStatus(data))
  expect(getResourceStatus('customresourcedefinitions', data)?.text).toBe(text)
})
it('prioritizes termination and rejected names over establishment', () => {
  const status = { conditions: [{ type: 'Established', status: 'True' }, { type: 'NamesAccepted', status: 'False' }] }
  expect(getCRDStatus({ status }).tone).toBe('unhealthy')
  expect(getCRDStatus({ metadata: { deletionTimestamp: 'now' }, status }).text).toBe('Terminating')
})
