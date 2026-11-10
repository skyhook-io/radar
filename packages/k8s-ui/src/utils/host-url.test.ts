import { describe, expect, it } from 'vitest'
import { ingressHostUsesTLS, ingressRuleTarget, routeGateways, routeHostTarget, type GatewayLookup } from './host-url'

const rule = (host: string | undefined, paths: Array<{ path?: string; pathType?: string }> = [{ path: '/', pathType: 'Prefix' }]) =>
  ({ host, http: { paths } })

describe('ingressRuleTarget', () => {
  it('uses https only for hosts listed under spec.tls', () => {
    const tls = [{ hosts: ['shop.example.com'], secretName: 'shop-tls' }]
    expect(ingressRuleTarget(rule('shop.example.com'), tls)).toEqual({ kind: 'url', url: 'https://shop.example.com' })
    expect(ingressRuleTarget(rule('Shop.Example.com'), tls)).toEqual({ kind: 'url', url: 'https://shop.example.com' })
    expect(ingressRuleTarget(rule('admin.example.com'), tls)).toEqual({ kind: 'url', url: 'http://admin.example.com' })
    expect(ingressRuleTarget(rule('admin.example.com'), [])).toEqual({ kind: 'url', url: 'http://admin.example.com' })
  })

  it('lets a wildcard TLS host cover exactly one extra label', () => {
    const tls = [{ hosts: ['*.example.com'] }]
    expect(ingressRuleTarget(rule('a.example.com'), tls)).toEqual({ kind: 'url', url: 'https://a.example.com' })
    expect(ingressRuleTarget(rule('a.b.example.com'), tls)).toEqual({ kind: 'url', url: 'http://a.b.example.com' })
    expect(ingressRuleTarget(rule('example.com'), tls)).toEqual({ kind: 'url', url: 'http://example.com' })
  })

  it('offers only the hostname when a TLS entry without hosts leaves the scheme to the controller', () => {
    const tls = [{ secretName: 'default-cert' }, { hosts: ['shop.example.com'] }]
    expect(ingressRuleTarget(rule('admin.example.com'), tls)).toEqual({ kind: 'hostname', hostname: 'admin.example.com' })
    expect(ingressRuleTarget(rule('shop.example.com'), tls)).toEqual({ kind: 'url', url: 'https://shop.example.com' })
  })

  it('offers nothing for wildcard or empty hosts', () => {
    expect(ingressRuleTarget(rule('*.example.com'), [{ hosts: ['*.example.com'] }])).toBeNull()
    expect(ingressRuleTarget(rule(undefined), [])).toBeNull()
    expect(ingressRuleTarget(rule(''), [])).toBeNull()
  })

  it('opens the rule path only when it is a literal Prefix or Exact path', () => {
    const tls: never[] = []
    expect(ingressRuleTarget(rule('h.io', [{ path: '/admin', pathType: 'Prefix' }]), tls)).toEqual({ kind: 'url', url: 'http://h.io/admin' })
    expect(ingressRuleTarget(rule('h.io', [{ path: '/health', pathType: 'Exact' }]), tls)).toEqual({ kind: 'url', url: 'http://h.io/health' })
    expect(ingressRuleTarget(rule('h.io', [{ path: '/api', pathType: 'Prefix' }, { path: '/', pathType: 'Prefix' }]), tls)).toEqual({ kind: 'url', url: 'http://h.io' })
    expect(ingressRuleTarget(rule('h.io', [{ path: '/api(/|$)(.*)', pathType: 'ImplementationSpecific' }]), tls)).toEqual({ kind: 'url', url: 'http://h.io' })
    expect(ingressRuleTarget(rule('h.io', [{ path: '/api(/|$)(.*)', pathType: 'Prefix' }]), tls)).toEqual({ kind: 'url', url: 'http://h.io' })
    expect(ingressRuleTarget(rule('h.io', [{ path: '/v1/.*', pathType: 'Prefix' }, { path: '/docs', pathType: 'Prefix' }]), tls)).toEqual({ kind: 'url', url: 'http://h.io/docs' })
    expect(ingressRuleTarget({ host: 'h.io' }, tls)).toEqual({ kind: 'url', url: 'http://h.io' })
  })
})

describe('ingressHostUsesTLS', () => {
  it('matches a wildcard rule host only to the same wildcard TLS host', () => {
    expect(ingressHostUsesTLS('*.example.com', [{ hosts: ['*.example.com'] }])).toBe(true)
    expect(ingressHostUsesTLS('*.example.com', [{ hosts: ['a.example.com'] }])).toBe(false)
  })
})

const listener = (name: string, protocol: string, port: number, extra: Record<string, unknown> = {}) => ({ name, protocol, port, ...extra })
const gw = (namespace: string, name: string, listeners: unknown[]): GatewayLookup =>
  ({ namespace, name, status: 'loaded', gateway: { metadata: { namespace, name }, spec: { listeners } as any } })
const route = (parentRefs: unknown[], extra: { rules?: unknown[]; status?: unknown } = {}) => ({
  metadata: { namespace: 'shop', name: 'storefront' },
  spec: { parentRefs, hostnames: ['store.example.com'], rules: extra.rules ?? [{ matches: [{ path: { type: 'PathPrefix', value: '/' } }] }] },
  status: extra.status,
})

describe('routeHostTarget', () => {
  const bothListeners = gw('shop', 'main', [listener('http', 'HTTP', 80), listener('https', 'HTTPS', 443)])

  it('uses https when the route attaches to an HTTPS listener', () => {
    expect(routeHostTarget('store.example.com', route([{ name: 'main' }]), [bothListeners])).toEqual({ kind: 'url', url: 'https://store.example.com' })
  })

  it('uses http when the parentRef picks the HTTP listener', () => {
    expect(routeHostTarget('store.example.com', route([{ name: 'main', sectionName: 'http' }]), [bothListeners])).toEqual({ kind: 'url', url: 'http://store.example.com' })
    expect(routeHostTarget('store.example.com', route([{ name: 'main', port: 80 }]), [bothListeners])).toEqual({ kind: 'url', url: 'http://store.example.com' })
  })

  it('keeps a non-default port', () => {
    const g = gw('shop', 'main', [listener('alt', 'HTTPS', 8443)])
    expect(routeHostTarget('store.example.com', route([{ name: 'main' }]), [g])).toEqual({ kind: 'url', url: 'https://store.example.com:8443' })
  })

  it('ignores listeners whose hostname does not match, and honours wildcard listener hostnames', () => {
    const g = gw('shop', 'main', [
      listener('other', 'HTTPS', 443, { hostname: 'other.example.com' }),
      listener('web', 'HTTP', 80, { hostname: '*.example.com' }),
    ])
    expect(routeHostTarget('store.example.com', route([{ name: 'main' }]), [g])).toEqual({ kind: 'url', url: 'http://store.example.com' })
  })

  it('skips a listener that a more specific listener on the same port shadows', () => {
    const g = gw('shop', 'main', [
      listener('wild', 'HTTPS', 443, { hostname: '*.example.com' }),
      listener('exact', 'HTTPS', 443, { hostname: 'store.example.com' }),
      listener('http', 'HTTP', 80),
    ])
    const r = route([{ name: 'main', sectionName: 'wild' }, { name: 'main', sectionName: 'http' }])
    expect(routeHostTarget('store.example.com', r, [g])).toEqual({ kind: 'url', url: 'http://store.example.com' })
  })

  it('skips listeners that do not admit the route', () => {
    const r = route([{ name: 'edge', namespace: 'infra' }])
    const sameNsOnly = gw('infra', 'edge', [listener('https', 'HTTPS', 443), listener('http', 'HTTP', 80, { allowedRoutes: { namespaces: { from: 'All' } } })])
    expect(routeHostTarget('store.example.com', r, [sameNsOnly])).toEqual({ kind: 'url', url: 'http://store.example.com' })
    const grpcOnly = gw('infra', 'edge', [listener('https', 'HTTPS', 443, { allowedRoutes: { namespaces: { from: 'All' }, kinds: [{ kind: 'GRPCRoute' }] } })])
    expect(routeHostTarget('store.example.com', r, [grpcOnly])).toEqual({ kind: 'hostname', hostname: 'store.example.com' })
  })

  it('skips a Gateway that rejected the route', () => {
    const r = route([{ name: 'main' }, { name: 'plain' }], {
      status: { parents: [{ parentRef: { name: 'main' }, conditions: [{ type: 'Accepted', status: 'False' }] }] },
    })
    const plain = gw('shop', 'plain', [listener('http', 'HTTP', 80)])
    expect(routeHostTarget('store.example.com', r, [bothListeners, plain])).toEqual({ kind: 'url', url: 'http://store.example.com' })
  })

  it('offers only the hostname when the scheme cannot be read', () => {
    const r = route([{ name: 'main' }, { name: 'other' }])
    const unread: GatewayLookup = { namespace: 'shop', name: 'other', status: 'unavailable' }
    const httpOnly = gw('shop', 'main', [listener('http', 'HTTP', 80)])
    expect(routeHostTarget('store.example.com', r, [httpOnly, unread])).toEqual({ kind: 'hostname', hostname: 'store.example.com' })
    expect(routeHostTarget('store.example.com', r, [bothListeners, unread])).toEqual({ kind: 'url', url: 'https://store.example.com' })
    expect(routeHostTarget('store.example.com', route([{ name: 'main' }]), undefined)).toEqual({ kind: 'hostname', hostname: 'store.example.com' })
    expect(routeHostTarget('store.example.com', route([]), [])).toEqual({ kind: 'hostname', hostname: 'store.example.com' })
  })

  it('treats a namespace selector as unknown admission', () => {
    const r = route([{ name: 'edge', namespace: 'infra' }])
    const g = gw('infra', 'edge', [
      listener('http', 'HTTP', 80, { allowedRoutes: { namespaces: { from: 'All' } } }),
      listener('https', 'HTTPS', 443, { allowedRoutes: { namespaces: { from: 'Selector' } } }),
    ])
    expect(routeHostTarget('store.example.com', r, [g])).toEqual({ kind: 'hostname', hostname: 'store.example.com' })
  })

  it('ignores a listener the Gateway rejected', () => {
    const g = gw('shop', 'main', [listener('http', 'HTTP', 80), listener('https', 'HTTPS', 443)])
    g.gateway!.status = { listeners: [{ name: 'https', conditions: [{ type: 'Accepted', status: 'False' }] }] }
    expect(routeHostTarget('store.example.com', route([{ name: 'main' }]), [g])).toEqual({ kind: 'url', url: 'http://store.example.com' })
  })

  it('ignores a leftover status parent the spec no longer names', () => {
    const r = route([{ name: 'plain' }], { status: { parents: [{ parentRef: { name: 'old' }, conditions: [{ type: 'Accepted', status: 'True' }] }] } })
    const plain = gw('shop', 'plain', [listener('http', 'HTTP', 80)])
    const old = gw('shop', 'old', [listener('https', 'HTTPS', 443)])
    expect(routeHostTarget('store.example.com', r, [plain, old])).toEqual({ kind: 'url', url: 'http://store.example.com' })
  })

  it('keeps literal Gateway paths and skips matches a browser GET cannot reach', () => {
    const plus = route([{ name: 'main' }], { rules: [{ matches: [{ path: { type: 'Exact', value: '/foo+bar' } }] }] })
    expect(routeHostTarget('store.example.com', plus, [bothListeners])).toEqual({ kind: 'url', url: 'https://store.example.com/foo+bar' })
    const post = route([{ name: 'main' }], { rules: [
      { matches: [{ path: { type: 'Exact', value: '/' }, method: 'POST' }] },
      { matches: [{ path: { type: 'Exact', value: '/admin' }, method: 'GET' }] },
    ] })
    expect(routeHostTarget('store.example.com', post, [bothListeners])).toEqual({ kind: 'url', url: 'https://store.example.com/admin' })
    const header = route([{ name: 'main' }], { rules: [
      { matches: [{ headers: [{ name: 'x-canary', value: '1' }] }] },
      { matches: [{ path: { type: 'PathPrefix', value: '/shop' } }] },
    ] })
    expect(routeHostTarget('store.example.com', header, [bothListeners])).toEqual({ kind: 'url', url: 'https://store.example.com/shop' })
  })

  it('ignores a listener that is not programmed or has a broken certificate reference', () => {
    const g = gw('shop', 'main', [listener('http', 'HTTP', 80), listener('https', 'HTTPS', 443)])
    g.gateway!.status = { listeners: [{ name: 'https', conditions: [
      { type: 'Accepted', status: 'True' }, { type: 'ResolvedRefs', status: 'False' }, { type: 'Programmed', status: 'False' },
    ] }] }
    expect(routeHostTarget('store.example.com', route([{ name: 'main' }]), [g])).toEqual({ kind: 'url', url: 'http://store.example.com' })
  })

  it('lets a more specific listener shadow a wildcard one even when it only maybe admits the route', () => {
    const g = gw('shop', 'main', [
      listener('wild', 'HTTPS', 443, { hostname: '*.example.com' }),
      listener('exact', 'HTTPS', 443, { hostname: 'store.example.com', allowedRoutes: { namespaces: { from: 'Selector' } } }),
    ])
    expect(routeHostTarget('store.example.com', route([{ name: 'main' }]), [g])).toEqual({ kind: 'hostname', hostname: 'store.example.com' })
  })

  it('applies a rejection the controller reports for a listener the parentRef did not name', () => {
    const r = route([{ name: 'main' }], {
      status: { parents: [{ parentRef: { name: 'main', sectionName: 'web' }, conditions: [{ type: 'Accepted', status: 'False' }] }] },
    })
    const g = gw('shop', 'main', [listener('web', 'HTTP', 80)])
    expect(routeHostTarget('store.example.com', r, [g])).toEqual({ kind: 'hostname', hostname: 'store.example.com' })
  })

  it('offers only the hostname when no rule matches a plain browser visit', () => {
    const r = route([{ name: 'main' }], { rules: [
      { matches: [{ headers: [{ name: 'x-canary', value: '1' }] }] },
      { matches: [{ path: { type: 'PathPrefix', value: '/' }, method: 'POST' }] },
    ] })
    expect(routeHostTarget('store.example.com', r, [bothListeners])).toEqual({ kind: 'hostname', hostname: 'store.example.com' })
  })

  it('scopes a rejection reported for one port to that port', () => {
    const r = route([{ name: 'edge', namespace: 'infra', port: 80 }, { name: 'edge', namespace: 'infra', port: 443 }], {
      status: { parents: [
        { parentRef: { name: 'edge', namespace: 'infra', port: 80 }, conditions: [{ type: 'Accepted', status: 'False' }] },
        { parentRef: { name: 'edge', namespace: 'infra', port: 443 }, conditions: [{ type: 'Accepted', status: 'True' }] },
      ] },
    })
    const g = gw('infra', 'edge', [listener('http', 'HTTP', 80), listener('https', 'HTTPS', 443, { allowedRoutes: { namespaces: { from: 'All' } } })])
    expect(routeHostTarget('store.example.com', r, [g])).toEqual({ kind: 'url', url: 'https://store.example.com' })
  })

  it('waits while a parent Gateway is loading', () => {
    expect(routeHostTarget('store.example.com', route([{ name: 'main' }]), [{ namespace: 'shop', name: 'main', status: 'loading' }])).toEqual({ kind: 'loading' })
  })

  it('offers nothing for wildcard or empty hostnames', () => {
    expect(routeHostTarget('*.example.com', route([{ name: 'main' }]), [bothListeners])).toBeNull()
    expect(routeHostTarget('', route([{ name: 'main' }]), [bothListeners])).toBeNull()
  })

  it('opens a literal rule path when no rule serves the root', () => {
    const r = route([{ name: 'main' }], { rules: [{ matches: [{ path: { type: 'PathPrefix', value: '/admin' } }] }] })
    expect(routeHostTarget('store.example.com', r, [bothListeners])).toEqual({ kind: 'url', url: 'https://store.example.com/admin' })
    const regex = route([{ name: 'main' }], { rules: [{ matches: [{ path: { type: 'RegularExpression', value: '/a.*' } }] }] })
    expect(routeHostTarget('store.example.com', regex, [bothListeners])).toEqual({ kind: 'hostname', hostname: 'store.example.com' })
    const matchAll = route([{ name: 'main' }], { rules: [{ matches: [{ path: { type: 'PathPrefix', value: '/admin' } }] }, { backendRefs: [] }] })
    expect(routeHostTarget('store.example.com', matchAll, [bothListeners])).toEqual({ kind: 'url', url: 'https://store.example.com' })
  })
})

describe('routeGateways', () => {
  it('reads Gateway parents from spec, ignoring other parent kinds and leftover status entries', () => {
    const r = {
      metadata: { namespace: 'shop' },
      spec: { parentRefs: [{ name: 'main' }, { name: 'main', sectionName: 'https' }, { name: 'edge', namespace: 'infra' }, { name: 'svc', group: '', kind: 'Service' }] },
      status: { parents: [{ parentRef: { name: 'old-gw', namespace: 'gw-system' } }] },
    }
    expect(routeGateways(r)).toEqual([
      { namespace: 'shop', name: 'main' },
      { namespace: 'infra', name: 'edge' },
    ])
  })

  it('uses status parents for a route without parentRefs', () => {
    const r = { metadata: { namespace: 'shop' }, spec: {}, status: { parents: [{ parentRef: { name: 'default-gw', namespace: 'gw-system' } }] } }
    expect(routeGateways(r)).toEqual([{ namespace: 'gw-system', name: 'default-gw' }])
  })
})
