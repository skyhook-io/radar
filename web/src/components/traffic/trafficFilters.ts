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
