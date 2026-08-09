export { inClusterOutcome, inClusterEligible } from './TracePanel'
export { ReachabilityView } from './ReachabilityView'
export { podReach, podProbeKey } from './podReach'
// Reachability v2 internals - exported so hosts can compose the same vocabulary
// (marks, origins) elsewhere without re-deriving it.
export { buildOrigins, defaultOrigin, strongestGap } from './reachOrigins'
export type { Origin, OriginId, Lane, OriginKind } from './reachOrigins'
export { routeMark, routeTone, routeChip, orderRoutes, MARKS, MARK_LEGEND } from './reachMarks'
export type { Mark, SevTone } from './reachMarks'
export { traceToSubgraph } from './traceToSubgraph'
export { traceFingerprint, staticPollUnreliable } from './traceFingerprint'
export { summarizeInClusterTests } from './inClusterSummary'
export type { InClusterTestRow, InClusterSummary } from './inClusterSummary'
export { TraceSummary } from './TraceSummary'
export type { InClusterCapability } from './TracePanel'
// ResourceRef intentionally NOT re-exported from the package root - it would
// collide with the global ResourceRef in types.ts. Trace consumers import it
// from the panel module directly when they need the typed shape.
export type { Trace, Hop, Finding, FindingSeverity, Verdict } from './types'
