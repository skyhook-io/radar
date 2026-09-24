import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { AdmissionCheckRenderer, ProvisioningRequestRenderer, provisioningConditionTone } from './KueueProvisioningRenderers'
import { getAdmissionCheckStatus, getProvisioningRequestStatus, getProvisioningRequestMessage } from '../resource-utils-kueue'

const resource = (conditions: any[] = []) => ({ apiVersion: 'autoscaling.x-k8s.io/v1', metadata: { generation: 3, namespace: 'ml' }, spec: { provisioningClassName: 'provider.example' }, status: { conditions } })
const condition = (type: string, extra = {}) => ({ type, status: 'True', observedGeneration: 3, ...extra })

describe('provisioning evidence', () => {
  it('uses current provisioning progress while Accepted, without turning the false condition into failure', () => {
    const data = resource([condition('Accepted', { message: 'Request accepted' }), condition('Provisioned', { status: 'False', message: 'Waiting for capacity' })])
    expect(getProvisioningRequestStatus(data).text).toBe('Accepted')
    expect(getProvisioningRequestMessage(data)).toBe('Waiting for capacity')
    data.status.conditions[1].observedGeneration = 2
    expect(getProvisioningRequestMessage(data)).toBe('Request accepted')
  })
  it('keeps negative outcomes above old success regardless of condition order', () => {
    for (const type of ['Failed', 'CapacityRevoked', 'BookingExpired']) {
      for (const conditions of [[condition('Provisioned'), condition(type)], [condition(type), condition('Provisioned')]]) {
        expect(getProvisioningRequestStatus(resource(conditions)).text).toBe(type)
      }
    }
    expect(getProvisioningRequestStatus(resource([condition('BookingExpired')])).level).toBe('neutral')
    expect(getProvisioningRequestStatus(resource([condition('CapacityRevoked')])).level).toBe('unhealthy')
  })
  it('does not invent a pending or healthy outcome from missing, unknown or stale observations', () => {
    expect(getProvisioningRequestStatus(resource()).text).toBe('Unknown')
    expect(getProvisioningRequestStatus(resource([condition('Provisioned', { status: 'Unknown' })])).text).toBe('Unknown')
    expect(getProvisioningRequestStatus(resource([condition('Provisioned', { observedGeneration: 2 })])).level).toBe('unknown')
    expect(provisioningConditionTone(condition('Failed'))).toBe('fail')
    expect(provisioningConditionTone(condition('BookingExpired'))).toBe('unknown')
  })
  it('renders requested template/count and outcome without claiming running Pods', () => {
    const data = { ...resource([condition('Provisioned')]), spec: { provisioningClassName: 'provider.example', podSets: [{ podTemplateRef: { name: 'workers' }, count: 0 }, { podTemplateRef: { name: 'missing-count' } }] } }
    const html = renderToStaticMarkup(<ProvisioningRequestRenderer data={data} />)
    for (const text of ['workers', 'Requested Pods', 'Not specified', 'not a live check', 'Provisioned does not mean the Workload is running']) expect(html).toContain(text)
    expect(html).toContain('>0<')
  })
  it('keeps failure reasons/messages and does not treat foreign Workload owners as Kueue', () => {
    const data = resource([condition('Failed', { reason: 'CapacityUnavailable', message: 'No matching capacity' })])
    Object.assign(data.metadata, { ownerReferences: [{ controller: true, kind: 'Workload', apiVersion: 'foreign.io/v1', name: 'foreign-owner' }] })
    const html = renderToStaticMarkup(<ProvisioningRequestRenderer data={data} />)
    expect(html).toContain('No matching capacity')
    expect(html).toContain('CapacityUnavailable')
    expect(html).not.toContain('foreign-owner')
  })
})

describe('AdmissionCheck controller readiness', () => {
  it('distinguishes active controller from Workload approval and preserves native failure', () => {
    for (const version of ['v1beta1', 'v1beta2']) {
      const data = { ...resource([condition('Active')]), apiVersion: `kueue.x-k8s.io/${version}`, spec: { controllerName: 'kueue.x-k8s.io/provisioning-request' } }
      const html = renderToStaticMarkup(<AdmissionCheckRenderer data={data} />)
      expect(html).toContain('Each Workload reports its own')
      expect(getAdmissionCheckStatus(data).text).toBe('Active')
      data.status.conditions = [condition('Active', { status: 'False', reason: 'BadParametersRef', message: 'Config not found' })]
      expect(renderToStaticMarkup(<AdmissionCheckRenderer data={data} />)).toContain('Config not found')
    }
  })
  it('keeps stale and missing readiness unknown, and only displays declared v1beta1 retry delay', () => {
    const data = { ...resource([condition('Active', { observedGeneration: 2 })]), apiVersion: 'kueue.x-k8s.io/v1beta1', spec: { retryDelayMinutes: 0 } }
    expect(getAdmissionCheckStatus(data).level).toBe('unknown')
    expect(renderToStaticMarkup(<AdmissionCheckRenderer data={data} />)).toContain('0 minutes')
    expect(renderToStaticMarkup(<AdmissionCheckRenderer data={{ ...data, apiVersion: 'kueue.x-k8s.io/v1beta2' }} />)).not.toContain('0 minutes')
    expect(getAdmissionCheckStatus(resource()).level).toBe('unknown')
  })
})
