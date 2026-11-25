import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { TraefikIngressRouteRenderer } from './TraefikIngressRouteRenderer'
import { tlsLine } from './TraefikRequestPath'
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
  it('names the missing member of a chain Middleware that exists, without marking the chain missing', () => {
    const html = render([facts({ middlewares: [{ namespace: 'shop', name: 'secure-chain', exists: true, missingTarget: 'Middleware "shop/gone-auth"' }] })])
    expect(html).toContain('references missing Middleware &quot;shop/gone-auth&quot;')
    expect(html).not.toMatch(/>missing</)
  })
  it('reads a middleware count as a lower bound when the caller sees only some routes', () => {
    const html = renderToString(<TraefikIngressRouteRenderer data={data} routeFacts={[facts()]} middlewareUse={{ 'shop/redirect': 3 }} middlewareUsePartial />).replace(/<!-- -->/g, '')
    expect(html).toContain('≥')
    expect(html).toContain('At least this many')
    expect(render([facts()], { 'shop/redirect': 3 })).not.toContain('≥')
  })
  it('draws several services as alternatives Traefik load-balances across, not a chain', () => {
    const svc = (name: string) => ({ namespace: 'shop', name, port: '80', kind: 'Service' as const, exists: true, readyPods: 1, totalPods: 1 })
    const html = render([facts({ middlewares: [], services: [svc('blue'), svc('green')] })])
    // entry point → match → services: one arrow before the services, not one per service.
    expect(html.match(/lucide-arrow-right/g)).toHaveLength(2)
    expect(html).toContain('load-balances')
  })
  it('starts a child route at its parent, not at entry points', () => {
    const html = render([facts({ parents: [{ group: 'traefik.io', namespace: 'gw', name: 'edge' }] })])
    expect(html).toMatch(/after<[^]*?>edge</)
    // The spec section still lists entryPoints; only the request path drops its badge.
    const count = (h: string) => h.split('websecure').length
    expect(count(html)).toBe(count(render([facts()])) - 1)
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
    const store = render([facts({ tls: { mode: 'default' } })])
    expect(store).toContain('TLS store')
    expect(store).not.toContain("Traefik's default certificate")
  })
  it('warns when the named Secret is not found, since Traefik serves another certificate', () => {
    const line = tlsLine({ mode: 'secret', secret: 'ui-tls', certificate: 'notFound' })
    expect(line.severity).toBe('warning')
    expect(line.text).toContain('another certificate')
  })
  it('shows a failed load as an alert', () => {
    const html = render(null)
    expect(html).toContain('Route facts could not be loaded')
    expect(html).toContain('bg-amber-50')
  })
})

it('says a route without spec.tls declares none, since an entry point can still add TLS', () => {
  expect(tlsLine({ mode: 'none' }).text).toBe('No TLS declared on this route; its entry point may still add it')
})

describe('tlsLine expiry boundary', () => {
  const now = Date.UTC(2026, 10, 12)
  const at = (days: number) => ({ mode: 'secret' as const, secret: 's', certificate: 'read' as const, notAfter: new Date(now + days * 864e5).toISOString() })
  it('turns amber only within 14 days, like the server', () => {
    expect(tlsLine(at(14.5), now).severity).toBeUndefined()
    expect(tlsLine(at(14.5), now).text).toContain('expires in 14d')
    expect(tlsLine(at(13.9), now).severity).toBe('warning')
  })
  it('says a certificate that just expired expired today', () => {
    const line = tlsLine(at(-0.2), now)
    expect(line.severity).toBe('error')
    expect(line.text).toContain('expired today')
    expect(tlsLine(at(-2.5), now).text).toContain('expired 2d ago')
  })
})

it('says why a TLS Secret is not read instead of waiting forever', () => {
  const line = (certificate: any) => tlsLine({ mode: 'secret', secret: 'ui-tls', certificate })?.text
  expect(line('loading')).toBe('Secret ui-tls: Secrets are still loading')
  expect(line('notCollected')).toBe('Secret ui-tls: Radar does not collect Secrets in this namespace')
  expect(line('failed')).toBe('Secret ui-tls: Radar could not load Secrets')
  expect(line('notLoaded')).toBe('Secret ui-tls: not loaded yet')
})

it('flags a backend port the Service does not expose', () => {
  const html = render([facts({ services: [{ namespace: 'shop', name: 'ui', port: '8080', kind: 'Service', exists: true, portMissing: true, readyPods: 2, totalPods: 2 }], problems: ['backend_port_missing'] })])
  expect(html).toContain('no port 8080 on this Service')
  expect(render([facts()])).not.toContain('no port')
})
