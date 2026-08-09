import type { Trace, Hop, ResourceRef, RouteResult, PodStatus, ProbeResult } from './types'
import type { Mark, SevTone } from './reachMarks'
import { routeMark, isSlow, formatLatency, declaredHosts, hostMatches, routeHostOf, originRouteEvidence, routeForOrigin, traceInClusterRunnable } from './reachMarks'
import { originOf, type Origin } from './reachOrigins'
import { podProbeKey } from './podReach'

/**
 * Layout constants. Positions are COMPUTED from content, never hand-placed:
 * node boxes size to their text, so fixed coordinates collide the moment a name
 * or a tag runs long. Columns are laid out left to right with a guaranteed
 * gutter wide enough to hold an edge pill without touching either neighbour.
 */
// Chains can reach five columns (origin -> Gateway -> Route -> Service -> Pods),
// so the gutter is tighter than a 3-column layout would allow.
const GUTTER = 98
/** The workload is worth having on the path and worth LESS width than a real
 *  network hop - its column and both its gutters are tightened. Still wide
 *  enough for the short edge pills that sit in them ("selects", "runs"). */
const WORKLOAD_GUTTER = 72
const WORKLOAD_W = 160
const ROW_GAP = 14
const LANE_PAD = { x: 14, top: 22, bottom: 14 }
const LANE_GAP = 20
/** Pills wrap to two lines, so the cap is what fits on two - not on one.
 *  At 16 it cut "HTTP 404 - reached" down to "HTTP 404...", which reports the
 *  status code and hides the only word that says what it meant. */
export const PILL_MAX_CHARS = 26
/** Hard cap on a pill's rendered width. MUST stay under GUTTER, or a pill
 *  overruns its gutter and lands on the node beside it. */
export const PILL_MAX_PX = 88

/** Columns are assigned in chain order. Every node belongs to exactly one,
 *  which is what guarantees left-to-right reading order and non-overlap. */
/** Width per column. The last column (Pods) is wider because it carries rows. */
const COL_W = { origin: 172, hop: 180, pods: 216 }

export interface GraphNode {
  id: string
  x: number
  y: number
  w: number
  h: number
  /** Small-caps type line, e.g. "SERVICE · PORT". */
  kind: string
  name: string
  sub: string
  /** The resource's OWN health. Never the path's truth - that lives on edges. */
  tone: SevTone
  tag?: string
  /** The hop's OWN findings, headline-first. The graph used to consume these
   *  only to pick a dot colour, discarding the cause, action and remediation
   *  the backend had already produced - so the one sentence that answers
   *  "what is wrong with this hop" was a click away behind a coloured pixel. */
  notes?: HopNote[]
  anomalies?: { mark: Mark; text: string; title?: string }[]
  /** Per-endpoint delivery results, rendered as rows inside the node.
   *  A column of pod boxes cost more width than the whole rest of the path and
   *  fit fewer of them; rows carry the same per-pod truth in less space. */
  podRows?: PodRow[]
  /** Endpoints beyond the row cap, named rather than silently dropped. */
  moreRows?: number
  dim?: boolean
  ref?: ResourceRef
  hop?: Hop
  isOrigin?: boolean
  /** An action offered ON the node, when the node IS the thing that would
   *  produce the missing evidence. Deliberately a button and never a
   *  click-the-capsule gesture: selecting a vantage is free, and this one
   *  creates Pods in the user's cluster. */
  action?: { text: string; kind: 'run-in-cluster'; disabledReason?: string }
  lane: 'control' | 'data'
}

export interface HopNote {
  /** Resource-health severity, NOT a traffic Mark: findings describe the object,
   *  marks describe what happened to a request. Keeping the two vocabularies
   *  apart is why the dot and the edge never mean the same thing. */
  severity: 'critical' | 'warning' | 'info'
  /** Short headline - the parsed cause when there is one. */
  text: string
  /** Everything the row could not fit, for the hover. */
  detail: string
}

export interface PodRow {
  name: string
  mark: Mark
  detail: string
  ref: ResourceRef
}

/** Rows shown before collapsing into a "+N more" line. */
export const POD_ROW_MAX = 6

/** Above this many sibling branches, only the selected one and the ones with
 *  findings stay expanded - the quiet remainder collapses to a single row. */
export const FAN_EXPANDED_MAX = 4

export interface GraphEdge {
  id: string
  /** SVG path data. */
  d: string
  mark: Mark
  /** Set on the edges of a BOUNDARY span (a Service→Pods routing failure whose
   *  segment the workload node happens to sit inside): 'start' carries the one
   *  observed break; 'continuation' extends the same break across the span
   *  without claiming a second observation (rendered dashed, no pill). Break
   *  detection skips both - a boundary anchors to the exit of its source node
   *  (GraphModel.breakAtExitOf), never to a node halo. */
  boundary?: 'start' | 'continuation'
  label: string
  /** The untruncated label. ALWAYS set: the pill has a pixel cap as well as a
   *  character cap, so text can be visually cut without `label` being shortened,
   *  and a hover that only appears sometimes is worse than one that always does. */
  title: string
  /** Pill centre, in canvas coordinates. Always inside a gutter. */
  px: number
  py: number
}

export interface LaneBox {
  x: number
  y: number
  w: number
  h: number
  label: string
  /** Hover text. The band label is the most prominent word in the graph and had
   *  no explanation anywhere. */
  help: string
  /** Lane tint + label colour. */
  color: string
  dashed?: boolean
}

export interface GraphModel {
  nodes: GraphNode[]
  edges: GraphEdge[]
  /** The node the first FAILED edge lands on - the first place a request is
   *  known to have stopped. Derived from edge marks (path truth), never from
   *  node tone (resource health), because those are different questions, and
   *  only from the SELECTED vantage's edges. */
  breakNodeId?: string
  /** A Service-routing boundary break: the node whose EXIT the request never
   *  got past. Set instead of breakNodeId for this boundary - the failure is
   *  the segment between the Service and its Pods, and a halo on either node
   *  (or on the workload that merely sits inside the span) would blame a
   *  resource for its edge's failure. */
  breakAtExitOf?: string
  /** Nodes rendered inline that are NOT network hops (the workload that runs
   *  the Pods). Consumers must never give them journey states - a workload is
   *  neither "reached" nor a place a request stops. */
  nonNetworkNodeIds?: string[]
  /** Configured-but-bypassed resources: entries the selected vantage's request
   *  does not traverse (a relay went past them), or parallel entries the
   *  selected route does not use. Rendered as CONTEXT, never as the journey. */
  contextNodeIds?: string[]
  /** Non-network nodes to splice into the sidebar's display after a journey
   *  hop - the workload after its Service. Display order without polluting the
   *  journey list. */
  interleave?: { id: string; afterId: string }[]
  /** When >1, the journey's entry hops are PARALLEL members of one entry
   *  stage - consumers must say so rather than implying a sequence. */
  entryParallelCount?: number
  /** The journey's entry-hop node ids (subset of pathNodeIds). */
  journeyEntryNodeIds?: string[]
  /** The selected route's own chain, in traversal order. Consumers must read
   *  this rather than sorting rendered nodes by position: sorting flattens
   *  PARALLEL backends into what looks like a serial path, and sweeps in
   *  branches the selected route never touches. */
  pathNodeIds: string[]
  brackets: { d: string }[]
  originIsControl: boolean
  canvas: { w: number; h: number }
  /** Lanes bound only their own nodes, so an unused half of the dataplane does
   *  not render as a large empty rectangle. */
  laneControl?: LaneBox
  laneData?: LaneBox
}

const refId = (r?: ResourceRef): string => (r ? `${r.kind}/${r.namespace ?? ''}/${r.name || 'pods'}` : '')

const isPodsHop = (h: Hop): boolean => h.resource?.kind === 'Pods' || /pods/i.test(h.edge ?? '')

function truncate(s: string, n = PILL_MAX_CHARS): string {
  return s.length <= n ? s : `${s.slice(0, n - 1)}…`
}

/**
 * The headline clause of a probe's evidence.
 *
 * The producer writes full explanations - "Connection refused. Nothing is
 * listening on the port." - which are right for a hover and far too long for an
 * edge pill or a node row: the pill truncated mid-word to "Connectior
 * refused...." and the row ran outside its box. Only the FIRST sentence is
 * taken, and only at a real sentence break, so a "·"-separated detail like
 * "HTTP 200 · 41 ms" survives intact. The untruncated text always stays on the
 * hover.
 */
export function shortEvidence(s: string | undefined): string {
  const t = (s ?? '').trim()
  const cut = t.search(/\.\s/)
  return (cut > 0 ? t.slice(0, cut) : t).replace(/\.$/, '').trim()
}

/** Estimated rendered height of a node box. The renderer uses fixed type sizes,
 *  so this stays accurate without a measure pass. */
function estHeight(n: {
  anomalies?: unknown[]
  notes?: { text: string }[]
  podRows?: unknown[]
  moreRows?: number
  isOrigin?: boolean
  sub?: string
  action?: unknown
}): number {
  const base = n.isOrigin ? 66 : 60
  const anomalies = n.anomalies?.length ?? 0
  // The action either shares the single status row (an origin capsule - the
  // row grows a little) or stacks as its own full-width button. Uncounted, the
  // rendered capsule grew straight out the bottom of its lane box.
  const actionH = n.action ? (n.isOrigin && anomalies === 1 ? 7 : 28) : 0
  const rows = n.podRows?.length ?? 0
  // Long sub-lines wrap; approximate at ~34 characters per line.
  const subLines = Math.max(1, Math.ceil((n.sub?.length ?? 0) / 34))
  // Notes wrap too, and a wrapped note that the layout did not reserve room for
  // is exactly how a node grows into the row beneath it.
  const noteLines = (n.notes ?? []).reduce((sum, x) => sum + Math.max(1, Math.ceil(x.text.length / 26)), 0)
  return (
    base +
    actionH +
    (subLines - 1) * 13 +
    (noteLines > 0 ? 8 + noteLines * 13 : 0) +
    (anomalies > 0 ? 8 + anomalies * 17 : 0) +
    (rows > 0 ? 8 + rows * 17 + (n.moreRows ? 15 : 0) : 0)
  )
}

/** A graph edge's cubic, kept so a pill can be re-placed anywhere along it.
 *  cy1/cy2 are the control-point heights - equal to the endpoints for an
 *  ordinary edge, pulled aside for one that has to route around a node. */
interface Cubic {
  x1: number
  y1: number
  x2: number
  y2: number
  dx: number
  cy1: number
  cy2: number
}

const cubicPath = (c: Cubic): string => `M${c.x1},${c.y1} C${c.x1 + c.dx},${c.cy1} ${c.x2 - c.dx},${c.cy2} ${c.x2},${c.y2}`

/** The point at fraction t along an edge's curve. Evaluates the SAME cubic the
 *  path draws, so a pill is on the line by construction. */
function bezierAt(c: Cubic, t: number): { x: number; y: number } {
  const u = 1 - t
  const f = (p0: number, p1: number, p2: number, p3: number) =>
    u * u * u * p0 + 3 * u * u * t * p1 + 3 * u * t * t * p2 + t * t * t * p3
  return { x: f(c.x1, c.x1 + c.dx, c.x2 - c.dx, c.x2), y: f(c.y1, c.cy1, c.cy2, c.y2) }
}

/**
 * What ONE origin actually observed at ONE hop.
 *
 * Every probe on the wire already names the hop it hit, the vantage that sent
 * it and the mechanism it used - so an entry edge never needs to borrow the
 * route's rollup to know how it went. It used to: a laptop that dialled a public
 * Ingress and got an answer was drawn as "not tested", because the route is
 * built from the SERVICE hop's probes and the Ingress dials never reach it.
 *
 * Returns undefined when this origin sent nothing here, so callers keep their
 * existing fallback rather than inventing a verdict.
 */
export function hopEvidenceFor(
  hop: Hop | undefined,
  origin: Pick<Origin, 'id'>,
  runVantage?: string,
  opts: { stale?: boolean; running?: boolean } = {},
): { mark: Mark; label: string; title?: string } | undefined {
  const live = probesFromOrigin(hop?.probes ?? [], origin, runVantage).filter((p) => !p.skipped)
  if (live.length === 0) return undefined
  if (opts.running) return { mark: 'running', label: 'testing now' }
  if (opts.stale) return { mark: 'stale', label: 'out of date' }
  const worst =
    live.find((p) => !p.ok || p.tone === 'unhealthy') ??
    live.find((p) => p.tone === 'degraded') ??
    // The most specific layer that succeeded is the most informative: an HTTP
    // answer says more than the TCP dial underneath it.
    [...live].sort((a, b) => LAYER_RANK.indexOf(b.layer) - LAYER_RANK.indexOf(a.layer))[0]
  const relayed = worst.path === 'apiserver'
  // Tone 'reached' is the producer saying "answered, but with a 3xx/4xx/app-5xx"
  // - real evidence the port serves, NOT a clean pass. Painting it the same
  // solid green as a 2xx made 'HTTP 404 · reached' read as healthy at a glance;
  // it is exactly the vocabulary's 'answered, but not with what we asked for'.
  const mark: Mark =
    !worst.ok || worst.tone === 'unhealthy'
      ? 'failed'
      : worst.tone === 'degraded' || worst.tone === 'reached'
        ? 'answered'
        : relayed
          ? 'proxied'
          : 'proved'
  const detail = shortEvidence(worst.detail) || shortEvidence(worst.reason)
  return { mark, label: detail || (relayed ? 'relayed by Kubernetes' : 'request'), title: worst.detail || worst.reason }
}

const LAYER_RANK = ['dns', 'tcp', 'tls', 'http']

/** The most telling thing one origin saw anywhere along the path - a failure if
 *  there is one, else the furthest it got. Used when the ROUTE has no row for
 *  this vantage but its probes plainly did something. */
function bestHopEvidence(trace: Trace, o: Origin, opts: { stale?: boolean; running?: boolean }) {
  const hops = [...(trace.upstreams ?? []), ...(trace.downstream ?? [])]
  const seen = hops.map((h) => hopEvidenceFor(h, o, trace.runVantage, opts)).filter((x): x is NonNullable<typeof x> => !!x)
  return seen.find((x) => x.mark === 'failed') ?? seen[0]
}

/** Clearance between a detouring edge and the node it routes around. */
const DETOUR_GAP = 22

/**
 * Slides overlapping pills along their own curves until they stop colliding.
 *
 * Several edges fanning into one node genuinely share a midpoint, so no fixed
 * offset separates them - but the curves are furthest apart near their SOURCES,
 * where each still hugs its own origin. Pulling a colliding pill back toward its
 * source therefore both separates it and keeps it on its line, which putting the
 * text on the origin capsule instead did not: that duplicated the edge and cost
 * a row of height per vantage.
 */
function separatePills(edges: GraphEdge[], geom: Map<string, Cubic>): void {
  const W = PILL_MAX_PX + 14
  const H = 26
  const t = new Map<string, number>()
  const withText = edges.filter((e) => e.label)
  for (let pass = 0; pass < 6; pass++) {
    let moved = false
    for (let i = 0; i < withText.length; i++) {
      for (let j = i + 1; j < withText.length; j++) {
        const a = withText[i]
        const b = withText[j]
        if (Math.abs(a.px - b.px) >= W || Math.abs(a.py - b.py) >= H) continue
        const c = geom.get(b.id)
        if (!c) continue
        // Direction-aware: edges leaving the SAME capsule are closest near
        // their source, so always pulling backward walked the pill INTO the
        // collision. Try both directions and keep whichever separates more.
        const cur = t.get(b.id) ?? 0.5
        const back = Math.max(0.16, cur - 0.09)
        const fwd = Math.min(0.84, cur + 0.09)
        const sep = (pt: { x: number; y: number }) => Math.max(Math.abs(pt.x - a.px) / W, Math.abs(pt.y - a.py) / H)
        const pB = bezierAt(c, back)
        const pF = bezierAt(c, fwd)
        const next = sep(pF) > sep(pB) ? fwd : back
        if (next === cur) continue
        t.set(b.id, next)
        const p = bezierAt(c, next)
        b.px = p.x
        b.py = p.y
        moved = true
      }
    }
    if (!moved) break
  }
  // Edges leaving the SAME capsule can run nearly coincident for their whole
  // span - no point on either curve separates them. Residual colliders stack
  // vertically as a last resort: slightly off the line beats unreadable.
  for (let i = 0; i < withText.length; i++) {
    for (let j = i + 1; j < withText.length; j++) {
      const a = withText[i]
      const b = withText[j]
      if (Math.abs(a.px - b.px) >= W || Math.abs(a.py - b.py) >= H) continue
      b.py = a.py + H + 2
    }
  }
}

/** Rendered per node before collapsing; the rest stay one click away. */
const HOP_NOTE_MAX = 2

/** Longest a note may be before it stops being a headline. */
const NOTE_MAX_CHARS = 46

/**
 * The headline of a finding, for a box the size of a node.
 *
 * Producer messages are written to be read in full - "Accepted:
 * NoMatchingListenerHostname - Gateway x/y listeners http, https: There were no
 * hostname intersections..." is nine lines inside a graph node, which buries the
 * path it is drawn on. Cut at the first real clause break so the CODE survives
 * ("Accepted: NoMatchingListenerHostname"), and let the hover carry the rest.
 */
/** Plain language for the Gateway API condition reasons an operator actually
 *  hits. "Accepted: NoMatchingListenerHostname" is precise condition data and
 *  user-hostile prose - it reads as if the route WAS accepted. The full raw
 *  message stays on the hover/detail for experts. */
const KNOWN_ROUTE_REASONS: Record<string, string> = {
  NoMatchingListenerHostname: 'Not attached: no listener matches its hosts',
  NoMatchingParent: 'Not attached: the Gateway has no matching listener',
  NotAllowedByListeners: 'Not attached: Gateway disallows this namespace',
  RefNotPermitted: 'Backend ref not permitted — missing ReferenceGrant',
  BackendNotFound: 'Backend not found',
}

export function noteHeadline(msg: string): string {
  const t = (msg ?? '').trim()
  // " - " and ". " separate a headline from its explanation; ":" does not - it
  // usually introduces the very thing worth naming.
  const cut = t.search(/\s[-–—]\s|\.\s/)
  let head = (cut > 0 ? t.slice(0, cut) : t).replace(/[.:]$/, '').trim()
  const known = head.match(/^(?:Accepted|ResolvedRefs|Programmed):\s*(\w+)$/)
  if (known && KNOWN_ROUTE_REASONS[known[1]]) head = KNOWN_ROUTE_REASONS[known[1]]
  return head.length <= NOTE_MAX_CHARS ? head : `${head.slice(0, NOTE_MAX_CHARS - 1)}…`
}

/**
 * A hop's findings as node rows: the parsed `cause` when the detector produced
 * one (it is written to be short), else the raw message. The full text - and the
 * remediation - stay in the inspector, so the graph carries the headline and
 * never becomes the place you read paragraphs.
 */
function hopNotes(hop?: Hop): HopNote[] {
  const findings = hop?.findings ?? []
  if (findings.length === 0) return []
  const rank = { critical: 0, warning: 1, info: 2 } as const
  const ordered = [...findings].sort((a, b) => (rank[a.severity] ?? 3) - (rank[b.severity] ?? 3))
  const notes = ordered.slice(0, HOP_NOTE_MAX).map((f) => ({
    severity: f.severity,
    text: noteHeadline(f.cause || f.message),
    // The WHOLE message, always, however short the headline got.
    detail: [f.message, f.action || f.remediation].filter(Boolean).join(' — '),
  }))
  const hidden = ordered.length - notes.length
  if (hidden > 0) {
    notes.push({ severity: ordered[notes.length].severity, text: `+${hidden} more`, detail: 'Select this resource to read the rest.' })
  }
  return notes
}

/** Health from a hop's findings alone - the same rule the topology view uses:
 *  a critical finding is red only when nothing is serving. */
function hopTone(hop: Hop | undefined): SevTone {
  if (!hop) return 'unknown'
  const findings = hop.findings ?? []
  const ready = typeof hop.meta?.ready === 'number' ? (hop.meta.ready as number) : undefined
  const selected = typeof hop.meta?.selected === 'number' ? (hop.meta.selected as number) : undefined
  const serving = typeof ready === 'number' && ready > 0
  if (findings.some((f) => f.severity === 'critical')) return serving ? 'degraded' : 'unhealthy'
  if (findings.some((f) => f.severity === 'warning')) return 'degraded'
  // Nothing ready to serve is resource health even without a finding - the one
  // state the old probe fallthrough was masking. publishNotReadyAddresses
  // deliberately serves not-ready Pods, so it is not unhealthy there.
  if (ready === 0 && (selected ?? 0) > 0 && !hop.meta?.publishNotReadyAddresses) return 'unhealthy'
  // FINDINGS ONLY past this point - never probe outcomes. The dot is the
  // resource's own health and must not change when the selected vantage does;
  // what a request experienced lives on edges and rows, which are
  // origin-scoped on purpose. A readable resource with no findings is healthy.
  return 'healthy'
}

function probesForPod(pod: PodStatus, probes: ProbeResult[]): ProbeResult[] {
  return probes.filter((p) => {
    const key = podProbeKey(p.target)
    return key === pod.name || (!!pod.ip && key === pod.ip)
  })
}

/**
 * Marks that mean this origin actually produced evidence for the scenario.
 * Anything else (untested / denied / blocked / excluded / config) means it did
 * not run, and must not inherit another origin's result.
 */
const EVIDENCE_MARKS: Mark[] = ['proved', 'failed', 'answered', 'proxied', 'stale', 'running', 'slow']

export function originProducedEvidence(origin: Origin): boolean {
  return EVIDENCE_MARKS.includes(origin.mark)
}

/**
 * A hop's probes belong to whichever origin produced them. The graph renders
 * ONE origin at a time, so it must read only that origin's probes - otherwise a
 * vantage that never ran inherits another's result and a laptop's success is
 * painted as a solid proved line inside the dataplane lane.
 */
function probesFromOrigin(probes: ProbeResult[], origin: Pick<Origin, 'id'>, runVantage?: string): ProbeResult[] {
  return probes.filter((p) => originOf(p, runVantage) === origin.id)
}

/**
 * Anomalies held out of a population aggregate. Averaging them away is exactly
 * how a single refusing endpoint that real users hit becomes invisible, so each
 * category is counted and named rather than folded into a percentage.
 */
function populationAnomalies(roster: PodStatus[], total: number, probes: ProbeResult[], publishNotReady: boolean): { mark: Mark; text: string }[] {
  const out: { mark: Mark; text: string }[] = []
  const failing = roster.filter((p) => p.ready && probesForPod(p, probes).some((x) => !x.skipped && (!x.ok || x.tone === 'unhealthy')))
  if (failing.length > 0) {
    out.push({ mark: 'failed', text: `${failing.length} endpoint${failing.length > 1 ? 's' : ''} refused the connection` })
  }
  const slow = roster.filter((p) => p.ready && probesForPod(p, probes).some((x) => !x.skipped && x.ok && isSlow(x)))
  if (slow.length > 0) {
    const worst = slow
      .flatMap((p) => probesForPod(p, probes))
      .filter((x) => !x.skipped && isSlow(x))
      .sort((a, b) => (b.latencyNs ?? 0) - (a.latencyNs ?? 0))[0]
    out.push({ mark: 'slow', text: `${slow.length} slow · ${formatLatency(worst?.latencyNs)}` })
  }
  // With publishNotReadyAddresses the dataplane routes to NotReady Pods too, so
  // they are eligible endpoints - calling them "excluded, never routed to" is
  // false precisely when someone is debugging a not-ready Pod that IS serving.
  const notReady = publishNotReady ? [] : roster.filter((p) => !p.ready)
  if (notReady.length > 0) {
    out.push({ mark: 'excluded', text: `${notReady.length} NotReady — never routed to` })
  }
  // The roster is capped for payload size; anything past it is eligible but
  // unobserved. Unprobed is not proven, so it must be stated, not implied.
  const omitted = total - roster.length
  if (omitted > 0) out.push({ mark: 'untested', text: `${omitted} eligible, not probed` })
  return out
}

/**
 * Short forms of each vantage's mechanism, for the capsule only.
 *
 * `sub` renders at 10px in a 172px box - about 28 characters per line - and
 * every full mech string is 33-46, so each capsule wrapped to two lines and paid
 * a row of height for it. The full wording still renders in the inspector under
 * MECHANISM, where there is room for it.
 */
const SHORT_MECH: Record<string, string> = {
  local: 'your machine, your network',
  apiserver: 'relayed by Kubernetes',
  incluster: 'a throwaway Pod, in-cluster',
  'radar-incluster': 'Radar’s own Pod',
  caller: 'your app’s own Pod',
  external: 'from the public internet',
}

/** Short forms for the origin capsule's tag - the full wording would overrun
 *  the box and collide with the node beside it. */
const SHORT_KIND_TAG: Record<string, string> = {
  SYNTHETIC: 'SYNTHETIC',
  'REAL CLIENT': 'REAL CLIENT',
  'AS A CLIENT': 'AS A CLIENT',
  'REAL CALLER': 'REAL CALLER',
  'RELAYED · NOT A CALLER': 'RELAYED',
}

interface Placed {
  col: number
  row: number
  w: number
  node: Omit<GraphNode, 'x' | 'y' | 'w' | 'h'>
}

interface BuildOpts {
  trace: Trace
  route?: RouteResult
  origin: Origin
  /** Every vantage that can actually run, so all of them are drawn at once and
   *  the comparison between them is visible rather than reconstructed across
   *  clicks. Defaults to just the selected one. */
  origins?: Origin[]
  stale?: boolean
  running?: boolean
}

/**
 * The words for an origin that produced no usable evidence for the scenario.
 * ONE helper for the capsule and every edge pill - two call sites drifted into
 * telling simultaneous different stories ("not routable" on the capsule beside
 * "couldn't test" on the edge). "not routable" itself is retired: it read as a
 * NETWORK verdict about the target, the one claim a vantage whose dials all
 * skipped has no standing to make. 'blocked' means tried-and-couldn't - say
 * that, and let the skip reason on hover carry the why. The in-cluster ATTEMPT
 * whose probe never started keeps its own words: execution failure is its own
 * state, and "not tested" would erase the attempt.
 */
export function originNoEvidenceLabel(o: Pick<Origin, 'id' | 'mark' | 'unavailable'>): string {
  if (o.mark === 'inconclusive') return 'ran — kept informational'
  if (o.mark === 'denied') return 'not permitted'
  if (o.id === 'incluster' && o.mark === 'blocked' && o.unavailable) return 'test couldn’t run'
  if (o.mark === 'blocked') return 'couldn’t test'
  return 'not tested'
}

/** One origin's own verdict on the selected route - the mark for its entry edge
 *  and the words for its pill. Computed per origin so a vantage that never ran
 *  is drawn as never-run beside one that succeeded, instead of the whole graph
 *  speaking for whichever is selected. */
export function originEntryEvidence(
  trace: Trace,
  route: RouteResult | undefined,
  o: Origin,
  opts: { stale?: boolean; running?: boolean } = {},
): { mark: Mark; label: string; title?: string } {
  // A vantage mid-run has no evidence YET, and the evidence gate below would
  // swallow that - so being in flight is stated first. It is the whole truth
  // about this origin right now.
  if (opts.running) return { mark: 'running', label: 'testing now' }
  const ev = originRouteEvidence(route, o.id, trace.runVantage)
  const asSeen = ev.kind === 'none' ? undefined : ev.result
  const fromConfig = ev.kind === 'config'
  const hasEvidence = ev.kind === 'own' || (ev.kind === 'rollup' && originProducedEvidence(o))
  const rm: Mark = asSeen ? routeMark(asSeen, opts) : 'untested'
  // Only the API-server RELAY may downgrade a proved result. A laptop is also
  // outside the cluster, but a request it sends to a real Ingress address goes
  // over the real network and exercises the real path from the ingress inward -
  // calling that "proxied" (which means "bypassed the dataplane") was false.
  const relayed = o.id === 'apiserver'
  // No row for this route does not mean this vantage did nothing - it may have
  // dialled the front door. Report what it actually reached, so the capsule's
  // words agree with its glyph instead of showing a proved dot beside the
  // phrase "not tested".
  const alongPath = !hasEvidence && !fromConfig ? bestHopEvidence(trace, o, opts) : undefined
  const noEvidenceLabel = originNoEvidenceLabel(o)
  const mark: Mark = fromConfig
    ? 'config'
    : !hasEvidence
      ? alongPath?.mark ?? (o.mark === 'denied' || o.mark === 'blocked' ? o.mark : 'untested')
      : relayed && rm === 'proved'
        ? 'proxied'
        : rm
  const label = fromConfig
    ? 'broken as configured'
    : !hasEvidence
      ? alongPath?.label ?? noEvidenceLabel
      : shortEvidence(asSeen?.evidence) || (relayed ? 'relayed by Kubernetes' : 'request')
  return { mark, label, title: hasEvidence || fromConfig ? asSeen?.evidence : alongPath?.title }
}

/**
 * Builds the laned graph for ONE scenario seen from ONE origin.
 *
 * Selecting a different origin genuinely re-routes the graph rather than
 * relabelling it: a control-plane origin enters the subject from the upper lane
 * through an apiserver node, having bypassed kube-proxy, NetworkPolicy and the
 * mesh entirely. Drawing both the same way would be the central lie this view
 * exists to prevent.
 */
export function buildGraph({ trace, route, origin, origins, stale, running }: BuildOpts): GraphModel {
  const placed: Placed[] = []
  const originIsControl = origin.lane === 'control'

  const subjectId = refId(trace.subject)
  const subjectNodeId = `n:${subjectId}`
  const downstream = trace.downstream ?? []
  const subjectHop = downstream.find((h) => refId(h.resource) === subjectId)
  const upstreams = trace.upstreams ?? []

  /**
   * Upstreams are PARALLEL entry points, and a subject can have several
   * backends each with their own Pods. Laying all of that out as one series
   * invented a path that does not exist - two Ingresses read as
   * "Ingress A then Ingress B", and only the first backend's Pods survived.
   * Parentage follows the same rule the topology converter uses.
   */
  const backends: { hop: Hop; id: string }[] = []
  const podGroups: { hop: Hop; id: string; parentId: string }[] = []
  let lastServiceId = subjectNodeId
  for (const dn of downstream) {
    const base = refId(dn.resource)
    if (base === subjectId) {
      lastServiceId = subjectNodeId
      continue
    }
    if (isPodsHop(dn)) {
      // An unnamed Pods hop's id collapses to the same value for every backend,
      // so it must be scoped to its owning Service or later groups are dropped.
      podGroups.push({ hop: dn, id: dn.resource?.name ? `n:${base}` : `${lastServiceId}::pods`, parentId: lastServiceId })
    } else {
      const id = `n:${base}`
      backends.push({ hop: dn, id })
      lastServiceId = id
    }
  }

  /** The scenario names a host; only the entries serving it are on this path.
   *  Shares its matcher with scenario grouping so dimming and grouping can never
   *  disagree about which front door serves a host. */
  const routeHost = routeHostOf(route?.route ?? '')
  const servesRoute = (h: Hop): boolean => !!routeHost && declaredHosts(h).some((d) => hostMatches(d, routeHost))
  // Prefer this origin's OWN result over the merged rollup. Without it the graph
  // painted whatever the worst vantage saw under the selected vantage's name -
  // the central misattribution this view exists to prevent. originProducedEvidence
  // remains the fallback gate for traces carrying no per-vantage breakdown.
  const ev = originRouteEvidence(route, origin.id)
  const asSeen = ev.kind === 'none' ? undefined : ev.result

  const matched = upstreams.filter(servesRoute)
  const activeUpstreams = matched.length > 0 ? matched : upstreams

  /**
   * Fan-out branches are peers, and until now every one of them rendered
   * identically - so on a Gateway with seven attached routes, changing the
   * selected scenario changed nothing on screen and there was no way to tell
   * which branch you were diagnosing. Only entry points were ever dimmed.
   *
   * A branch is on the selected path when it serves the scenario's host or
   * carries its backend. When NOTHING matches we dim nothing: a graph where
   * every branch is greyed out says "none of this is relevant", which is worse
   * than saying nothing at all.
   */
  const routeTarget = (route?.target ?? '').split(':')[0].trim().toLowerCase()
  // Namespace is part of the identity: a Gateway API backendRef can point at a
  // same-named Service in another namespace, and matching on name alone would
  // focus the wrong branch.
  const routeTargetNs = (route?.targetNamespace || trace.subject.namespace || '').toLowerCase()
  const onSelectedPath = (h: Hop): boolean => {
    if (servesRoute(h)) return true
    const name = (h.resource?.name ?? '').toLowerCase()
    if (!routeTarget || name !== routeTarget) return false
    const ns = (h.resource?.namespace || '').toLowerCase()
    return !ns || !routeTargetNs || ns === routeTargetNs
  }
  const matchedBackends = backends.filter((b) => onSelectedPath(b.hop))
  const focusBranches = matchedBackends.length > 0 && matchedBackends.length < backends.length

  /**
   * Exception-first. A Gateway with seven attached routes rendered seven equally
   * loud cards that overflowed the pane, and six of them were fine. What the
   * operator needs is the selected branch and anything WRONG; the quiet
   * remainder is context and can be one line.
   *
   * Collapsing is by relevance, never by position - dropping "the last three"
   * would hide whichever route happened to sort late.
   */
  const worthExpanding = (b: { hop: Hop }): boolean =>
    onSelectedPath(b.hop) || (b.hop.findings ?? []).length > 0
  const expanded = backends.length > FAN_EXPANDED_MAX ? backends.filter(worthExpanding) : backends
  const collapsed = backends.filter((b) => !expanded.includes(b))

  const hopSub = (h: Hop): string => {
    const c = h.config
    if (c?.clusterIP) return `ClusterIP ${c.clusterIP}`
    if (c?.addresses?.length) return c.addresses.join(', ')
    if (c?.hostnames?.length) return c.hostnames.join(', ')
    if (c?.serviceType) return c.serviceType
    return h.resource?.namespace ?? ''
  }

  // ---- columns follow depth in the branch, not position in a list ----
  const COL_ORIGIN = 0
  const COL_ENTRY = 1
  const colSubject = upstreams.length > 0 ? COL_ENTRY + 1 : COL_ENTRY
  const colBackend = colSubject + 1
  // The workload sits between the Service and its Pods, the way the topology
  // view draws it. It routes nothing - both its edges are declared, not observed
  // - but it is what the reader opened, and a network view that omits it makes
  // them hold the connection in their head. Only in the single-chain case: a
  // multi-backend entry has no single workload to place.
  // Only the vantage actually being exercised reads as running. Passing the
  // flag to every origin made all of them animate during one probe run - the
  // same "one vantage's state shown under another's name" this view exists to
  // prevent, in motion.
  const isRunning = (id: string) => !!running && id === 'incluster'

  const workloadNodeId = 'n:workload'
  // The producer resolves this from the Pods' owner chain, so it is present for
  // ANY subject - a Service traced on its own gets its Deployment too, not just
  // the workload-scoped tab. Its absence is a statement, not a gap: the producer
  // returns nil when the selected Pods have no single owner, and substituting
  // the workload the reader happened to open would claim another workload's
  // Pods for it.
  const theWorkload = downstream.find(isPodsHop)?.config?.workload
  const showWorkload = !!theWorkload && backends.length === 0
  const colWorkload = showWorkload ? colSubject + 1 : -1
  const colPods = backends.length > 0 ? colBackend + 1 : showWorkload ? colSubject + 2 : colSubject + 1

  /**
   * Two mechanisms never touch the front door, so drawing them through it is a
   * lie the picture tells before any label is read:
   *
   *  - the API-server proxy dials the Service/Pod subresource directly;
   *  - the in-cluster Job dials the BACKEND Service - internal/trace's own
   *    ProbeRequest doc says the in-cluster dial bypasses the front door, and
   *    the server only stamps its results onto downstream hops.
   *
   * Only the workstation vantage actually requests the declared hostname. When
   * the origin bypasses, it enters beside the declared entries rather than
   * through them: both then point at the subject, which is the truth - one
   * exercised, one merely configured.
   */
  // Route kinds carry no address; a probe reaches their backends, never them.
  const subjectIsAddressable = trace.subject.kind !== 'HTTPRoute' && trace.subject.kind !== 'GRPCRoute'
  const skipsEntries = (id: string) => upstreams.length > 0 && (id === 'apiserver' || id === 'incluster')
  const originSkipsEntries = skipsEntries(origin.id)

  // Every runnable vantage is drawn, in ONE stable order that does not depend on
  // which is selected. Placing the selected one first made the capsules swap
  // places on every click, so the thing you just clicked moved out from under
  // the cursor and the rail's spatial memory was lost.
  const liveOrigins = (origins?.length ? origins : [origin]).filter((o) => !o.unsupported)
  const rowInCol = new Map<number, number>([[COL_ENTRY, upstreams.length]])
  const originPlacement = new Map<string, { col: number; skips: boolean }>()
  for (const o of liveOrigins) {
    const skips = skipsEntries(o.id)
    const col = skips ? COL_ENTRY : COL_ORIGIN
    const row = rowInCol.get(col) ?? 0
    rowInCol.set(col, row + 1)
    originPlacement.set(o.id, { col, skips })
    // The verdict rides on the capsule rather than on the edge: several origins
    // fan into one node, so their edge pills genuinely want the same point, and
    // "what this vantage found" is a property of the vantage anyway.
    const verdict = originEntryEvidence(trace, route, o, { stale, running: isRunning(o.id) })
    placed.push({
      col,
      row,
      w: COL_W.origin,
      node: {
        id: `origin:${o.id}`,
        kind: 'TESTED FROM',
        name: o.name,
        sub: SHORT_MECH[o.id] ?? o.mech,
        // title carries the untruncated evidence - the row truncates visually
        // and a cut "HTTP 308 · reached, redirect…" with no hover hid the
        // destination it redirected to. The vantage's own dial count rides
        // along: zero pixels, and the hover says how much work stands behind
        // the one-line verdict.
        anomalies: [
          {
            mark: verdict.mark,
            text: verdict.label,
            title: (() => {
              const base = verdict.title || verdict.label
              const n = [...(trace.upstreams ?? []), ...(trace.downstream ?? [])]
                .flatMap((h) => h.probes ?? [])
                .filter((pr) => !pr.skipped && originOf(pr, trace.runVantage) === o.id).length
              return n > 0 ? `${base} — ${n} check${n === 1 ? '' : 's'} from this vantage` : base
            })(),
          },
        ],
        // Offered where the gap is: this vantage says "not tested", and the
        // control that would change that sits on it. Only when it has produced
        // nothing yet - an origin with results has nothing to run.
        action:
          o.id === 'incluster' && !originProducedEvidence(o)
            ? {
                text: 'Run this test',
                kind: 'run-in-cluster' as const,
                // Fail closed: permission alone isn't enough - with no runnable
                // request the click is a guaranteed no-op (redis: every port is
                // non-HTTP until native-protocol probing lands).
                disabledReason:
                  o.unavailable ??
                  (traceInClusterRunnable(trace)
                    ? undefined
                    : 'No path on this resource has a request Radar can send from inside the cluster.'),
              }
            : undefined,
        tone: 'info',
        tag: SHORT_KIND_TAG[o.kindTag] ?? o.kindTag,
        isOrigin: true,
        // Dimmed when it is not the one driving the rest of the graph. Its EDGE
        // keeps full-strength marking - that mark is why it is drawn at all.
        dim: o.id !== origin.id,
        lane: o.lane === 'control' ? 'control' : 'data',
      },
    })
  }
  const others = liveOrigins.filter((o) => o.id !== origin.id)

  upstreams.forEach((up, i) => {
    const active = activeUpstreams.includes(up)
    placed.push({
      col: COL_ENTRY,
      row: i,
      w: COL_W.hop,
      node: {
        id: `n:${refId(up.resource)}`,
        kind: (up.resource?.kind || 'ENTRY').toUpperCase(),
        name: up.resource?.name ?? 'entry',
        sub: active ? hopSub(up) : 'does not serve this host',
        tone: hopTone(up),
        notes: hopNotes(up),
        // Entries that do not serve the selected host are shown, but dimmed -
        // hiding them would misrepresent what is attached to this resource.
        dim: !active,
        ref: up.resource,
        hop: up,
        lane: 'data',
      },
    })
  })

  placed.push({
    col: colSubject,
    row: 0,
    w: COL_W.hop,
    node: {
      id: subjectNodeId,
      kind: (trace.subject.kind || 'SERVICE').toUpperCase(),
      name: `${trace.subject.name}${route?.target ? ` ${route.target}` : ''}`,
      sub: subjectHop ? hopSub(subjectHop) : trace.subject.namespace || '',
      tone: hopTone(subjectHop),
      notes: hopNotes(subjectHop),
      ref: trace.subject,
      hop: subjectHop,
      lane: 'data',
    },
  })

  expanded.forEach((b, i) => {
    const onPath = !focusBranches || matchedBackends.includes(b)
    placed.push({
      col: colBackend,
      row: i,
      w: COL_W.hop,
      node: {
        id: b.id,
        kind: (b.hop.resource?.kind || 'BACKEND').toUpperCase(),
        name: b.hop.resource?.name ?? '',
        sub: onPath ? hopSub(b.hop) : 'not on the selected path',
        tone: hopTone(b.hop),
        dim: !onPath,
        notes: hopNotes(b.hop),
        ref: b.hop.resource,
        hop: b.hop,
        lane: 'data',
      },
    })
  })

  if (collapsed.length > 0) {
    placed.push({
      col: colBackend,
      row: expanded.length,
      w: COL_W.hop,
      node: {
        id: 'collapsed:backends',
        kind: `${collapsed.length} MORE`,
        name: collapsed.length === 1 ? '1 more route' : `${collapsed.length} more routes`,
        // Named so the row is a statement, not a truncation: these are quiet
        // because nothing was found on them, not because they were dropped.
        sub: 'nothing found · not on the selected path',
        tone: 'unknown',
        dim: true,
        lane: 'data',
      },
    })
  }

  const deliveryBlocked = (asSeen ? routeMark(asSeen, { stale, running }) : 'untested') === 'failed'
  if (showWorkload && theWorkload) {
    placed.push({
      col: colWorkload,
      row: 0,
      w: WORKLOAD_W,
      node: {
        id: workloadNodeId,
        kind: theWorkload.kind.toUpperCase(),
        name: theWorkload.name,
        ref: theWorkload,
        // Deliberately thin. This is a network view; the workload's own health,
        // replicas and rollout state live on its other tabs.
        sub: 'runs these Pods',
        tone: 'unknown',
        lane: 'data',
      },
    })
  }

  podGroups.forEach((g, i) => {
    const hop = g.hop
    const roster = hop.config?.pods ?? []
    const total = hop.config?.podTotal ?? roster.length
    const ready = typeof hop.meta?.ready === 'number' ? (hop.meta.ready as number) : roster.filter((p) => p.ready).length
    const selected = typeof hop.meta?.selected === 'number' ? (hop.meta.selected as number) : total
    const publishNotReady = !!hop.meta?.publishNotReadyAddresses
    const probes = probesFromOrigin(hop.probes ?? [], origin, trace.runVantage)

    // Anomaly-first: keeping the FIRST six rows hid the failing or excluded Pod
    // behind five healthy ones, which is exactly the row worth showing.
    const rank = (x: PodStatus): number => {
      const mine = probesForPod(x, probes).filter((q) => !q.skipped)
      if (mine.some((q) => !q.ok || q.tone === 'unhealthy')) return 0
      if (mine.some(isSlow)) return 1
      if (!x.ready) return 2
      if (mine.length === 0) return 3
      return 4
    }
    const ordered = [...roster].sort((a, b) => rank(a) - rank(b))
    const podRows: PodRow[] = []
    for (const p of ordered.slice(0, POD_ROW_MAX)) {
      const mine = probesForPod(p, probes).filter((x) => !x.skipped)
      const failed = mine.find((x) => !x.ok || x.tone === 'unhealthy')
      let mark: Mark
      let detail: string
      if (!p.ready && !publishNotReady) {
        mark = 'excluded'
        detail = 'not ready — nothing sent here'
      } else if (failed) {
        mark = 'failed'
        detail = failed.detail || failed.error || 'refused'
      } else if (mine.length === 0 && deliveryBlocked) {
        mark = 'blocked'
        detail = 'not reached — failed earlier'
      } else if (mine.length === 0) {
        mark = 'untested'
        detail = 'not tested'
      } else {
        const slowest = mine.filter(isSlow).sort((a, b) => (b.latencyNs ?? 0) - (a.latencyNs ?? 0))[0]
        mark = slowest ? 'slow' : 'proved'
        const best = mine.find((x) => x.layer === 'http') ?? mine[0]
        detail = slowest ? `slow · ${formatLatency(slowest.latencyNs)}` : shortEvidence(best?.detail) || 'reached'
      }
      if (!p.ready && publishNotReady) detail = `${detail} · not ready, sent traffic anyway`
      // The Pods hop carries its own namespace, which differs from the subject's
      // for a cross-namespace backend - navigating by the subject's would open a
      // Pod that isn't there.
      podRows.push({
        name: p.name,
        mark,
        detail,
        ref: { kind: 'Pod', name: p.name, namespace: hop.resource?.namespace || trace.subject.namespace },
      })
    }

    placed.push({
      col: colPods,
      row: i,
      w: COL_W.pods,
      node: {
        id: g.id,
        kind: 'PODS',
        // "taking traffic" claims observed delivery; readiness only establishes
        // ELIGIBILITY. The inspector already says "eligible" for this same
        // number - the graph must not contradict it.
        name: `${ready} of ${selected} eligible`,
        // The name line already counts eligibility - "every selected Pod is
        // eligible" under "1 of 1 eligible" said the same fact twice. The sub
        // only speaks when it adds something the count can't say.
        sub: publishNotReady
          ? 'not-ready Pods are sent traffic too'
          : ready === selected
            ? ''
            : `${selected - ready} not eligible`,
        tone: hopTone(hop),
        hop,
        notes: hopNotes(hop),
        anomalies: populationAnomalies(roster, total, probes, publishNotReady),
        podRows,
        moreRows: Math.max(0, roster.length - POD_ROW_MAX),
        lane: 'data',
      },
    })
  })

  // ---- resolve geometry ----
  const usedCols = [...new Set(placed.map((p) => p.col))].sort((a, b) => a - b)
  const colX = new Map<number, number>()
  const colW = new Map<number, number>()
  const wlCol = placed.find((p) => p.node.id === workloadNodeId)?.col
  let x = 0
  for (let i = 0; i < usedCols.length; i++) {
    const c = usedCols[i]
    const w = Math.max(...placed.filter((p) => p.col === c).map((p) => p.w))
    colW.set(c, w)
    colX.set(c, x)
    // Both gutters AROUND the workload column tighten - it is a less
    // interesting object for a network view and was stretching the
    // Service→Deployment→Pods stretch into dead space.
    const gutter = c === wlCol || usedCols[i + 1] === wlCol ? WORKLOAD_GUTTER : GUTTER
    x += w + gutter
  }

  const heightOf = (p: Placed) => estHeight(p.node)

  const controlPlaced = placed.filter((p) => p.node.lane === 'control')
  const dataPlaced = placed.filter((p) => p.node.lane === 'data')

  const nodes: GraphNode[] = []
  const pos = new Map<string, GraphNode>()

  /**
   * Stacks one lane's nodes by column and returns its height.
   *
   * The control lane used to pin every node to a single y, which was fine while
   * only one vantage was ever drawn - and put two on top of each other the
   * moment they all became visible at once.
   */
  const layoutLane = (list: Placed[], top: number): number => {
    const byCol = new Map<number, Placed[]>()
    for (const p of list) {
      const arr = byCol.get(p.col) ?? []
      arr.push(p)
      byCol.set(p.col, arr)
    }
    const colHeights = new Map<number, number>()
    for (const [c, l] of byCol) {
      colHeights.set(c, l.reduce((sum, p) => sum + heightOf(p), 0) + (l.length - 1) * ROW_GAP)
    }
    const laneH = Math.max(0, ...colHeights.values())
    for (const [c, l] of byCol) {
      let y = top + (laneH - colHeights.get(c)!) / 2
      for (const p of [...l].sort((a, b) => a.row - b.row)) {
        const h = heightOf(p)
        const n: GraphNode = { ...p.node, x: colX.get(c)!, y, w: colW.get(c)!, h }
        nodes.push(n)
        pos.set(n.id, n)
        y += h + ROW_GAP
      }
    }
    return laneH
  }

  const controlTop = controlPlaced.length > 0 ? LANE_PAD.top : 0
  const controlH = layoutLane(controlPlaced, controlTop)
  const dataTop = controlPlaced.length > 0 ? controlTop + controlH + LANE_PAD.bottom + LANE_GAP + LANE_PAD.top : LANE_PAD.top
  layoutLane(dataPlaced, dataTop)

  // ---- edges ----
  const edges: GraphEdge[] = []
  const geom = new Map<string, Cubic>()
  const brackets: { d: string }[] = []
  const crossesLanes = new Set<string>()

  // `full` is the untruncated text for the hover. Without it a shortened pill
  // label became the tooltip too, so the explanation was discarded rather than
  // moved somewhere it fits.
  /**
   * Where along an edge its pill sits, as a fraction of the curve.
   *
   * Every pill used to sit at the midpoint, which is fine for one edge and
   * collides the moment several fan into the same node: their midpoints are
   * genuinely the same point. Slotting moves them apart ALONG their own curve,
   * so each stays on the line it describes instead of floating beside it.
   */
  const pillT = (slot: number): number => {
    if (slot <= 0) return 0.5
    const step = 0.13
    // Alternate above/below the midpoint so a fan spreads both ways.
    const dir = slot % 2 === 1 ? -1 : 1
    const mag = Math.ceil(slot / 2) * step
    return Math.min(0.78, Math.max(0.22, 0.5 + dir * mag))
  }

  const edgeTo = new Map<string, string>()
  // Edges belonging to a vantage OTHER than the selected one. They are drawn so
  // the comparison is visible, but they must never speak for the selection -
  // reading a break off them let a workstation failure order the report while
  // the in-cluster vantage the reader had chosen was succeeding.
  const foreignEdges = new Set<string>()
  const connect = (id: string, fromId: string, toId: string, mark: Mark, label: string, full?: string, slot = 0, boundarySpan?: 'start' | 'continuation') => {
    const a = pos.get(fromId)
    const b = pos.get(toId)
    if (!a || !b) return
    if (a.lane !== b.lane) crossesLanes.add(id)
    const x1 = a.x + a.w
    const y1 = a.y + a.h / 2
    const x2 = b.x
    const y2 = b.y + b.h / 2
    const dx = Math.max(30, (x2 - x1) * 0.45)
    // An edge that SKIPS a column runs straight through whatever sits in it -
    // the in-cluster probe bypassing an HTTPRoute drew its line, and parked its
    // pill, on top of the very node it does not touch. Route around instead:
    // detour under the obstacles when the curve would cross them.
    const lo = Math.min(y1, y2)
    const hi = Math.max(y1, y2)
    const blocked = [...pos.values()].filter(
      (n) =>
        n.id !== fromId &&
        n.id !== toId &&
        n.x > Math.min(x1, x2) &&
        n.x + n.w < Math.max(x1, x2) &&
        n.y < hi + DETOUR_GAP &&
        n.y + n.h > lo - DETOUR_GAP,
    )
    // A cubic only travels ~3/4 of the way toward its control points, so setting
    // them AT the clearance line leaves the curve short of it - fine for a short
    // node, straight through a tall one. Solve for the control height that puts
    // the CURVE itself past the obstacle:
    //   y(0.5) = (y1 + y2)/8 + (3/4)·D   ⇒   D = (clear - (y1 + y2)/8) / 0.75
    //
    // The SIDE is chosen, never assumed. An edge dropping from the control lane
    // is already above the obstacle; forcing it under sends it swooping below
    // and back up, which is longer and reads as a detour to nowhere. Go round
    // whichever way the line is already passing.
    const controlFor = (clear: number) => (clear - (y1 + y2) / 8) / 0.75
    let cy1 = y1
    let cy2 = y2
    if (blocked.length > 0) {
      const mid = (y1 + y2) / 2
      const above = Math.min(...blocked.map((n) => n.y)) - DETOUR_GAP
      const below = Math.max(...blocked.map((n) => n.y + n.h)) + DETOUR_GAP
      const goAbove = Math.abs(mid - above) < Math.abs(mid - below)
      const overTop = goAbove && controlFor(above) > LANE_PAD.top

      // Clearing the curve's MIDPOINT is not the same as clearing the node. An
      // asymmetric edge has already descended by the time it crosses the node's
      // far edge, so it grazed the corner while the midpoint sat comfortably
      // clear. Sample the span the node actually occupies and push the control
      // until every sampled point clears it.
      const worstOverlap = (control: number): number => {
        const c: Cubic = { x1, y1, x2, y2, dx, cy1: control, cy2: control }
        let worst = 0
        for (let i = 1; i < 20; i++) {
          const pt = bezierAt(c, i / 20)
          for (const n of blocked) {
            if (pt.x < n.x - 2 || pt.x > n.x + n.w + 2) continue
            const need = overTop ? n.y - DETOUR_GAP : n.y + n.h + DETOUR_GAP
            const over = overTop ? pt.y - need : need - pt.y
            if (over > worst) worst = over
          }
        }
        return worst
      }

      let control = controlFor(overTop ? above : below)
      for (let i = 0; i < 8; i++) {
        const over = worstOverlap(control)
        if (over <= 0.5) break
        control += overTop ? -over : over
      }
      cy1 = control
      cy2 = control
    }
    const cubic: Cubic = { x1, y1, x2, y2, dx, cy1, cy2 }
    // Evaluate the SAME cubic the path draws, so the pill is on the line by
    // construction rather than by a midpoint approximation that only holds when
    // the curve happens to be symmetric.
    const t = pillT(slot)
    const p = bezierAt(cubic, t)
    geom.set(id, cubic)
    edgeTo.set(id, toId)
    edges.push({
      id,
      d: cubicPath(cubic),
      mark,
      boundary: boundarySpan,
      label: truncate(label),
      title: full || label,
      px: p.x,
      py: p.y,
    })
  }

  const originNodeId = `origin:${origin.id}`
  // 'own' is this origin's result. 'none' means the producer told us it did not
  // test this route, which outranks anything it did on OTHER routes - so the
  // coarse pooled gate only applies to legacy traces with no breakdown at all.
  const hasEvidence = ev.kind === 'own' || (ev.kind === 'rollup' && originProducedEvidence(origin))
  // Known broken from configuration: true of every vantage, dialled by none.
  const fromConfig = ev.kind === 'config'
  const routeMarkNow: Mark = asSeen ? routeMark(asSeen, { stale, running: isRunning(origin.id) }) : 'untested'
  // A relay can never read as proof: it bypassed the real network path however
  // clean the response was.
  const entryMark: Mark = isRunning(origin.id)
    ? 'running'
    : fromConfig
    ? 'config'
    : !hasEvidence
    ? origin.mark
    : origin.id === 'apiserver' && routeMarkNow === 'proved'
      ? 'proxied'
      : routeMarkNow
  const originBlocked = !!origin.unavailable && origin.mark === 'blocked'
  const noEvidenceLabel = originNoEvidenceLabel(origin)
  const entryLabel = isRunning(origin.id)
    ? 'testing now'
    : fromConfig
    ? 'broken as configured'
    : !hasEvidence
    ? noEvidenceLabel
    : shortEvidence(asSeen?.evidence) || (origin.id === 'apiserver' ? 'relayed by Kubernetes' : 'request')
  // The whole sentence, for the hover.
  const entryTitle = !hasEvidence && !fromConfig ? undefined : asSeen?.evidence || undefined

  // Only the backends this route resolves to - shared by the selected origin's
  // bypass edge and by every other origin's.
  const bypassBackends = focusBranches ? matchedBackends : expanded
  // The request enters at the entry points that serve this host; everything
  // after is configuration, drawn dotted. There is no segment-local evidence to
  // claim otherwise.
  if (upstreams.length > 0 && !originSkipsEntries) {
    for (const up of upstreams) {
      const id = `n:${refId(up.resource)}`
      const active = activeUpstreams.includes(up)
      // This entry's OWN result for this origin, when it has one. The route
      // rollup is the fallback, not the source: it is built from the backend's
      // probes and cannot see a front-door dial.
      const own = active ? hopEvidenceFor(up, origin, trace.runVantage, { stale, running: isRunning(origin.id) }) : undefined
      connect(
        `e:origin-${id}`,
        originNodeId,
        id,
        originBlocked ? 'blocked' : active ? own?.mark ?? 'untested' : 'untested',
        active ? own?.label ?? 'not tested' : 'other host',
        active ? own?.title : undefined,
      )
      connect(`e:${id}-subject`, id, subjectNodeId, 'config', 'routes to')
    }
  } else {
    // The declared entries stay drawn, and stay CONFIG: they are how traffic is
    // meant to arrive, and this run did not use them.
    for (const up of upstreams) {
      connect(`e:${`n:${refId(up.resource)}`}-subject`, `n:${refId(up.resource)}`, subjectNodeId, 'config', 'routes to')
    }
    // An HTTPRoute/GRPCRoute has no address of its own - reachability lives on
    // the parent Gateway and the backend Service. A bypassing origin therefore
    // dials the BACKEND, and terminating its edge on the route object would draw
    // a request to something nothing can dial. Land it on the backends instead.
    // Only the backends this route actually resolves to. Landing the edge on
    // every expanded backend would spread ONE route's evidence across siblings
    // it says nothing about - the cross-backend leak the producer now refuses to
    // make, reintroduced a layer up.
    const bypassTargets = !subjectIsAddressable && originSkipsEntries ? bypassBackends.map((b) => b.id) : []
    if (bypassTargets.length > 0) {
      for (const id of bypassTargets) {
        connect(`e:origin-${id}`, originNodeId, id, originBlocked ? 'blocked' : entryMark, 'dialled directly')
      }
    } else {
      connect(
        'e:origin-subject',
        originNodeId,
        subjectNodeId,
        originBlocked ? 'blocked' : entryMark,
        originSkipsEntries ? 'direct to backend' : entryLabel,
        originSkipsEntries ? undefined : entryTitle,
      )
    }
  }
  // Each unselected origin gets its own edge carrying ITS OWN mark AND its own
  // words. Without them the capsule is decoration and the comparison still costs
  // a click - which is the entire reason the vantages are drawn together. The
  // origins stack vertically, so their edge midpoints do too and the pills
  // separate on their own.
  others.forEach((o, oi) => {
    const p = originPlacement.get(o.id)
    const from = `origin:${o.id}`
    const mine = (id: string) => {
      foreignEdges.add(id)
      return id
    }
    const ev = originEntryEvidence(trace, route, o, { stale, running: isRunning(o.id) })
    const slot = oi + 1
    if (!p?.skips && upstreams.length > 0) {
      for (const up of activeUpstreams) {
        connect(mine(`e:origin-${o.id}-${refId(up.resource)}`), from, `n:${refId(up.resource)}`, ev.mark, '', ev.title || ev.label, slot)
      }
      return
    }
    if (!subjectIsAddressable && p?.skips) {
      for (const b of bypassBackends) connect(mine(`e:origin-${o.id}-${b.id}`), from, b.id, ev.mark, '', ev.title || ev.label, slot)
      return
    }
    connect(mine(`e:origin-${o.id}-subject`), from, subjectNodeId, ev.mark, '', ev.title || ev.label, slot)
  })

  for (const b of expanded) {
    const onPath = !focusBranches || matchedBackends.includes(b)
    connect(`e:subject-${b.id}`, subjectNodeId, b.id, onPath ? 'config' : 'excluded', onPath ? 'sends to' : 'other host')
  }
  if (collapsed.length > 0) connect('e:subject-collapsed', subjectNodeId, 'collapsed:backends', 'config', 'also serves')
  // When the producer localized the break to the Service's own routing, the
  // Service->Pods edge is where it happened - packets reached the workload, so
  // what sits between them is what failed. Only ever drawn from a boundary the
  // producer actually established; an unlocalized failure colours nothing.
  const boundary = ev.kind === 'own' ? routeForOrigin(route, origin.id)?.failedBoundary : undefined
  // The producer establishes a boundary for ONE backend. Painting it on every
  // Service->Pods edge would condemn siblings whose pods were never probed -
  // the same cross-backend leak, one layer up from where it was just fixed.
  // Where the owning branch can't be identified, colour nothing.
  const boundaryParents =
    matchedBackends.length > 0
      ? new Set(matchedBackends.map((b) => b.id))
      : backends.length === 0
        ? new Set([subjectNodeId]) // single chain: the subject IS the Service
        : new Set<string>()
  let breakAtExitOf: string | undefined
  for (const g of podGroups) {
    const broke = boundary === 'service-routing' && boundaryParents.has(g.parentId)
    // A Service-routing break is a BOUNDARY, not a node: the request never got
    // past the Service's exit. The break anchors there (breakAtExitOf) with no
    // node halo - a halo on the workload blamed the Deployment for kube-proxy,
    // and one on the Pods blamed the endpoints for their Service's routing.
    if (broke) breakAtExitOf = g.parentId
    if (showWorkload) {
      const breakTitle =
        'The Service\u2019s routing to its Pods fails here. The Deployment sits between them as the thing that runs the Pods \u2014 it is not a network hop and not the culprit.'
      connect(
        `e:${g.id}-workload`,
        g.parentId,
        workloadNodeId,
        broke ? 'failed' : 'config',
        broke ? 'breaks here' : 'selects',
        broke ? breakTitle : undefined,
        0,
        broke ? 'start' : undefined,
      )
      // The continuation extends the SAME observed break across the span the
      // workload sits inside - dashed, no pill, never a second failed claim.
      connect(
        `e:${g.id}`,
        workloadNodeId,
        g.id,
        broke ? 'failed' : 'config',
        broke ? '' : 'runs',
        broke ? 'Continuation of the same break \u2014 the routing failure is between the Service and its Pods.' : undefined,
        0,
        broke ? 'continuation' : undefined,
      )
      continue
    }
    connect(
      `e:${g.id}`,
      g.parentId,
      g.id,
      broke ? 'failed' : 'config',
      broke ? 'breaks here' : 'selects',
      undefined,
      0,
      broke ? 'start' : undefined,
    )
  }

  separatePills(edges, geom)

  // ---- lane boxes: bound only their own nodes ----
  const boxFor = (list: GraphNode[], label: string, help: string, color: string, dashed?: boolean): LaneBox | undefined => {
    if (list.length === 0) return undefined
    const x0 = Math.min(...list.map((n) => n.x)) - LANE_PAD.x
    const x1 = Math.max(...list.map((n) => n.x + n.w)) + LANE_PAD.x
    const y0 = Math.min(...list.map((n) => n.y)) - LANE_PAD.top
    const y1 = Math.max(...list.map((n) => n.y + n.h)) + LANE_PAD.bottom
    return { x: x0, y: y0, w: x1 - x0, h: y1 - y0, label, help, color, dashed }
  }
  const laneControl = boxFor(
    nodes.filter((n) => n.lane === 'control'),
    'NOT FROM INSIDE THE CLUSTER',
    'Neither request originates inside the cluster’s dataplane, but they differ: a dial from your machine sends real traffic from outside - through a real entry it exercises the path from the entry inward (routing, NetworkPolicy, mesh included). A relayed request is carried by the Kubernetes control plane and bypasses the traffic path entirely.',
    'var(--color-info)',
    true,
  )
  const laneData = boxFor(
    nodes.filter((n) => n.lane === 'data'),
    'FROM INSIDE THE CLUSTER',
    'Started from a Pod inside the cluster, so the request goes through the cluster’s own routing, network policy and mesh. It dials the backend directly, so it says nothing about whether the front door works.',
    'var(--accent-text)',
  )

  // A cross-lane edge's midpoint lands on the dataplane lane's top edge, which
  // is exactly where the lane label sits. Park those pills in the gap between
  // the lanes - otherwise empty - so neither covers the other. This runs AFTER
  // separatePills and can restack pills it had separated (every crossing edge
  // gets the same y), so colliders are re-spread here: the first keeps the
  // gap centre, later ones step outward alternating above/below - slightly
  // into a lane beats two pills on top of each other.
  if (laneControl && laneData) {
    const gapCentre = (laneControl.y + laneControl.h + laneData.y) / 2
    const parked: GraphEdge[] = []
    for (const e of edges) {
      if (!crossesLanes.has(e.id)) continue
      e.py = gapCentre
      for (const other of parked) {
        if (Math.abs(other.px - e.px) < PILL_MAX_PX + 14 && Math.abs(other.py - e.py) < 26) {
          const step = Math.ceil(parked.length / 2) * 28
          e.py = gapCentre + (parked.length % 2 === 1 ? step : -step)
        }
      }
      parked.push(e)
    }
  }

  // Detouring edges bow BELOW the nodes they route around, so the canvas has to
  // reserve room for the deepest of them or the curve clips at the bottom edge.
  const deepest = Math.max(0, ...edges.map((e) => e.py))
  const canvas = {
    w: Math.max(...nodes.map((n) => n.x + n.w), 300) + LANE_PAD.x + 4,
    h: Math.max(...nodes.map((n) => n.y + n.h), deepest, 200) + LANE_PAD.bottom + 4,
  }

  // Leftmost failed edge = the first place a request is known to have stopped.
  // Column order, not array order, so it does not depend on the sequence edges
  // happened to be built in.
  const failed = edges
    .filter((e) => e.mark === 'failed' && !e.boundary && !foreignEdges.has(e.id))
    .map((e) => pos.get(edgeTo.get(e.id) ?? ''))
    .filter((n): n is GraphNode => !!n)
    .sort((a, b) => a.x - b.x)
  const breakNodeId = failed[0]?.id

  // Traversal order, not screen order: entries the route arrives through, the
  // subject, the workload behind it, only the backends this route resolves to,
  // and only those backends' Pods.
  const selectedBackends = focusBranches ? matchedBackends : expanded
  const selectedBackendIds = new Set(selectedBackends.map((b) => b.id))
  // The journey is what the selected vantage's request traverses. A bypassing
  // origin (apiserver relay, in-cluster Job) traverses NO entry; a matched host
  // traverses only ITS entries; parallel non-matched entries are context. The
  // workload is rendered inline but is not a hop - it interleaves in display,
  // never in the journey.
  const journeyUpstreams = originSkipsEntries ? [] : activeUpstreams
  const contextNodeIds = upstreams
    .filter((up) => !journeyUpstreams.includes(up))
    .map((up) => `n:${refId(up.resource)}`)
  const pathNodeIds = [
    ...journeyUpstreams.map((up) => `n:${refId(up.resource)}`),
    subjectNodeId,
    ...selectedBackends.map((b) => b.id),
    ...podGroups.filter((g) => g.parentId === subjectNodeId || selectedBackendIds.has(g.parentId)).map((g) => g.id),
  ].filter((id, i, all) => pos.has(id) && all.indexOf(id) === i)

  return {
    nodes,
    edges,
    brackets,
    originIsControl,
    canvas,
    laneControl,
    laneData,
    breakNodeId,
    breakAtExitOf,
    nonNetworkNodeIds: showWorkload ? [workloadNodeId] : undefined,
    contextNodeIds: contextNodeIds.length ? contextNodeIds : undefined,
    interleave: showWorkload ? [{ id: workloadNodeId, afterId: subjectNodeId }] : undefined,
    entryParallelCount: journeyUpstreams.length > 1 ? journeyUpstreams.length : undefined,
    journeyEntryNodeIds: journeyUpstreams.length ? journeyUpstreams.map((up) => `n:${refId(up.resource)}`) : undefined,
    pathNodeIds,
  }
}
