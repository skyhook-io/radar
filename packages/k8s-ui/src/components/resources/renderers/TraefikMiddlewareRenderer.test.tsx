import { describe, expect, it } from 'vitest'
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
    expect(html).not.toContain('No IngressRoute uses this Middleware')
    expect(html).toContain('in namespaces you can view')
    expect(render({ routes: [], truncated: false, partial: false })).toContain('No IngressRoute uses this Middleware')
  })
  it('keeps the drawer unchanged without the prop', () => {
    expect(render(undefined)).not.toContain('Used by')
  })
})
