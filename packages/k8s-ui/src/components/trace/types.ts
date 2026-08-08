// Mirrors internal/trace types.go on the Go side. Keep field names in sync
// when extending - JSON tags on Go must match these property names exactly.

export type Verdict = 'healthy' | 'degraded' | 'broken' | 'unknown'
export type FindingSeverity = 'critical' | 'warning' | 'info'

export interface ResourceRef {
  group?: string
  kind: string
  namespace?: string
  name: string
}

export interface Finding {
  code: string
  severity: FindingSeverity
  message: string
  /** Parsed root cause (plain English) when the detector classified the
   *  failure - e.g. "Image not found in registry", "Exit code 137 (OOMKilled)".
   *  Empty for generic detections; UI renders message in that case. */
  cause?: string
  /** Next-step guidance paired with cause - e.g. "Push the image or fix the
   *  reference", "Increase the container memory limit". Empty when not parsed. */
  action?: string
  remediation?: string
  command?: string
  /** Renderable chip strip carrying structured detail a sentence can't hold -
   *  today the egress-note's allowed destinations. On-chip text is jargon-free;
   *  the jargon lives in each chip's title. */
  chips?: FindingChip[]
  /** Tiny field label printed before the chip strip ("Allowed out"). Empty when
   *  the chips read on their own (deny-all, anywhere). */
  chipsLabel?: string
  /** Quiet tertiary caveats printed under the chips (declared-vs-enforced, the
   *  gated DNS observation). Kept subordinate to the inbound verdict. */
  chipNotes?: string[]
  /** The specific object this finding is about when it differs from the hop it
   *  hangs on - a pod-fanout hop carries one finding per code but the culprit is
   *  a named Pod. Empty = the hop's own resource is the subject. */
  resource?: ResourceRef
}

/** One tag in a Finding's chip strip. Tone 'accent' renders the info-blue
 *  variant (anywhere / deny-all); omitted renders the muted variant. */
export interface FindingChip {
  text: string
  title?: string
  tone?: 'accent' | ''
}

/** Known hop.meta keys stamped by the Go tracer (buildPodsHop et al. in
 *  internal/trace). The wire is an open map - property names must match the Go
 *  meta keys exactly; unknown keys pass through the index signature. */
export interface HopMeta {
  /** Count of endpoints the dataplane actually routes to. For a
   *  publishNotReadyAddresses Service this is the PUBLISHED endpoint count
   *  (every selected pod, readiness not required) - NOT the kubelet-ready
   *  count, so it must never render as "N/M ready" there. */
  ready?: number
  /** Pods matched by the Service selector. */
  selected?: number
  /** Set when the Service publishes endpoints regardless of pod readiness
   *  (spec.publishNotReadyAddresses) - the signal that meta.ready is a
   *  published count, not a readiness count. */
  publishNotReadyAddresses?: boolean
  /** 'pod-readiness' when endpoints were judged from pod state; 'unknown' when
   *  the pod lister failed and the count can't be trusted. */
  endpointSource?: string
  selectorless?: boolean
  [key: string]: unknown
}

export interface Hop {
  resource: ResourceRef
  edge: string
  findings: Finding[]
  meta?: HopMeta
  config?: HopConfig
  probes?: ProbeResult[]
}

export type ProbeLayer = 'dns' | 'tcp' | 'tls' | 'http'
export type ProbeVantage = 'in-cluster' | 'local'
/**
 * Which route a Service/Pod probe took. "data" = straight to the resource
 * over the network (kube-proxy for ClusterIP, direct dial for PodIP).
 * "apiserver" = through the K8s API server's proxy subresource. Empty when
 * the question doesn't apply (DNS, HTTP to an Ingress hostname, etc.).
 */
export type ProbePath = 'data' | 'apiserver'

/**
 * Tone classifies a Result for the UI when ok alone is too coarse. It encodes
 * the honest distinction between reaching a target, verifying what we asked
 * for, and the server erroring:
 *   healthy   - reached AND verified (HTTP 2xx; clean DNS/TCP/TLS)
 *   reached   - reached an HTTP server, but the thing we asked for is unproven
 *               (3xx redirect not followed, or 4xx route/auth not what we hit).
 *               NOT a reachability failure; never renders as "verified" or
 *               "degraded".
 *   degraded  - reached, but the server answered 5xx. Traffic passed.
 *   unhealthy - could not reach (transport failure).
 * Empty falls back to (skipped → unknown, !ok → unhealthy, ok → healthy).
 */
export type ProbeTone = 'healthy' | 'reached' | 'degraded' | 'unhealthy'

export interface ProbeResult {
  layer: ProbeLayer
  target: string
  vantage: ProbeVantage
  path?: ProbePath
  ok: boolean
  tone?: ProbeTone
  skipped?: boolean
  /** Structured skip class from the producer. 'informational' means the probe
   *  RAN and was deliberately kept out of the verdict - a disposition, never a
   *  coverage gap. */
  skipClass?: string
  reason?: string
  latencyNs?: number
  /** The Service/target port this result is for, when port-scoped (lets the UI group
   *  per-port results into one row per path across directions). */
  port?: number
  /** Declared L4 protocol when not TCP - two ServicePorts may share a number. */
  protocol?: string
  detail?: string
  error?: string
  /** What a DNS lookup resolved to. */
  addresses?: string[]
  /** Scope of those addresses. Describes the ADDRESS, not the journey: a public
   *  address does not prove a packet crossed the public internet. */
  addressScope?: 'public' | 'private' | 'mixed'
  /** WHO issued this probe. Radar's own process and a throwaway probe Job are
   *  both in-cluster over the data path but are different observers with
   *  different identities. Absent means 'radar'. */
  source?: 'radar' | 'probe-job'
  /** Copyable command to verify what this probe couldn't (non-HTTP port, HTTPS
   *  backend, an address only reachable in-cluster). Set at the skip site. */
  command?: string
}

export interface PortMap {
  name?: string
  port: number
  targetPort?: string
  protocol?: string
  appProtocol?: string
}

export interface ContainerPortRef {
  container: string
  name?: string
  port: number
  protocol?: string
}

/** One row of the Pods hop's per-pod reachability grid. Mirrors Go trace.PodStatus.
 *  A ready pod joins its probe result by name (apiserver path) or ip (data path);
 *  a not-ready pod has no probe and reads its cause from `reason`. */
export interface PodStatus {
  name: string
  ip?: string
  ready: boolean
  reason?: string
}

export interface ProbeRef {
  container: string
  type: string
  port?: string
  path?: string
  scheme?: string
}

export interface BackendRef {
  kind: string
  name: string
  namespace?: string
  port?: string
}

export interface RouteRule {
  hosts?: string[]
  paths?: string[]
  backends?: BackendRef[]
}

export interface GatewayListener {
  name?: string
  port: number
  protocol?: string
  hostname?: string
}

export interface HopConfig {
  ports?: PortMap[]
  serviceType?: string
  clusterIP?: string
  selector?: Record<string, string>
  containerPorts?: ContainerPortRef[]
  probes?: ProbeRef[]
  hostnames?: string[]
  rules?: RouteRule[]
  tlsHosts?: string[]
  listeners?: GatewayListener[]
  addresses?: string[]
  servedBy?: string
  servedByTitle?: string
  podIPs?: string[]
  podNames?: string[]
  pods?: PodStatus[]
  podTotal?: number
  /** What RUNS these Pods, resolved through their owner chain by the producer.
   *  Absent when the selected Pods have no single owner - a bare Pod, or two
   *  workloads behind one Service, where naming one would be a guess. */
  workload?: ResourceRef
}

export type UnknownClass = 'by-design' | 'investigate'

export interface Trace {
  subject: ResourceRef
  upstreams: Hop[]
  downstream: Hop[]
  verdict: Verdict
  brokenAt: number
  reason?: string
  /** Where RADAR ITSELF ran the inline probes from: 'in-cluster' when Radar is
   *  a Pod (its direct dials are real pod-to-pod traffic, issued with Radar's
   *  own identity) or 'local' when it runs on a workstation. Without this an
   *  in-cluster probe with no `source` is ambiguous between Radar's own dial and
   *  the throwaway Job's. */
  runVantage?: 'in-cluster' | 'local'
  /** Distinguishes the two flavors of the unknown verdict so the UI can
   *  pick the right visual register. 'by-design' covers configurations
   *  where auto-verification doesn't apply (e.g. selectorless Service);
   *  the banner renders as informational. 'investigate' covers cases
   *  where the trace tried and couldn't read state (RBAC, cache cold,
   *  lookup failure); the banner renders as warning. Absent on
   *  non-unknown verdicts. */
  unknownClass?: UnknownClass
  truncated?: boolean
  // Coverage-honest projection (server-authoritative, mirrors internal/trace/coverage.go).
  // These describe ONLY what was actually tested. Localization on a RouteResult is the
  // behind-the-gate evidence, kept separate so it can never feed the headline/outcome.
  coverage?: Coverage
  routes?: RouteResult[]
  notTested?: RouteSkip[]
  brokenRoute?: ResourceRef
  /** The single coverage-honest banner sentence - same string the MCP renders. */
  headline?: string
  /** The one finding that matters - cause, the named culprit, the next action -
   *  promoted from the path so a consumer reads the "why + what next" without
   *  walking the hop chain. Absent when there is nothing to diagnose (every
   *  route verified over real traffic). */
  diagnosis?: Diagnosis
  /** Faults on the DECLARED ENTRY POINTS (an Ingress or route that will never
   *  receive traffic). Deliberately separate from `diagnosis`: entries are
   *  parallel, so a dead front door must not condemn a Service that another
   *  entry still serves - but a verdict scoped to the tested path cannot see it
   *  either. Vantage-invariant; deduped against `diagnosis` by the producer. */
  entryProblems?: EntryProblem[]
}

/** One declared entry point that cannot carry traffic, mirrors
 *  internal/trace/trace.go EntryProblem. */
export interface EntryProblem {
  resource: ResourceRef
  /** The human line; `detail` carries the raw controller cause for the hover. */
  summary: string
  detail?: string
  severity: string
  code?: string
  action?: string
  command?: string
}

/** Hoisted lead diagnosis, mirrors internal/trace/coverage.go Diagnosis. Every
 *  field is PROMOTED from a real finding (or coverage state), never synthesized.
 *  causeCode is the structured code, present ONLY for trustworthy structural
 *  fingerprints (missing-ref / svc:*); a pod-state code is omitted and summary
 *  carries the honest prose instead. */
export interface Diagnosis {
  /** "fault" (something wrong, promoted from a finding) or "coverage" (a
   *  statement about what could be tested). The problem list renders faults
   *  only - a coverage sentence there restates the headline. */
  class?: string
  /** Severity of the finding this was promoted from. */
  severity?: string
  causeCode?: string
  /** Which route this diagnosis explains, when attributable to exactly one.
   *  Absent means it describes the resource as a whole. The selected-path panel
   *  must not present another path's cause as this path's. */
  route?: string
  summary: string
  culpritResource?: ResourceRef
  nextAction?: string
  command?: string
}

/** Per-route outcome (pairs with a confidence). Mirrors the Go Outcome* constants. */
export type RouteOutcome = 'verified' | 'reached' | 'server-error' | 'unreachable' | 'not-tested'
/** How an outcome was learned: 'real' = real-traffic path; 'indirect' = apiserver proxy
 *  (annotates, never sets the headline). '' for a static-known break (missing backend). */
export type RouteConfidence = 'real' | 'indirect' | ''
/** Skip impact: 'coverage' + 'vantage' are real gaps (cap green); 'benign' loses no coverage. */
export type SkipReasonClass = 'coverage' | 'benign' | 'vantage'

export interface Coverage {
  tested: number
  passed: number
  failed: number
  skipped: number
  /** Routes known broken WITHOUT being dialled - from what is declared, or from
   *  current cluster state. Real breaks, but not test results, so they are
   *  reported apart from tested/passed/failed rather than as requests that
   *  failed. */
  derived?: number
}

export interface ProbeFact {
  layer: string
  path?: string
  target?: string
  ok: boolean
  tone?: string
  detail?: string
}

export interface RouteResult {
  route: string
  target?: string
  /** Backend Service namespace - set when it differs from the subject namespace
   *  (cross-namespace Gateway API backendRef) so the in-cluster probe dials an FQDN. */
  targetNamespace?: string
  outcome: RouteOutcome
  /** Which layer failed on a non-reachable route, so the UI can say exactly what
   *  broke: 'tcp' | 'tls' | 'http' (unreachable), or 'upstream' for a 502/504 where
   *  HTTP was reached but the gateway couldn't reach its backend. */
  failedLayer?: 'tcp' | 'tls' | 'http' | 'upstream'
  confidence?: RouteConfidence
  evidence?: string
  command?: string
  /** Unreachable by DESIGN (backing workload intentionally scaled to 0) - read
   *  amber-benign, not red. */
  benign?: boolean
  /** Behind-the-gate facts (apiserver/direct-pod). Render as plain "checked behind it"
   *  sub-lines - never label them "localization" in the UI. */
  localization?: ProbeFact[]
  /** Best-guess concrete request to test this route from inside the cluster.
   *  A STARTING POINT the user edits before running; pathGuessed marks a path
   *  derived from a pattern (no single faithful request exists). */
  inClusterRequest?: ProbeRequest
  /** Where this route's `outcome` came from. Absent (the normal case) means it
   *  was OBSERVED and `byVantage` says by whom. Otherwise it was DERIVED and no
   *  vantage dialled it, so it must never be rendered as the selected origin's
   *  observation nor in the language of a request:
   *
   *   - 'declared-config'  read off what is declared (a backendRef naming a
   *                        Service that does not exist) - broken regardless of
   *                        what the cluster is currently doing.
   *   - 'cluster-state'    read off current state (no ready endpoints) - true
   *                        now, and changes when the workload does. */
  basis?: 'declared-config' | 'cluster-state'
  /** Each vantage's OWN view of this route.
   *
   *  `outcome` / `confidence` / `evidence` above are a documented lossy rollup:
   *  the producer buckets by mechanism and takes worst-wins, so a route that
   *  works in-cluster and fails from a laptop collapses to "unreachable" with no
   *  field left to say where it DID work. Anything answering "did this path work
   *  from THIS vantage" must read this instead of the rollup. */
  byVantage?: VantageResult[]
}

/** One vantage's unmerged view of a route. Keyed by (vantage, path): the same
 *  vantage relayed through the API server is a different claim from one that
 *  used the real network path. */
export interface VantageResult {
  vantage: ProbeVantage
  path: ProbePath
  /** Who dialled: 'radar' (Radar's own process) or 'probe-job' (a throwaway Pod
   *  Radar created). Absent means 'radar'. Kept apart from `vantage` because the
   *  two share (in-cluster, data) but are different observers. */
  source?: 'radar' | 'probe-job'
  outcome: RouteOutcome
  confidence?: RouteConfidence
  evidence?: string
  failedLayer?: 'tcp' | 'tls' | 'http' | 'upstream'
  /** Which hop-to-hop boundary broke, when two observations either side of it
   *  establish that. Empty means we could not tell - the common case, and never
   *  to be guessed at in the UI. */
  failedBoundary?: 'service-routing'
  /** Which part of the DECLARED path this row's dial exercised. 'backend' means
   *  the dial bypassed the route's front door, so this row can never vouch for
   *  the entry path. Empty when there is no separate entry segment. */
  segment?: 'backend'
}

/** A concrete request the in-cluster runner can send to the Service. HTTP
 * fields are absent for transport-only TCP probes. */
export interface ProbeRequest {
  protocol: 'http' | 'https' | 'tcp'
  scheme?: 'http' | 'https'
  host?: string
  path?: string
  pathGuessed?: boolean
}

export interface RouteSkip {
  route?: string
  reason: string
  reasonClass?: SkipReasonClass
  command?: string
}
