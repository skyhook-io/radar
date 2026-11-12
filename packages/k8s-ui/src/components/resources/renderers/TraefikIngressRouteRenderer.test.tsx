import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { TraefikIngressRouteRenderer } from './TraefikIngressRouteRenderer'
import type { TraefikRouteFacts } from '../traefik-route-facts'

const data = {
  apiVersion: 'traefik.io/v1alpha1', kind: 'IngressRoute', metadata: { name: 'ui', namespace: 'shop' },
  spec: { entryPoints: ['websecure'], routes: [{ match: 'Host(`ui.example.com`)', kind: 'Rule', services: [{ name: 'ui', port: 80 }], middlewares: [{ name: 'redirect' }, { name: 'auth' }] }] },
}
const facts = (patch: Partial<TraefikRouteFacts> = {}): TraefikRouteFacts => ({
  ref: { group: 'traefik.io', namespace: 'shop', name: 'ui' }, rule: 0, match: 'Host(`ui.example.com`)', hosts: ['ui.example.com'], entryPoints: ['websecure'],
  middlewares: [{ namespace: 'shop', name: 'redirect', exists: false }, { namespace: 'shop', name: 'auth', exists: null }],
  services: [{ namespace: 'shop', name: 'ui', port: '80', kind: 'Service', exists: true, readyPods: 2, totalPods: 2 }],
  tls: { mode: 'secret', secret: 'ui-tls', certificate: 'read', notAfter: new Date(Date.now() + 5 * 864e5).toISOString() },
  problems: ['middleware_missing', 'cert_expiring'],
  ...patch,
})
const render = (routeFacts?: TraefikRouteFacts[] | null, middlewareUse?: Record<string, number>) =>
  renderToString(<TraefikIngressRouteRenderer data={data} routeFacts={routeFacts} middlewareUse={middlewareUse} />).replace(/<!-- -->/g, '')

describe('TraefikIngressRouteRenderer request path', () => {
  it('renders the request path with missing and can\'t-tell marks', () => {
    const html = render([facts()], { 'shop/redirect': 212 })
    expect(html).toContain('Request path')
    expect(html).toContain('missing')
    expect(html).toContain('2 of 2 pods ready')
    expect(html).toContain('Radar can&#x27;t confirm this reference')
    expect(html).toContain('used by 212 routes')
    expect(html).not.toContain('0 of 0')
  })
  it('never shows 0 of 0 for unknown pod counts', () => {
    const html = render([facts({ services: [{ namespace: 'shop', name: 'ui', port: '80', kind: 'Service', exists: null, readyPods: null, totalPods: null }] })])
    expect(html).not.toContain('0 of 0')
    expect(html).toContain('pods unknown')
  })
  it('renders today\'s drawer without facts', () => {
    expect(render(undefined)).not.toContain('Request path')
  })
  it('words the TLS line per mode', () => {
    expect(render([facts({ tls: { mode: 'resolver', resolver: 'le' } })])).toContain('ACME resolver')
    const denied = render([facts({ tls: { mode: 'secret', secret: 'ui-tls', certificate: 'denied' } })])
    expect(denied).toContain('no access')
    expect(render([facts()])).toMatch(/certificate expires in \d+d/)
    expect(render([facts({ tls: { mode: 'default' } })])).toContain('default certificate')
  })
})
