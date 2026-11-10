import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it } from 'vitest'
import { ServiceRenderer } from './ServiceRenderer'

const data = { metadata: { name: 'web', namespace: 'team' }, spec: { selector: { app: 'web' } } }
const props = { data, onCopy: () => {}, copied: null }
const slice = (name: string, ready = true) => ({
  metadata: { name },
  addressType: 'IPv4',
  endpoints: [{ addresses: ['10.0.0.1'], conditions: { ready } }],
})

it('renders supplied slices for a selector Service when the host enables the inventory', () => {
  const html = renderToStaticMarkup(<ServiceRenderer {...props} endpointSlicesEnabled endpointSlices={[slice('published', false)]} />)
  expect(html).toContain('EndpointSlices')
  expect(html).toContain('published')
  expect(html).toContain('0/1')
  expect(html).not.toContain('Showing the first')
})

it('keeps the section hidden for selector Services unless the host enables it', () => {
  expect(renderToStaticMarkup(<ServiceRenderer {...props} endpointSlices={[slice('published')]} />)).not.toContain('EndpointSlices')
  const selectorless = { ...data, spec: {} }
  expect(renderToStaticMarkup(<ServiceRenderer {...props} data={selectorless} endpointSlices={[slice('manual')]} />)).toContain('manual')
})

it('separates unavailable, loading and ready-empty inventory', () => {
  const error = renderToStaticMarkup(<ServiceRenderer {...props} endpointSlicesEnabled endpointSlices={[]} endpointSlicesError="Read denied" />)
  expect(error).toContain('EndpointSlice inventory unavailable')
  expect(error).toContain('Read denied')
  expect(error).not.toContain('No EndpointSlices found')
  expect(renderToStaticMarkup(<ServiceRenderer {...props} endpointSlicesEnabled endpointSlicesLoading />)).toContain('Loading EndpointSlices')
  expect(renderToStaticMarkup(<ServiceRenderer {...props} endpointSlicesEnabled endpointSlices={[]} />)).toContain('No EndpointSlices found')
})

it('says when the inventory was cut off', () => {
  const html = renderToStaticMarkup(<ServiceRenderer {...props} endpointSlicesEnabled endpointSlicesTruncated endpointSlices={[slice('a'), slice('b')]} />)
  expect(html).toContain('Showing the first 2 EndpointSlices.')
})

it('does not show slice inventory for ExternalName even when supplied', () => {
  expect(renderToStaticMarkup(<ServiceRenderer {...props} data={{ ...data, spec: { type: 'ExternalName', externalName: 'example.org' } }} endpointSlicesEnabled endpointSlices={[]} />)).not.toContain('EndpointSlices')
})
