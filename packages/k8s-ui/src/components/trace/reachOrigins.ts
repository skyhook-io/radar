import type { Trace, ProbeResult, RouteResult } from './types'
import type { Mark } from './reachMarks'
import { originRouteEvidence, routeMark } from './reachMarks'

/**
 * An Origin is a vantage point - WHERE a test ran from, and as WHOM. It is a
 * first-class entity in the graph, never a Kubernetes resource, because two
 * results about the same Service can mean completely different things depending
 * on who sent the request and by what mechanism.
 *
 * Radar has three real origins today. `caller` and `external` are declared here
 * and rendered as unavailable rather than omitted: an operator reading this tab
 * needs to know that the strongest evidence - the real application caller, and a
 * genuine request from outside the cluster - is absent, not merely unrun.
 * Hiding them would make synthetic evidence look complete.
 */
export type OriginId = 'incluster' | 'radar-incluster' | 'apiserver' | 'local' | 'caller' | 'external'

/** Which lane an origin's traffic belongs to. */
export type Lane = 'dataplane' | 'control'

/** What the origin's identity means for the result's strength. */
export type OriginKind = 'synthetic' | 'radar' | 'real-client' | 'real-caller' | 'relayed'

export interface Origin {
  id: OriginId
  glyph: string
  name: string
  /** How the request is actually sent. */
  mech: string
  /** Namespace / ServiceAccount / mesh participation, as far as we know it. */
  identity: string
  kind: OriginKind
  kindTag: string
  lane: Lane
  mark: Mark
  /** Set when this origin cannot be used at all. Renders instead of an action. */
  unavailable?: string
  /** True for origins Radar does not implement (vs merely denied by RBAC). */
  unsupported?: boolean
}

export const ORIGIN_KIND_TAG: Record<OriginKind, string> = {
  synthetic: 'SYNTHETIC',
  radar: 'RADAR ITSELF',
  'real-client': 'REAL CLIENT',
  'real-caller': 'REAL CALLER',
  relayed: 'RELAYED · NOT A CALLER',
}

/** The laptop's own tag. It shares the 'real-client' KIND with the external
 *  origin, but the tags must differ: a genuine request from the public internet
 *  IS a real client; Radar dialling from the operator's machine behaves AS one
 *  - the sender is Radar, and saying "real client" upgraded it into a user. */
export const LAPTOP_TAG = 'AS A CLIENT'

/** Evidence strength, strongest first. Drives the "next strongest test" prompt. */
export const ORIGIN_STRENGTH: OriginId[] = ['caller', 'external', 'incluster', 'radar-incluster', 'local', 'apiserver']

/** The live work this page actually did: how many real dials were sent, at
 *  which layers, from how many vantages. The route counts alone undersold it -
 *  "1 got through" can be twenty-odd DNS/TLS/HTTP checks across hostnames,
 *  ports, pods and vantages, and that volume IS the trust signal. */
export function probeCheckStats(trace?: Trace): { checks: number; byLayer: Record<string, number>; vantages: number } {
  const live = allProbes(trace).filter((p) => !p.skipped)
  const byLayer: Record<string, number> = {}
  for (const p of live) byLayer[p.layer] = (byLayer[p.layer] ?? 0) + 1
  const vantages = new Set(live.map((p) => originOf(p, trace?.runVantage))).size
  return { checks: live.length, byLayer, vantages }
}

export function allProbes(trace?: Trace): ProbeResult[] {
  if (!trace) return []
  return [...(trace.upstreams ?? []), ...(trace.downstream ?? [])].flatMap((h) => h.probes ?? [])
}

/** Which origin produced a probe. The apiserver path wins over vantage: a proxy
 *  relay is a control-plane mechanism no matter where the process calling it runs. */
export function originOf(p: ProbeResult, runVantage?: string): OriginId {
  if (p.path === 'apiserver') return 'apiserver'
  if (p.vantage !== 'in-cluster') return 'local'
  if (p.source === 'probe-job') return 'incluster'
  if (p.source === 'radar') return 'radar-incluster'
  // No source: an older producer. An in-cluster dataplane probe can only be
  // Radar's own when Radar itself runs in the cluster; from a laptop the only
  // way to get one is the throwaway Job. runVantage is what the producer knows
  // about itself, so it settles the ambiguity without guessing.
  return runVantage === 'in-cluster' ? 'radar-incluster' : 'incluster'
}

interface OriginContext {
  inClusterAllowed?: boolean
  /** The scenario on screen. Marks are scored against THIS route when the
   *  producer sent per-vantage results, so the rail cannot disagree with the
   *  graph about what a vantage found. */
  route?: RouteResult
  inClusterRunning?: boolean
  inClusterDeniedReason?: string
  /** A run was ATTEMPTED and its probe never started (image pull, quota,
   *  webhook). "Not tested" would erase the attempt; execution failure is its
   *  own state, distinct from reachability. */
  inClusterRunError?: string
  stale?: boolean
}

/**
 * Derives an origin's mark from the probes it actually produced.
 *
 * A live failure always wins over a success: when one origin's probes disagree
 * among themselves, surfacing the failure is the safe direction. An origin whose
 * every probe was SKIPPED did not fail - it never ran, which is `blocked`, and
 * the skip reason explains why (a ClusterIP is not routable from a laptop).
 */
function markFor(probes: ProbeResult[], id: OriginId, ctx: OriginContext): Mark {
  if (id === 'incluster' && ctx.inClusterRunning) return 'running'
  // The SELECTED route's own result for this origin wins when the producer sent
  // one. Without it the rail scored an origin across EVERY route at once, so a
  // vantage that failed on one path read as failed while the graph beside it -
  // now route-scoped - showed that same vantage succeeding on the selected one.
  const ev = ctx.route ? originRouteEvidence(ctx.route, id) : undefined
  if (ev?.kind === 'own') {
    if (ctx.stale) return 'stale'
    return routeMark(ev.result, {})
  }
  // The producer sent a breakdown and this origin is not in it. That means the
  // ROUTE has no result from here - it does NOT mean this vantage sent nothing.
  // A laptop that dialled a public Ingress and got an answer has real evidence
  // on the entry hop; the route is built from the backend's probes and cannot
  // see it. Saying "not tested" there contradicted the graph beside it, which
  // was drawing that very dial. Fall through to what this origin actually did.
  if (ev?.kind === 'none' && probes.filter((p) => !p.skipped).length === 0) {
    if (id === 'incluster' && ctx.inClusterAllowed === false) return 'denied'
    return 'untested'
  }
  if (probes.length === 0) {
    if (id === 'incluster' && ctx.inClusterAllowed === false) return 'denied'
    return 'untested'
  }
  const live = probes.filter((p) => !p.skipped)
  // A demoted run is a DISPOSITION, not a coverage gap: the probe dialled and
  // its answer was deliberately kept informational. 'blocked' ("never tried -
  // something failed earlier") was false on both clauses.
  if (live.length === 0 && probes.some((p) => p.skipped && p.skipClass === 'informational')) return 'inconclusive'
  // A refusal is not a dead end the operator has to accept: 'blocked' ("an
  // earlier failure or a skip stopped it") hides that this one names a grant
  // they can ask for.
  if (live.length === 0 && probes.some((p) => p.skipped && p.skipClass === 'denied')) return 'denied'
  if (live.length === 0) return 'blocked'
  if (ctx.stale) return 'stale'
  if (live.some((p) => !p.ok || p.tone === 'unhealthy')) return 'failed'
  // 'reached' = answered with a 3xx/4xx/app-5xx: evidence the port serves, not a
  // clean pass - same rule as the route rollup, where reached maps to answered.
  if (live.some((p) => p.tone === 'degraded' || p.tone === 'reached')) return 'answered'
  return id === 'apiserver' ? 'proxied' : 'proved'
}

/** The reason an all-skipped origin never ran, taken from the probes themselves. */
function skipReason(probes: ProbeResult[]): string | undefined {
  return probes.find((p) => p.skipped && p.reason)?.reason
}

/** The reason an origin was refused. Identity-scoped, so unlike `skipReason` it
 *  speaks for every port and carries no misattribution risk. */
function deniedReason(probes: ProbeResult[]): string | undefined {
  return probes.find((p) => p.skipped && p.skipClass === 'denied' && p.reason)?.reason
}

/**
 * Why THIS origin's dials for a route were skipped, from the skip rows
 * themselves. Each skip carries (vantage, path, source), so the reason is
 * attributed by the exact identity that produced it - inferring from the run
 * mode repeated the proxy's reason under the laptop, and lost it entirely when
 * the run came from inside the cluster (which relays through the proxy too).
 * A port-scoped skip only speaks for its own port; a shared (portless) skip
 * speaks for every port. Another port's reason is never borrowed.
 */
export function originSkipReason(trace: Trace | undefined, originId: OriginId, route?: RouteResult): string | undefined {
  const target = route?.target || route?.route || ''
  const m = /:(\d+)$/.exec(target)
  const port = m ? Number(m[1]) : undefined
  const backend = m ? target.slice(0, target.lastIndexOf(':')).trim() : ''
  const pick = (probes: ProbeResult[]): string | undefined => {
    const skips = probes.filter((p) => p.skipped && p.reason && originOf(p, trace?.runVantage) === originId)
    if (port !== undefined) {
      return (skips.find((p) => p.port === port) ?? skips.find((p) => !p.port))?.reason
    }
    return skips[0]?.reason
  }
  // Two backends can share a port, so a route with a named backend takes its
  // reason ONLY from that backend's own hops - borrowing a sibling's would be
  // the exact misattribution this lookup exists to prevent. The flat pool
  // remains only for routes that never name a backend.
  if (backend) {
    const own = [...(trace?.upstreams ?? []), ...(trace?.downstream ?? [])].filter((h) => h.resource.name === backend)
    if (own.length > 0) {
      for (const h of own) {
        const r = pick(h.probes ?? [])
        if (r) return r
      }
      return undefined
    }
  }
  return pick(allProbes(trace))
}

/**
 * Why this origin's run was kept informational. A demotion applies to the RUN,
 * not to one backend, so this is origin-scoped rather than route-scoped: the
 * route-scoped lookup asks which declared path a skip speaks for, which is the
 * wrong question when the whole run was demoted.
 */
export function originInformationalReason(trace: Trace | undefined, originId: OriginId): string | undefined {
  return allProbes(trace).find(
    (p) => p.skipped && p.skipClass === 'informational' && !!p.reason && originOf(p, trace?.runVantage) === originId,
  )?.reason
}

/**
 * Builds the origin rail for a trace.
 *
 * Order is by evidence strength, not by availability, so the gap between what
 * was tested and what would be conclusive stays legible.
 */
export function buildOrigins(trace: Trace | undefined, ctx: OriginContext = {}): Origin[] {
  const probes = allProbes(trace)
  const byOrigin = new Map<OriginId, ProbeResult[]>()
  for (const p of probes) {
    const id = originOf(p, trace?.runVantage)
    const arr = byOrigin.get(id) ?? []
    arr.push(p)
    byOrigin.set(id, arr)
  }
  const ns = trace?.subject.namespace ?? ''

  const caller: Origin = {
    id: 'caller',
    glyph: '◈',
    name: 'Real caller workload',
    mech: 'one of your running Pods, as itself',
    identity: 'the app’s own account and namespace',
    kind: 'real-caller',
    kindTag: ORIGIN_KIND_TAG['real-caller'],
    lane: 'dataplane',
    mark: 'blocked',
    unsupported: true,
    unavailable: 'Radar can’t send a request from one of your running Pods yet. Until it can, anything that depends on who is calling — mesh mTLS, authorization policy, network policy — stays untested.',
  }
  const external: Origin = {
    id: 'external',
    glyph: '◉',
    name: 'Direct external client',
    mech: 'a request from the public internet',
    identity: 'no cluster identity · anonymous client',
    kind: 'real-client',
    kindTag: ORIGIN_KIND_TAG['real-client'],
    lane: 'dataplane',
    mark: 'blocked',
    unsupported: true,
    unavailable: 'Radar has nowhere to test from that is provably on the public internet. A test from your machine is reported as exactly that, never as “from outside”.',
  }

  const inClusterProbes = byOrigin.get('incluster') ?? []
  // A failed ATTEMPT is not "not tested": the operator clicked Run and the
  // probe never started. Only when no live probe made it through - a partial
  // run that produced results keeps its evidence-based mark.
  const inClusterFailedToRun =
    !!ctx.inClusterRunError && !ctx.inClusterRunning && inClusterProbes.filter((p) => !p.skipped).length === 0
  const incluster: Origin = {
    id: 'incluster',
    glyph: '⚗',
    name: 'In-cluster probe',
    mech: 'a throwaway Pod Radar starts inside the cluster',
    // The Job sets no serviceAccountName and disables token automount, so it runs
    // as the target namespace's default SA with no credentials mounted - NOT as
    // Radar. Sidecar injection is admission's call, not ours; the runner handles
    // injected sidecars precisely because they do appear.
    identity: `in ${ns || 'the namespace'} · the namespace’s default account, no token`,
    kind: 'synthetic',
    kindTag: ORIGIN_KIND_TAG.synthetic,
    lane: 'dataplane',
    mark: inClusterFailedToRun && ctx.inClusterAllowed !== false ? 'blocked' : markFor(inClusterProbes, 'incluster', ctx),
    unavailable:
      ctx.inClusterAllowed === false
        ? ctx.inClusterDeniedReason || 'Not permitted in this cluster.'
        : inClusterFailedToRun
          ? ctx.inClusterRunError
          : undefined,
  }

  const apiProbes = byOrigin.get('apiserver') ?? []
  const apiMark = markFor(apiProbes, 'apiserver', ctx)
  const apiserver: Origin = {
    id: 'apiserver',
    glyph: '⌸',
    name: 'API-server proxy',
    mech: 'Kubernetes relays the request for us',
    identity: 'Radar’s own credentials — not a Pod',
    kind: 'relayed',
    kindTag: ORIGIN_KIND_TAG.relayed,
    lane: 'control',
    mark: apiMark,
    // The relay carries the identity Radar is running as, which is not always
    // one that may proxy. Without the reason the capsule stated a refusal and
    // left the reader no way to know what to grant.
    //
    // Only a DENIAL is read here, never any skip: a refusal is a property of the
    // identity and so speaks for every port, while the other skips (an HTTPS
    // port the relay can't verify, a non-HTTP port) are port-specific and would
    // be misattributed the moment another port is selected.
    unavailable: apiMark === 'denied' ? deniedReason(apiProbes) : undefined,
  }

  const localProbes = byOrigin.get('local') ?? []
  const localMark = markFor(localProbes, 'local', ctx)
  // A probed run that produced NOTHING from the laptop, on a subject with no
  // front door, is not "not tested yet" - there is nothing the laptop could
  // dial. The vantage stays on the rail (the evidence ceiling must stay
  // visible, exactly like the caller and external gaps) but it must say WHY it
  // cannot be used - a bare "not tested" reads as a test someone forgot to run.
  // Only claimed after a run: with no probes anywhere, "not tested yet" is the
  // honest state for everyone.
  const localCannotDial = probes.length > 0 && localProbes.length === 0 && localMark === 'untested' && (trace?.upstreams ?? []).length === 0
  // "No Ingress, Gateway or LoadBalancer" is only true of a ClusterIP subject.
  // A LoadBalancer/NodePort Service with no Ingress reaches this branch too -
  // Radar just doesn't dial its external address yet, which is a claim about
  // Radar, not about the Service.
  const subjectType = (trace?.downstream ?? [])
    .find((h) => h.resource.kind === 'Service' && h.resource.name === trace?.subject.name)
    ?.config?.serviceType?.trim()
    .toUpperCase()
  const externallyAddressable = subjectType === 'LOADBALANCER' || subjectType === 'NODEPORT' || subjectType === 'EXTERNALNAME'
  const local: Origin = {
    id: 'local',
    glyph: '▣',
    name: 'Radar on your machine',
    mech: 'this machine, over your own network',
    kind: 'real-client',
    kindTag: LAPTOP_TAG,
    // Not the cluster dataplane: from a laptop a request never traverses
    // kube-proxy, NetworkPolicy or the mesh, so it cannot sit in that lane.
    lane: 'control',
    mark: localCannotDial ? 'blocked' : localMark,
    identity: localIdentity(localProbes),
    unavailable:
      localMark === 'blocked'
        ? skipReason(localProbes)
        : localCannotDial
          ? externallyAddressable
            ? 'Radar doesn’t dial this Service’s external address from your machine yet. From here it can only relay through the API-server proxy.'
            : 'This Service has no entry point Radar can dial from your machine — no Ingress, Gateway address or LoadBalancer. From here Radar can only relay through the API-server proxy.'
          : undefined,
  }

  const radarProbes = byOrigin.get('radar-incluster') ?? []
  // Radar running AS a Pod is not the throwaway Job: it dials with Radar's own
  // ServiceAccount from Radar's own namespace, and whatever sidecars Radar has.
  // Collapsing the two credited Radar's direct dials to a Job never created.
  const radarInCluster: Origin = {
    id: 'radar-incluster',
    glyph: '◈',
    name: 'Radar, inside the cluster',
    mech: 'Radar\u2019s own process, running as a Pod',
    identity: 'Radar\u2019s own namespace and ServiceAccount',
    kind: 'radar',
    kindTag: ORIGIN_KIND_TAG.radar,
    lane: 'dataplane',
    mark: markFor(radarProbes, 'radar-incluster', ctx),
    unsupported: trace?.runVantage !== 'in-cluster' && radarProbes.length === 0,
    unavailable:
      trace?.runVantage !== 'in-cluster' && radarProbes.length === 0
        ? 'Radar is not running in this cluster, so it cannot dial from inside on its own. The in-cluster test starts a throwaway Pod instead.'
        : undefined,
  }

  const all: Record<OriginId, Origin> = { caller, external, incluster, 'radar-incluster': radarInCluster, local, apiserver }
  // Radar not being a Pod is not an evidence GAP - the in-cluster Job covers the
  // same ground. Listing it as "never tested" would pad the coverage caveat with
  // a deployment detail, next to genuine holes like "as your application".
  const radarIsAbsent = radarProbes.length === 0 && trace?.runVantage !== 'in-cluster'
  // The mirror image, and the same reasoning. When Radar runs as a Pod - hosted,
  // or self-hosted in-cluster - there is no Radar on the operator's machine to
  // dial from, so this vantage is not one that can be run and then isn't. The
  // browser being on a laptop is irrelevant: the request is sent by the Radar
  // process. What a machine outside the cluster would see is still declared, by
  // the external origin, which is a real gap and stays. Guarded on having no
  // probes so a mislabelled result can never hide evidence that exists.
  const laptopIsAbsent = localProbes.length === 0 && trace?.runVantage === 'in-cluster'
  return ORIGIN_STRENGTH.filter(
    (id) => !(id === 'radar-incluster' && radarIsAbsent) && !(id === 'local' && laptopIsAbsent),
  ).map((id) => all[id])
}

/**
 * What we can honestly say about where the laptop sat on the network.
 *
 * This used to be a constant - "we can't tell where on the network you are" -
 * for every trace forever. DNS already resolves the address, so when the name
 * resolves to a globally routable one we DO know the request left for a public
 * address, and saying nothing under-claims.
 *
 * It stops there deliberately. A public address is not proof the packet crossed
 * the public internet: a VPN, hairpin NAT or split-horizon DNS all break that
 * inference. So this describes the ADDRESS and never the journey, and it does
 * not make the external vantage supported - that is a different claim.
 */
export function localIdentity(probes: ProbeResult[]): string {
  const scopes = new Set(probes.filter((p) => !p.skipped && p.addressScope).map((p) => p.addressScope))
  if (scopes.has('mixed')) {
    return 'this name resolves to both public and private addresses — we can’t tell which one you reached'
  }
  if (scopes.has('public')) {
    return 'you dialled a public address — though a VPN or split-horizon DNS could still have kept it internal'
  }
  if (scopes.has('private')) {
    return 'you dialled a private address, so this did not come from the public internet'
  }
  return 'we can’t tell where on the network you are'
}

/** The origin that should be selected when the view opens: the strongest one
 *  that actually produced evidence, else the strongest usable one. */
export function defaultOrigin(origins: Origin[], route?: RouteResult): OriginId {
  // First paint must AGREE with the headline: the headline speaks from the
  // rollup's deciding evidence, and default-selecting a different vantage put
  // "not tested" directly under a sentence about a mechanism that vantage
  // never used. Prefer the origin whose row decided the rollup.
  const fromHeadline = headlineEvidenceOrigin(route)
  if (fromHeadline && origins.some((o) => o.id === fromHeadline && !o.unsupported)) return fromHeadline
  const evidence: Mark[] = ['proved', 'failed', 'answered', 'proxied', 'stale', 'running']
  const withEvidence = origins.find((o) => evidence.includes(o.mark))
  if (withEvidence) return withEvidence.id
  const usable = origins.find((o) => !o.unsupported && !o.unavailable)
  return usable?.id ?? 'apiserver'
}

/** The origin whose evidence DECIDED the route rollup - what the headline
 *  speaks from. Undefined when nothing decided it (untested / no rows). */
export function headlineEvidenceOrigin(route?: RouteResult): OriginId | undefined {
  if (!route || route.outcome === 'not-tested') return undefined
  if (route.confidence === 'indirect') return 'apiserver'
  const rows = route.byVantage ?? []
  const row = rows.find((v) => v.confidence === 'real' && v.outcome === route.outcome) ?? rows.find((v) => v.outcome === route.outcome)
  if (!row) return undefined
  if (row.path === 'apiserver') return 'apiserver'
  if (row.vantage !== 'in-cluster') return 'local'
  return row.source === 'radar' ? 'radar-incluster' : 'incluster'
}

const isGap = (o: Origin): boolean => o.mark === 'untested' || o.mark === 'denied' || (!!o.unsupported && o.mark === 'blocked')

/**
 * The strongest origin that is not yet proven, including ones Radar cannot use.
 * This is the honest ceiling on the evidence - useful as a caveat, but NEVER as
 * a call to action.
 */
export function strongestGap(origins: Origin[]): Origin | undefined {
  return origins.find(isGap)
}

/**
 * The strongest gap Radar can actually close.
 *
 * Kept separate from `strongestGap` deliberately. The real caller is almost
 * always the strongest missing origin AND permanently unavailable, so driving
 * the next-step prompt from `strongestGap` made every resource advertise a test
 * that cannot be run - a dead end exactly where the operator needs a next move.
 * The unreachable ceiling belongs in the caveats; the prompt belongs here.
 */
export function actionableGap(origins: Origin[]): Origin | undefined {
  // ORIGIN_STRENGTH is an evidence ranking, and a gap is only worth proposing
  // if it is STRONGER than what has already been proven. Without this the
  // panel told you your laptop (control lane) was "the strongest evidence
  // Radar can still collect" about in-cluster reachability, after the
  // in-cluster probe had already succeeded.
  const provenAt = origins.findIndex((o) => o.mark === 'proved')
  const ceiling = provenAt === -1 ? origins.length : provenAt
  return origins.slice(0, ceiling).find((o) => isGap(o) && !o.unsupported && o.mark !== 'denied')
}

