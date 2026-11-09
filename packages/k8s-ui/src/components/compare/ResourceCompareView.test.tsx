// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { ResourceCompareView, type ResourceCompareViewProps } from './ResourceCompareView'

vi.mock('../ui/YamlEditor', () => ({ YamlDiffEditor: () => null }))

const checkout = { kind: 'Deployment', namespace: 'shop', name: 'checkout' }

function header(props: Partial<ResourceCompareViewProps>) {
  const html = renderToStaticMarkup(
    <ResourceCompareView a={checkout} b={checkout} aData={{}} bData={{}} onSwap={() => {}} onClose={() => {}} {...props} />,
  )
  // Tags split the name from its namespace, so compare the text a reader sees.
  const host = document.createElement('div')
  host.innerHTML = html
  return host.textContent ?? ''
}

describe('ResourceCompareView sides', () => {
  it('names the resource once and tells the sides apart by where they run', () => {
    const text = header({ aSubtitle: 'payments-eu', bSubtitle: 'payments-us' })
    expect(text.split('shop/checkout')).toHaveLength(2)
    expect(text).toContain('payments-eu')
    expect(text).toContain('payments-us')
  })

  it('keeps a cut-off location readable on hover', () => {
    const html = renderToStaticMarkup(
      <ResourceCompareView a={checkout} b={checkout} aData={{}} bData={{}} onSwap={() => {}} onClose={() => {}} aSubtitle="payments-eu" bSubtitle="payments-us" />,
    )
    expect(html).toContain('title="payments-eu"')
    expect(html).toContain('title="payments-us"')
  })

  it('names both sides when they are different resources', () => {
    const text = header({ b: { ...checkout, name: 'billing' }, aSubtitle: 'payments-eu', bSubtitle: 'payments-eu' })
    expect(text).toContain('shop/checkout')
    expect(text).toContain('shop/billing')
  })

  it('keeps names when the same kind and name come from different API groups', () => {
    const text = header({ a: { ...checkout, group: 'apps' }, b: { ...checkout, group: 'example.com' }, aSubtitle: 'eu', bSubtitle: 'us' })
    expect(text.split('shop/checkout')).toHaveLength(3)
  })

  it('keeps names when both sides come from the same place', () => {
    expect(header({ aSubtitle: 'payments-eu', bSubtitle: 'payments-eu' }).split('shop/checkout')).toHaveLength(3)
  })

  it('keeps names with no location to show', () => {
    expect(header({}).split('shop/checkout')).toHaveLength(3)
  })
})
