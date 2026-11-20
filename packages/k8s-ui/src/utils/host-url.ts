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

/** A hostname that names one host, normalized, or null for empty and wildcard
 * names. For routes a browser can't open (gRPC), it is all there is to copy. */
export function concreteHostname(host: string | undefined | null): string | null {
  const h = normalizeHost(host)
  return isConcreteHost(h) ? h : null
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
  status?: { listeners?: Array<{ name?: string; conditions?: Array<{ type?: string; status?: string }> }> }
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

/** Gateway parents named in spec.parentRefs. A route without any (attached to
 * default Gateways by its controller) uses the parents its status reports;
 * otherwise status entries can be left over from removed parents. */
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
  const specRefs: ParentRef[] = route?.spec?.parentRefs ?? []
  if (specRefs.length > 0) specRefs.forEach(add)
  else for (const parent of route?.status?.parents ?? []) add(parent?.parentRef)
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

type Admission = 'yes' | 'no' | 'maybe'

function listenerAdmitsRoute(listener: ListenerSpec, gatewayNamespace: string, routeNamespace: string): Admission {
  const kinds = listener.allowedRoutes?.kinds
  if (kinds && kinds.length > 0 && !kinds.some(k => (k.group ?? GATEWAY_GROUP) === GATEWAY_GROUP && k.kind === 'HTTPRoute')) {
    return 'no'
  }
  const from = listener.allowedRoutes?.namespaces?.from ?? 'Same'
  if (from === 'All') return 'yes'
  if (from === 'Same') return gatewayNamespace === routeNamespace ? 'yes' : 'no'
  // Selector: depends on namespace labels Radar doesn't read here.
  return 'maybe'
}

const FAILED_LISTENER_CONDITIONS = new Set(['Accepted', 'Programmed', 'ResolvedRefs'])

/** A listener the Gateway reports as not accepted, not programmed or with
 * unresolved references (a missing certificate) doesn't serve traffic. */
function listenerFailed(gateway: GatewayObject, name: string | undefined): boolean {
  const status = (gateway.status?.listeners ?? []).find(l => l.name === name)
  return (status?.conditions ?? []).some(c => FAILED_LISTENER_CONDITIONS.has(c.type ?? '') && c.status === 'False')
}

/** Where a Gateway rejected the route: the whole Gateway, named listeners, or
 * ports. Some controllers report a listener name the parentRef didn't give. */
function routeRejections(route: any, gatewayNamespace: string, gatewayName: string, routeNamespace: string): { all: boolean; sections: Set<string>; ports: Set<number> } {
  const out = { all: false, sections: new Set<string>(), ports: new Set<number>() }
  for (const p of route?.status?.parents ?? []) {
    const ref = p?.parentRef
    if (!isGatewayParent(ref) || ref.name !== gatewayName || (ref.namespace ?? routeNamespace) !== gatewayNamespace) continue
    if (!(p.conditions ?? []).some((c: any) => c.type === 'Accepted' && c.status === 'False')) continue
    if (ref.sectionName) out.sections.add(ref.sectionName)
    else if (ref.port !== undefined) out.ports.add(Number(ref.port))
    else out.all = true
  }
  return out
}

// Gateway API Exact and PathPrefix values are literal paths, never patterns.
const GATEWAY_PATH = /^\/[\x21-\x7e]*$/

/** Paths a browser's plain GET reaches. A match that needs another method, a
 * header or a query parameter isn't one. */
function routeBrowserPaths(route: any): { literals: string[]; servesRoot: boolean } {
  const literals: string[] = []
  let servesRoot = false
  for (const rule of route?.spec?.rules ?? []) {
    const matches = rule?.matches ?? []
    if (matches.length === 0) servesRoot = true
    for (const match of matches) {
      if ((match?.method && match.method !== 'GET') || match?.headers?.length || match?.queryParams?.length) continue
      if (!match?.path) {
        servesRoot = true
        continue
      }
      const type = match.path.type ?? 'PathPrefix'
      const value: string = match.path.value ?? '/'
      if ((type === 'PathPrefix' || type === 'Exact') && GATEWAY_PATH.test(value) && !/[?#]/.test(value)) literals.push(value)
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
 * Gateway can't be read, or a listener admits the route only through a
 * namespace selector, and no certain HTTPS listener was found, the scheme is
 * unknown and only the hostname is offered.
 */
export function routeHostTarget(hostname: string | undefined | null, route: any, gateways: GatewayLookup[] | undefined): HostTarget | null {
  const host = normalizeHost(hostname)
  if (!isConcreteHost(host)) return null
  const routeNamespace = route?.metadata?.namespace ?? ''
  const refs = routeParentRefs(route)
  if (refs.length === 0 || !gateways) return { kind: 'hostname', hostname: host }

  const lookups = new Map(gateways.map(g => [`${g.namespace}/${g.name}`, g]))
  let unread = false
  const candidates: Array<ListenerSpec & { admission: Admission }> = []
  for (const ref of refs) {
    const namespace = ref.namespace ?? routeNamespace
    const lookup = lookups.get(`${namespace}/${ref.name}`)
    if (!lookup || lookup.status === 'loading') return { kind: 'loading' }
    if (lookup.status !== 'loaded' || !lookup.gateway) {
      unread = true
      continue
    }
    const rejected = routeRejections(route, namespace, ref.name!, routeNamespace)
    if (rejected.all) continue
    const gateway = lookup.gateway
    const serving = (gateway.spec?.listeners ?? []).filter(l =>
      (l.protocol === 'HTTP' || l.protocol === 'HTTPS')
      && !listenerFailed(gateway, l.name)
      && listenerHostSpecificity(l.hostname, host) >= 0)
    // A request goes to the most specific listener on its port, whether or not
    // that listener takes this route.
    for (const l of serving) {
      const mine = listenerHostSpecificity(l.hostname, host)
      if (serving.some(o => Number(o.port) === Number(l.port) && listenerHostSpecificity(o.hostname, host) > mine)) continue
      if (ref.sectionName && l.name !== ref.sectionName) continue
      if (ref.port !== undefined && Number(l.port) !== Number(ref.port)) continue
      if ((l.name && rejected.sections.has(l.name)) || rejected.ports.has(Number(l.port))) continue
      const admission = listenerAdmitsRoute(l, namespace, routeNamespace)
      if (admission !== 'no') candidates.push({ ...l, admission })
    }
  }

  const sure = (protocol: string) => candidates.filter(l => l.protocol === protocol && l.admission === 'yes')
  const unsure = (protocol: string) => candidates.some(l => l.protocol === protocol && l.admission === 'maybe')
  const https = sure('HTTPS')
  const http = sure('HTTP')
  let origin: string
  if (https.length > 0) {
    const port = pickPort(https, 443)
    origin = `https://${host}${port === 443 ? '' : `:${port}`}`
  } else if (!unread && !unsure('HTTPS') && http.length > 0) {
    const port = pickPort(http, 80)
    origin = `http://${host}${port === 80 ? '' : `:${port}`}`
  } else {
    return { kind: 'hostname', hostname: host }
  }
  const { literals, servesRoot } = routeBrowserPaths(route)
  // Every rule needs another method, a header or a query parameter: a plain
  // visit reaches nothing, so there's nothing to open.
  if (!servesRoot && literals.length === 0 && (route?.spec?.rules ?? []).length > 0) return { kind: 'hostname', hostname: host }
  return { kind: 'url', url: `${origin}${choosePath(literals, servesRoot)}` }
}
