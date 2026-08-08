import type { Trace, RouteResult, RouteOutcome, ProbeResult, Hop, VantageResult } from './types'

/**
 * A Mark is the evidence class of ONE path segment for one scenario and one
 * origin. It is deliberately distinct from health: a node's dot says whether a
 * resource is well, a mark says what we actually learned about traffic reaching
 * it. The two must never be conflated - a healthy node behind a failed edge
 * stays healthy and the edge stays red.
 *
 * The vocabulary is closed. Adding a member means deciding its glyph, its tone,
 * and its line style, because every mark renders in all three.
 */
export type Mark =
  | 'proved' // observed through the dataplane - real packets, real path
  | 'answered' // something replied, but not the thing we asked for
  | 'proxied' // reached via the apiserver proxy - bypasses the dataplane
  | 'config' // declared in configuration; predicted, never observed
  | 'failed' // confirmed failure
  | 'blocked' // never ran because an earlier segment failed
  | 'excluded' // not in the path by design (NotReady, not an eligible endpoint)
  | 'untested' // no origin has exercised this
  | 'inconclusive' // ran, and deliberately kept informational - never a verdict
  | 'stale' // observed, but before a change that invalidates it
  | 'running' // in flight right now
  | 'denied' // we are not permitted to run this test
  | 'slow' // answered, far outside the normal latency band

export interface MarkStyle {
  glyph: string
  /** CSS custom property reference - never a literal color. */
  color: string
  /** SVG stroke-dasharray; 'none' for the solid lines that carry proof. */
  dash: string
  strokeWidth: number
  strokeOpacity: number
}

/**
 * Only `proved` and `failed` draw solid. Solid means "a packet did this" -
 * everything softer is dashed so a predicted or relayed segment can never be
 * misread as observed truth at a glance.
 */
export const MARKS: Record<Mark, MarkStyle> = {
  proved: { glyph: '●', color: 'var(--color-success-dark)', dash: 'none', strokeWidth: 2.6, strokeOpacity: 1 },
  answered: { glyph: '◑', color: 'var(--color-warning-dark)', dash: '3 6', strokeWidth: 1.8, strokeOpacity: 1 },
  proxied: { glyph: '◐', color: 'var(--color-info)', dash: '8 5', strokeWidth: 1.8, strokeOpacity: 1 },
  config: { glyph: '◇', color: 'var(--text-tertiary)', dash: '2 5', strokeWidth: 1.8, strokeOpacity: 1 },
  failed: { glyph: '✕', color: 'var(--color-error-dark)', dash: 'none', strokeWidth: 2.6, strokeOpacity: 1 },
  blocked: { glyph: '⊘', color: 'var(--text-disabled)', dash: '3 6', strokeWidth: 1.8, strokeOpacity: 0.75 },
  excluded: { glyph: '⊗', color: 'var(--text-disabled)', dash: '3 6', strokeWidth: 1.8, strokeOpacity: 0.75 },
  // Info-blue, not amber: amber is "answered with a caveat / needs attention"
  // (answered, slow, stale, denied); blue is "not the real path / disposition"
  // (proxied, running, inconclusive); grey is "nothing ran". Three families
  // instead of one overloaded amber.
  inconclusive: { glyph: '◍', color: 'var(--color-info)', dash: '3 6', strokeWidth: 1.8, strokeOpacity: 0.9 },
  untested: { glyph: '○', color: 'var(--text-disabled)', dash: '3 6', strokeWidth: 1.8, strokeOpacity: 0.75 },
  stale: { glyph: '◷', color: 'var(--color-warning-dark)', dash: '6 4', strokeWidth: 1.8, strokeOpacity: 1 },
  running: { glyph: '◌', color: 'var(--color-info)', dash: '4 4', strokeWidth: 1.8, strokeOpacity: 1 },
  denied: { glyph: '⊘', color: 'var(--color-warning-dark)', dash: '3 6', strokeWidth: 1.8, strokeOpacity: 1 },
  slow: { glyph: '◔', color: 'var(--color-warning-dark)', dash: '3 6', strokeWidth: 1.8, strokeOpacity: 1 },
}

/** One vocabulary, shared with the lane labels and the sidebar. The legend used
 *  to say "observed through the dataplane" while the lane said "REAL TRAFFIC"
 *  for the same thing.
 *
 *  ONE flat registry; `category` is metadata for grouping at render time. The
 *  flat list teaches the enum as one axis, which it is not - what happened,
 *  how it was tested, and why nothing ran are different questions. */
export type MarkCategory = 'happened' | 'tested' | 'why-not' | 'state'
export const MARK_LEGEND: { mark: Mark; text: string; category: MarkCategory }[] = [
  { mark: 'proved', text: 'a request got through', category: 'happened' },
  // 'answered' also wears a proxy-only failure (nothing answered there), so the
  // legend describes the CLASS - an attempt that fell short of verification -
  // and leaves "answered" vs "couldn't get through" to the chip.
  { mark: 'answered', text: 'the attempt didn’t verify the asked-for path', category: 'happened' },
  { mark: 'failed', text: 'a request was refused', category: 'happened' },
  { mark: 'proxied', text: 'answered via the API server — not live traffic', category: 'tested' },
  { mark: 'inconclusive', text: 'tested, but kept informational — a throwaway identity can\u2019t condemn the path', category: 'tested' },
  { mark: 'config', text: 'configured this way — not tested', category: 'tested' },
  { mark: 'untested', text: 'not tested from here', category: 'why-not' },
  // 'blocked' covers two honest cases - a segment downstream of a failure, and
  // a vantage whose every dial was skipped or abandoned. The legend must not
  // pick one: "never tried" was false for a proxy dial that ran and timed out.
  { mark: 'blocked', text: 'never completed — an earlier failure or a skip stopped it', category: 'why-not' },
  { mark: 'denied', text: 'not allowed to test this', category: 'why-not' },
  { mark: 'excluded', text: 'not sent any traffic', category: 'why-not' },
  { mark: 'running', text: 'testing now', category: 'state' },
  { mark: 'stale', text: 'out of date', category: 'state' },
  { mark: 'slow', text: 'answered, but very slowly', category: 'state' },
]

export const MARK_CATEGORY_LABEL: Record<MarkCategory, string> = {
  happened: 'what happened',
  tested: 'how it was tested',
  'why-not': 'why nothing ran',
  state: 'state',
}

export function markStyle(m: Mark): MarkStyle {
  return MARKS[m] ?? MARKS.untested
}

/** Hover text for a mark. The legend sits below the graph and was the ONLY
 *  decoder, so reading a symbol meant travelling to it and back every time. */
export function markHelp(m: Mark): string {
  return MARK_LEGEND.find((l) => l.mark === m)?.text ?? m
}

/**
 * What a LINE means, when the generic mark help would mislead.
 *
 * Structural edges are drawn `config` because nothing was dialled along them -
 * but "not tested" then invites the reader to ask why we didn't, and the honest
 * answer differs per edge: ownership is not a traffic hop at all, a selector is
 * read from the cluster rather than proven by a request, and declared routing
 * can only be exercised by a request that actually goes through that entry.
 * Saying "not tested" for all three trains the reader to expect a test that in
 * two of the cases does not exist.
 */
export function edgeHelp(label: string, mark: Mark): string {
  if (mark === 'config') {
    switch (label.trim().toLowerCase()) {
      case 'runs':
        return 'ownership, not a traffic hop - the workload owns these Pods'
      case 'selects':
        return 'read from the cluster - which Pods this Service selects'
      case 'routes to':
      case 'sends to':
        return 'declared routing - only a request through this entry exercises it'
      case 'also serves':
        return 'other backends this entry declares, collapsed here'
    }
  }
  // Excluded-by-design, not a gap: this backend serves a different host, so no
  // request for THIS path would ever go down it. "not sent any traffic" read as
  // something we skipped.
  if (mark === 'excluded' && label.trim().toLowerCase() === 'other host') {
    return 'not part of this path - this backend serves a different host'
  }
  return markHelp(mark)
}

/** Whether a line's own words already say what its mark would say. An origin
 *  edge is labelled with its evidence ("not tested"), so appending the mark help
 *  ("not tested from here") repeated it back at the reader. */
export function edgeHelpIsRedundant(title: string, label: string, mark: Mark): boolean {
  const t = title.trim().toLowerCase()
  if (!t) return false
  const help = edgeHelp(label, mark).toLowerCase()
  return help.startsWith(t) || t.startsWith(help)
}

/** Inline style for a mark glyph. */
export function glyphStyle(m: Mark): React.CSSProperties {
  return { color: markStyle(m).color, fontWeight: 700, fontSize: '12px', lineHeight: 1.1, flex: 'none' }
}

/**
 * The selected origin's OWN result for a route, or undefined when that origin
 * produced nothing for it.
 *
 * Origin ids map onto (vantage, path) exactly as originOf() derives them in the
 * other direction: anything relayed by the API server is the apiserver origin
 * whatever machine issued it, and everything else is named by its vantage.
 */
export function routeForOrigin(route: RouteResult | undefined, originId: string, runVantage?: string): VantageResult | undefined {
  const rows = route?.byVantage
  if (!rows || rows.length === 0) return undefined
  if (originId === 'apiserver') return rows.find((v) => v.path === 'apiserver')
  if (originId === 'local') return rows.find((v) => v.path !== 'apiserver' && v.vantage === 'local')
  // Both in-cluster origins share (in-cluster, data); `source` is what tells
  // Radar's own dial apart from the throwaway Job's. An absent source resolves
  // the same way originOf does, so the rail and the rows can't disagree.
  const wantJob = originId === 'incluster'
  return rows.find((v) => {
    if (v.path === 'apiserver' || v.vantage !== 'in-cluster') return false
    const src = v.source || (runVantage === 'in-cluster' ? 'radar' : 'probe-job')
    return wantJob ? src === 'probe-job' : src === 'radar'
  })
}

/**
 * What we can honestly say about a route FROM one origin. Four cases that must
 * never be collapsed:
 *
 *  - `own`     this origin's own result for this route. Use it.
 *  - `config`  the producer says the outcome was DERIVED, not dialled (`basis`):
 *              read off what is declared, or off current cluster state. It is
 *              true of every origin and observed by none, so it is rendered as
 *              the fact it is and never as the selected origin's dial.
 *  - `none`    the producer DID send per-vantage results and this origin has no
 *              row, so it did not test this route. Not "unknown, guess from the
 *              rollup" - the absence is itself the evidence, and inheriting the
 *              merged verdict here is precisely the misattribution byVantage
 *              exists to remove. What the origin did on OTHER routes is
 *              irrelevant to this one.
 *  - `rollup`  no per-vantage results at all (a trace from a producer that
 *              predates the field). The merged verdict is all there is; it is
 *              NOT a claim about the selected origin, and callers must keep
 *              treating it with the coarse pre-existing gate.
 */
export type OriginEvidence =
  | { kind: 'own'; result: RouteResult }
  | { kind: 'config'; result: RouteResult }
  | { kind: 'rollup'; result: RouteResult }
  | { kind: 'none' }

export function originRouteEvidence(route: RouteResult | undefined, originId: string, runVantage?: string): OriginEvidence {
  if (!route) return { kind: 'none' }
  // Checked before the per-vantage rows: a config-derived break has none by
  // construction, and without this it would fall through to the rollup branch
  // and be attributed to whichever origin happened to be selected.
  if (route.basis) return { kind: 'config', result: route }
  const own = routeForOrigin(route, originId, runVantage)
  if (own) {
    return {
      kind: 'own',
      result: { ...route, outcome: own.outcome, confidence: own.confidence, evidence: own.evidence, failedLayer: own.failedLayer },
    }
  }
  const hasBreakdown = !!route.byVantage && route.byVantage.length > 0
  return hasBreakdown ? { kind: 'none' } : { kind: 'rollup', result: route }
}

/** The route as ONE origin saw it, or undefined when that origin has nothing to
 *  say about it. Prefer originRouteEvidence when the caller needs to tell a
 *  legacy rollup apart from a genuine "not tested from here". */
export function routeAsSeenFrom(route: RouteResult | undefined, originId: string): RouteResult | undefined {
  const ev = originRouteEvidence(route, originId)
  return ev.kind === 'none' ? undefined : ev.result
}

/**
 * The evidence class of a route's headline result.
 *
 * `confidence` is what separates proof from relay: an 'indirect' outcome came
 * through the apiserver proxy, which bypasses kube-proxy, NetworkPolicy and the
 * mesh - so it can never be `proved` no matter how clean the response was.
 * A benign unreachable (deliberately scaled to zero) is not a failure.
 */
export function routeMark(r: RouteResult, opts: { stale?: boolean; running?: boolean } = {}): Mark {
  if (opts.running) return 'running'
  if (opts.stale) return 'stale'
  // A derived break was never dialled. 'failed' is the mark for a request that
  // was sent and did not arrive, so using it here claims an observation that
  // never happened - the contradiction the basis field exists to end.
  if (r.basis) return 'config'
  const indirect = r.confidence === 'indirect'
  switch (r.outcome) {
    case 'verified':
      return indirect ? 'proxied' : 'proved'
    case 'reached':
      return indirect ? 'proxied' : 'answered'
    case 'server-error':
      return 'answered'
    case 'unreachable':
      // A proxy-only failure never condemns the real path - it was never tested.
      if (indirect) return 'answered'
      return r.benign ? 'excluded' : 'failed'
    case 'not-tested':
    default:
      return 'untested'
  }
}

/** A route outcome's severity tone, for the scenario tab dot and verdict dot. */
export type SevTone = 'healthy' | 'degraded' | 'alert' | 'unhealthy' | 'unknown' | 'info'

export const SEV_COLOR: Record<SevTone, string> = {
  healthy: 'var(--color-success)',
  degraded: 'var(--color-warning)',
  alert: 'var(--color-alert)',
  unhealthy: 'var(--color-error)',
  unknown: 'var(--text-disabled)',
  info: 'var(--color-info)',
}

/** Badge class for a tone - the repo's canonical `.status-*` vocabulary. */
export const SEV_BADGE: Record<SevTone, string> = {
  healthy: 'status-healthy',
  degraded: 'status-degraded',
  alert: 'status-alert',
  unhealthy: 'status-unhealthy',
  unknown: 'status-unknown',
  info: 'status-neutral',
}

export function routeTone(r: RouteResult, opts: { stale?: boolean; running?: boolean } = {}): SevTone {
  if (opts.running) return 'info'
  if (opts.stale) return 'unknown'
  const indirect = r.confidence === 'indirect'
  switch (r.outcome) {
    case 'verified':
      return indirect ? 'degraded' : 'healthy'
    case 'reached':
    case 'server-error':
      return 'degraded'
    case 'unreachable':
      // Indirect-only and benign unreachables are never red - see routeMark.
      return indirect || r.benign ? 'degraded' : 'unhealthy'
    case 'not-tested':
    default:
      return 'unknown'
  }
}

/** Short qualifier chip for a scenario tab - what kind of evidence backs it. */
/** Every row's dial bypassed the front door - rows exist and none exercised
 *  the entry path. The chip and headline must then never read as a bare pass. */
export function routeBackendScoped(r: RouteResult): boolean {
  const rows = r.byVantage ?? []
  return rows.length > 0 && rows.every((v) => v.segment === 'backend')
}

export function routeChip(r: RouteResult, opts: { stale?: boolean; running?: boolean } = {}): string {
  if (opts.running) return 'probing…'
  if (opts.stale) return 'stale'
  // Named for what was read, not for a request: "could not get through" would
  // describe traffic that was never sent.
  if (r.basis === 'declared-config') return 'broken as declared'
  if (r.basis === 'cluster-state') return 'nothing ready to serve'
  const indirect = r.confidence === 'indirect'
  switch (r.outcome) {
    case 'verified':
      if (indirect) return 'got through via the API server'
      // The segment qualifier APPENDS to the outcome word, never replaces it:
      // proof strength stays outcome-derived, so a future transport-only reach
      // reads "answered · backend", not "verified".
      return routeBackendScoped(r) ? 'got through · backend' : 'got through'
    case 'reached':
      if (indirect) return 'answered via the API server'
      return routeBackendScoped(r) ? 'answered · backend' : 'answered, not confirmed'
    case 'server-error':
      // server-error is NEVER an ordinary app 5xx (those classify as reached):
      // it is a TLS verification failure or a gateway's 502/504 - blaming "the
      // app" here named the one party that is not at fault.
      if (r.failedLayer === 'tls') return 'TLS certificate failed'
      if (r.failedLayer === 'upstream') return 'the front door couldn’t reach the backend'
      return 'answered with a server-side fault'
    case 'unreachable':
      // "only the shortcut failed" left "shortcut" undefined on the page.
      if (indirect) return 'couldn’t get through via the API server'
      return r.benign ? 'nothing running (on purpose)' : 'could not get through'
    case 'not-tested':
    default:
      return 'not tested'
  }
}

const OUTCOME_RANK: Record<RouteOutcome, number> = {
  unreachable: 0,
  'server-error': 1,
  'not-tested': 2,
  reached: 3,
  verified: 4,
}

/** Worst-first ordering, so the scenario that needs attention leads the strip. */
export function orderRoutes(routes: RouteResult[]): RouteResult[] {
  return [...routes].sort((a, b) => OUTCOME_RANK[a.outcome] - OUTCOME_RANK[b.outcome])
}

/**
 * A scenario is one testable path as an operator thinks about it. It is NOT
 * always one RouteResult: a Gateway route declaring several hostnames produces
 * one result per host, and when those results agree in every respect they are
 * one situation with several front doors, not several situations.
 */
export interface Scenario {
  key: string
  /** Tab title. */
  label: string
  /** Secondary line - the backend, plus the host count when grouped. */
  sub: string
  /** Every route folded into this scenario. */
  routes: RouteResult[]
  /** Representative used for tone, chip and the graph. */
  primary: RouteResult
  /** The distinct entry hostnames, when this scenario groups several. */
  hosts: string[]
  /** Every route label folded into this tab, for the hover. Not all of them are
   *  hostnames - a Service port and a Pod both appear here as themselves. */
  members: string[]
  /** Name of the front door serving this scenario, when one is declared. */
  entry?: string
}

/** The host part of a route label like "example.com/path". */
function hostOf(route: string): string {
  const slash = route.indexOf('/')
  return slash === -1 ? route : route.slice(0, slash)
}

/**
 * The request host a route label names, or "" when it names none.
 *
 * Route labels are NOT all hostnames: the producer emits "host+path" for
 * host-based rules but ":80 → 8080" for port-based ones and "default backend"
 * where no host is declared (internal/trace routeLabel). Feeding those to a
 * host matcher is how a Gateway listener with no hostname - which legitimately
 * serves ANY host - ends up claiming a port-based route that never went near it.
 */
export function routeHostOf(route: string): string {
  const candidate = hostOf(route).trim().toLowerCase()
  // A dot is required. Without it "GET / · :80 → 8080" yields the bare word
  // "get", which reads as a valid single-label host and gets attributed to a
  // catch-all listener. A genuinely single-label host loses its front-door tag,
  // which is the safe direction to fail: no attribution beats a wrong one.
  return /^[a-z0-9*][a-z0-9*-]*(\.[a-z0-9*-]+)+$/.test(candidate) ? candidate : ''
}

/**
 * Whether a declared hostname serves a request host. Gateway API and Ingress
 * both allow a leading `*.` wildcard, which matches exactly one extra label -
 * `*.example.com` covers `api.example.com` but NOT `a.b.example.com` or the
 * apex. An EMPTY declared hostname is the Gateway-API "any host" listener.
 */
export function hostMatches(declared: string, host: string): boolean {
  const d = declared.trim().toLowerCase()
  const h = host.trim().toLowerCase()
  if (!h) return false
  if (d === '') return true
  if (!d.startsWith('*.')) return d === h
  const suffix = d.slice(1) // ".example.com"
  if (!h.endsWith(suffix)) return false
  return !h.slice(0, h.length - suffix.length).includes('.')
}

/**
 * Every hostname a hop declares. Ingress/HTTPRoute/GRPCRoute publish them on
 * `hostnames` (+ `tlsHosts`); a GATEWAY publishes them per listener and has no
 * top-level `hostnames` at all - reading only the former made every real Gateway
 * look like it served nothing.
 */
export function declaredHosts(h: Hop): string[] {
  const listeners = (h.config?.listeners ?? []).map((l) => l.hostname ?? '')
  return [...(h.config?.hostnames ?? []), ...(h.config?.tlsHosts ?? []), ...listeners]
}

/**
 * Which declared entry point serves a hostname. Two hosts that land on the same
 * backend with the same outcome are still different situations when they come in
 * through DIFFERENT front doors: one Gateway can be misconfigured while its
 * sibling is fine, and merging them would hide that behind a shared verdict.
 *
 * When several entries could serve the host, the most SPECIFIC declaration wins
 * (exact over wildcard over catch-all), mirroring how the proxies themselves
 * resolve it - otherwise a catch-all listener would swallow every host and
 * collapse the scenarios it exists to separate.
 */
export function entryForHost(host: string, upstreams: Hop[] = []): string {
  const h = host.trim().toLowerCase()
  if (!h) return ''
  const specificity = (d: string): number => (d === '' ? 0 : d.startsWith('*.') ? 1 : 2)
  let best: { hop: Hop; rank: number } | undefined
  for (const u of upstreams) {
    for (const d of declaredHosts(u)) {
      if (!hostMatches(d, h)) continue
      const rank = specificity(d.trim().toLowerCase())
      if (!best || rank > best.rank) best = { hop: u, rank }
    }
  }
  const r = best?.hop.resource
  return r ? `${r.kind}/${r.namespace ?? ''}/${r.name}` : ''
}

/**
 * Groups routes that are indistinguishable in outcome. Splitting them out again
 * happens exactly when it carries information - a differing outcome, layer,
 * confidence or serving entry point - so identical hosts collapse but a host
 * that behaves differently, or arrives through a different front door, always
 * keeps its own tab.
 */
/**
 * A route's stable identity: what it IS, never what happened to it.
 *
 * Composed exactly like the producer's InClusterResultKey (coverage.go), so the
 * two cannot drift. Selection anchors on this rather than on a scenario's group
 * key, which mixes in outcome, confidence and evidence - so a re-run that
 * changed a result changed the key and silently moved the user to a different
 * path.
 */
/** Whether the in-cluster test has ANYTHING to run: the server only tests a
 *  route carrying a concrete InClusterRequest. Offering the control without
 *  one spends a click (and a consent dialog) on a guaranteed no-op. */
export function traceInClusterRunnable(trace: Trace): boolean {
  return (trace.routes ?? []).some((r) => !!r.inClusterRequest && !r.benign)
}

export function routeIdentity(r: RouteResult): string {
  return `${r.route}\u0000${r.target ?? ''}\u0000${r.targetNamespace ?? ''}`
}

/** A route's per-vantage outcomes, order-independent so two routes observed in a
 *  different sequence still compare equal. */
export function vantageSignature(r: RouteResult): string {
  return (r.byVantage ?? [])
    .map(
      (v) =>
        `${v.vantage}/${v.path}/${v.source || 'radar'}=${v.outcome}${v.failedBoundary ? `@${v.failedBoundary}` : ''}${v.segment ? `#${v.segment}` : ''}`,
    )
    .sort()
    .join(',')
}

export function groupRoutes(routes: RouteResult[], upstreams: Hop[] = []): Scenario[] {
  const groups = new Map<string, RouteResult[]>()
  for (const r of orderRoutes(routes)) {
    // For an untested route the REASON is the whole content - "port 443 can't
    // be verified through the proxy" and "port 80 timed out" are different
    // situations even though both are merely not-tested. Folding them together
    // would hide the distinction the operator needs.
    // The evidence distinguishes routes at EVERY outcome, not only not-tested:
    // "connection refused" and "timed out" are the same outcome and layer but
    // different situations, and folding them shows the operator one and hides
    // the other behind a tab that claims to speak for both.
    const distinguishing = r.evidence ?? ''
    const entry = entryForHost(routeHostOf(r.route), upstreams)
    const key = [
      r.target ?? '',
      // Same-named Services in different namespaces are different backends.
      r.targetNamespace ?? '',
      r.outcome,
      r.confidence ?? '',
      r.failedLayer ?? '',
      r.benign ? 'benign' : '',
      r.basis ?? '',
      // Two routes whose rollups agree can still disagree per vantage - one
      // working from a laptop and one not. Collapsing them puts rs[0] in charge
      // of the whole board and destroys exactly the distinction byVantage was
      // added to preserve.
      vantageSignature(r),
      distinguishing,
      entry,
    ].join('|')
    const arr = groups.get(key) ?? []
    arr.push(r)
    groups.set(key, arr)
  }
  return [...groups.entries()].map(([key, rs]) => {
    const primary = rs[0]
    // Only genuine hostnames. hostOf returns the whole label when there is no
    // path separator, so port- and pod-shaped route labels ("port 80",
    // "web-abc123 port 8080") were being counted and announced as "hostnames".
    const hosts = [...new Set(rs.map((r) => routeHostOf(r.route)).filter(Boolean))]
    const members = [...new Set(rs.map((r) => r.route).filter(Boolean))]
    const grouped = rs.length > 1
    const entryId = entryForHost(routeHostOf(rs[0].route), upstreams)
    const entry = entryId ? entryId.split('/').pop() : undefined
    const via = entry ? ` · via ${entry}` : ''
    return {
      key,
      label: grouped ? primary.target || `${rs.length} routes` : primary.route,
      sub: grouped
        ? // Named for what the members ACTUALLY are. Routes folded together can
          // share a hostname and differ only by path, and many carry no hostname
          // at all - so "N hostnames" both undercounted and mis-described them.
          `${hosts.length === rs.length ? `${hosts.length} hostname${hosts.length === 1 ? '' : 's'}` : `${rs.length} paths`}${primary.target ? ` · ${primary.target}` : ''}${via}`
        : `${primary.target || ''}${via}`,
      routes: rs,
      primary,
      hosts,
      members,
      entry,
    }
  })
}

/**
 * Routes the tracer declined to test still describe real paths, so they become
 * scenarios too. Without this an untested resource renders no strip at all and
 * the paths it has are invisible.
 */
export function scenariosFor(
  routes: RouteResult[],
  notTested: { route?: string; reason: string }[],
  upstreams: Hop[] = [],
): Scenario[] {
  // A not-tested ROUTE and the raw skip rows for its host describe the SAME
  // gap (the producer preserves the declared candidate as a route so the
  // in-cluster recovery has a target). Mirroring recountCoverage's host-level
  // absorption: rows whose host a route already covers must not become a
  // second scenario for the same gap.
  const covered = new Set(
    routes.flatMap((r) => [routeHostOf(r.route), hostOfTarget(r.target)]).filter(Boolean),
  )
  const synthesized: RouteResult[] = notTested
    .filter((s) => !!s.route)
    .filter((s) => {
      const h = routeHostOf(s.route as string) || hostOfTarget(s.route)
      return !h || !covered.has(h)
    })
    .map((s) => ({ route: s.route as string, outcome: 'not-tested' as RouteOutcome, evidence: s.reason }))
  return groupRoutes([...routes, ...synthesized], upstreams)
}

/** The bare host of a probe-target-shaped string ("api:80" → "api"). */
function hostOfTarget(t?: string): string {
  const s = (t ?? '').trim()
  if (!s) return ''
  const i = s.lastIndexOf(':')
  return (i > 0 ? s.slice(0, i) : s).toLowerCase()
}

/**
 * Latency far outside the band is its own signal - a route that answers in
 * seconds is not simply "healthy". Threshold is deliberately generous: this
 * flags pathology, not ordinary variance.
 */
export const SLOW_THRESHOLD_NS = 1_000_000_000

export function isSlow(p: ProbeResult): boolean {
  return typeof p.latencyNs === 'number' && p.latencyNs >= SLOW_THRESHOLD_NS
}

export function formatLatency(ns?: number): string {
  if (typeof ns !== 'number' || ns <= 0) return ''
  const ms = ns / 1_000_000
  if (ms < 1) return '<1 ms'
  if (ms < 1000) return `${Math.round(ms)} ms`
  return `${(ms / 1000).toFixed(1)} s`
}
