import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { KueueWorkloadRenderer } from './KueueWorkloadRenderer'

const mock = vi.hoisted(() => ({ query: {} as any }))
vi.mock('../../../api/client', () => ({ useKueueProvisioning: () => mock.query }))
const data = { metadata: { name: 'work', namespace: 'ml', uid: 'new' }, status: { admissionChecks: [{ name: 'capacity', state: 'Rejected', message: 'Capacity unavailable' }] } }
const render = () => renderToStaticMarkup(<KueueWorkloadRenderer data={data} />)

describe('Workload provisioning observation', () => {
  beforeEach(() => { mock.query = {} })
  it('never reports failed or old-generation lookups as an empty collection', () => {
    mock.query = { error: new Error('no access to list ProvisioningRequests'), data: { uid: 'new', requests: [] } }
    expect(render()).toContain('Could not observe')
    expect(render()).not.toContain('No retained')
    mock.query = { data: { uid: 'old', installed: true, requests: [{ metadata: { name: 'old-request' } }] } }
    expect(render()).toContain('Looking for')
    expect(render()).not.toContain('old-request')
  })
  it('keeps the rejected explanation when requests have already been cleaned up', () => {
    mock.query = { data: { uid: 'new', installed: true, requests: [], total: 0 } }
    expect(render()).toContain('Capacity unavailable')
    expect(render()).toContain('may have been cleaned up')
    mock.query.data.installed = false
    expect(render()).toContain('API is not served')
    expect(render()).not.toContain('No retained')
  })
  it('shows retained request state before PodSets without inferring check membership', () => {
    mock.query = { data: { uid: 'new', installed: true, total: 21, truncated: true, requests: [{
      metadata: { name: 'request-1', namespace: 'ml', uid: 'request-id' }, spec: { provisioningClassName: 'test.invalid' },
      status: { conditions: [{ type: 'Failed', status: 'True', message: 'No capacity' }] },
    }] } }
    const html = render()
    expect(html).toContain('request-1')
    expect(html).toContain('Showing 1 of 21')
    expect(html.indexOf('Provisioning Requests')).toBeLessThan(html.indexOf('Pod Sets'))
    expect(html).toContain('not a retry history')
  })
})
