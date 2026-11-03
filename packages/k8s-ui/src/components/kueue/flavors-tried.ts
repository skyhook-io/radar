// Parses the flavor attempts out of a pending Workload's QuotaReserved
// message. Kueue builds it as "couldn't assign flavors to pod set <ps>: <reasons>"
// per failing pod set, joined by "; ", with each pod set's reasons sorted
// alphabetically and joined by ", " — so the order of the reasons is not the
// order flavors were tried. A message with any part this parser doesn't
// recognise yields null and is shown verbatim instead.

export type FlavorAttemptKind =
  | 'insufficient_quota'
  | 'insufficient_unused_quota'
  | 'untolerated_taint'
  | 'node_affinity'
  | 'flavor_not_found'
  | 'resource_unavailable'
  | 'skipped_by_annotation'
  | 'pinned_by_original'

export interface FlavorAttempt {
  kind: FlavorAttemptKind
  flavor?: string
  resource?: string
  /** The controller's own words for this reason. */
  text: string
}

export interface PodSetAttempts {
  podSet: string
  attempts: FlavorAttempt[]
}

const podSetPrefix = "couldn't assign flavors to pod set "

/** The message is Kueue's per-flavor account of why quota wasn't assigned (parsed or not). Its error form isn't. */
export function isFlavorAssignmentMessage(message: string | undefined): boolean {
  return !!message && message.startsWith(podSetPrefix)
}

const patterns: { kind: FlavorAttemptKind; re: RegExp; flavor?: number; resource?: number }[] = [
  { kind: 'insufficient_unused_quota', re: /^insufficient unused quota for (\S+) in flavor (\S+), \S+ more needed$/, resource: 1, flavor: 2 },
  { kind: 'insufficient_quota', re: /^insufficient quota for (\S+) in flavor (\S+), (?:previously considered podsets requests \(\S+\) \+ current podset request \(\S+\) > maximum capacity \(\S+\)|request > maximum capacity \(\S+ > \S+\))$/, resource: 1, flavor: 2 },
  { kind: 'untolerated_taint', re: /^untolerated taint .+ in flavor (\S+)$/, flavor: 1 },
  { kind: 'node_affinity', re: /^flavor (\S+) doesn't match node affinity$/, flavor: 1 },
  { kind: 'flavor_not_found', re: /^flavor (\S+) not found$/, flavor: 1 },
  { kind: 'resource_unavailable', re: /^resource (\S+) unavailable in ClusterQueue$/, resource: 1 },
  { kind: 'skipped_by_annotation', re: /^skipping flavor (\S+) due to WorkloadAllowedResourceFlavorAnnotation annotation$/, flavor: 1 },
  // Names the candidate flavor, then the flavor the original Workload holds.
  { kind: 'pinned_by_original', re: /^could not assign (\S+) flavor since the original workload is assigned: \S+$/, flavor: 1 },
]

// A reason begins only at one of these; ", " inside a reason ("…, 2 more needed")
// is not a boundary.
const reasonStart = /, (?=insufficient (?:unused )?quota for |untolerated taint |flavor \S+ (?:doesn't match node affinity|not found)|resource \S+ unavailable in ClusterQueue|skipping flavor |could not assign )/

function parseReason(text: string): FlavorAttempt | null {
  for (const { kind, re, flavor, resource } of patterns) {
    const match = re.exec(text)
    if (match) return { kind, text, ...(flavor ? { flavor: match[flavor] } : {}), ...(resource ? { resource: match[resource] } : {}) }
  }
  return null
}

export function parseFlavorAttempts(message: string | undefined): PodSetAttempts[] | null {
  if (!message?.startsWith(podSetPrefix)) return null
  const result: PodSetAttempts[] = []
  for (const part of message.slice(podSetPrefix.length).split(`; ${podSetPrefix}`)) {
    const separator = part.indexOf(': ')
    if (separator <= 0) return null
    const podSet = part.slice(0, separator)
    const reasons = part.slice(separator + 2).split(reasonStart)
    const attempts: FlavorAttempt[] = []
    for (const reason of reasons) {
      const attempt = parseReason(reason)
      if (!attempt) return null
      attempts.push(attempt)
    }
    result.push({ podSet, attempts })
  }
  return result
}
