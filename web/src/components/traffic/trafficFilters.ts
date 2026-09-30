import type { AggregatedFlow, TrafficFlow } from '../../types'

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
  return flow.avgLatencyMs ? (flow.requestRate || 1) : 0
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
    into.avgLatencyMs = wInto > 0
      ? (into.avgLatencyMs! * wInto + flow.avgLatencyMs! * wFlow) / (wInto + wFlow)
      : flow.avgLatencyMs
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
 * The server orients a response like its request, caller → callee. A Radar that
 * predates that sends it server → client — Radar Hub renders clusters running
 * such builds — so a response is matched in either orientation.
 */
export function dedupeHTTPPairs(flows: TrafficFlow[]): TrafficFlow[] {
  const key = (from: string, to: string, f: TrafficFlow) => `${from}|${to}|${f.httpMethod}|${f.httpPath}`
  const responseKeys = new Set<string>()
  for (const f of flows) {
    if (f.l7Protocol === 'HTTP' && f.l7Type === 'RESPONSE') {
      responseKeys.add(key(f.source.name, f.destination.name, f))
      responseKeys.add(key(f.destination.name, f.source.name, f))
    }
  }
  return flows.filter(f =>
    !(f.l7Protocol === 'HTTP' && f.l7Type === 'REQUEST' && responseKeys.has(key(f.source.name, f.destination.name, f))))
}
