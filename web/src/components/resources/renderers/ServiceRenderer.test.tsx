import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, expect, it, vi } from 'vitest'
import { RadarFeatureUnsupportedError } from '../../../api/radarFeatures'
import { ServiceRenderer } from './ServiceRenderer'

const mock = vi.hoisted(() => ({
  serviceQuery: {} as any,
  namespaceQuery: {} as any,
  serviceReads: [] as any[],
  namespaceReads: [] as any[],
}))
vi.mock('../../../api/client', () => ({
  useServiceEndpointSlices: (...args: any[]) => { mock.serviceReads.push(args); return mock.serviceQuery },
  useResources: (...args: any[]) => { mock.namespaceReads.push(args); return mock.namespaceQuery },
}))
vi.mock('../../../contexts/CapabilitiesContext', () => ({ useNamespacedCapabilities: () => ({ canPortForward: false }), useIsLocalDeployment: () => true }))
vi.mock('../../portforward/PortForwardButton', () => ({ PortForwardInlineButton: () => null }))
vi.mock('../../curl/ServiceCurlButton', () => ({ CurlButton: () => null, CurlPanel: () => null, isHttpishPort: () => false, defaultScheme: () => '', defaultPathForPort: () => '' }))

const service = { apiVersion: 'v1', kind: 'Service', metadata: { name: 'web', namespace: 'team' }, spec: { selector: { app: 'web' } } }
const selectorless = { ...service, spec: {} }
const slice = (name: string, labels?: Record<string, string>) => ({ apiVersion: 'discovery.k8s.io/v1', kind: 'EndpointSlice', metadata: { namespace: 'team', name, labels } })
const render = (data: any = service) => renderToStaticMarkup(<ServiceRenderer data={data} onCopy={() => {}} copied={null} />)
const unsupported = () => new RadarFeatureUnsupportedError('serviceEndpointSlices', { currentVersion: 'v1.7.0' })

beforeEach(() => {
  mock.serviceQuery = {}
  mock.namespaceQuery = {}
  mock.serviceReads = []
  mock.namespaceReads = []
})

it('renders the server-selected slices for a selector Service without listing the namespace', () => {
  mock.serviceQuery = { data: { items: [slice('web-abc', { 'kubernetes.io/service-name': 'web' })], truncated: false } }
  const html = render()
  expect(mock.serviceReads[0]).toEqual(['team', 'web', true])
  expect(mock.namespaceReads[0][3].enabled).toBe(false)
  expect(html).toContain('EndpointSlices')
  expect(html).toContain('web-abc')
  expect(html).not.toContain('Showing the first')
})

it('says when the server cut the inventory off', () => {
  mock.serviceQuery = { data: { items: [slice('web-abc')], truncated: true } }
  expect(render()).toContain('Showing the first 1 EndpointSlices.')
})

it('surfaces a failed lookup instead of an empty inventory', () => {
  mock.serviceQuery = { error: new Error('insufficient permissions to list endpointslices') }
  const html = render()
  expect(html).toContain('EndpointSlice inventory unavailable')
  expect(html).toContain('insufficient permissions to list endpointslices')
  expect(html).not.toContain('No EndpointSlices found')
})

it('does not query slices for ExternalName Services', () => {
  const html = render({ ...service, spec: { type: 'ExternalName', externalName: 'example.org' } })
  expect(mock.serviceReads[0][2]).toBe(false)
  expect(mock.namespaceReads[0][3].enabled).toBe(false)
  expect(html).not.toContain('EndpointSlices')
})

it('falls back to the label-filtered namespace list for selector-less Services on an older Radar', () => {
  mock.serviceQuery = { error: unsupported() }
  mock.namespaceQuery = { data: [
    slice('manual-abc', { 'kubernetes.io/service-name': 'web' }),
    slice('other-abc', { 'kubernetes.io/service-name': 'other' }),
  ] }
  const html = render(selectorless)
  expect(mock.namespaceReads[0]).toEqual(['endpointslices', 'team', 'discovery.k8s.io', { enabled: true, refetchInterval: 30000 }])
  expect(html).toContain('manual-abc')
  expect(html).not.toContain('other-abc')
  expect(html).not.toContain('EndpointSlice inventory unavailable')
})

it('keeps selector Services free of slice lookups on an older Radar', () => {
  mock.serviceQuery = { error: unsupported() }
  const html = render()
  expect(mock.namespaceReads[0][3].enabled).toBe(false)
  expect(html).not.toContain('EndpointSlices')
})
