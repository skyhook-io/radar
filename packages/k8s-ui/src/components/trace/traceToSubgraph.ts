import type { Trace, RouteResult, RouteOutcome, RouteConfidence, ResourceRef, Hop, Verdict } from './types'
import type { Topology, TopologyNode, TopologyEdge, HealthStatus } from '../../types/core'

// outcomeToStatus maps a route outcome onto the subject NODE's health. A node's
// color is the resource's OWN health - did it respond - NOT how we reached it. So a
// route that was reached (verified OR reached, real traffic OR only via the apiserver
// proxy) means the resource ANSWERED → healthy/green; the "real in-cluster traffic not
// confirmed" caveat is a property of the PATH and lives on the EDGE (dashed green) +
// the verdict, it never recolors the node (painting a healthy Service amber just
// because the vantage was the proxy is the misattribution this avoids). Only a real
// own-health problem downgrades it: a 5xx (the app erroring) or an unreachable break.
export function outcomeToStatus(outcome: RouteOutcome, confidence?: RouteConfidence): HealthStatus {
  switch (outcome) {
    case 'verified':
    case 'reached':
      return 'healthy'
    case 'server-error':
      return 'degraded'
    case 'unreachable':
      // A proxy-only (indirect) unreachable never condemns the real path - the
      // real in-cluster path was never tested. Fail toward silence (neutral),
      // mirroring reachVerdict's onlyIndirectUnreach guard, instead of red.
      return confidence === 'indirect' ? 'unknown' : 'unhealthy'
    case 'not-tested':
    default:
      return 'unknown'
  }
}

function verdictToStatus(v: Verdict): HealthStatus {
  switch (v) {
    case 'healthy':
      return 'healthy'
    case 'degraded':
      return 'degraded'
    case 'broken':
      return 'unhealthy'
    default:
      return 'unknown'
  }
}

// A NetworkPolicy finding describes the PATH into a pod (who may reach it), not
// the pod's OWN health - the pod can be perfectly healthy and answering while a
// rule restricts the route. So policy findings never recolor the node, the same
// way a probe vantage doesn't. They surface in the hop detail + edge, not the
// node dot. (Same reasoning as the nodeOwnStatus doc below.)
const isOwnHealthFinding = (f: { code?: string }): boolean => !(f.code ?? '').startsWith('netpol:')

// A Service's "0/N selected pods ready" is a SYMPTOM of its backing pods' health,
// not the Service's OWN health - the Service object is correctly configured. The
// real fault (crashloop, image pull, OOM) is carried red on the downstream Pods
// node. So this finding caps the Service at DEGRADED (amber: "wired, no live
// backend") instead of the false red "the Service itself is broken". N > 0 here
// guarantees a Pods hop exists to carry the red, so the break is never hidden.
const isBackendSymptomFinding = (f: { code?: string }): boolean =>
  f.code === 'svc:no-ready-endpoints' || /^problem:0\/[1-9]\d* selected pods ready$/.test(f.code ?? '')

// hopHasReadyEndpoints reports whether a backend hop still has ready endpoints
// (meta.ready > 0) - the Service serves traffic to the ready pods.
const hopHasReadyEndpoints = (hop: Hop): boolean => typeof hop.meta?.ready === 'number' && (hop.meta.ready as number) > 0

function findingStatus(hop: Hop): HealthStatus | undefined {
  const f = (hop.findings ?? []).filter(isOwnHealthFinding)
  const crit = f.filter((x) => x.severity === 'critical')
  if (crit.length > 0) {
    // A critical paints the node red ONLY when it's a real own-health break AND the
    // backend has no ready endpoints. A backend that still serves (ready > 0 - one
    // replica crashing while siblings serve) or a backend-symptom critical (0/N
    // ready, whose red lives on the downstream Pods node) caps at degraded - never
    // the false red "all down" (mirrors backend hopReachSeverity).
    const hardRed = !hopHasReadyEndpoints(hop) && crit.some((x) => !isBackendSymptomFinding(x))
    return hardRed ? 'unhealthy' : 'degraded'
  }
  if (f.some((x) => x.severity === 'warning')) return 'degraded'
  return undefined
}

// worseStatus returns the more severe of two health statuses (unhealthy >
// degraded/alert > healthy > neutral/unknown).
function worseStatus(a: HealthStatus, b: HealthStatus): HealthStatus {
  const rank: Record<string, number> = { unhealthy: 4, degraded: 3, alert: 3, healthy: 2, neutral: 1, unknown: 0 }
  return (rank[b] ?? 0) > (rank[a] ?? 0) ? b : a
}

function outcomeRank(o: RouteOutcome): number {
  const order: Record<RouteOutcome, number> = { unreachable: 0, 'server-error': 1, 'not-tested': 2, reached: 3, verified: 4 }
  return order[o] ?? 5
}

const refId = (r: ResourceRef): string => `${r.kind}/${r.namespace ?? ''}/${r.name || 'pods'}`

const isRbac = (hop: Hop): boolean => (hop.findings ?? []).some((f) => f.code === 'rbac:cross-namespace-redacted')
// A NetworkPolicy finding is a PREDICTION, not a confirmed reachability break - it
// must never mark an edge unreachable and cascade-block downstream (the node logic
// already excludes netpol: via isOwnHealthFinding). Mirror that exclusion here.
// A backend that still has ready endpoints is reachable - a per-pod crash among
// serving replicas must NOT mark the edge unreachable and cascade-block downstream
// (mirrors hopReachSeverity). A 0-ready backend keeps its critical (real break).
const hasCritical = (hop: Hop): boolean =>
  !hopHasReadyEndpoints(hop) && (hop.findings ?? []).some((f) => f.severity === 'critical' && isOwnHealthFinding(f))

// A NODE's status reflects the resource's OWN health (its findings/probes), NEVER a
// route's outcome - so a healthy router Ingress is not painted red just because one of
// its routes is broken (that per-route truth lives on the edges). Critically, the
// probe VANTAGE never recolors the node: a resource REACHED only via the apiserver
// proxy still responded, so its own health is fine - the "real in-cluster traffic not
// confirmed" caveat is a property of the PATH and lives on the edge (dashed green),
// not on the node. Only a real own-health signal downgrades it: a finding, a transport
// failure to the resource, or a 5xx (the app itself erroring).
function nodeOwnStatus(hop: Hop): HealthStatus {
  if (isRbac(hop)) return 'unknown'
  const finding = findingStatus(hop)
  if (finding) return finding
  const live = (hop.probes ?? []).filter((p) => !p.skipped)
  if (live.length === 0) return 'unknown'
  // A FAILED apiserver-proxy probe is indirect evidence: the proxy path itself may
  // be what failed, and the real network path was never tested - so it must never
  // paint the node red (mirrors the route-level isIndirectUnreach guard: a proxy
  // 2xx is positive evidence the resource answered, a proxy failure is only a
  // localization hint). Drop failed proxy probes before judging own health; if
  // they were the ONLY live evidence, own health is untested → unknown.
  const considered = live.filter((p) => !(p.path === 'apiserver' && (!p.ok || p.tone === 'unhealthy')))
  if (considered.length === 0) return 'unknown'
  const bad = considered.filter((p) => !p.ok || p.tone === 'unhealthy')
  // A DNS success only proves the NAME resolves - it is not transport evidence
  // that the resource answered, so it must never soften a real break: an
  // ExternalName whose every port failed is unhealthy, not "partially healthy"
  // off its DNS row. DNS FAILURES stay in `bad` - a name that doesn't resolve
  // is a real reachability break.
  const good = considered.filter((p) => p.ok && p.tone !== 'unhealthy' && p.layer !== 'dns')
  // Partial own-health: a multi-port Service where one port answered and another
  // didn't is DEGRADED (amber), not fully dead (red) - red over-claims "nothing
  // works" when port 80 served HTTP 200.
  if (bad.length > 0 && good.length > 0) return 'degraded'
  if (bad.length > 0) return 'unhealthy'
  if (considered.some((p) => p.tone === 'degraded')) return 'degraded' // app answered 5xx - a real own-health problem, not a vantage caveat
  // DNS resolving a name to an IP is not own-health/reachability. When the only
  // live probe is a DNS success (transport/HTTP skipped - ExternalName host, or a
  // non-HTTP backend port), there's no evidence the resource was actually reached:
  // fail toward silence (unknown), never paint it green 'healthy'.
  if (!considered.some((p) => p.layer !== 'dns')) return 'unknown'
  return 'healthy'
}

// An EDGE's reachOutcome = did traffic flow along THIS route/hop. A proxy-only
// "verified" reads 'reached' (green is fine; the node/verdict carry the not-real caveat).
function edgeReach(hop: Hop, route?: RouteResult): TopologyEdge['reachOutcome'] {
  if (hasCritical(hop)) return 'unreachable'
  if (route) {
    switch (route.outcome) {
      case 'verified':
        return route.confidence === 'real' ? 'verified' : 'reached'
      case 'reached':
      case 'server-error':
        // A 5xx REACHED the backend - traffic flowed; the app erroring is the
        // node's own degraded health, not a broken edge. Green-dashed reached, not
        // a red "route break" (which would falsely say traffic never arrived).
        return 'reached'
      case 'unreachable':
        // A benign scale-to-0 is dormant by DESIGN, not a break. A proxy-only
        // (indirect) unreachable never tested the real path. Neither must color
        // the edge red or cascade-block the pods behind it (the node stays neutral
        // via its own logic). Treat both as not-tested for edge purposes.
        return route.benign || route.confidence === 'indirect' ? 'not-tested' : 'unreachable'
      case 'not-tested':
        return 'not-tested'
    }
  }
  // No route on this hop (e.g. the Service→Pods edge): the edge answers "did any
  // traffic get through" from this hop's own probes.
  return probeReach(hop)
}

// probeReach classifies an edge purely from a hop's OWN probe evidence, ignoring
// findings. If ANY non-skipped TRANSPORT probe reached, the edge reached - a
// multi-port pod where port 80 served 200 but 9090 refused is REACHED (the
// per-port failure shows in the node detail), never a hard-blocked red edge. A
// DNS success is NOT traffic-flow evidence (resolving a name reaches nothing), so
// it can neither mark the edge reached nor mask every transport port failing; a
// DNS failure IS a real break. Findings are deliberately NOT consulted so a
// sibling route's break on the SAME entry never condemns this edge.
function probeReach(hop: Hop): TopologyEdge['reachOutcome'] {
  const live = (hop.probes ?? []).filter((p) => !p.skipped)
  if (live.length === 0) return 'not-tested'
  const transport = live.filter((p) => p.layer !== 'dns')
  if (transport.length === 0) {
    // DNS-only evidence: a failed resolve is a real break; a resolve alone
    // proves nothing about traffic - not-tested, mirroring nodeOwnStatus's
    // DNS-only unknown.
    return live.some((p) => !p.ok) ? 'unreachable' : 'not-tested'
  }
  if (transport.some((p) => p.ok)) return 'reached'
  // Every transport failure came through the apiserver proxy - the real path was
  // never tested (mirrors the indirect-unreachable route rule above): not-tested,
  // never a red edge that would also cascade-block everything downstream.
  return transport.every((p) => p.path === 'apiserver') ? 'not-tested' : 'unreachable'
}

// nodeSubtitle builds the diagram node's functional one-liner from the hop's
// declared config + live meta - the same facts the Tree shows as pills: the
// service port→targetPort mapping (or a Pod's container ports) and pod readiness.
// This is what gives the diagram parity with the Tree instead of a bare "ClusterIP".
// Falls back to the caller's status/route string when there's no port/readiness.
function nodeSubtitle(hop: Hop | undefined, fallback: string): string {
  const cfg = hop?.config
  const parts: string[] = []
  const ports = cfg?.ports ?? []
  if (ports.length > 0) {
    const shown = ports.slice(0, 2).map((p) => `${p.port} → :${p.targetPort ?? p.port}`)
    parts.push(shown.join(', ') + (ports.length > 2 ? ` +${ports.length - 2}` : ''))
  }
  const cps = cfg?.containerPorts ?? []
  if (ports.length === 0 && cps.length > 0) {
    parts.push(':' + [...new Set(cps.map((c) => c.port))].slice(0, 2).join(', :'))
  }
  const ready = hop?.meta?.['ready']
  const selected = hop?.meta?.['selected']
  if (typeof ready === 'number' && typeof selected === 'number') {
    // For a publishNotReadyAddresses Service, meta.ready counts PUBLISHED
    // endpoints (every selected pod, readiness not required) - "N/M ready"
    // would claim crashlooping pods are ready. Say what the count actually is.
    parts.push(
      hop?.meta?.publishNotReadyAddresses
        ? `${ready} published (readiness not required)`
        : `${ready}/${selected} ready`,
    )
  }
  return parts.length ? parts.join(' · ') : fallback
}

/**
 * traceToSubgraph projects a reachability Trace into the topology-graph shape so the
 * existing TopologyGraph can render the path HONESTLY:
 *  - it BRANCHES by hop-edge semantics - backend Services hang off the entry, Pods off
 *    THEIR parent Service (a multi-backend Ingress fans out; N ingresses converge), never
 *    a linear chain;
 *  - per-route truth lives on the EDGE (reachOutcome) with a route LABEL - a router node
 *    stays neutral unless ALL its routes fail (no over-attribution);
 *  - nothing downstream of a real break is "reached": from any broken node/edge, descendant
 *    edges become 'blocked' (dashed) - never a green edge flowing past a break.
 * Frontend-only; probe detail stays out of the graph (it lives in the detail/tree).
 */
// routeEdgeLabel renders the path on a route edge. By default it's the route's
// DECLARED path. When the operator overrides the tested path (probePath, e.g.
// "/test"), the edge echoes what was actually tested so the graph agrees with
// the banner/probe rows - but we NEVER overwrite the route identity: the label
// shows the tested path and the tooltip keeps the declared route, so we never
// imply the Ingress declares a rule it doesn't.
function routeEdgeLabel(declared: string | undefined, probePath?: string): { label?: string; labelTitle?: string } {
  if (!declared) return {}
  const ov = probePath?.trim()
  // Active override only: a non-default path that differs from the declared one,
  // on a route that actually has a path component.
  if (!ov || ov === '/' || ov === declared || !declared.includes('/')) return { label: declared }
  const i = declared.indexOf('/')
  const tested = declared.slice(0, i) + ov // keep any host prefix, swap the path
  return { label: tested, labelTitle: `declared ${declared} · testing ${ov}` }
}

export function traceToSubgraph(trace: Trace, probePath?: string): Topology {
  const nodes: TopologyNode[] = []
  const edges: TopologyEdge[] = []
  const byId = new Map<string, TopologyNode>()
  const add = (n: TopologyNode): string => {
    if (!byId.has(n.id)) {
      byId.set(n.id, n)
      nodes.push(n)
    }
    return n.id
  }

  const routes = trace.routes ?? []
  // Match a route to a backend by EXACT name, not substring - a substring match
  // would pair backend "api" with a route targeting "api-v2" (wrong edge color/label).
  // The target is "<name>" or "<name>:<port>", so compare the part before the colon.
  // When the route carries a target namespace (cross-namespace Gateway API
  // backendRef), it must also match the ref's namespace so two same-named backends
  // across namespaces don't swap labels/outcomes.
  const routeMatchesRef = (r: RouteResult, ref: ResourceRef): boolean => {
    const nameMatch = (r.target ?? '').split(':')[0] === ref.name || r.route === ref.name
    if (!nameMatch) return false
    if (r.targetNamespace && ref.namespace && r.targetNamespace !== ref.namespace) return false
    // An UNSET targetNamespace means the route targets the subject's namespace - so a
    // hop in a DIFFERENT namespace (a cross-ns backendRef) must not match it, or a
    // same-ns route's outcome would attach to a cross-ns same-named backend hop.
    if (!r.targetNamespace && ref.namespace && trace.subject.namespace && ref.namespace !== trace.subject.namespace) return false
    return true
  }
  // Pick the WORST matching route, not the first - an Ingress with two paths to the
  // same Service (/web verified, /api unreachable) must not hide the failure behind
  // a verified-first ordering on the single backend edge.
  const routeFor = (ref: ResourceRef): RouteResult | undefined => {
    const matches = routes.filter((r) => routeMatchesRef(r, ref))
    if (matches.length === 0) return undefined
    return matches.slice().sort((a, b) => outcomeRank(a.outcome) - outcomeRank(b.outcome))[0]
  }

  // Subject = the entry. A ROUTER (Ingress/Gateway) stays NEUTRAL unless ALL routes fail -
  // its per-route truth is on the edges, not its node color.
  const subjectId = refId(trace.subject)
  const isRouter = trace.subject.kind === 'Ingress' || trace.subject.kind === 'Gateway'
  // The reused Service node defaults its type subtitle to "ClusterIP" when none is
  // passed - a lie for an ExternalName Service (a DNS alias, no ClusterIP). Surface
  // the real type so the node reads "ExternalName" instead of the false "ClusterIP".
  const isExternalName = (trace.downstream ?? []).some((h) => h.resource?.kind === 'ExternalName')
  const okCount = routes.filter((r) => r.outcome === 'verified' || r.outcome === 'reached').length
  // A proxy-only (indirect) unreachable is NOT a real failure - the real path was
  // never tested. Exclude it everywhere a failure would redden/degrade the node,
  // mirroring reachVerdict's onlyIndirectUnreach guard.
  const isIndirectUnreach = (r: RouteResult): boolean => r.outcome === 'unreachable' && r.confidence === 'indirect'
  const failCount = routes.filter((r) => (r.outcome === 'unreachable' && !isIndirectUnreach(r)) || r.outcome === 'server-error').length
  // A ROUTER (Ingress/Gateway) node = the front door's OWN health. It goes red
  // ONLY when the door genuinely can't pass traffic on every real route (all
  // non-benign routes are hard unreachable). A server-error means traffic DID
  // flow - the backend answered 5xx - so the front door works: that's amber, not
  // red. A benign scale-to-zero route isn't a failure at all. Counting either as
  // a hard fail would paint a working front door red.
  const hardUnreach = routes.filter((r) => r.outcome === 'unreachable' && !r.benign && !isIndirectUnreach(r)).length
  const nonBenignRoutes = routes.filter((r) => !(r.outcome === 'unreachable' && (r.benign || isIndirectUnreach(r)))).length
  // "All failed" = every real (non-benign) route is a genuine unreachable. A
  // server-error doesn't count (traffic flowed to a 5xx backend), nor does a
  // benign scale-to-zero - so neither paints the front door red or the edge
  // "unreachable".
  const allHardFail = nonBenignRoutes > 0 && hardUnreach === nonBenignRoutes
  const anySoftFail = hardUnreach > 0 || routes.some((r) => r.outcome === 'server-error')
  const anyTrafficFlowed = okCount > 0 || routes.some((r) => r.outcome === 'server-error')
  const worst = routes.slice().sort((a, b) => outcomeRank(a.outcome) - outcomeRank(b.outcome))[0]
  // worstReal ignores benign scale-to-zero so a dormant Service backend doesn't
  // read as a hard (red) failure - benign-only fails fall through to amber below.
  const worstReal = routes
    .filter((r) => !(r.outcome === 'unreachable' && (r.benign || isIndirectUnreach(r))))
    .sort((a, b) => outcomeRank(a.outcome) - outcomeRank(b.outcome))[0]
  // onlyIndirectUnreach: every failure is a proxy-only unreachable and nothing
  // passed - the node must stay neutral/unknown, never red, mirroring
  // reachVerdict.ts's onlyIndirectUnreach guard.
  const onlyIndirectUnreach =
    routes.some((r) => isIndirectUnreach(r) && !r.benign) &&
    !routes.some(
      (r) =>
        (r.outcome === 'unreachable' && !r.benign && !isIndirectUnreach(r)) ||
        r.outcome === 'server-error' ||
        r.outcome === 'verified' ||
        r.outcome === 'reached',
    )
  const subjectHop = (trace.downstream ?? []).find((h) => refId(h.resource) === subjectId)
  const routeDerivedSubj: HealthStatus = onlyIndirectUnreach
    ? 'unknown'
    : isRouter
    ? allHardFail ? 'unhealthy' : anySoftFail ? 'degraded' : 'unknown'
    : okCount > 0 && failCount > 0 ? 'degraded' // partial - some ports/routes reach, some don't → amber, not a red "all dead"
    : worstReal ? outcomeToStatus(worstReal.outcome, worstReal.confidence)
    : worst ? 'degraded' // only benign (scaled-to-zero) failures remain → dormant, amber not red
    : verdictToStatus(trace.verdict)
  // Fold the subject hop's OWN health (its findings - e.g. ingress:no-controller /
  // controller-unready - and a failed front-door probe) in as a FLOOR. Without
  // this the router node stays neutral while reachVerdict headlines degraded /
  // unreachable, under-representing a real front-door problem. Only a genuine own
  // problem (degraded/unhealthy) floors; a healthy/unknown own status never
  // downgrades the route-derived status.
  const ownFloor = subjectHop ? nodeOwnStatus(subjectHop) : 'unknown'
  const subjStatus: HealthStatus =
    ownFloor === 'unhealthy' || ownFloor === 'degraded' ? worseStatus(routeDerivedSubj, ownFloor) : routeDerivedSubj
  const subjType = isExternalName ? 'ExternalName' : subjectHop?.config?.serviceType
  const subjFallback = routes.length > 1 ? `${okCount} of ${routes.length} routes reachable` : (worst?.evidence ?? trace.headline ?? '')
  add({
    id: subjectId,
    kind: trace.subject.kind,
    name: trace.subject.name,
    status: subjStatus,
    data: { ref: trace.subject, ...(subjType ? { type: subjType } : {}), subtitleOverride: nodeSubtitle(subjectHop, subjFallback), hop: subjectHop, routes },
  })

  const subjReach: TopologyEdge['reachOutcome'] = allHardFail ? 'unreachable' : anyTrafficFlowed ? 'reached' : 'not-tested'
  // Entry paths converging on a Service subject. An upstream is an ENTRY that routes
  // here - we test the SUBJECT's reachability, not each upstream's path individually. So
  // the upstream NODE stays NEUTRAL: it is never colored by its own/other-route findings
  // (that was the over-attribution bug - e.g. `multi` painted red on echo's view because
  // of multi's UNRELATED /api→ghost break), and a neutral upstream also never triggers
  // the frontier block-cascade through the shared subject to its pods. RBAC-redacted
  // upstreams keep their distinct "no access" label.
  for (const up of trace.upstreams ?? []) {
    const id = add({ id: refId(up.resource), kind: up.resource.kind, name: up.resource.name, status: 'unknown', data: { ref: up.resource, subtitleOverride: isRbac(up) ? 'no access (RBAC)' : nodeSubtitle(up, ''), hop: up } })
    // When this upstream WAS probed at its own front door, color the edge from its
    // OWN probe evidence (findings ignored, so a sibling route's break never
    // condemns it) - a genuinely-failed entry shows unreachable instead of
    // inheriting the subject's reach. An unprobed upstream falls back to the
    // subject's reach (the thing we actually tested).
    const upProbed = (up.probes ?? []).some((p) => !p.skipped)
    edges.push({ id: `e:${id}->${subjectId}`, source: id, target: subjectId, type: 'routes-to', reachOutcome: upProbed ? probeReach(up) : subjReach })
  }

  // Downstream - BRANCH: backends (X->Service) hang off the subject; Pods (Service->Pods)
  // hang off their parent Service (the last-seen Service while walking).
  let lastService = subjectId
  for (const dn of trace.downstream ?? []) {
    const baseId = refId(dn.resource)
    if (baseId === subjectId) {
      lastService = subjectId
      continue
    }
    const isPods = dn.resource.kind === 'Pods' || /pods/i.test(dn.edge)
    const parent = isPods ? lastService : subjectId
    // An empty-name Pods hop's refId collapses to "Pods/<ns>/pods" for every
    // backend; without parent-scoping, a multi-backend Ingress drops the second
    // pod group (add() dedups by id) and both Service→Pods edges merge into one
    // node. Scope the id to the owning Service so each backend keeps its own pods.
    const id = isPods && !dn.resource.name ? `${parent}::pods` : baseId
    const route = isPods ? undefined : routeFor(dn.resource)
    const subtitleOverride = isRbac(dn) ? 'no access (RBAC)' : hasCritical(dn) ? (dn.findings[0]?.message ?? '') : nodeSubtitle(dn, '')
    add({ id, kind: dn.resource.kind || 'Pods', name: dn.resource.name || 'Pods', status: nodeOwnStatus(dn), data: { ref: dn.resource, subtitleOverride, hop: dn, ...(route ? { routes: [route] } : {}) } })
    const { label: edgeLabel, labelTitle } = routeEdgeLabel(route?.route, probePath)
    edges.push({ id: `e:${parent}->${id}`, source: parent, target: id, type: 'routes-to', label: edgeLabel, labelTitle, reachOutcome: edgeReach(dn, route) })
    if (!isPods) lastService = id
  }

  // Frontier rule: nothing downstream of a real break is "reached". From any broken NODE
  // (a no-endpoints Service, a down Pod) or unreachable EDGE, mark descendant edges 'blocked'
  // (dashed gray) and their nodes "blocked upstream" - never a green/reached edge past a break.
  const out = new Map<string, TopologyEdge[]>()
  for (const e of edges) {
    const arr = out.get(e.source) ?? []
    arr.push(e)
    out.set(e.source, arr)
  }
  const blockFrom = (start: string) => {
    const stack = [start]
    const seen = new Set<string>()
    while (stack.length) {
      const cur = stack.pop()
      if (cur === undefined || seen.has(cur)) continue
      seen.add(cur)
      for (const nx of out.get(cur) ?? []) {
        if (nx.reachOutcome !== 'unreachable' && nx.reachOutcome !== 'blocked') {
          nx.reachOutcome = 'blocked'
          const n = byId.get(nx.target)
          if (n && n.status !== 'unhealthy') {
            n.status = 'unknown'
            ;(n.data as Record<string, unknown>).subtitleOverride = 'not reached - break upstream'
          }
        }
        stack.push(nx.target)
      }
    }
  }
  // A backend reached by ANOTHER route/port must not be cascade-blocked just because
  // the WORST matching route colored its edge unreachable (e.g. /web→svc:80 verified,
  // /api→svc:9090 down - port 80 reached the service AND its pods). The worst-route is
  // honest for the edge LABEL/color, but blocking descendants a sibling route reached
  // would false-condemn them. Skip the cascade when the edge's backend has any reach.
  const backendReachedByAnother = (targetId: string): boolean => {
    const ref = (byId.get(targetId)?.data as { ref?: ResourceRef } | undefined)?.ref
    if (!ref) return false
    // server-error counts as backend-reach: a 5xx route DID reach the backend
    // (edgeReach maps server-error to 'reached'), so it must not cascade-block the
    // pods that route actually reached.
    return routes.some((r) => routeMatchesRef(r, ref) && (r.outcome === 'verified' || r.outcome === 'reached' || r.outcome === 'server-error'))
  }
  for (const n of nodes) if (n.status === 'unhealthy') blockFrom(n.id)
  for (const e of edges) if (e.reachOutcome === 'unreachable' && !backendReachedByAnother(e.target)) blockFrom(e.target)

  return { nodes, edges }
}
