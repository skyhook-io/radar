import type { Trace, RouteResult, ResourceRef } from './types'
import type { Mark, SevTone } from './reachMarks'
import { routeMark, routeChip, routeTone, routeAsSeenFrom, originRouteEvidence, routeForOrigin, traceInClusterRunnable, markHelp } from './reachMarks'
import type { Origin, OriginId } from './reachOrigins'
import { strongestGap, actionableGap, originSkipReason, originInformationalReason } from './reachOrigins'
import { hopEvidenceFor, originProducedEvidence, type GraphNode } from './reachGraphModel'

// 'run-probes' re-runs the reachability probes. It is deliberately NOT
// 'refresh': the panel that offers it promises fresh evidence, and a static
// refetch collects none.
export type InspectorAction = 'run-in-cluster' | 'run-probes' | 'open-resource' | 'copy-command'

export interface InspectorCTA {
  text: string
  primary?: boolean
  action?: InspectorAction
  ref?: ResourceRef
  command?: string
  /** Set when the CTA describes something Radar cannot do - rendered inert. */
  disabledReason?: string
}

/** Only nodes are selectable now. Edges carry no segment-local evidence, so
 *  clicking one could only ever repeat the path result - see `Sidebar`. */
export type Selection = string | undefined

/**
 * What the sidebar shows.
 *
 * `path` is ALWAYS present: whether traffic got through, from where, with what
 * caveats, and what to do next. That question must never require a click - it
 * is the reason the tab exists. `resource` is ADDITIVE, appearing when a node
 * is selected, and never replaces the diagnosis.
 */
export interface Sidebar {
  path: {
    chipTone: SevTone
    chipText: string
    title: string
    /** The concrete path under test. Always visible: it was inside the collapsed
     *  details, so the panel described a result without ever naming what was
     *  requested. */
    request?: string
    body: string
    scope: { k: string; v: string }[]
    evidence: { mark: Mark; text: string }[]
    notProve: string[]
    next: { header: string; body: string; blocked?: string; ctas: InspectorCTA[] }
  }
  /** Every hop on the selected path, in order - the whole story for this path
   *  seen from this vantage, rather than a summary plus whichever node was last
   *  clicked. Reading beat clicking: understanding a path used to take three
   *  clicks whose panel meant something different after each one. */
  hops: HopReport[]
  /** Configured-but-bypassed resources: on screen for orientation, outside the
   *  journey. The label says WHY they are not part of this vantage's path. */
  context?: { label: string; hops: HopReport[] }
}

export interface HopDetail {
  kind: string
  name: string
  chipTone: SevTone
  chipText: string
  body: string
  facts: { k: string; v: string }[]
  rows?: { mark: Mark; name: string; detail: string }[]
  moreRows?: number
  anomalies?: { mark: Mark; text: string }[]
  notProve: string[]
  openRef?: ResourceRef
}

/** Configured-but-bypassed resources, grouped under one label naming WHY they
 *  are outside this vantage's journey. Their config sections stay readable;
 *  their journey-state words do not apply. */
function contextGroup(ctx: Ctx, byId: Map<string, GraphNode>, sel: Selection): Sidebar['context'] {
  const nodes = (ctx.contextNodeIds ?? []).map((id) => byId.get(id)).filter((n): n is GraphNode => !!n)
  if (nodes.length === 0) return undefined
  const label =
    ctx.origin.id === 'apiserver'
      ? 'CONFIGURED ENTRY — KUBERNETES RELAYED THE REQUEST PAST THIS'
      : ctx.origin.id === 'incluster' || ctx.origin.id === 'radar-incluster'
        ? 'CONFIGURED ENTRY — THE PROBE DIALS THE SERVICE DIRECTLY'
        : 'PARALLEL ENTRY — NOT ON THIS ROUTE\u2019S PATH'
  return {
    label,
    // Context hops are collapsed by default - they are not the journey. But the
    // SELECTED one opens: the entry-problem row's whole purpose is "show me the
    // cause", and landing on a still-collapsed section makes the reader spend a
    // second click on the thing they just asked for.
    hops: nodes.map((n) => ({
      ...resourceSection(n),
      id: n.id,
      state: 'plain' as const,
      chipText: '',
      expanded: sel === n.id,
    })),
  }
}

/**
 * A hop's place in the story.
 *
 *  - `break`   where the request is known to have stopped. Expanded by default;
 *              it is the answer.
 *  - `before`  reached before that. Collapsed - it worked, so it is context.
 *  - `after`   never tried, because something earlier stopped. Collapsed, and
 *              labelled so its silence is not read as health.
 *  - `plain`   no break to order around.
 */
export type HopState = 'before' | 'break' | 'after' | 'plain'

export interface HopReport extends HopDetail {
  id: string
  state: HopState
  expanded: boolean
  /** Set on entry hops when they are parallel members of one entry stage - the
   *  note reads "one of N parallel entry points" instead of implying sequence. */
  parallelCount?: number
}

const NOT_DATAPLANE = 'Nothing about the normal network path. Kubernetes relayed this for us, so routing, network policy and the mesh were all skipped.'
const SYNTHETIC_IDENTITY =
  'That your app can reach it. This test ran from a throwaway Pod under the namespace’s default account with no token mounted — not as your application — so anything that checks who is calling may answer differently.'

function originScope(o: Origin, trace: Trace, request?: string): { k: string; v: string }[] {
  const runsIn: Record<OriginId, string> = {
    incluster: `a throwaway Pod in ${trace.subject.namespace || 'the cluster'}`,
    'radar-incluster': 'Radar\u2019s own Pod',
    apiserver: 'the kube-apiserver process',
    local: 'your workstation (outside the cluster)',
    caller: 'the application workload’s own Pod',
    external: 'a client on the public internet',
  }
  return [
    { k: 'TESTED FROM', v: o.name },
    { k: 'RUNS IN', v: runsIn[o.id] },
    // The laptop row's content is where the dial came FROM ("you dialled a
    // public address…"), not who dialled - identity and network position are
    // different facts and must not share a label.
    { k: o.id === 'local' ? 'NETWORK POSITION' : 'IDENTITY', v: o.identity },
    { k: 'MECHANISM', v: o.mech },
    // A status code cannot be read without knowing what was asked for - "404"
    // means nothing until you know the request was GET /.
    ...(request ? [{ k: 'REQUEST', v: request }] : []),
  ]
}

/**
 * The next-step prompt, driven by the strongest gap Radar can CLOSE. The
 * unreachable ceiling is stated underneath as a caveat instead of being offered
 * as an action - a button that can never be pressed is not a next step.
 */
function gapNext(
  origins: Origin[],
  current: Origin,
  namespace?: string,
  multiPath?: boolean,
  inClusterRunnable = true,
  canRun = true,
): Sidebar['path']['next'] {
  // The server can only test a route that carries a concrete in-cluster request.
  // When none does, the run fails with "not supported for this subject" - so the
  // panel must say that HERE, instead of recommending it as the strongest
  // evidence available and letting the operator find out by clicking.
  const notRunnable = 'No path on this resource has a request Radar can send from inside the cluster, so this test would have nothing to run.'
  // No button here. The in-cluster run is offered ON the vantage capsule in the
  // graph, which is the thing that would produce the missing evidence - and a
  // third copy of the same control (header, panel, capsule) was one too many.
  // The panel keeps the REASONING, which is what it is good at.
  const inClusterCTA = (): InspectorCTA[] => {
    // No handler wired (a library consumer that omits it) means the click would
    // do nothing at all - offer nothing rather than a button that lies.
    if (!canRun) return []
    return inClusterRunnable
      ? [{ text: '⚗ Run the in-cluster test', action: 'run-in-cluster', primary: true }]
      : [{ text: '⚗ Run the in-cluster test', action: 'run-in-cluster', disabledReason: notRunnable }]
  }
  const actionable = actionableGap(origins)
  const ceiling = strongestGap(origins)
  const ceilingNote = ceiling?.unsupported ? `Even then, ${ceiling.name.toLowerCase()} stays untested — ${ceiling.unavailable}` : undefined
  const denied = origins.find((o) => o.mark === 'denied')
  // A run is never scoped to the selected path - the server tests every declared
  // path in one pass. Saying so here stops the picker above from reading as a
  // filter on what the button will do.
  const allPaths = multiPath ? ' The test covers every path on this resource, not only this one.' : ''

  // Mid-run, a running origin is not a "gap" any more, so the no-gap branch
  // below would claim Radar "already has the strongest evidence" BEFORE the
  // result exists. Being in flight is its own state, and it is the whole truth
  // about this panel right now.
  const runningOrigin = origins.find((o) => o.mark === 'running')
  if (runningOrigin) {
    return {
      header: 'TEST RUNNING',
      body: `${runningOrigin.name} is testing now — the result lands here when it finishes.`,
      ctas: [],
    }
  }

  if (!actionable && denied) {
    const ns = namespace || '<namespace>'
    // Which grant to ask for depends on WHICH vantage was refused. The Job and
    // the relay need different permissions, and naming the wrong one sends the
    // operator to their cluster admin with a request that would not fix this.
    // When both are refused the Job wins by strength order, which is also the
    // more valuable ask - it is the stronger evidence.
    const relay = denied.id === 'apiserver'
    return {
      header: 'ASK FOR THIS PERMISSION',
      body: relay
        ? `Relaying through the API server needs \`get\` on \`services/proxy\` and \`pods/proxy\` in ${ns}. Grant it, or test from inside the cluster instead.`
        : `Running an in-cluster test needs \`create\` on \`jobs\` in ${ns}. Grant it, or run the check from a workload you already control.`,
      blocked: denied.unavailable,
      ctas: [
        {
          text: 'Copy the permission check',
          action: 'copy-command',
          command: relay ? `kubectl auth can-i get services/proxy -n ${ns}` : `kubectl auth can-i create jobs -n ${ns}`,
        },
      ],
    }
  }
  if (actionable && actionable.id === current.id) {
    // You are looking at the vantage that is itself the gap. The section body
    // already said nothing ran from here, and the caveats section already
    // carries the ceiling - so this is the ACTION and nothing else.
    return {
      header: '',
      body: inClusterRunnable ? '' : `${notRunnable} Every declared path here was skipped before a request could be formed.`,
      ctas: actionable.id === 'incluster' ? inClusterCTA() : [{ text: '⟳ Re-run', action: 'run-probes' }],
    }
  }
  if (!actionable) {
    // Nothing to offer: say nothing. This was a resource-level statement living
    // in the selection-scoped panel, identical on every vantage - the scope
    // mixing this redesign exists to remove - and the ceiling it gestured at is
    // already stated, with specifics, in WHAT THIS DOESN'T PROVE and in the
    // footer's coverage ledger. An empty section is the honest render of
    // "there is no next step".
    return { header: '', body: '', ctas: [] }
  }
  return {
    header: 'RUN THIS NEXT',
    body:
      actionable.id === 'incluster' && !inClusterRunnable
        ? `${notRunnable} Every declared path here was skipped before a request could be formed.`
        : `${actionable.name} has not been used for this path, and is the strongest evidence Radar can still collect.${allPaths}`,
    blocked: ceilingNote,
    ctas: actionable.id === 'incluster' ? inClusterCTA() : [{ text: '⟳ Re-run', action: 'run-probes' }],
  }
}

interface Ctx {
  trace: Trace
  route?: RouteResult
  origin: Origin
  origins: Origin[]
  nodes: GraphNode[]
  /** Where the path first broke, from the graph model. */
  breakNodeId?: string
  /** A Service-routing boundary: the node whose EXIT the request never got
   *  past. The break renders on that hop with exit-phrased copy - no node is
   *  blamed. */
  breakAtExitOf?: string
  /** Inline nodes that are NOT network hops (the workload). Never given
   *  journey states - a workload is neither "reached" nor a stopping place. */
  nonNetworkNodeIds?: string[]
  /** Configured-but-bypassed entries - context, never the journey. */
  contextNodeIds?: string[]
  /** Non-network nodes spliced into the display after a journey hop. */
  interleave?: { id: string; afterId: string }[]
  /** >1 when the journey's entries are parallel members of one stage. */
  entryParallelCount?: number
  /** The journey's entry-hop node ids, from the graph model. */
  journeyEntryNodeIds?: string[]
  /** The selected route's chain in traversal order, from the graph model. */
  pathNodeIds?: string[]
  stale?: boolean
  running?: boolean
  /** More than one scenario is on screen, so scope has to be stated explicitly. */
  multiPath?: boolean
  /** The HTTP path the run requests, as chosen in "what to test". */
  httpPath?: string
  /** False when the host wired no in-cluster handler, or permission is denied:
   *  the run must not be offered at all then. */
  canRunInCluster?: boolean
}

/**
 * Whether an in-cluster run has anything to send.
 *
 * The server tests a route only when it carries a concrete InClusterRequest; a
 * route whose probes were all skipped never becomes one, so a subject can offer
 * paths and still have nothing runnable. Knowing this from the trace is what
 * lets the panel say so BEFORE the operator spends a Job on it.
 */


/** Whether the diagnosis says nothing the headline has not already said. Both
 *  are generated, so they collide whenever the producer falls back to the same
 *  generic sentence - and a banner repeating the line above it reads as a second
 *  problem rather than the same one. */
function restatesTitle(summary: string | undefined, title: string): boolean {
  const norm = (x: string) => x.trim().toLowerCase().replace(/[.!]$/, '')
  return !!summary && !!title && norm(summary) === norm(title)
}

/** The request this route would send, in one line - "GET /healthz", or "TCP
 *  connect" where there is no application request to make. */
function requestLabel(route: RouteResult | undefined, httpPath?: string): string | undefined {
  const r = route?.inClusterRequest
  if (!r) return undefined
  if (r.protocol === 'tcp') return 'TCP connect'
  const path = httpPath || r.path || '/'
  return `GET ${path}`
}

/**
 * What an ANSWER means for this page's question.
 *
 * "reached" plus an HTTP status is the single most-misread result on the tab:
 * a reader sees amber and 404 and cannot tell whether that is a problem. It is
 * not - for the question this page asks. The network path is proven the moment
 * the app answers at all; the status code is a statement about the app's own
 * routing, auth or health, which reachability does not judge. Say that, and say
 * what would turn it into a verified pass.
 */
/**
 * What an answer MEANS, and whether that answer is evidence the path works.
 *
 * `pathWorks` is the load-bearing half: an app answering 404 proves the journey
 * reached it, while a gateway answering 502 proves the opposite. Both are
 * "answers", so a caller that prefixes every meaning with "the path works"
 * states the reverse of the truth on exactly the cases that matter most.
 */
function statusMeaning(evidence?: string, httpPath?: string, failedLayer?: string): { text: string; pathWorks: boolean } | undefined {
  // A cert failure never gets an HTTP status - the handshake stops before a
  // request is sent - so this must be decided before looking for a code.
  if (failedLayer === 'tls') {
    return { text: 'the TLS handshake did not verify - a certificate problem, not an application one.', pathWorks: false }
  }
  const m = /HTTP\s+(\d{3})/i.exec(evidence ?? '')
  if (!m) return undefined
  const code = Number(m[1])
  const asked = httpPath && httpPath !== '/' ? httpPath : '/'
  // 502/504 are NOT the app: a gateway answering that it could not reach its
  // upstream. The producer says so with failedLayer 'upstream'; blaming app
  // health here contradicted the chip beside it.
  if (failedLayer === 'upstream' || code === 502 || code === 504) {
    return {
      text: `the front door answered, but only to say it could not reach the backend (HTTP ${code}) - the break is between the entry and the app, not inside the app.`,
      pathWorks: false,
    }
  }
  if (code >= 500) {
    return { text: `the request reached the app and the app itself returned an error (HTTP ${code}) - application health, which this page does not judge.`, pathWorks: true }
  }
  if (code === 401 || code === 403 || code === 407) {
    return { text: `the app answered by demanding credentials (HTTP ${code}) - it is enforcing auth, which is a different thing from reachability.`, pathWorks: true }
  }
  if (code >= 300 && code < 400) {
    return { text: `the app answered with a redirect (HTTP ${code}), which Radar does not follow - so whatever it points at is untested from here.`, pathWorks: true }
  }
  if (code >= 400) {
    return { text: `the app answered (HTTP ${code}) - that is it saying it serves no route for ${asked}, not a reachability problem. To verify a real route, re-run with a path your app serves.`, pathWorks: true }
  }
  return undefined
}

/** The persistent diagnosis: did traffic get through, from where, and what next. */
/** The suffix the localization lines carry; stripped when deduping so one
 *  observation cannot appear twice at two lengths. */
const LOCALIZED_SUFFIX_RE = / [-\u2014] checked directly, past the entry point$/

function pathSection(ctx: Ctx): Sidebar['path'] {
  const { trace, route, origin, origins } = ctx
  // The route outcome is merged across origins. Without this gate the panel
  // rendered another vantage's success under the selected vantage's name - a
  // permanently unavailable origin could read "a real request went through"
  // while the graph beside it said "not routable". Same lie the graph already
  // guards against, in the surface users actually read.
  // This origin's OWN result when the producer sent one; the coarse
  // "did this origin produce anything" gate only remains as the fallback.
  const ev = originRouteEvidence(route, origin.id)
  const asSeen = ev.kind === 'none' ? undefined : ev.result
  // A config-derived break is true of every vantage and observed by none, so it
  // reads as a configuration fact - never as this origin's failed dial.
  const fromConfig = ev.kind === 'config'
  // Which KIND of derived break. Declared-config is broken whatever the cluster
  // is doing; cluster-state is true right now and changes when the workload
  // does. Calling the second one a configuration failure sends the reader to
  // edit YAML when the fix is to get Pods ready.
  const basis = ev.kind === 'config' ? ev.result.basis : undefined
  const hasEvidence = ev.kind === 'own' || (ev.kind === 'rollup' && originProducedEvidence(origin))
  const mark: Mark = fromConfig
    ? 'config'
    : hasEvidence
      ? asSeen
        ? routeMark(asSeen, { stale: ctx.stale, running: ctx.running })
        : 'untested'
      : origin.mark

  const notProve: string[] = []
  if (origin.kind === 'synthetic') notProve.push(SYNTHETIC_IDENTITY)
  if (origin.kind === 'relayed') notProve.push(NOT_DATAPLANE)
  const hasFrontDoor = (trace.upstreams ?? []).length > 0
  const external = origins.find((o) => o.id === 'external')
  if (hasFrontDoor && external?.unsupported && origin.id !== 'external') {
    // "No request has come in from outside" is only true until Radar's own
    // machine dials the public entry and gets an answer - that request DID come
    // from outside. What stays unproven then is narrower: a real user's request.
    const outsideDialAnswered = (trace.upstreams ?? []).some((h) => {
      const m = hopEvidenceFor(h, { id: 'local' }, trace.runVantage)?.mark
      return m === 'proved' || m === 'answered'
    })
    notProve.push(
      outsideDialAnswered
        ? 'That real users can reach it — Radar dialled the public entry from outside and got an answer, but no real user request has been observed.'
        : 'That people on the internet can reach it — no request has come in from outside.',
    )
  }

  const evidence: { mark: Mark; text: string }[] = []
  const seen = new Map<string, number>()
  // Two lines can be the SAME observation worded at different lengths: a route's
  // rollup evidence and the localization fact from that same dial differ only by
  // the " - checked directly..." suffix, so exact-string dedupe let both through
  // and the reader saw "HTTP 404 - reached" twice. Key on the observation and
  // keep the more specific wording.
  const add = (m: Mark, text: string) => {
    const t = text.trim()
    const key = t.toLowerCase().replace(LOCALIZED_SUFFIX_RE, '').trim()
    if (!key) return
    const at = seen.get(key)
    if (at !== undefined) {
      if (t.length > evidence[at].text.length) evidence[at] = { mark: evidence[at].mark, text: t }
      return
    }
    seen.set(key, evidence.length)
    evidence.push({ mark: m, text: t })
  }
  if ((hasEvidence || fromConfig) && asSeen?.evidence) add(mark, asSeen.evidence)
  // A result with no evidence STRING is still a result: show what the mark
  // means rather than leaving the section empty, which would read as "nothing
  // ran" for a vantage that did.
  else if (hasEvidence && asSeen) add(mark, markHelp(mark))
  // What this vantage saw at each HOP. The route is built from the backend's
  // probes, so a laptop that dialled the front door and got an answer had all
  // of it discarded and read as "no test has been run from here" - beside a
  // graph drawing that very dial.
  const hopSeen = ctx.nodes
    .filter((n) => !n.isOrigin && n.hop)
    .map((n) => ({ node: n, ev: hopEvidenceFor(n.hop, origin, trace.runVantage, { stale: ctx.stale, running: ctx.running }) }))
    .filter((x): x is { node: GraphNode; ev: NonNullable<ReturnType<typeof hopEvidenceFor>> } => !!x.ev)
  if (!hasEvidence && !fromConfig) {
    for (const { node, ev: e } of hopSeen) add(e.mark, `${node.kind.toLowerCase()} ${node.name} — ${e.title || e.label}`)
  }
  // The one boundary two observations can establish. Stated as the reasoning
  // that produced it, so it reads as evidence rather than as a verdict.
  if (ev.kind === 'own' && routeForOrigin(route, origin.id)?.failedBoundary === 'service-routing') {
    add('failed', 'the Pods answered directly, but the Service did not — so the Service’s own routing is what breaks')
  }
  // Localization facts are behind-the-gate (apiserver / direct-pod) evidence.
  // They belong to the relayed origin, not to whichever origin is selected -
  // listing them under the in-cluster probe credited it with observations it
  // never made.
  for (const f of origin.id === 'apiserver' && hasEvidence ? route?.localization ?? [] : []) {
    const layer = f.layer.toUpperCase()
    const detail = f.detail?.trim()
    const body = !detail ? layer : detail.toUpperCase().startsWith(layer) ? detail : `${layer} · ${detail}`
    add(f.ok ? 'proxied' : 'failed', `${body} — checked directly, past the entry point`)
  }
  if (evidence.length === 0) {
    // A route the run SKIPPED still carries why. The reason is looked up from
    // the skip rows by the exact identity that produced it, so it appears only
    // under the vantage whose dial was skipped - never charging the in-cluster
    // probe with the proxy's limits, or the laptop with the proxy's timeouts.
    const skipReason = ev.kind === 'rollup' && asSeen?.outcome === 'not-tested' ? originSkipReason(trace, origin.id, route) : undefined
    // An in-cluster run that was ATTEMPTED and never started is not "no test
    // has been run" - the capsule preserves the attempt and this row must not
    // erase it. The error itself is the observation.
    const attemptError = origin.id === 'incluster' && origin.mark === 'blocked' ? origin.unavailable : undefined
    // A demoted run produced a real observation; "no test has been run from
    // here" erased it.
    const informational = origin.mark === 'inconclusive' ? originInformationalReason(trace, origin.id) : undefined
    add(
      mark,
      // The attempt error outranks skip rows: a prior Job's leftover skips
      // would otherwise paint a fresh failed run (image pull, quota) with last
      // run's reason, one pane from a capsule saying "test couldn't run".
      informational ||
        attemptError ||
        skipReason ||
        (origin.unsupported
          ? 'Radar cannot test from here, so nothing has been learned this way'
          : origin.mark === 'denied'
            ? 'not permitted to run this test'
            : // A vantage that CANNOT be used states why (nothing dialable from
              // the laptop, a skipped mechanism) - "no test has been run" reads
              // as a test someone forgot, which is a different claim.
              origin.unavailable || ''),
    )
  }

  const failed = mark === 'failed'
  // Only when it is about THIS path. A trace carries one diagnosis but many
  // routes, so an unattributed or sibling-owned cause must not be rendered as
  // the selected path's - the route's own evidence is what remains true.
  const rawDiagnosis = trace.diagnosis
  const diagnosis = rawDiagnosis && (!rawDiagnosis.route || rawDiagnosis.route === route?.route) ? rawDiagnosis : undefined
  const reachedSomething = !hasEvidence && !fromConfig && hopSeen.some((x) => x.ev.mark !== 'failed')
  const answer = statusMeaning(asSeen?.evidence, ctx.httpPath, asSeen?.failedLayer)
  const body = basis === 'cluster-state'
    ? 'Nothing is ready to serve this path right now, so it cannot work from any vantage. No request was sent to establish that — it is read off the current state of the cluster, and it changes when the workload does.'
    : fromConfig
    ? 'The configuration itself is broken, so this path cannot work from any vantage. No request was sent to establish that — it is read off what is declared.'
    : reachedSomething
    ? 'This vantage did reach part of the path — see what it saw below. It has no result for this route as a whole, so the end-to-end journey from here is still unproven.'
    : origin.mark === 'inconclusive' || mark === 'inconclusive'
    // A demoted run RAN and answered - it is only kept out of the verdict. The
    // "nothing has been tested" branch below fired first and denied it happened.
    // Both the origin's mark and the route's own can carry the demotion, and the
    // sentence is the same either way; the reason comes from the informational
    // skip that recorded it, never from the route-scoped skip lookup.
    ? `The probe ran and got an answer, but it is kept as evidence rather than a verdict${
        originInformationalReason(trace, origin.id) ? `: ${originInformationalReason(trace, origin.id)}` : ''
      }. A throwaway identity cannot stand in for your application, so what it saw informs but never decides.`
    : !hasEvidence
    ? (origin.unavailable || 'Nothing has been tested from here, so this says nothing about whether traffic gets through.') +
      // When NOTHING was tested anywhere, the health dots are the only colour
      // on the board and read as a passed test - the one state where the
      // dot/line split needs saying out loud, not just in the caption.
      ((trace.coverage?.tested ?? 0) === 0 ? ' The dots on the graph show each resource’s own reported health — cluster state, not a test result.' : '')
    : failed
    ? 'This is the first confirmed failure. Everything after it was never tried, so there is nothing to report past this point.'
    : mark === 'proved'
      ? 'A real request went through and the target answered.'
      : mark === 'proxied'
        ? [
            'Kubernetes relayed a request and the target answered — which shows something is serving, not that the normal path works.',
            // The relay caveat alone leaves "404" unreadable: the reader still
            // cannot tell whether the answer itself was a problem.
            answer && `As for the answer itself: ${answer.text}`,
          ]
            .filter(Boolean)
            .join(' ')
        : mark === 'untested'
          ? 'Nothing has been tried from here yet. Configuration may look right, but that is intent, not proof.'
          : mark === 'stale'
            ? 'This result predates a change to the cluster, so it is set aside rather than trusted.'
            : mark === 'excluded'
              // Benign by design (deliberately scaled to zero, a not-eligible
              // endpoint). It fell through to the generic "answered" sentence,
              // which described a request that was never sent.
              ? `${asSeen?.evidence || 'Nothing is behind this path right now'} — that is deliberate, not a failure: nothing was sent, because there is nothing to reach.`
            : mark === 'running'
              ? 'A test is running. Earlier results stay until new ones replace them.'
              : // A proxy-only failure wears the same amber mark as a real answer,
                // but nothing answered - saying "the target answered" here sent
                // the reader to debug an application response that never existed.
                asSeen?.outcome === 'unreachable' && asSeen?.confidence === 'indirect'
                ? 'The relayed dial failed. The proxy bypasses the real path, so this does not condemn it — but nothing answered, and the real path is still untested.'
                : // Only an answer that proves the journey completed earns "the
                  // path works" - a 502 or a failed handshake is an answer that
                  // says the opposite.
                  (answer ? (answer.pathWorks ? `The path works: ${answer.text}` : `The path did not work: ${answer.text}`) : undefined) ??
                // A transport-only reach: nothing was asked of the application.
                (asSeen?.outcome === 'reached'
                  ? 'The port accepted a connection, but nothing was asked of the application - the transport works and the application protocol is unverified.'
                  : 'The target answered, but not with what was asked for.')

  return {
    chipTone: asSeen ? routeTone(asSeen, { stale: ctx.stale, running: ctx.running }) : 'unknown',
    chipText: asSeen ? routeChip(asSeen, { stale: ctx.stale, running: ctx.running }) : 'not tested',
    title: `${origin.name} → ${route?.target || trace.subject.name}`,
    request: route ? `${route.route}${ctx.httpPath && ctx.httpPath !== '/' ? ` · HTTP path ${ctx.httpPath}` : ''}` : undefined,
    body,
    scope: [...originScope(origin, trace, requestLabel(route, ctx.httpPath)), ...(route ? [{ k: 'PATH', v: route.route }] : [])],
    evidence,
    notProve,
    next:
      failed && diagnosis
        ? {
            header: 'LIKELY CAUSE',
            body: diagnosis.summary + (diagnosis.nextAction ? ` ${diagnosis.nextAction}` : ''),
            ctas: [
              ...(diagnosis.culpritResource ? [{ text: 'Open the culprit', primary: true, action: 'open-resource' as InspectorAction, ref: diagnosis.culpritResource }] : []),
              ...(diagnosis.command ? [{ text: 'Copy the command', action: 'copy-command' as InspectorAction, command: diagnosis.command }] : []),
            ],
          }
        : gapNext(origins, origin, trace.subject.namespace, ctx.multiPath, traceInClusterRunnable(trace), ctx.canRunInCluster !== false),
  }
}

/** The additive detail for a selected node. Never replaces the diagnosis. */
function resourceSection(node: GraphNode): HopDetail {
  const hop = node.hop
  const findings = hop?.findings ?? []

  if (node.podRows) {
    const roster = hop?.config?.pods ?? []
    const total = hop?.config?.podTotal ?? roster.length
    const ready = typeof hop?.meta?.ready === 'number' ? (hop.meta.ready as number) : roster.filter((p) => p.ready).length
    const selected = typeof hop?.meta?.selected === 'number' ? (hop.meta.selected as number) : total
    const publishNotReady = !!hop?.meta?.publishNotReadyAddresses
    const notReady = publishNotReady ? [] : roster.filter((p) => !p.ready)
    const omitted = total - roster.length
    const notProve: string[] = []
    if (omitted > 0) notProve.push(`The ${omitted} Pods that were not tested. Untested is not proven.`)
    if (notReady.length > 0) notProve.push(`The ${notReady.length} not-ready Pod${notReady.length > 1 ? 's' : ''} — nothing was sent to them, so nothing was learned.`)
    return {
      kind: 'PODS',
      name: `${ready} of ${selected} eligible`,
      chipTone: node.tone,
      chipText: 'backends',
      body: publishNotReady
        ? 'The Pods behind this Service. This Service is set to send traffic to Pods even before they report ready.'
        : 'The Pods behind this Service. Kubernetes only sends traffic to the ones that report ready.',
      facts: [
        { k: 'MATCHING PODS', v: `${selected}` },
        // Derived from readiness, NOT from observed delivery - "taking traffic"
        // claimed evidence we do not have.
        { k: 'ELIGIBLE', v: `${ready}` },
        {
          k: 'SITTING OUT',
          v: publishNotReady ? 'none — not-ready Pods get traffic too' : notReady.length > 0 ? `${notReady.length} not ready` : 'none',
        },
      ],
      rows: node.podRows,
      moreRows: node.moreRows,
      anomalies: node.anomalies,
      notProve,
    }
  }

  const c = hop?.config
  const facts: { k: string; v: string }[] = []
  if (c?.clusterIP) facts.push({ k: 'CLUSTER IP', v: c.clusterIP })
  if (c?.serviceType) facts.push({ k: 'TYPE', v: c.serviceType })
  if (c?.ports?.length) facts.push({ k: 'PORTS', v: c.ports.map((x) => `${x.port}→${x.targetPort ?? x.port}`).join(', ') })
  if (c?.addresses?.length) facts.push({ k: 'ADDRESS', v: c.addresses.join(', ') })
  if (c?.hostnames?.length) facts.push({ k: 'HOSTS', v: c.hostnames.join(', ') })
  if (c?.selector) facts.push({ k: 'SELECTOR', v: Object.entries(c.selector).map(([k, v]) => `${k}=${v}`).join(', ') })
  if (node.ref?.namespace) facts.push({ k: 'NAMESPACE', v: node.ref.namespace })

  return {
    kind: node.kind,
    name: node.name,
    chipTone: node.tone,
    chipText: node.dim ? 'not on this path' : '',
    body: node.dim
      ? 'This entry point is attached to the resource but does not serve the host being tested.'
      : findings.length > 0
        ? findings[0].cause || findings[0].message
        : 'Configuration and health of this resource.',
    facts,
    notProve: [],
    openRef: node.ref,
  }
}

/**
 * Builds the sidebar. The diagnosis is always computed; a node selection only
 * appends to it.
 */
export function buildSidebar(sel: Selection, ctx: Ctx): Sidebar {
  const path = pathSection(ctx)
  // The selected route's own chain, from the graph's traversal - NOT every node
  // sorted by position. Sorting served siblings up as if they were sequential
  // hops and included branches this route never touches.
  const byId = new Map(ctx.nodes.map((n) => [n.id, n]))
  const journey = (ctx.pathNodeIds ?? [])
    .map((id) => byId.get(id))
    .filter((n): n is GraphNode => !!n && !n.isOrigin)
  // Display order interleaves non-network nodes (the workload) after their
  // parent - present to read, absent from the journey's state machine.
  const chain: GraphNode[] = []
  for (const n of journey) {
    chain.push(n)
    for (const iv of ctx.interleave ?? []) {
      if (iv.afterId === n.id) {
        const wl = byId.get(iv.id)
        if (wl) chain.push(wl)
      }
    }
  }
  const entryIds = new Set(ctx.journeyEntryNodeIds ?? [])
  const nonNetwork = new Set(ctx.nonNetworkNodeIds ?? [])
  // A boundary break anchors to the EXIT of its source hop (the Service);
  // breakNodeId anchors to a landing node. Either way one index carries 'break'.
  const anchorId = ctx.breakNodeId ?? ctx.breakAtExitOf
  const breakIdx = anchorId ? chain.findIndex((n) => n.id === anchorId) : -1
  // A break at ONE of N parallel entries orders nothing: a sibling entry is
  // not "before" it, and the hops behind the stage were not necessarily
  // stopped - the request may have gone through a sibling. Serial
  // before/after semantics apply only when the break is outside the stage.
  const breakInParallelStage =
    breakIdx >= 0 && (ctx.entryParallelCount ?? 0) > 1 && entryIds.has(chain[breakIdx]?.id ?? '')
  const stateOf = (i: number, id: string): HopState => {
    // The workload is not a network hop: it is never "reached", never a place
    // a request stops - whatever happens around it.
    if (nonNetwork.has(id)) return 'plain'
    if (breakIdx < 0) return 'plain'
    if (breakInParallelStage) return i === breakIdx ? 'break' : 'plain'
    if (i < breakIdx) return 'before'
    return i === breakIdx ? 'break' : 'after'
  }
  // What to open when the reader has not chosen: the break if there is one,
  // else the destination - the point of the path. Never everything at once,
  // which is how a report becomes a wall.
  const defaultOpen = breakIdx >= 0 ? breakIdx : chain.length - 1
  return {
    path,
    hops: chain.map((n, i) => {
      const section = resourceSection(n)
      return {
        ...section,
        id: n.id,
        state: stateOf(i, n.id),
        // Exit-phrased for a boundary break: the SERVICE hop carries it, and
        // "stopped here" would blame the hop itself for its exit's failure.
        chipText:
          i === breakIdx && ctx.breakAtExitOf === n.id
            ? 'routing to its Pods breaks just past here'
            : section.chipText,
        parallelCount: entryIds.has(n.id) && (ctx.entryParallelCount ?? 0) > 1 ? ctx.entryParallelCount : undefined,
        expanded: sel ? n.id === sel : i === defaultOpen,
      }
    }),
    context: contextGroup(ctx, byId, sel),
  }
}

/** The headline verdict band. Derived from the selected scenario, never from an
 *  aggregate that could hide a failing route behind passing siblings. */
const VERDICT_TONE: Record<string, SevTone> = { healthy: 'healthy', degraded: 'degraded', broken: 'unhealthy', unknown: 'unknown' }

export function buildVerdict(
  trace: Trace,
  route: RouteResult | undefined,
  opts: { stale?: boolean; running?: boolean; pathLabel?: string; originId?: string; originName?: string } = {},
): {
  tone: SevTone
  chipText: string
  /** Set when the resource has more than one path, so the reader can tell the
   *  route-scoped badge apart from the resource-wide headline beside it. */
  chipScope?: string
  scopeLabel?: string
  /** The selected vantage's name - the viewing strip renders it. */
  originName?: string
  title: string
  problem?: string
  body: string
} {
  // With no route there is nothing to derive a tone from, but the backend has
  // still reached a verdict (e.g. a config fault found without probing). Falling
  // through to 'unknown' showed a grey dot on a resource the tracer called
  // degraded.
  // The band sits directly above the inspector, which reads the SELECTED
  // origin's own result. Leaving the band on the merged rollup let the two
  // contradict each other in adjacent panes - "could not get through" over
  // "got through" - on exactly the disagreeing traces per-vantage evidence
  // exists to represent.
  const seen = opts.originId ? routeAsSeenFrom(route, opts.originId) : route
  // A route exists but this origin has no row: the badge says "not tested",
  // and it must wear the NEUTRAL tone - painting it with the resource-wide
  // verdict colour (amber on a degraded trace) dressed a vantage-scoped
  // "not tested" as a warning about that vantage. The verdict fallback stays
  // for traces with no routes at all (a config fault found without probing).
  const tone: SevTone = opts.running ? 'info' : seen ? routeTone(seen, opts) : route ? 'unknown' : VERDICT_TONE[trace.verdict] ?? 'unknown'
  return {
    tone,
    chipText: opts.running ? 'testing' : seen ? routeChip(seen, opts) : 'not tested',
    // Title, problem and coverage describe the WHOLE resource; tone and chip
    // follow the selected path. With several paths on screen that difference is
    // invisible unless each side says which scope it speaks for.
    // The badge now follows the selected path AND vantage while the headline,
    // problem and coverage stay resource-wide. Both scopes are legitimate; what
    // is not legitimate is leaving the reader to guess which is which, so the
    // badge names its own scope in full.
    // Prefixed here rather than at the render site: with no pathLabel the old
    // template produced the visible "for from Radar on your machine".
    chipScope: [opts.pathLabel ? `for ${opts.pathLabel}` : '', opts.originName ? `from ${opts.originName}` : '']
      .filter(Boolean)
      .join(' · ') || undefined,
    scopeLabel: opts.pathLabel || opts.originName ? 'THIS RESOURCE' : undefined,
    originName: opts.originName,
    // A stale screen previously led with the old headline ("Reachable...") and
    // then said underneath that the result was excluded. That is a contradiction,
    // not an exclusion.
    title: opts.stale
      ? 'This result is out of date — re-test'
      : trace.headline || route?.route || `Reachability · ${trace.subject.name}`,
    // The diagnosis is a named fault with a culprit and a next action - it
    // answers "why not", where the headline only says how much was tested. It
    // is called out rather than rendered as body prose, which made the more
    // important fact read as an explanation of the less important one.
    // A diagnosis that only restates the headline is not a second fact. The
    // banner rendered "couldn't actively test any route from here" directly
    // under a title saying exactly that.
    problem: restatesTitle(
      trace.diagnosis?.summary,
      opts.stale ? '' : trace.headline || route?.route || `Reachability · ${trace.subject.name}`,
    )
      ? undefined
      : trace.diagnosis?.summary,
    body: trace.diagnosis ? '' : trace.reason || '',
  }
}
