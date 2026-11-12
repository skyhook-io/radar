// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import { TraefikMiddlewareRenderer } from './TraefikMiddlewareRenderer'

const data = { apiVersion: 'traefik.io/v1alpha1', kind: 'Middleware', metadata: { name: 'redirect', namespace: 'shop' }, spec: { redirectScheme: { scheme: 'https' } } }
const render = (usedBy?: any) => renderToString(<TraefikMiddlewareRenderer data={data} usedBy={usedBy} />).replace(/<!-- -->/g, '')

describe('TraefikMiddlewareRenderer used by', () => {
  it('lists the routes using the middleware once each', () => {
    const html = render({ routes: [{ namespace: 'shop', name: 'ui' }, { namespace: 'shop', name: 'ui' }, { namespace: 'shop', name: 'api' }], truncated: false, partial: false })
    expect(html).toContain('Used by 2 routes')
    expect(html).toContain('api')
  })
  it('never reads partial coverage as unused', () => {
    const html = render({ routes: [], truncated: false, partial: true })
    expect(html).not.toContain('No IngressRoute references this Middleware')
    expect(html).toContain('in namespaces you can view')
    expect(render({ routes: [], truncated: false, partial: false })).toContain('No IngressRoute references this Middleware directly')
  })
  it('says chains and provider references are not counted', () => {
    expect(render({ routes: [], truncated: false, partial: false })).toContain('chain Middlewares')
  })
  it('links a route under its own API group', () => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
    const onNavigate = vi.fn()
    const el = document.createElement('div')
    const root = createRoot(el)
    act(() => root.render(<TraefikMiddlewareRenderer data={data} onNavigate={onNavigate} usedBy={{ routes: [{ group: 'traefik.containo.us', namespace: 'shop', name: 'old' }], truncated: false, partial: false }} />))
    const link = [...el.querySelectorAll('button')].find((b) => b.textContent === 'shop/old')
    act(() => link?.click())
    expect(onNavigate).toHaveBeenCalledWith(expect.objectContaining({ kind: 'ingressroutes', group: 'traefik.containo.us', name: 'old' }))
    act(() => root.unmount())
  })
  it('keeps the drawer unchanged without the prop', () => {
    expect(render(undefined)).not.toContain('Used by')
  })
})
