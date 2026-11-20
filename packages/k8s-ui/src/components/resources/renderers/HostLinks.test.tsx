// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { IngressRenderer } from './IngressRenderer'
import { HTTPRouteRenderer } from './HTTPRouteRenderer'
import { GRPCRouteRenderer } from './GRPCRouteRenderer'
import type { GatewayLookup } from '../../../utils/host-url'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

let cleanup: (() => Promise<void>) | undefined
afterEach(async () => {
  await cleanup?.()
  cleanup = undefined
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})

async function mount(node: React.ReactNode) {
  const element = document.createElement('div')
  document.body.append(element)
  const root = createRoot(element)
  await act(async () => root.render(node))
  cleanup = async () => {
    await act(async () => root.unmount())
    element.remove()
  }
  return element
}

const ingress = {
  metadata: { name: 'shop', namespace: 'shop' },
  spec: {
    ingressClassName: 'traefik',
    tls: [{ hosts: ['shop.example.com'], secretName: 'shop-tls' }],
    rules: [
      { host: 'shop.example.com', http: { paths: [{ path: '/', pathType: 'Prefix', backend: { service: { name: 'web', port: { number: 80 } } } }] } },
      { host: 'admin.example.com', http: { paths: [{ path: '/admin', pathType: 'Prefix', backend: { service: { name: 'web', port: { number: 80 } } } }] } },
      { host: '*.apps.example.com', http: { paths: [{ path: '/', pathType: 'Prefix', backend: { service: { name: 'web', port: { number: 80 } } } }] } },
    ],
  },
}

const openLinks = (el: HTMLElement) =>
  [...el.querySelectorAll<HTMLAnchorElement>('a[target="_blank"]')].map(a => a.getAttribute('href'))

describe('hostname Open and Copy', () => {
  it('links each concrete Ingress host to its URL in a new tab and skips wildcard hosts', async () => {
    const el = await mount(<IngressRenderer data={ingress} />)
    expect(openLinks(el)).toEqual(['https://shop.example.com', 'http://admin.example.com/admin'])
    const link = el.querySelector<HTMLAnchorElement>('a[href="https://shop.example.com"]')!
    expect(link.rel).toContain('noopener')
    expect(el.querySelectorAll('button[aria-label^="Copy URL"]')).toHaveLength(2)
  })

  it('copies the URL', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const el = await mount(<IngressRenderer data={ingress} />)
    await act(async () => { el.querySelector<HTMLButtonElement>('button[aria-label="Copy URL http://admin.example.com/admin"]')!.click() })
    expect(writeText).toHaveBeenCalledWith('http://admin.example.com/admin')
  })

  const route = {
    metadata: { name: 'storefront', namespace: 'shop' },
    spec: { parentRefs: [{ name: 'main-gw' }], hostnames: ['store.example.com', '*.store.example.com'], rules: [] },
  }

  it('opens HTTPRoute hostnames with the scheme of the Gateway listener', async () => {
    const gateways: GatewayLookup[] = [{
      namespace: 'shop', name: 'main-gw', status: 'loaded',
      gateway: { spec: { listeners: [{ name: 'https', protocol: 'HTTPS', port: 443 }] } },
    }]
    const el = await mount(<HTTPRouteRenderer data={route} parentGateways={gateways} />)
    expect(openLinks(el)).toEqual(['https://store.example.com'])
  })

  it('offers only Copy hostname when the Gateway is not known', async () => {
    const el = await mount(<HTTPRouteRenderer data={route} />)
    expect(openLinks(el)).toEqual([])
    expect(el.querySelectorAll('button[aria-label="Copy hostname store.example.com"]')).toHaveLength(1)
  })

  it('shows no buttons while the Gateway is loading', async () => {
    const el = await mount(<HTTPRouteRenderer data={route} parentGateways={[{ namespace: 'shop', name: 'main-gw', status: 'loading' }]} />)
    expect(openLinks(el)).toEqual([])
    expect(el.querySelectorAll('button[aria-label^="Copy"]')).toHaveLength(0)
  })

  it('offers only Copy hostname for GRPCRoute hostnames, and nothing for wildcards', async () => {
    const grpc = {
      metadata: { name: 'orders', namespace: 'shop' },
      spec: { parentRefs: [{ name: 'main-gw' }], hostnames: ['grpc.example.com', '*.grpc.example.com'], rules: [] },
    }
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const el = await mount(<GRPCRouteRenderer data={grpc} />)
    expect(openLinks(el)).toEqual([])
    const copies = el.querySelectorAll<HTMLButtonElement>('button[aria-label^="Copy"]')
    expect(copies).toHaveLength(1)
    await act(async () => { copies[0].click() })
    expect(writeText).toHaveBeenCalledWith('grpc.example.com')
  })
})
