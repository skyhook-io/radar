// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'
import { HTTPRouteRenderer } from './HTTPRouteRenderer'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const mock = vi.hoisted(() => ({ reads: [] as unknown[][], responses: new Map<string, () => Promise<unknown>>() }))
vi.mock('../../../api/client', () => ({
  resourceQueryOptions: (kind: string, namespace: string, name: string, group: string) => {
    mock.reads.push([kind, namespace, name, group])
    return { queryKey: ['resource', kind, namespace, name, group], queryFn: mock.responses.get(`${namespace}/${name}`)!, retry: false }
  },
}))

const route = {
  metadata: { name: 'storefront', namespace: 'shop' },
  spec: { parentRefs: [{ name: 'main-gw' }], hostnames: ['store.example.com'], rules: [] },
}

async function mount() {
  const element = document.createElement('div')
  document.body.append(element)
  const root = createRoot(element)
  const client = new QueryClient()
  await act(async () => root.render(<QueryClientProvider client={client}><HTTPRouteRenderer data={route} /></QueryClientProvider>))
  await act(async () => { await new Promise(r => setTimeout(r, 0)) })
  return { element, done: async () => { await act(async () => root.unmount()); element.remove() } }
}

describe('HTTPRoute host wrapper', () => {
  it('reads the parent Gateway from the resource envelope and opens with its listener scheme', async () => {
    mock.reads = []
    mock.responses.set('shop/main-gw', async () => ({ resource: { spec: { listeners: [{ name: 'https', protocol: 'HTTPS', port: 443 }] } }, relationships: {} }))
    const { element, done } = await mount()
    expect(new Set(mock.reads.map(r => r.join('|')))).toEqual(new Set(['gateways|shop|main-gw|gateway.networking.k8s.io']))
    expect(element.querySelector('a[target="_blank"]')?.getAttribute('href')).toBe('https://store.example.com')
    await done()
  })

  it('falls back to copying the hostname when the Gateway read fails', async () => {
    mock.responses.set('shop/main-gw', async () => { throw new Error('forbidden') })
    const { element, done } = await mount()
    expect(element.querySelector('a[target="_blank"]')).toBeNull()
    expect(element.querySelector('button[aria-label^="Copy hostname"]')).not.toBeNull()
    await done()
  })
})
