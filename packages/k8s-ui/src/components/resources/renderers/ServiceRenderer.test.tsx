import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it } from 'vitest'
import { ServiceRenderer } from './ServiceRenderer'
const data = { metadata: { name: 'web', namespace: 'team' }, spec: { selector: { app: 'web' } } }
const props = { data, onCopy: () => {}, copied: null }
it('renders queried slices for selected Services without inferring health from template selection', () => {
  const html = renderToStaticMarkup(<ServiceRenderer {...props} endpointSlicesEnabled endpointSlices={[{ metadata: { name: 'published' }, endpoints: [{ addresses: ['10.0.0.1'], conditions: { ready: false } }] }]} />)
  expect(html).toContain('EndpointSlices')
  expect(html).toContain('published')
  expect(html).toContain('0/1')
})
it('separates unavailable, loading, ready-empty and unprovided inventory', () => {
  expect(renderToStaticMarkup(<ServiceRenderer {...props} />)).not.toContain('EndpointSlices')
  const error = renderToStaticMarkup(<ServiceRenderer {...props} endpointSlicesEnabled endpointSlices={[]} endpointSlicesError="Read denied" />)
  expect(error).toContain('EndpointSlice inventory unavailable')
  expect(error).not.toContain('No EndpointSlices found')
  expect(renderToStaticMarkup(<ServiceRenderer {...props} endpointSlicesEnabled endpointSlicesLoading />)).toContain('Loading EndpointSlices')
  expect(renderToStaticMarkup(<ServiceRenderer {...props} endpointSlicesEnabled endpointSlices={[]} />)).toContain('No EndpointSlices found')
})
it('does not show slice inventory for ExternalName even when supplied', () => {
  expect(renderToStaticMarkup(<ServiceRenderer {...props} data={{ ...data, spec: { type: 'ExternalName', externalName: 'example.org' } }} endpointSlicesEnabled endpointSlices={[]} />)).not.toContain('EndpointSlices')
})

it('marks unlabeled owned slices without describing them as published endpoints', () => {
  const owned = { apiVersion: 'discovery.k8s.io/v1', kind: 'EndpointSlice', metadata: { namespace: 'team', name: 'owned', ownerReferences: [{ apiVersion: 'v1', kind: 'Service', name: 'web', uid: 'web-now' }] } }
  const html = renderToStaticMarkup(<ServiceRenderer {...props} endpointSlicesEnabled endpointSlices={[owned]} />)
  expect(html).toContain('Owner reference only')
  expect(html).toContain('ownership does not establish published Service endpoints')
})

it('filters raw namespace inventory in the shared renderer, independently from the host label set', () => {
  const owned = { apiVersion:'discovery.k8s.io/v1',kind:'EndpointSlice',metadata:{name:'owned',namespace:'team',ownerReferences:[{apiVersion:'v1',kind:'Service',name:'web',uid:'web-now'}]} }
  const replaced = {...owned,metadata:{...owned.metadata,name:'replaced',ownerReferences:[{apiVersion:'v1',kind:'Service',name:'web',uid:'web-before'}]}}
  const html=renderToStaticMarkup(<ServiceRenderer {...props} data={{...data,metadata:{...data.metadata,uid:'web-now'}}} endpointSlicesEnabled endpointSlices={[]} endpointSliceInventory={[owned,replaced]} />)
  expect(html).toContain('owned');expect(html).not.toContain('replaced')
})
