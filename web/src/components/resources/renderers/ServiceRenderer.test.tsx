import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, expect, it, vi } from 'vitest'
import { ServiceRenderer } from './ServiceRenderer'
const mock = vi.hoisted(() => ({ query: {} as any, reads: [] as any[] }))
vi.mock('../../../api/client', () => ({ useResources: (...args: any[]) => { mock.reads.push(args); return mock.query } }))
vi.mock('../../../contexts/CapabilitiesContext', () => ({ useNamespacedCapabilities: () => ({ canPortForward: false }), useIsLocalDeployment: () => true }))
vi.mock('../../portforward/PortForwardButton', () => ({ PortForwardInlineButton: () => null }))
vi.mock('../../curl/ServiceCurlButton', () => ({ CurlButton: () => null, CurlPanel: () => null, isHttpishPort: () => false, defaultScheme: () => '', defaultPathForPort: () => '' }))
const service = { apiVersion: 'v1', kind: 'Service', metadata: { name: 'web', namespace: 'team', uid: 'web-now' }, spec: { selector: { app: 'web' } } }
const slice = (name: string, metadata: object) => ({ apiVersion: 'discovery.k8s.io/v1', kind: 'EndpointSlice', metadata: { namespace: 'team', name, ...metadata } })
const render = (data=service) => renderToStaticMarkup(<ServiceRenderer data={data} onCopy={() => {}} copied={null} />)
beforeEach(() => { mock.query={};mock.reads=[] })
it('queries slices for selected Services and includes exact label or owner associations', () => {
  mock.query={ data:[
    slice('labeled',{labels:{'kubernetes.io/service-name':'web'}}),
    slice('owned',{ownerReferences:[{apiVersion:'v1',kind:'Service',name:'web',uid:'web-now'}]}),
    slice('other',{labels:{'kubernetes.io/service-name':'different'}}),
    slice('replaced',{ownerReferences:[{apiVersion:'v1',kind:'Service',name:'web',uid:'web-deleted'}]}),
  ] }
  const html=render()
  expect(mock.reads[0]).toEqual(['endpointslices','team','discovery.k8s.io',{enabled:true,refetchInterval:30000}])
  expect(html).toContain('labeled');expect(html).toContain('owned');expect(html).not.toContain('replaced');expect(html).not.toContain('different')
})
it('exposes inventory read failures rather than empty results', () => {
  mock.query={error:new Error('Read denied'),data:[]}
  const html=render()
  expect(html).toContain('EndpointSlice inventory unavailable');expect(html).not.toContain('No EndpointSlices found')
})
it('disables EndpointSlice inventory for ExternalName', () => {
  const data={...service,spec:{type:'ExternalName',externalName:'example.org'}}
  const html=render(data as typeof service)
  expect(mock.reads[0][3].enabled).toBe(false);expect(html).not.toContain('EndpointSlices')
})
