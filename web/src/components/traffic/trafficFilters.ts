import type { AggregatedFlow, TrafficFlow } from '../../types'
import type { TrafficEndpointPair } from '../../api/traffic'
import type { TrafficGraphSelection } from './TrafficGraph'

/**
 * Does a flow fall into one of the selected HTTP status ranges?
 *
 * Shared by the graph's aggregated filter and the flow list's per-row filter.
 * They read different fields — one has a bucket histogram and an error count, the
 * other a single status code and an error rate — but the rule has to be the same,
 * and keeping it in one place is what stops the two from drifting apart.
 *
 * `hasErrors` exists because a rate-based source (Beyla, Istio) measures a 5xx
 * rate rather than observing individual responses. It has no status code to offer
 * and no bucket to match, so a filter that insists on one hides precisely the
 * traffic the user asked to see.
 *
 * @param ranges  selected buckets, e.g. {"2xx", "5xx"}; empty means no filtering
 * @param buckets buckets this flow actually reports
 * @param hasErrors whether the flow carries a non-zero error signal
 */
export function matchesStatusRanges(
  ranges: Set<string>,
  buckets: string[],
  hasErrors: boolean
): boolean {
  if (ranges.size === 0) return true
  if (ranges.has('5xx') && hasErrors) return true
  return buckets.some(b => ranges.has(b))
}

/** Buckets an aggregated flow reports, from its status histogram. */
export function bucketsFromCounts(counts: Record<string, number> | undefined): string[] {
  if (!counts) return []
  return Object.keys(counts).filter(k => (counts[k] ?? 0) > 0)
}

/** The single bucket a raw flow reports, if it carries a status code at all. */
export function bucketsFromStatus(httpStatus: number | undefined): string[] {
  if (!httpStatus) return []
  return [`${Math.floor(httpStatus / 100)}xx`]
}

/**
 * Does this source measure rates rather than counting individual events?
 *
 * Beyla and Istio read counters out of Prometheus, so what they report per edge is
 * a per-second rate: "connections" is really requests per second, and the error
 * count is errors per second. Hubble and Caretta observe individual flows and
 * report genuine counts. The distinction changes units, labels and the sensible
 * scale of a volume filter, so it lives in one place rather than being re-derived
 * wherever it is needed.
 */
export function isRateBasedSource(source: string | undefined): boolean {
  return source === 'istio' || source === 'beyla'
}

/**
 * The request and 5xx rates of an edge from a rate-based source, unrounded.
 *
 * requestCount / errorCount carry the same figures rounded with a floor of one,
 * which keeps a trickle visible but makes their ratio meaningless at low rates:
 * 0.3 req/s with 0.01 err/s rounds to one of each, a 100% error rate. The counts
 * are only read when the rates are absent — Radar Hub renders clusters whose
 * Radar predates requestRate / errorRate, and for those they are all there is.
 */
export function requestRateOf(flow: Pick<AggregatedFlow, 'requestRate' | 'requestCount'>): number {
  return flow.requestRate ?? flow.requestCount ?? 0
}
export function errorRateOf(flow: Pick<AggregatedFlow, 'errorRate' | 'errorCount'>): number {
  return flow.errorRate ?? flow.errorCount ?? 0
}

/**
 * The volume shown for an edge. For a rate-based source `connections` is its
 * request rate rounded with a floor of one — the graph sizes, sorts and filters
 * on that integer — so what is printed is the unrounded rate where the edge has
 * one. An edge with no HTTP rate (plain TCP) keeps its connection figure.
 */
export function displayVolume(flow: Pick<AggregatedFlow, 'requestRate' | 'connections'>, isRateBased: boolean): number {
  return isRateBased && flow.requestRate ? flow.requestRate : flow.connections
}

/**
 * A merged edge records the weight behind its average latency on itself, so a
 * later `{...flow}` copy — the Internet collapse, addon grouping — carries it.
 * Client-only: the server never sends it.
 */
type LatencyWeighted = AggregatedFlow & { latencyWeight?: number }

/**
 * How much traffic an edge's average latency stands for: its request rate, or
 * one for an edge with no rate, summed through merges. Zero when it has none.
 * Used to average latencies so a slow trickle does not stand for a busy path.
 */
export function latencyWeightOf(flow: AggregatedFlow): number {
  const merged = (flow as LatencyWeighted).latencyWeight
  if (merged !== undefined) return merged
  return flow.avgLatencyMs ? (flow.requestRate || flow.latencySamples || 1) : 0
}

/**
 * Fold one aggregated flow's volume into another's, for the client-side merges
 * that collapse several edges into one. Every figure displayVolume or the error
 * rate reads has to be summed here: a field left out keeps only the first
 * merged edge's value.
 */
export function mergeFlowVolume(into: AggregatedFlow, flow: AggregatedFlow): void {
  // A metric-based source reports only an average latency per edge, so the
  // merged edge's is theirs weighted by request rate. The weight behind a merged
  // average is carried apart from the edge's request rate, which also counts
  // traffic that had no latency measured: weighting by that would make the
  // result depend on the order the edges arrived in.
  const wInto = latencyWeightOf(into)
  const wFlow = latencyWeightOf(flow)
  if (wFlow > 0) {
    if (wInto > 0) {
      into.avgLatencyMs = (into.avgLatencyMs! * wInto + flow.avgLatencyMs! * wFlow) / (wInto + wFlow)
      // Percentiles of two edges cannot be combined into a percentile of both,
      // so a merged edge that had latency on both sides keeps only the average.
      delete into.latencyP50Ms
      delete into.latencyP95Ms
      delete into.latencyP99Ms
    } else {
      into.avgLatencyMs = flow.avgLatencyMs
      into.latencyP50Ms = flow.latencyP50Ms
      into.latencyP95Ms = flow.latencyP95Ms
      into.latencyP99Ms = flow.latencyP99Ms
    }
  }
  const weighted: LatencyWeighted = into
  weighted.latencyWeight = wInto + wFlow
  into.connections += flow.connections
  into.bytesSent += flow.bytesSent
  into.bytesRecv += flow.bytesRecv
  into.flowCount += flow.flowCount
  if (flow.requestCount) into.requestCount = (into.requestCount || 0) + flow.requestCount
  if (flow.errorCount) into.errorCount = (into.errorCount || 0) + flow.errorCount
  if (flow.requestRate) into.requestRate = (into.requestRate || 0) + flow.requestRate
  if (flow.errorRate) into.errorRate = (into.errorRate || 0) + flow.errorRate
  // New objects rather than in-place sums: a merged edge starts as a shallow
  // copy of the server's, so its maps are still the server's.
  into.httpStatusCounts = sumCounts(into.httpStatusCounts, flow.httpStatusCounts)
  into.verdictCounts = sumCounts(into.verdictCounts, flow.verdictCounts)
  into.dropReasons = sumCounts(into.dropReasons, flow.dropReasons)
}

function sumCounts(a: Record<string, number> | undefined, b: Record<string, number> | undefined): Record<string, number> | undefined {
  if (!b) return a
  if (!a) return { ...b }
  const sum = { ...a }
  for (const [k, v] of Object.entries(b)) sum[k] = (sum[k] ?? 0) + v
  return sum
}

/**
 * How much of the requested window the flows cover, as a short label: "last
 * 1m 40s". Null when the whole window is covered. Measured back from when the
 * data was collected, not from now.
 */
export function coverageLabel(coveredSince: string | undefined, collectedAt: string | undefined): string | null {
  if (!coveredSince || !collectedAt) return null
  const seconds = Math.max(0, Math.round((Date.parse(collectedAt) - Date.parse(coveredSince)) / 1000))
  if (!Number.isFinite(seconds)) return null
  const m = Math.floor(seconds / 60)
  const s = seconds % 60
  return `last ${m > 0 ? `${m}m ${s}s` : `${s}s`}`
}

/** A per-second rate, precise enough to tell a trickle from nothing: 0.30, 12, 1.2K. */
export function formatRate(rate: number): string {
  if (rate >= 1000) return `${(rate / 1000).toFixed(1)}K`
  if (rate >= 10) return rate.toFixed(0)
  if (rate >= 1) return rate.toFixed(1)
  if (rate >= 0.01) return rate.toFixed(2)
  if (rate > 0) return '<0.01'
  return '0'
}

/**
 * Narrow a set of chosen filter values to the ones still offered.
 *
 * The filter controls are built from what the flows actually contain, so a choice
 * can outlive its button: pick 5xx, let the errors stop, and the toggle disappears
 * while the selection keeps filtering — a blank map with nothing left to clear it.
 * Applying the intersection at the point of use rather than editing the stored
 * selection means the choice is ignored while it is unavailable and takes effect
 * again by itself if the traffic comes back.
 */
export function keepAvailable(selected: Set<string>, available: string[]): Set<string> {
  if (selected.size === 0) return selected
  const offered = new Set(available)
  const kept = new Set<string>()
  for (const value of selected) {
    if (offered.has(value)) kept.add(value)
  }
  return kept
}

/** Volume-filter steps for a source that counts events. */
export const CONNECTION_THRESHOLDS = [
  { value: 0, label: 'All traffic' },
  { value: 100, label: '100+ connections' },
  { value: 1000, label: '1K+ connections' },
  { value: 10000, label: '10K+ connections' },
  { value: 100000, label: '100K+ connections' },
]

/**
 * Volume-filter steps for a source that measures rates. Both the unit and the
 * scale differ: a busy service runs at single-digit requests per second, so the
 * connection steps above would filter the whole map away.
 */
export const RATE_THRESHOLDS = [
  { value: 0, label: 'All traffic' },
  { value: 1, label: '1+ req/s' },
  { value: 10, label: '10+ req/s' },
  { value: 100, label: '100+ req/s' },
  { value: 1000, label: '1K+ req/s' },
]

export function volumeThresholds(isRateBased: boolean | undefined) {
  return isRateBased ? RATE_THRESHOLDS : CONNECTION_THRESHOLDS
}

/** Which quantity the volume filter is counting. */
export type VolumeUnit = 'connections' | 'rate'

export function volumeUnit(isRateBased: boolean | undefined): VolumeUnit {
  return isRateBased ? 'rate' : 'connections'
}

/**
 * A volume threshold only means something alongside the unit it was chosen under.
 * 100 appears in both scales — "100+ connections" and "100+ req/s" — so the number
 * alone cannot say whether a stored choice still applies, and carrying it across a
 * source change turns a mild connection filter into a rate filter that hides every
 * edge while the dropdown still looks deliberately set. Falls back to no filtering
 * when the unit has changed, because there is no honest conversion between them.
 */
export function effectiveThreshold(value: number, chosenUnit: VolumeUnit, currentUnit: VolumeUnit): number {
  return chosenUnit === currentUnit ? value : 0
}

/**
 * Is this endpoint kind drawn outside the cluster's workloads?
 *
 * "External" is what every source reports for the world outside; Hubble also
 * tells nodes (`Host`) and endpoints it could not identify (`Unknown`) apart,
 * because a policy evaluation treats each differently. The graph does not:
 * none of them is a workload, so they take the external node's place and
 * styling, and stay out of namespace counts.
 */
export function isExternalKind(kind: string): boolean {
  const k = kind.toLowerCase()
  return k === 'external' || k === 'host' || k === 'unknown'
}

/**
 * Is a drop a NetworkPolicy question at all?
 *
 * Only on positive evidence: Hubble's two policy drop reasons — `POLICY_DENIED`
 * (no rule allowed it) and `POLICY_DENY` (an explicit deny rule) — or the
 * plugin naming the policy itself. A drop with no reported reason is not
 * assumed to be one. The two codes are spelled differently enough that a
 * substring test on either misses the other.
 */
export function isPolicyDropReason(dropReasonDesc: string | undefined, deniedByCount: number): boolean {
  // deniedByCount includes references withheld for permissions: the plugin
  // still named a policy.
  if (deniedByCount > 0) return true
  const code = (dropReasonDesc ?? '').toUpperCase()
  return code === 'POLICY_DENIED' || code === 'POLICY_DENY'
}

/**
 * Drop each HTTP REQUEST record that has a matching RESPONSE: the response
 * carries the status and latency, so it stands for the call. A request with no
 * response stays, because a missing response is worth seeing.
 *
 * `callerOriented` is the server saying its responses run caller → callee on
 * the server's port, like their requests, so they match on endpoints, route and
 * port exactly. Without it the server is a Radar that predates that — Radar Hub
 * renders clusters running such builds — whose responses run server → client on
 * the client's ephemeral port, so they match reversed and without the port.
 * The orientation is taken from the server, never inferred from which records
 * happen to be in the window.
 */
export function dedupeHTTPPairs(flows: TrafficFlow[], callerOriented: boolean): TrafficFlow[] {
  const isHTTP = (f: TrafficFlow, type: string) => f.l7Protocol === 'HTTP' && f.l7Type === type
  const call = (from: string, to: string, f: TrafficFlow) =>
    callerOriented
      ? `${from}|${to}|${f.httpMethod}|${f.httpPath}|${f.port}`
      : `${from}|${to}|${f.httpMethod}|${f.httpPath}`

  const answered = new Set<string>()
  for (const f of flows) {
    if (!isHTTP(f, 'RESPONSE')) continue
    answered.add(callerOriented
      ? call(f.source.name, f.destination.name, f)
      : call(f.destination.name, f.source.name, f))
  }
  return flows.filter(f => !(isHTTP(f, 'REQUEST') && answered.has(call(f.source.name, f.destination.name, f))))
}

/**
 * A graph edge, carrying the server edges merged into it. The graph renames and
 * merges endpoints (external services, the Internet node, the addon group), so
 * these are what a selection is traced back through to ask the server for its
 * records.
 */
export type GraphFlow = AggregatedFlow & { rawPairs?: TrafficEndpointPair[] }

export function endpointPair(flow: AggregatedFlow): TrafficEndpointPair {
  const ref = (e: AggregatedFlow['source']) => ({
    namespace: e.namespace || undefined,
    name: e.name,
    kind: e.kind,
    ...(e.workloadKind && { workloadKind: e.workloadKind }),
  })
  return {
    source: ref(flow.source),
    destination: ref(flow.destination),
    port: flow.port,
    ...(flow.directionUnknown && { directionUnknown: true }),
  }
}

/** Adds a merged edge's pairs to the edge it was merged into. The target owns
 *  its array, so merging appends in place: copying it on every merge is
 *  quadratic, and an ingress with tens of thousands of distinct clients
 *  collapses into a single Internet edge. */
export function mergeRawPairs(into: GraphFlow, from: GraphFlow): void {
  const target = into.rawPairs ?? (into.rawPairs = [])
  for (const pair of from.rawPairs ?? []) target.push(pair)
}

// The renderer draws the addon group's virtual endpoints as one group node.
function selectableId(e: { namespace?: string; name: string; kind?: string }): string {
  if (e.kind === 'AddonGroupTarget' || e.kind === 'AddonGroupSource') return 'addon-group'
  return graphEndpointId(e)
}

/**
 * A record's endpoint as the graph names it: a pod with a known workload is
 * drawn as that workload. Mirrors the server's GraphEndpoint (pkg/traffic),
 * which builds the aggregation the graph is drawn from — the two have to agree
 * for a record to be found under its node.
 */
export function graphEndpoint(e: TrafficFlow['source']): { namespace?: string; name: string } {
  if (e.kind === 'Pod' && e.workload && e.namespace && e.workload !== e.name) return { namespace: e.namespace, name: e.workload }
  return e
}

export function graphEndpointId(e: { namespace?: string; name: string }): string {
  return e.namespace ? `${e.namespace}/${e.name}` : e.name
}

export function pairKey(source: { namespace?: string; name: string }, destination: { namespace?: string; name: string }, port: number, directionUnknown?: boolean): string {
  return `${graphEndpointId(source)}->${graphEndpointId(destination)}:${port}${directionUnknown ? ':u' : ''}`
}

/**
 * The server edges behind the selected node or edge, deduplicated and in a
 * stable order — the server returns its aggregation in no particular order,
 * and the result keys the records query. Null when the selection traces back
 * to none. `inAddonGroup` says which endpoints the addon group node holds
 * while addons are grouped, so selecting the group selects their traffic too.
 */
export function selectionRawPairs(
  flows: GraphFlow[],
  selection: TrafficGraphSelection | null,
  inAddonGroup?: (e: { namespace?: string; name: string }) => boolean,
): TrafficEndpointPair[] | null {
  if (!selection) return null
  const pairs = new Map<string, TrafficEndpointPair>()
  const groupId = (e: GraphFlow['source']) =>
    inAddonGroup && selection.nodeId === 'addon-group' && inAddonGroup(e) ? 'addon-group' : selectableId(e)
  for (const flow of flows) {
    const sourceId = groupId(flow.source)
    const destId = groupId(flow.destination)
    // An edge is drawn per port, so a selected edge stands for its own port.
    // An edge merged from several ports carries one of them, and which one can
    // change between refreshes, so any port merged into it identifies it.
    const selected = selection.type === 'node'
      ? sourceId === selection.nodeId || destId === selection.nodeId
      : sourceId === selection.sourceId && destId === selection.destId &&
        (selection.port === undefined || flow.port === selection.port ||
          (flow.rawPairs ?? []).some(p => p.port === selection.port)) &&
        !!flow.directionUnknown === !!selection.directionUnknown
    if (!selected) continue
    for (const pair of flow.rawPairs ?? []) {
      pairs.set(pairKey(pair.source, pair.destination, pair.port, pair.directionUnknown), pair)
    }
  }
  if (pairs.size === 0) return null
  return Array.from(pairs.entries()).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)).map(([, pair]) => pair)
}

/**
 * How costly the graph is to draw. The layout runs on the main thread and its
 * cost grows faster than linearly, edges more than nodes, so edges count
 * double, as Weave Scope weighs them. Laying out whole namespaces of a dense
 * service graph took 0.55s at a score of 493, 2s at 728, 4.5s at 1,433, 13s
 * at 1,883 and 32s at 2,329; larger graphs overflowed ELK's stack.
 */
export function graphSize(flows: AggregatedFlow[]): { nodes: number; edges: number; score: number } {
  const nodes = new Set<string>()
  for (const flow of flows) {
    nodes.add(graphEndpointId(flow.source))
    nodes.add(graphEndpointId(flow.destination))
  }
  return { nodes: nodes.size, edges: flows.length, score: nodes.size + 2 * flows.length }
}

/** Above this the graph is not drawn until the view is narrowed (about 1.5s of layout). */
export const GRAPH_DRAW_BUDGET = 600
/** Above this it is not drawn at all (about 5s of layout, freezing the tab). */
export const GRAPH_DRAW_CEILING = 1500

/** 5xx responses on an edge: counted where the source observes responses,
 *  a per-second rate where it measures one. */
export function flowErrors(flow: AggregatedFlow): number {
  return flow.httpStatusCounts?.['5xx'] || errorRateOf(flow)
}

export function flowDrops(flow: AggregatedFlow): number {
  return flow.verdictCounts?.dropped ?? 0
}

/**
 * A workload (or any endpoint) the view is narrowed to: it and everything it
 * talks to. Identified by namespace and name, the way the graph names nodes,
 * so it means the same thing whether pods are grouped or not — ungrouped, it
 * is the pods the workload owns.
 */
export interface TrafficFocus {
  namespace?: string
  name: string
}

export function parseFocus(value: string | null): TrafficFocus | null {
  if (!value) return null
  const slash = value.indexOf('/')
  if (slash < 0) return { name: value }
  const namespace = value.slice(0, slash)
  const name = value.slice(slash + 1)
  if (!name) return null
  return namespace ? { namespace, name } : { name }
}

export function focusId(focus: TrafficFocus): string {
  return graphEndpointId(focus)
}

export function touchesFocus(e: AggregatedFlow['source'], focus: TrafficFocus): boolean {
  if ((e.namespace || undefined) !== focus.namespace) return false
  return e.name === focus.name || (e.kind === 'Pod' && e.workload === focus.name)
}

/** The edges with the focus at either end. */
export function focusNeighborhood<T extends AggregatedFlow>(flows: T[], focus: TrafficFocus): T[] {
  return flows.filter(f => touchesFocus(f.source, focus) || touchesFocus(f.destination, focus))
}

export interface EndpointSummary {
  id: string
  name: string
  namespace?: string
  kind: string
  workloadKind?: string
  volume: number
  errors: number
  drops: number
}

/** Every endpoint drawn from these flows, with the traffic on its edges. The
 *  options a focus can be chosen from, so each one has something to show. */
export function endpointSummaries(flows: AggregatedFlow[]): EndpointSummary[] {
  const byId = new Map<string, EndpointSummary>()
  const add = (e: AggregatedFlow['source'], flow: AggregatedFlow) => {
    const id = graphEndpointId(e)
    let s = byId.get(id)
    if (!s) {
      s = { id, name: e.name, namespace: e.namespace || undefined, kind: e.kind, workloadKind: e.workloadKind, volume: 0, errors: 0, drops: 0 }
      byId.set(id, s)
    }
    s.volume += flow.connections
    s.errors += flowErrors(flow)
    s.drops += flowDrops(flow)
  }
  for (const flow of flows) {
    add(flow.source, flow)
    if (graphEndpointId(flow.destination) !== graphEndpointId(flow.source)) add(flow.destination, flow)
  }
  return Array.from(byId.values())
}

/** Where the problems are first, then the busiest. */
export function byProblemsThenVolume(a: { errors: number; drops: number; volume: number }, b: { errors: number; drops: number; volume: number }): number {
  const pa = a.errors > 0 || a.drops > 0 ? 1 : 0
  const pb = b.errors > 0 || b.drops > 0 ? 1 : 0
  if (pa !== pb) return pb - pa
  if (a.drops + a.errors !== b.drops + b.errors) return b.drops + b.errors - (a.drops + a.errors)
  return b.volume - a.volume
}

/**
 * Search results for a query: a name that starts with the query before one
 * that only contains it, and within each, problems first, then volume.
 */
export function searchEndpoints(summaries: EndpointSummary[], query: string, limit = 50): EndpointSummary[] {
  const q = query.trim().toLowerCase()
  const rank = (s: EndpointSummary) => {
    if (!q) return 1
    const name = s.name.toLowerCase()
    if (name === q) return 0
    if (name.startsWith(q)) return 1
    if (name.includes(q)) return 2
    if (s.id.toLowerCase().includes(q)) return 3
    return -1
  }
  return summaries
    .map(s => ({ s, r: rank(s) }))
    .filter(x => x.r >= 0)
    .sort((a, b) => a.r - b.r || byProblemsThenVolume(a.s, b.s))
    .slice(0, limit)
    .map(x => x.s)
}

export interface NamespaceSummary {
  name: string
  endpoints: number
  connections: number
  volume: number
  errors: number
  drops: number
}

/**
 * Per namespace, what narrowing to it would show. Narrowing keeps an edge
 * when either end is in the namespace, as the server does, so a cross-namespace
 * edge counts toward both of its namespaces, and its far end toward the
 * namespace's endpoints.
 */
export function namespaceSummaries(flows: AggregatedFlow[]): NamespaceSummary[] {
  const byNs = new Map<string, { endpoints: Set<string>; connections: number; volume: number; errors: number; drops: number }>()
  for (const flow of flows) {
    const namespaces = new Set([flow.source.namespace, flow.destination.namespace].filter(Boolean))
    for (const ns of namespaces) {
      let s = byNs.get(ns)
      if (!s) {
        s = { endpoints: new Set(), connections: 0, volume: 0, errors: 0, drops: 0 }
        byNs.set(ns, s)
      }
      s.endpoints.add(graphEndpointId(flow.source))
      s.endpoints.add(graphEndpointId(flow.destination))
      s.connections += 1
      s.volume += flow.connections
      s.errors += flowErrors(flow)
      s.drops += flowDrops(flow)
    }
  }
  return Array.from(byNs.entries())
    .map(([name, s]) => ({ name, endpoints: s.endpoints.size, connections: s.connections, volume: s.volume, errors: s.errors, drops: s.drops }))
    .sort((a, b) => byProblemsThenVolume(a, b) || a.name.localeCompare(b.name))
}

/** The edges as table rows, problems first, then volume. */
export function connectionRows<T extends AggregatedFlow>(flows: T[]): T[] {
  const keyed = flows.map(flow => ({ flow, errors: flowErrors(flow), drops: flowDrops(flow), volume: flow.connections }))
  keyed.sort(byProblemsThenVolume)
  return keyed.map(k => k.flow)
}
