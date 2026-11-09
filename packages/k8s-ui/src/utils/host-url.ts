/**
 * Browser URLs for the hostnames an Ingress or HTTPRoute serves. A hostname
 * gets a URL only when Radar can name its scheme from the object itself
 * (Ingress spec.tls) or from the listeners of the Gateways the route attaches
 * to. Otherwise the host is offered for copying as a bare name.
 */

const GATEWAY_GROUP = 'gateway.networking.k8s.io'

export type HostTarget =
  | { kind: 'url'; url: string }
  /** The scheme can't be told; only the bare hostname is offered. */
  | { kind: 'hostname'; hostname: string }
  /** Still reading what decides the scheme. */
  | { kind: 'loading' }

function normalizeHost(host: string | undefined | null): string {
  return (host ?? '').trim().toLowerCase()
}

function isConcreteHost(host: string): boolean {
  return host !== '' && !host.includes('*')
}

// Plain URL path characters only. Prefix/Exact paths are literal to the API,
// but some controllers (ingress-nginx with use-regex) read them as patterns,
// so a path with regex syntax is never put into a URL.
const LITERAL_PATH = /^\/[A-Za-z0-9._~\-/%:@!&'=,;]*$/

/** The path to open: the host root when any literal path is `/` (or a rule
 * matches everything), else the first literal path, else the root. */
function choosePath(literals: string[], servesRoot: boolean): string {
  if (servesRoot || literals.length === 0 || literals.includes('/')) return ''
  return literals[0]
}

/** Ingress TLS hosts follow certificate matching: `*.example.com` covers
 * exactly one extra label. */
function ingressTLSHostMatches(tlsHost: string, host: string): boolean {
  const pattern = normalizeHost(tlsHost)
  if (!pattern) return false
  if (pattern === host) return true
  if (!pattern.startsWith('*.') || host.includes('*')) return false
  const suffix = pattern.slice(1)
  if (!host.endsWith(suffix)) return false
  const label = host.slice(0, -suffix.length)
  return label !== '' && !label.includes('.')
}

/** Whether a host is listed under the Ingress's spec.tls. */
export function ingressHostUsesTLS(host: string | undefined | null, tls: any[] | undefined | null): boolean {
  const h = normalizeHost(host)
  if (!h) return false
  return (tls ?? []).some((entry: any) =>
    (entry?.hosts ?? []).some((tlsHost: string) => ingressTLSHostMatches(tlsHost, h)))
}

function ingressLiteralPath(path: any): string | null {
  const type = path?.pathType ?? 'Prefix'
  if (type !== 'Prefix' && type !== 'Exact') return null
  const value = path?.path ?? '/'
  return LITERAL_PATH.test(value) ? value : null
}

/**
 * What to offer for one Ingress rule's host, or null when the host isn't a
 * single concrete name (empty or wildcard). https when spec.tls lists the host;
 * http when no TLS entry could cover it. A TLS entry without hosts covers
 * whatever its controller decides, so other hosts on that Ingress get only the
 * bare hostname.
 */
export function ingressRuleTarget(rule: any, tls: any[] | undefined | null): HostTarget | null {
  const host = normalizeHost(rule?.host)
  if (!isConcreteHost(host)) return null
  const https = ingressHostUsesTLS(host, tls)
  if (!https && (tls ?? []).some((entry: any) => (entry?.hosts ?? []).length === 0)) {
    return { kind: 'hostname', hostname: host }
  }
  const literals = (rule?.http?.paths ?? [])
    .map(ingressLiteralPath)
    .filter((p: string | null): p is string => p !== null)
  return { kind: 'url', url: `${https ? 'https' : 'http'}://${host}${choosePath(literals, false)}` }
}

export interface GatewayRef {
  namespace: string
  name: string
}

interface ListenerSpec {
  name?: string
  hostname?: string
  port?: number
  protocol?: string
  allowedRoutes?: {
    namespaces?: { from?: string }
    kinds?: Array<{ group?: string; kind?: string }>
  }
}

export interface GatewayObject {
  metadata?: { namespace?: string; name?: string }
  spec?: { listeners?: ListenerSpec[] }
}

export interface GatewayLookup extends GatewayRef {
  /** The Gateway once read; absent while loading or when it couldn't be read. */
  gateway?: GatewayObject
  status: 'loaded' | 'loading' | 'unavailable'
}

interface ParentRef {
  group?: string
  kind?: string
  namespace?: string
  name?: string
  sectionName?: string
  port?: number
}

function isGatewayParent(ref: ParentRef | undefined): ref is ParentRef & { name: string } {
  return Boolean(ref?.name) && (ref?.group ?? GATEWAY_GROUP) === GATEWAY_GROUP && (ref?.kind ?? 'Gateway') === 'Gateway'
}

function refKey(ref: ParentRef, routeNamespace: string): string {
  return [ref.namespace ?? routeNamespace, ref.name, ref.sectionName ?? '', ref.port ?? ''].join('/')
}

/** Gateway parents named in spec.parentRefs, plus those only status.parents
 * names (attachments the controller made, such as default Gateways). */
function routeParentRefs(route: any): ParentRef[] {
  const routeNamespace = route?.metadata?.namespace ?? ''
  const refs: ParentRef[] = []
  const seen = new Set<string>()
  const add = (ref: ParentRef | undefined) => {
    if (!isGatewayParent(ref)) return
    const key = refKey(ref, routeNamespace)
    if (seen.has(key)) return
    seen.add(key)
    refs.push(ref)
  }
  for (const ref of route?.spec?.parentRefs ?? []) add(ref)
  for (const parent of route?.status?.parents ?? []) add(parent?.parentRef)
  return refs
}

/** The Gateways a route's hostnames depend on, deduplicated. */
export function routeGateways(route: any): GatewayRef[] {
  const routeNamespace = route?.metadata?.namespace ?? ''
  const out = new Map<string, GatewayRef>()
  for (const ref of routeParentRefs(route)) {
    const namespace = ref.namespace ?? routeNamespace
    out.set(`${namespace}/${ref.name}`, { namespace, name: ref.name! })
  }
  return [...out.values()]
}

/** Gateway listener hostnames: `*.example.com` is a suffix match over any
 * number of labels; an unset hostname matches every host. Higher is more
 * specific; -1 means no match. */
function listenerHostSpecificity(listenerHost: string | undefined, host: string): number {
  const pattern = normalizeHost(listenerHost)
  if (!pattern) return 0
  if (pattern === host) return Number.MAX_SAFE_INTEGER
  if (pattern.startsWith('*.') && host.endsWith(pattern.slice(1)) && host.length > pattern.length - 1) {
    return pattern.length
  }
  return -1
}

function listenerAdmitsRoute(listener: ListenerSpec, gatewayNamespace: string, routeNamespace: string): boolean {
  const kinds = listener.allowedRoutes?.kinds
  if (kinds && kinds.length > 0 && !kinds.some(k => (k.group ?? GATEWAY_GROUP) === GATEWAY_GROUP && k.kind === 'HTTPRoute')) {
    return false
  }
  // A Selector depends on namespace labels Radar doesn't read here; it counts as admitting.
  const from = listener.allowedRoutes?.namespaces?.from ?? 'Same'
  return from !== 'Same' || gatewayNamespace === routeNamespace
}

function routeRejectedBy(route: any, ref: ParentRef, routeNamespace: string): boolean {
  const key = refKey(ref, routeNamespace)
  return (route?.status?.parents ?? []).some((p: any) =>
    isGatewayParent(p?.parentRef)
    && refKey(p.parentRef, routeNamespace) === key
    && (p.conditions ?? []).some((c: any) => c.type === 'Accepted' && c.status === 'False'))
}

function routeLiteralPaths(route: any): { literals: string[]; servesRoot: boolean } {
  const literals: string[] = []
  let servesRoot = false
  for (const rule of route?.spec?.rules ?? []) {
    const matches = rule?.matches ?? []
    if (matches.length === 0) servesRoot = true
    for (const match of matches) {
      if (!match?.path) {
        servesRoot = true
        continue
      }
      const type = match.path.type ?? 'PathPrefix'
      const value = match.path.value ?? '/'
      if ((type === 'PathPrefix' || type === 'Exact') && LITERAL_PATH.test(value)) literals.push(value)
    }
  }
  return { literals, servesRoot }
}

function pickPort(listeners: ListenerSpec[], preferred: number): number {
  const ports = listeners.map(l => Number(l.port)).filter(p => Number.isFinite(p) && p > 0)
  return ports.includes(preferred) ? preferred : ports[0] ?? preferred
}

/**
 * What to offer for an HTTPRoute hostname, or null for an empty or wildcard
 * hostname. The scheme comes from the Gateway listeners the route can attach
 * to and that would serve this host: https when any of them is HTTPS. When a
 * Gateway can't be read and none of the others has an HTTPS listener for the
 * host, the scheme is unknown and only the hostname is offered.
 */
export function routeHostTarget(hostname: string | undefined | null, route: any, gateways: GatewayLookup[] | undefined): HostTarget | null {
  const host = normalizeHost(hostname)
  if (!isConcreteHost(host)) return null
  const routeNamespace = route?.metadata?.namespace ?? ''
  const refs = routeParentRefs(route)
  if (refs.length === 0 || !gateways) return { kind: 'hostname', hostname: host }

  const lookups = new Map(gateways.map(g => [`${g.namespace}/${g.name}`, g]))
  let unread = false
  const candidates: ListenerSpec[] = []
  for (const ref of refs) {
    const namespace = ref.namespace ?? routeNamespace
    const lookup = lookups.get(`${namespace}/${ref.name}`)
    if (!lookup || lookup.status === 'loading') return { kind: 'loading' }
    if (lookup.status !== 'loaded' || !lookup.gateway) {
      unread = true
      continue
    }
    if (routeRejectedBy(route, ref, routeNamespace)) continue
    const listeners = lookup.gateway.spec?.listeners ?? []
    const attached = listeners.filter(l =>
      (!ref.sectionName || l.name === ref.sectionName)
      && (ref.port === undefined || Number(l.port) === Number(ref.port))
      && (l.protocol === 'HTTP' || l.protocol === 'HTTPS')
      && listenerAdmitsRoute(l, namespace, routeNamespace)
      && listenerHostSpecificity(l.hostname, host) >= 0)
    // A request goes to the most specific listener on its port; a route on a
    // less specific one never sees this host there.
    for (const l of attached) {
      const mine = listenerHostSpecificity(l.hostname, host)
      const shadowed = listeners.some(other =>
        !attached.includes(other)
        && Number(other.port) === Number(l.port)
        && listenerHostSpecificity(other.hostname, host) > mine)
      if (!shadowed) candidates.push(l)
    }
  }

  const https = candidates.filter(l => l.protocol === 'HTTPS')
  const http = candidates.filter(l => l.protocol === 'HTTP')
  let origin: string
  if (https.length > 0) {
    const port = pickPort(https, 443)
    origin = `https://${host}${port === 443 ? '' : `:${port}`}`
  } else if (!unread && http.length > 0) {
    const port = pickPort(http, 80)
    origin = `http://${host}${port === 80 ? '' : `:${port}`}`
  } else {
    return { kind: 'hostname', hostname: host }
  }
  const { literals, servesRoot } = routeLiteralPaths(route)
  return { kind: 'url', url: `${origin}${choosePath(literals, servesRoot)}` }
}
