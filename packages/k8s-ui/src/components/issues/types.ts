// Shared Issues identity contract + data shapes for the live-issues queue.
//
// k8s-ui owns these because the Issues queue presentation (IssuesView) is
// host-agnostic: Radar Hub feeds it fleet-resolved grouped issues, and OSS
// Radar feeds a single-cluster ("fleet of one") set. Hosts map their wire
// payloads onto these types; the component renders against them.
//
// Mirrors the grouped Issue model radar emits (internal/issues.GroupIssues →
// /api/issues, and the hub's /api/fleet/issues). IssueResourceRef intentionally
// matches the Checks queue's contract (components/checks/types.ts) and
// radar/pkg/audit.ResourceKey — so Issues and Checks share deep-links rather
// than forking a second convention.

/** Operational severity for live issues — distinct from the Checks 4-tier
 *  posture ladder on purpose (operational urgency vs compliance risk are
 *  separate axes). Matches radar's issues.Severity. */
export type IssueSeverity = 'critical' | 'warning';

/** Ordered worst→least. */
export const ISSUE_SEVERITIES: IssueSeverity[] = ['critical', 'warning'];

export const ISSUE_SEVERITY_RANK: Record<IssueSeverity, number> = {
  critical: 2,
  warning: 1,
};

const ISSUE_SOURCE_RANK: Record<string, number> = {
  scheduling: 4,
  missing_ref: 3,
  condition: 2,
  problem: 1,
};

export function isIssueSeverity(s: string): s is IssueSeverity {
  return s === 'critical' || s === 'warning';
}

/**
 * Canonical resource identity. `group` is '' for the core API group;
 * `namespace` is '' for cluster-scoped resources. `cluster_id` scopes the ref
 * to its source cluster (the hub injects it; single-cluster OSS leaves it
 * undefined). Same shape as Checks' CheckResourceRef so deep-link plumbing is
 * shared.
 */
export interface IssueResourceRef {
  cluster_id?: string;
  // group/namespace are optional to match the Go wire (omitempty): a
  // cluster-scoped or core-group member (Node, a core/v1 object) arrives
  // without them. Consumers default to '' (subjectRef/memberRef do this).
  group?: string;
  kind: string;
  namespace?: string;
  name: string;
}

/** Rollup of the underlying resources folded into a grouped issue, by kind
 *  bucket. Empty for single-resource issues (no fan-out). Mirrors the Go
 *  issues.Affected struct. */
export interface IssueAffected {
  pods?: number;
  workloads?: number;
  services?: number;
  pvcs?: number;
  nodes?: number;
}

export type IssueDiagnosticRole = 'candidate' | 'affected' | 'rollup' | 'context';

export interface IssueDiagnosticIssueRef {
  ref: IssueResourceRef;
  reason?: string;
  category?: string;
  severity?: IssueSeverity;
  /** How many affected resources fold into this linked issue from the root's
   *  perspective (e.g. 5 of a PVC's mounting pods under one Deployment issue).
   *  Absent when the link covers a single resource. */
  count?: number;
}

export type IssueDiagnosticConfidence = 'high' | 'medium' | 'low';

/** Reverse pointer from a symptom issue to the root issue that explains it
 *  (the inverse of diagnostic_context's root→symptom facts). Set only when a
 *  single root is unambiguous. `ref` is the parent subject for display + deep
 *  navigation (thread the issue's cluster_id onto it via memberRef). */
export interface IssueIncidentParent {
  id: string;
  ref: IssueResourceRef;
  category?: string;
  confidence?: IssueDiagnosticConfidence;
  fact_type?: string;
}

export interface IssueDiagnosticFact {
  type: string;
  message?: string;
  /** How certain a cross-subject causal link is. Absent for non-causal facts. */
  confidence?: IssueDiagnosticConfidence;
  refs?: IssueResourceRef[];
  related_issues?: IssueDiagnosticIssueRef[];
}

export interface IssueDiagnosticContext {
  role?: IssueDiagnosticRole;
  facts?: IssueDiagnosticFact[];
}

export interface IssueChangeContext {
  changed: boolean;
  what?: string;
  when?: string;
  evidence?: string;
}

export interface IssueRecentChangeField {
  path: string;
  oldValue?: unknown;
  newValue?: unknown;
}

export interface IssueRecentChange {
  source?: string;
  kind: string;
  namespace?: string;
  name: string;
  changeType: string;
  summary?: string;
  timestamp: string;
  change_category?: 'spec_config' | 'lifecycle' | 'runtime_status' | string;
  rank_reason?: string;
  /** Ranking hint for workload runtime configuration (including the image) or directly consumed ConfigMap data; not a causal claim. */
  application_configuration_change?: boolean;
  /** Set only on top-level recent_changes in an eligible, unfiltered issues response with complete linkage evidence. */
  not_linked_to_returned_issues?: boolean;
  fields?: IssueRecentChangeField[];
  /** Workloads that mount/reference this ConfigMap directly ("Deployment/flagd").
   *  Direct spec references only — runtime consumers via an intermediary
   *  service are not captured. */
  consumed_by?: string[];
}

/** "No tracked non-status changes in the window" — the claim is scoped by
 *  window_seconds so a change just outside it can't be misread as absent. */
export interface IssueNoRecentChanges {
  window_seconds: number;
}

/**
 * A grouped live issue — one row of the triage queue. Subject (kind/group/
 * namespace/name) is the topmost owner when the rows folded under a workload,
 * else the resource itself; `members` are the folded underlying resources
 * (the fan-out), bounded inline with `members_truncated`. Mirrors the Go
 * issues.Issue after GroupIssues.
 */
export interface Issue {
  id: string;
  severity: IssueSeverity;
  /** Detection channel (problem|missing_ref|scheduling|condition) — an output
   *  label, not the triage axis. */
  source: string;
  /** Symptom taxonomy (image_pull_failed, crashloop, …) — the triage axis. */
  category: string;
  /** Coarse rollup of category (startup|runtime|networking|…). Server-emitted
   *  so the UI never needs its own category→group map. */
  category_group: string;
  /** Subject kind bucket (workload|service|pvc|ingress|node|unknown). */
  grouping_scope: string;

  // Subject identity (the grouped thing). group is omitted for the core API
  // group, namespace for cluster-scoped subjects — both optional to match the
  // wire (radar emits them omitempty).
  cluster_id?: string;
  cluster_name?: string;
  group?: string;
  kind: string;
  namespace?: string;
  name: string;

  reason: string;
  message?: string;
  raw_message?: string;
  /** Parsed domain diagnosis: plain-English cause, suggested next step, and
   *  an optional structured one-click fix.
   *  Server-emitted (omitempty); empty for issues without a parser. */
  cause?: string;
  action?: string;
  remediation_kind?: string;
  remediation_target?: string;
  /** Controller-operation retry count (e.g. Argo's "(retried N times)") —
   *  distinct from restart_count (pod restarts). stuck = not expected to
   *  self-recover. */
  operation_retry_count?: number;
  stuck?: boolean;
  /** True for an unschedulable pod that explicitly requires a Karpenter NodePool.
   *  Set server-side from a structural pod-spec check (nodeSelector / required
   *  nodeAffinity on karpenter.sh/nodepool), NOT by parsing the scheduler
   *  message. The Issues view uses it to link these — and only these — to the
   *  Capacity / Demand view, so a generic scheduling failure never links. */
  capacity_relevant?: boolean;
  /** Earliest evidence-backed time the issue was active. This may be when Radar
   *  first observed the state rather than exact onset: read as "active at least
   *  since", never as proof of a healthy-duration boundary. */
  first_seen?: string;
  /** Radar can confirm the issue is active, but current cluster evidence does
   *  not establish when the failing state began. */
  onset_unknown?: boolean;
  onset_coverage?: {
    known: number;
    unknown: number;
  };
  /** Kubernetes object creation time. Context and a stable sort fallback only;
   *  it is not evidence that the issue began then. */
  resource_created_at?: string;
  last_seen?: string;
  /** Affected-resource fan-out, EXCLUDING the subject (the row header).
   *  0/omitted for a single-resource issue; e.g. 50 for one Deployment's
   *  50 crashlooping pods. Exposed to API/MCP/CEL consumers, not just here. */
  count?: number;

  affected?: IssueAffected;
  owner?: IssueResourceRef;
  members?: IssueResourceRef[];
  members_truncated?: boolean;
  diagnostic_context?: IssueDiagnosticContext;
  incident_parent?: IssueIncidentParent;
  change_context?: IssueChangeContext;

  // Pod crash context carried from the representative member.
  restart_count?: number;
  last_terminated_reason?: string;

  /**
   * Best-effort timing evidence from K8s-native signals. Absent when Radar has
   * no confident signal. This is not a root-cause verdict.
   *
   * "started_at_resource_creation"        — failing state began during resource
   *                                        creation or first reconciliation.
   * "started_after_resource_was_healthy"  — a meaningful healthy window preceded
   *                                        the failing state. Owner-condition
   *                                        evidence is workload-level, not timing
   *                                        or attribution for this specific reason.
   */
  issue_timing?: 'started_at_resource_creation' | 'started_after_resource_was_healthy';
  /** The evidence that determined issue_timing (for auditability). */
  issue_timing_basis?: 'condition' | 'owner_condition' | 'pod_creation' | 'deletion' | 'phase' | 'spec';
  /** Plain-language explanation when timing fields describe different scopes. */
  timing_summary?: string;

  /** Recent non-status changes (spec/config and lifecycle) on this issue's
   *  subject (and, for workload subjects, its referenced ConfigMaps) —
   *  deterministic evidence, not a causal claim. Populated only on
   *  single-namespace MCP issue responses; never set on /api/issues today. */
  correlated_changes?: IssueRecentChange[];
  /** Explicit "no tracked changes in the window" evidence. An issue with
   *  NEITHER correlation field was not checked — absence must not be read as
   *  "no changes". MCP-only, like correlated_changes. */
  no_recent_changes?: IssueNoRecentChanges;
}

/** subjectRef builds a deep-linkable ref for an issue's subject — the row's
 *  cluster_id threaded onto its group/kind/namespace/name. */
export function subjectRef(issue: Issue): IssueResourceRef {
  return {
    cluster_id: issue.cluster_id,
    group: issue.group ?? '',
    kind: issue.kind,
    namespace: issue.namespace ?? '',
    name: issue.name,
  };
}

/** memberRef threads the issue's cluster_id onto a member ref (members carry
 *  no cluster_id of their own — every member shares the issue's cluster). */
export function memberRef(issue: Issue, member: IssueResourceRef): IssueResourceRef {
  // Normalize the same wire-omitted optionals subjectRef does: Go's Ref.Group /
  // Ref.Namespace are omitempty, so core-API members (Pods) arrive with group /
  // namespace undefined — left raw they'd interpolate "undefined" into host
  // deep-links / React keys and break callbacks that assume a string.
  return {
    ...member,
    group: member.group ?? '',
    namespace: member.namespace ?? '',
    cluster_id: issue.cluster_id,
  };
}

/**
 * compareIssues is the queue's stable sort order (extracted from IssuesView so
 * it can be unit-tested). Severity first (critical before warning), then
 * direct-blocker source priority, then the evidence-backed active-time anchor —
 * first_seen DESC, deliberately
 * NOT last_seen: last_seen bumps to compose-time on every poll, so sorting by
 * it would reshuffle same-severity rows on each refetch. The remaining keys
 * (cluster → namespace → name → id) are a fully deterministic tiebreak so the
 * order never churns under auto-refresh.
 * JavaScript timestamp comparison is millisecond-precision; sub-millisecond
 * ties fall through to the stable keys below.
 */
export function issueSortAnchor(issue: Issue): string {
  return issue.first_seen ?? issue.resource_created_at ?? '';
}

export function compareIssueSortAnchors(a: Issue, b: Issue): number {
  const ta = Date.parse(issueSortAnchor(a)) || 0;
  const tb = Date.parse(issueSortAnchor(b)) || 0;
  return tb - ta;
}

export function compareIssues(a: Issue, b: Issue): number {
  const r = ISSUE_SEVERITY_RANK[b.severity] - ISSUE_SEVERITY_RANK[a.severity];
  if (r !== 0) return r;
  const sr = (ISSUE_SOURCE_RANK[b.source] ?? 0) - (ISSUE_SOURCE_RANK[a.source] ?? 0);
  if (sr !== 0) return sr;
  const onset = compareIssueSortAnchors(a, b);
  if (onset !== 0) return onset;
  const c = (a.cluster_name ?? '').localeCompare(b.cluster_name ?? '');
  if (c !== 0) return c;
  const ns = (a.namespace ?? '').localeCompare(b.namespace ?? '');
  if (ns !== 0) return ns;
  const nm = a.name.localeCompare(b.name);
  if (nm !== 0) return nm;
  return a.id.localeCompare(b.id);
}

/**
 * normalizeImagePullMessage turns a raw containerd/CRI image-pull error — which
 * is verbose and re-quotes the image ref at every wrapped layer ("Back-off
 * pulling image X: ErrImagePull: rpc error: code = NotFound desc = failed to
 * pull and unpack image X: failed to resolve reference X: X: not found") — into
 * a short headline: cause + the image ref once. Returns null for shapes it
 * doesn't recognize, so the caller falls back to the raw string.
 */
export function normalizeImagePullMessage(raw: string): string | null {
  if (!raw) return null;
  const ref = raw.match(/image "([^"]+)"/)?.[1];
  const lower = raw.toLowerCase();
  let cause: string | null = null;
  if (/not\s*found|manifest\s*unknown|no such (image|manifest)/.test(lower)) cause = 'Image not found';
  else if (/unauthorized|forbidden|denied|\b401\b|\b403\b|authentication required/.test(lower)) cause = 'Not authorized to pull image';
  else if (/no such host|i\/o timeout|\btimeout\b|connection refused|dial tcp/.test(lower)) cause = 'Registry unreachable';
  else if (/toomanyrequests|too many requests|rate limit/.test(lower)) cause = 'Registry rate-limited';
  if (!cause) return null;
  return ref ? `${cause}: ${ref}` : cause;
}

/**
 * issueMessageParts splits an issue's message into the inline headline and the
 * raw secondary detail. For image-pull issues the headline is a normalized
 * one-liner and detail holds the original CRI string; for every other issue the
 * headline IS the (already concise) message and detail is empty — no
 * duplication. Gated on image-pull so a generic "not found" in, say, a
 * missing_config_ref message ('secret "x" not found') is never mislabeled.
 */
export function issueMessageParts(issue: Issue): { headline: string; detail: string } {
  const raw = issue.message ?? '';
  const isImagePull = issue.category === 'image_pull_failed' || /ImagePull|ErrImage|InvalidImageName|ImageInspect/i.test(issue.reason ?? '');
  const normalized = isImagePull ? normalizeImagePullMessage(raw) : null;
  if (normalized && normalized !== raw) return { headline: normalized, detail: raw };
  return { headline: raw, detail: '' };
}
