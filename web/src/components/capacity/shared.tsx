import {
  useCallback,
  useEffect,
  useState,
  type ComponentType,
  type ReactNode,
} from "react";
import {
  AlertTriangle,
  ChevronLeft,
  ChevronRight,
  Gauge,
  Shield,
} from "lucide-react";
import {
  FreshnessControl,
  formatDuration,
  memberRef,
  PaneLoader,
  ResourceBar,
  Tooltip,
  WithTooltip,
  type CapacityActivityEpisode,
  type CapacityCertainty,
  type CapacityClaimStage,
  type CapacityCondition,
  type CapacityCoverageBySource,
  type CapacityCoverageSource,
  type CapacityDemandGroup,
  type CapacityDemandState,
  type CapacityIntegrationState,
  type CapacityLimitPressure,
  type CapacityManager,
  type CapacityManagerRollupStatus,
  type CapacityManagerSummary,
  type CapacityMemberType,
  type CapacityPoolSummary,
  type CapacityQuantityObservation,
  type CapacityResourceIdentity,
  type CapacityResponseMeta,
  type CapacitySourceCoverage,
  type CapacityUsageObservation,
  type Issue,
  type StatusTone,
} from "@skyhook-io/k8s-ui";
import { Badge } from "@skyhook-io/k8s-ui/components/ui/Badge";
import {
  formatCPUString,
  formatMemoryString,
} from "@skyhook-io/k8s-ui/utils/format";
import {
  isCapacityCursorInvalidError,
  isForbiddenError,
  isNotFoundError,
  useCapacityPools,
} from "../../api/client";
import type { SelectedResource } from "../../types";
import { refToSelectedResource } from "../../utils/navigation";

// ============================================================================
// Route model + navigation
// ============================================================================

export type CapacityTopTab = "overview" | "demand" | "activity";
export type PoolSection = "summary" | "workloads" | "members" | "configuration";
export type CapacityConnectionState =
  "connected" | "disconnected" | "connecting";

export interface CapacityRoute {
  topTab: CapacityTopTab;
  poolName?: string;
  poolSection: PoolSection;
}

export const POOL_SECTIONS: { id: PoolSection; label: string }[] = [
  { id: "summary", label: "Summary" },
  { id: "workloads", label: "Workloads" },
  { id: "members", label: "Nodes & claims" },
  { id: "configuration", label: "Configuration" },
];

export function parseCapacityRoute(pathname: string): CapacityRoute {
  const segments = pathname.replace(/^\/+|\/+$/g, "").split("/");
  if (segments[0] !== "capacity")
    return { topTab: "overview", poolSection: "summary" };
  if (segments[1] === "pools" && segments[2]) {
    const section = segments[3];
    return {
      topTab: "overview",
      poolName: decodePathSegment(segments[2]),
      poolSection:
        section === "workloads" ||
        section === "members" ||
        section === "configuration"
          ? section
          : "summary",
    };
  }
  if (segments[1] === "demand" || segments[1] === "activity")
    return { topTab: segments[1], poolSection: "summary" };
  return { topTab: "overview", poolSection: "summary" };
}

function decodePathSegment(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

export function capacityPoolPath(name: string): string {
  return `/capacity/pools/${encodeURIComponent(name)}`;
}

export function identityToSelectedResource(
  identity: CapacityResourceIdentity,
): SelectedResource {
  return refToSelectedResource(identity.ref);
}

export function identityKey(identity: CapacityResourceIdentity): string {
  return `${identity.ref.group ?? ""}/${identity.ref.kind}/${identity.ref.namespace ?? ""}/${identity.ref.name}`;
}

export function CapacityIssueEvidence({
  issue,
  onOpenResource,
  onOpenMembers,
}: {
  issue: Issue;
  onOpenResource: (resource: SelectedResource) => void;
  onOpenMembers?: () => void;
}) {
  const members = issue.members ?? [];
  const total = issue.count ?? members.length;
  if (total === 0 && members.length === 0) return null;
  const linksToPoolMembers = members.some(
    (member) => member.kind === "Node" || member.kind === "NodeClaim",
  );

  return (
    <div className="mt-2 rounded-lg border border-theme-border-subtle bg-theme-base/50 px-3 py-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs font-medium text-theme-text-secondary">
          {total} affected {total === 1 ? "resource" : "resources"}
        </span>
        {onOpenMembers && linksToPoolMembers && (
          <LinkButton onClick={onOpenMembers}>
            Inspect nodes &amp; claims →
          </LinkButton>
        )}
      </div>
      {members.length > 0 && (
        <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-1">
          {members.map((member, index) => {
            const ref = memberRef(issue, member);
            return (
              <button
                key={`${ref.group}/${ref.kind}/${ref.namespace}/${ref.name}#${index}`}
                type="button"
                className="font-mono text-xs font-medium text-accent-text hover:underline"
                onClick={() => onOpenResource(refToSelectedResource(ref))}
              >
                {ref.kind}/{ref.name}
              </button>
            );
          })}
        </div>
      )}
      {issue.members_truncated && (
        <p className="mt-1.5 text-[11px] text-theme-text-tertiary">
          {total > members.length
            ? `Showing ${members.length} of ${total} affected resources.`
            : `Additional affected resources are omitted beyond the ${members.length} shown.`}
        </p>
      )}
    </div>
  );
}

// ============================================================================
// Coverage semantics ("unavailable ≠ zero", lower-bound, staleness)
// ============================================================================

export function coverageHasObservations(
  coverage?: CapacitySourceCoverage,
): boolean {
  return coverage?.status === "available" || coverage?.status === "partial";
}

export function coverageIsLowerBound(
  coverage?: CapacitySourceCoverage,
): boolean {
  return Boolean(
    coverageHasObservations(coverage) &&
    ((coverage?.status === "partial" && !nodeMetricsAreStale(coverage)) ||
      coverage?.scope !== "cluster"),
  );
}

export function coverageIsDenied(coverage?: CapacitySourceCoverage): boolean {
  return coverage?.status === "denied";
}

function nodeMetricsAreStale(coverage?: CapacitySourceCoverage): boolean {
  return coverage?.reasonCode === "node_metrics_stale";
}

export function namespaceCoverageDescription(
  coverage: CapacitySourceCoverage | undefined,
): string {
  if (coverage?.scope === "explicit_namespaces") {
    const count = coverage.namespaces?.length ?? 0;
    return count > 0
      ? `${count} selected ${count === 1 ? "namespace" : "namespaces"}`
      : "the selected namespaces";
  }
  if (coverage?.scope === "all_authorized_namespaces")
    return "all namespaces authorized for this user";
  return "the observed inventory";
}

export function memberCoverageSource(
  type: CapacityMemberType,
): CapacityCoverageSource {
  if (type === "node") return "nodes";
  if (type === "claim") return "nodeClaims";
  return "workloads";
}

export function coverageMessage(
  coverage: CapacitySourceCoverage | undefined,
  subject: string,
): string {
  if (!coverage) return `${subject} coverage was not reported`;
  if (coverage.reason) return coverage.reason;
  if (coverageIsLowerBound(coverage))
    return `${subject} is a lower-bound view of ${namespaceCoverageDescription(coverage)}`;
  if (coverage.status === "available") return `${subject} observed`;
  if (coverage.status === "partial") return `${subject} has partial coverage`;
  if (coverage.status === "denied")
    return `${subject} is hidden by permissions`;
  if (coverage.status === "syncing") return `${subject} is still syncing`;
  if (coverage.status === "error") return `${subject} could not be observed`;
  return `${subject} is unavailable`;
}

function missingUsageMessage(coverage?: CapacitySourceCoverage): string {
  if (nodeMetricsAreStale(coverage))
    return "The retained node metrics are stale and this pool has no sample";
  if (coverage?.status === "available")
    return "No live node samples were reported for this pool";
  if (coverage?.status === "partial" && !coverage.reason)
    return "No live sample was present in the partially observed nodes";
  return coverageMessage(coverage, "Live usage");
}

// ============================================================================
// Certainty glyphs — the design's per-value trust vocabulary
// = exact · ≥ lower bound · ≤ upper bound · ? unknown
// ============================================================================

export function certaintyGlyph(certainty: CapacityCertainty): string {
  if (certainty === "exact") return "=";
  if (certainty === "lower_bound") return "≥";
  if (certainty === "upper_bound") return "≤";
  return "?";
}

export function certaintyValueLabel(certainty: CapacityCertainty): string {
  if (certainty === "exact") return "Exact";
  if (certainty === "lower_bound") return "Lower bound";
  if (certainty === "upper_bound") return "Upper bound";
  return "Unknown certainty";
}

export function observationTitle(
  observation: CapacityQuantityObservation,
  definition?: string,
): string {
  const parts = [
    definition,
    `${certaintyValueLabel(observation.certainty)}.`,
    observation.sources.length > 0
      ? `Source: ${sourceLabel(observation.sources)}.`
      : undefined,
    `Observed ${formatTimestamp(observation.asOf)}.`,
  ].filter(Boolean);
  return parts.join(" ");
}

/** Small bordered mono glyph chip; hover reveals certainty/source/asOf. */
export function CertaintyGlyph({
  certainty,
  title,
}: {
  certainty: CapacityCertainty;
  title?: string;
}) {
  return (
    <WithTooltip tip={title ?? certaintyValueLabel(certainty)}>
      <span
        tabIndex={0}
        role="note"
        aria-label={title ?? certaintyValueLabel(certainty)}
        className="cursor-help rounded border border-theme-border-light px-1 font-mono text-[10px] leading-tight text-theme-text-tertiary focus-visible:outline focus-visible:outline-2 focus-visible:outline-skyhook-500"
      >
        {certaintyGlyph(certainty)}
      </span>
    </WithTooltip>
  );
}

// ============================================================================
// Quantity / resource formatting
// ============================================================================

export function formatQuantity(resource: string, quantity: string): string {
  if (resource === "cpu") return formatCPUString(quantity);
  if (resource === "memory" || resource === "ephemeral-storage")
    return formatMemoryString(quantity);
  return quantity;
}

export function resourceLabel(resource: string): string {
  if (resource === "cpu") return "CPU";
  if (resource === "memory") return "Memory";
  if (resource === "ephemeral-storage") return "Ephemeral storage";
  if (resource === "nvidia.com/gpu") return "GPU";
  if (resource === "pods") return "Pods";
  if (resource === "nodes") return "Nodes";
  return resource;
}

export function quantityResourceRank(resource: string): number {
  if (resource === "cpu") return 0;
  if (resource === "memory") return 1;
  if (resource.includes("gpu") || resource.includes("accelerator")) return 2;
  if (resource.includes("/")) return 3;
  if (resource === "pods" || resource === "nodes") return 4;
  if (resource === "ephemeral-storage") return 5;
  if (resource.startsWith("hugepages-")) return 7;
  return 6;
}

export function shortResourceLabel(resource: string): string {
  if (resource === "memory") return "MEM";
  if (resource === "ephemeral-storage") return "DISK";
  if (resource === "nvidia.com/gpu") return "GPU";
  if (resource.includes("/")) {
    const suffix = resource.split("/").at(-1);
    if (!suffix) return "EXT";
    return (suffix.startsWith("pod-") ? suffix.slice(4) : suffix)
      .slice(0, 4)
      .toUpperCase();
  }
  return resource.slice(0, 4).toUpperCase();
}

export function sortedResourceEntries(
  resources: Record<string, string>,
): [string, string][] {
  return Object.entries(resources).sort(
    ([left], [right]) =>
      quantityResourceRank(left) - quantityResourceRank(right) ||
      left.localeCompare(right),
  );
}

/** "8 CPU · 32Gi" style compact one-liner for a quantity observation. */
export function quantityText(
  observation: CapacityQuantityObservation | undefined,
  options?: { max?: number },
): string | null {
  if (!observation) return null;
  const entries = sortedResourceEntries(observation.resources);
  if (entries.length === 0) return null;
  const max = options?.max ?? 4;
  const shown = entries
    .slice(0, max)
    .map(([resource, value]) => formatQuantity(resource, value));
  if (entries.length > max) shown.push(`+${entries.length - max}`);
  return shown.join(" · ");
}

function sourceLabel(sources: string[]): string {
  return sources.length > 0 ? sources.join(" + ") : "source not reported";
}

export function formatTimestamp(value: string): string {
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp)
    ? new Date(timestamp).toLocaleString()
    : value;
}

export function relativeTime(value?: string, now = Date.now()): string {
  if (!value) return "—";
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) return value;
  const seconds = Math.max(0, Math.round((now - timestamp) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) {
    const hours = Math.floor(seconds / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);
    return minutes > 0 ? `${hours}h ${minutes}m ago` : `${hours}h ago`;
  }
  return `${Math.floor(seconds / 86400)}d ago`;
}

export function formatTaint(taint: {
  key: string;
  value?: string;
  effect: string;
}): string {
  return `${taint.key}${taint.value ? `=${taint.value}` : ""}:${taint.effect}`;
}

function generatedAtMillis(generatedAt: string, fallback: number): number {
  const value = Date.parse(generatedAt);
  return Number.isFinite(value) ? value : fallback;
}

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "An unknown error occurred.";
}

// ============================================================================
// Label helpers
// ============================================================================

export function poolReadinessDetail(
  pools: CapacityPoolSummary[],
  truncated: boolean,
): string {
  const ready = pools.filter((pool) => pool.ready === true).length;
  const notReady = pools.filter((pool) => pool.ready === false).length;
  const unknown = pools.filter((pool) => pool.ready === undefined).length;
  const parts: string[] = [];
  if (ready > 0) parts.push(`${ready} ready`);
  if (notReady > 0) parts.push(`${notReady} unready`);
  if (unknown > 0) parts.push(`${unknown} unknown`);
  if (parts.length === 0)
    return pools.length === 0 ? "No pools visible" : "Readiness unknown";
  return truncated ? `${parts.join(" · ")} (first page)` : parts.join(" · ");
}

/** Human label for a detected capacity manager. */
export function capacityManagerLabel(manager: CapacityManager): string {
  if (manager === "karpenter") return "Karpenter";
  if (manager === "gke_autoscaler") return "GKE autoscaler";
  if (manager === "cluster_autoscaler") return "Cluster Autoscaler";
  if (manager === "aks_autoscaler") return "AKS autoscaler";
  return manager;
}

/** Manager rollup status → status tone. `unknown` maps to neutral (an absence
 *  of health signal), never to the alarming `unknown` slate — the capacity
 *  contract treats "we haven't determined health" as informational, not bad. */
export function managerStatusTone(
  status: CapacityManagerRollupStatus,
): StatusTone {
  if (status === "healthy") return "healthy";
  if (status === "degraded") return "degraded";
  return "neutral";
}

const MANAGER_STATUS_RANK: Record<string, number> = {
  degraded: 0,
  unknown: 1,
  healthy: 2,
};

/** Severity rank of a manager rollup status (lower is worse). A status this
 *  build does not know ranks as `unknown` — never as an accidental best-of that
 *  would hide a real degradation behind a newer wire value. */
export function managerStatusRank(status: string): number {
  return MANAGER_STATUS_RANK[status] ?? MANAGER_STATUS_RANK.unknown;
}

/** Worst-of status across managers (degraded worst), driving the tile accent.
 *  Undefined when no managers were detected. */
export function worstManagerStatus(
  managers: CapacityManagerSummary[],
): CapacityManagerRollupStatus | undefined {
  if (managers.length === 0) return undefined;
  return managers.reduce<CapacityManagerRollupStatus>(
    (worst, manager) =>
      managerStatusRank(manager.status) < managerStatusRank(worst)
        ? manager.status
        : worst,
    managers[0].status,
  );
}

export function demandStateLabel(state: CapacityDemandState): string {
  if (state === "waiting_for_scheduler") return "Waiting for scheduler";
  if (state === "held") return "Held by scheduling gate";
  if (state === "awaiting_capacity") return "Awaiting capacity";
  if (state === "blocked") return "Blocked";
  return "Unknown";
}

export function activityTypeLabel(
  type: CapacityActivityEpisode["type"],
): string {
  if (type === "launch_failure") return "Launch failure";
  if (type === "registration_failure") return "Registration failure";
  if (type === "initialization_failure") return "Initialization failure";
  if (type === "config_change") return "Configuration change";
  return type.charAt(0).toUpperCase() + type.slice(1);
}

export function actionSeverity(
  severity?: string,
): "error" | "warning" | "info" | "neutral" {
  if (severity === "critical" || severity === "error") return "error";
  if (severity === "warning" || severity === "alert") return "warning";
  if (severity === "info") return "info";
  return "neutral";
}

export function humanizeCode(code: string): string {
  const words = code
    .replaceAll("_", " ")
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2");
  if (!words) return "Capacity signal";
  return `${words.charAt(0).toUpperCase()}${words.slice(1)}`
    .replaceAll("Nodeclass", "NodeClass")
    .replaceAll("Node Class", "NodeClass")
    .replaceAll("Node Pool", "NodePool");
}

export function conditionSummary(
  conditions: { type: string; status: string; reason?: string }[] | undefined,
): string {
  if (!conditions || conditions.length === 0) return "No conditions reported";
  const failing = conditions.find((condition) => condition.status === "False");
  const unknown = conditions.find(
    (condition) => condition.status === "Unknown",
  );
  const selected =
    failing ??
    unknown ??
    conditions.find((condition) => condition.status === "True") ??
    conditions[0];
  return `${selected.type}: ${selected.reason || selected.status}`;
}

export function retentionLabel(retention: {
  mode: string;
  maxAgeSeconds?: number;
  maxEvents?: number;
}): string {
  const details: string[] = [retention.mode.replaceAll("_", " ")];
  if (retention.maxAgeSeconds !== undefined)
    details.push(`up to ${formatDuration(retention.maxAgeSeconds * 1000)}`);
  if (retention.maxEvents !== undefined)
    details.push(`${retention.maxEvents} events max`);
  return details.join(" · ");
}

export function activityWindowPreset(
  since: string | undefined,
  now = Date.now(),
): number | "custom" | undefined {
  if (!since) return undefined;
  const timestamp = Date.parse(since);
  if (!Number.isFinite(timestamp)) return "custom";
  const age = now - timestamp;
  const tolerance = 10 * 60 * 1000;
  const preset = [1, 6, 24].find(
    (hours) => Math.abs(age - hours * 60 * 60 * 1000) <= tolerance,
  );
  return preset ?? "custom";
}

// ============================================================================
// Badges
// ============================================================================

export function PoolReadyBadge({ ready }: { ready?: boolean }) {
  return ready === true ? (
    <Badge severity="success" size="sm">
      Ready
    </Badge>
  ) : ready === false ? (
    <Badge severity="error" size="sm">
      Unready
    </Badge>
  ) : (
    <Badge severity="neutral" size="sm">
      Status unknown
    </Badge>
  );
}

export function NodeReadyBadge({
  ready,
  cordoned,
}: {
  ready?: boolean;
  cordoned: boolean;
}) {
  if (ready === false)
    return (
      <Badge severity="error" size="sm">
        NotReady
      </Badge>
    );
  if (ready === undefined)
    return (
      <Badge severity="neutral" size="sm">
        Unknown
      </Badge>
    );
  if (cordoned)
    return (
      <Badge severity="warning" size="sm">
        Cordoned
      </Badge>
    );
  return (
    <Badge severity="success" size="sm">
      Ready
    </Badge>
  );
}

export function ClaimStageBadge({ stage }: { stage?: CapacityClaimStage }) {
  if (!stage || stage === "unknown")
    return (
      <Badge severity="neutral" size="sm">
        Unknown
      </Badge>
    );
  const severity =
    stage === "failed"
      ? "error"
      : stage === "terminating" || stage === "pending"
        ? "warning"
        : stage === "ready"
          ? "success"
          : "info";
  return (
    <Badge severity={severity} size="sm">
      {stage}
    </Badge>
  );
}

export function DemandStateBadge({ state }: { state: CapacityDemandState }) {
  const severity =
    state === "blocked"
      ? "error"
      : state === "held"
        ? "info"
        : state === "awaiting_capacity"
          ? "warning"
          : "neutral";
  return (
    <Badge severity={severity} size="sm">
      {demandStateLabel(state)}
    </Badge>
  );
}

export function PoolEvaluationBadge({
  result,
}: {
  result: CapacityDemandGroup["poolEvaluations"][number]["result"];
}) {
  // Declared-compatible is deliberately NOT success-styled: the pod is still
  // pending, and the claim is about declared constraints only (no instance
  // shape / bin-packing simulation) — green would overpromise placement.
  const severity = result === "incompatible" ? "warning" : "neutral";
  return (
    <Badge severity={severity} size="sm">
      {result === "declared_compatible"
        ? "Declared compatible"
        : result === "incompatible"
          ? "Declared incompatible"
          : "Unknown"}
    </Badge>
  );
}

const ACTIVITY_STATE_SEVERITY: Record<
  string,
  "error" | "warning" | "info" | "success" | "neutral"
> = {
  failed: "error",
  blocked: "warning",
  open: "info",
  completed: "success",
  // `ended` is deliberately toneless. The episode terminated with no evidence
  // either way, so green would claim a success and red a failure that Radar
  // never observed. An unrecognized future state lands on the same default.
  ended: "neutral",
  observed: "neutral",
  unknown: "neutral",
};

export function ActivityStateBadge({
  state,
}: {
  state: CapacityActivityEpisode["state"];
}) {
  return (
    <WithTooltip tip={activityStateTip(state)}>
      <Badge severity={ACTIVITY_STATE_SEVERITY[state] ?? "neutral"} size="sm">
        {state}
      </Badge>
    </WithTooltip>
  );
}

function activityStateTip(
  state: CapacityActivityEpisode["state"],
): string | undefined {
  if (state === "ended")
    return "Ended without a recorded cause — the subject went away before reaching its goal. Radar does not claim this failed or succeeded.";
  return undefined;
}

export function ConditionBadge({
  status,
}: {
  status: CapacityCondition["status"];
}) {
  const severity =
    status === "True" ? "success" : status === "False" ? "warning" : "neutral";
  return (
    <Badge severity={severity} size="sm">
      {status}
    </Badge>
  );
}

function CoverageBadge({ coverage }: { coverage?: CapacitySourceCoverage }) {
  if (!coverage)
    return (
      <Badge severity="neutral" size="sm">
        Not reported
      </Badge>
    );
  const lowerBound = coverageIsLowerBound(coverage);
  const severity = lowerBound
    ? "warning"
    : coverage.status === "available"
      ? "success"
      : coverage.status === "partial"
        ? "warning"
        : coverage.status === "error" || coverage.status === "denied"
          ? "error"
          : coverage.status === "syncing"
            ? "info"
            : "neutral";
  return (
    <Badge severity={severity} size="sm">
      {lowerBound ? "lower bound" : coverage.status.replace("_", " ")}
    </Badge>
  );
}

function UsageCoverageBadge({
  coverage,
}: {
  coverage?: CapacitySourceCoverage;
}) {
  if (nodeMetricsAreStale(coverage))
    return (
      <Badge severity="warning" size="sm">
        Stale
      </Badge>
    );
  if (coverage?.status === "available")
    return (
      <Badge severity="neutral" size="sm">
        No sample
      </Badge>
    );
  return <CoverageBadge coverage={coverage} />;
}

/** Alert-toned "Unavailable — Pod access denied" chip. Never rendered as zero. */
export function DeniedBadge({ label = "Unavailable" }: { label?: string }) {
  return (
    <WithTooltip tip="Pod access is denied. This value cannot be computed — it is unavailable, not zero.">
      <Badge severity="warning" size="sm">
        {`${label} — Pod access denied`}
      </Badge>
    </WithTooltip>
  );
}
// ============================================================================
// Quantity presentation
// ============================================================================

/**
 * A quantity observation that carries no resource entries. Only certainty can
 * tell a measured zero (a pool scaled to zero) apart from a source that was
 * never read — `empty` is this page's "not observed" wording, so an exact
 * observation must never land there, and an unobserved source must never land
 * on a zero.
 */
function EmptyQuantity({
  observation,
  empty,
}: {
  observation?: CapacityQuantityObservation;
  empty: string;
}) {
  if (!observation)
    return <span className="text-xs text-theme-text-tertiary">{empty}</span>;
  if (observation.certainty === "unknown")
    return (
      <WithTooltip tip={observationTitle(observation)}>
        <span className="text-xs text-theme-text-tertiary">Unknown</span>
      </WithTooltip>
    );
  if (observation.certainty === "exact")
    return (
      <WithTooltip tip={observationTitle(observation)}>
        <span className="font-mono text-xs text-theme-text-secondary">0</span>
      </WithTooltip>
    );
  return (
    <span className="text-xs text-theme-text-tertiary">
      {empty} <CertaintyGlyph certainty={observation.certainty} />
    </span>
  );
}

export function QuantityInline({
  observation,
  empty,
}: {
  observation?: CapacityQuantityObservation;
  empty: string;
}) {
  const text = quantityText(observation);
  if (!text) return <EmptyQuantity observation={observation} empty={empty} />;
  return (
    <WithTooltip tip={observation ? observationTitle(observation) : text}>
      <span className="font-mono text-xs text-theme-text-secondary">
        {text}
        {observation && observation.certainty !== "exact" && (
          <>
            {" "}
            <CertaintyGlyph certainty={observation.certainty} />
          </>
        )}
      </span>
    </WithTooltip>
  );
}

/**
 * Inventory-table quantity cell. CPU ("N cores") and memory ("N GiB") lead; a
 * GPU resource is promoted into the visible cell ("gpu N") because it changes
 * what can schedule; every other resource collapses into a "+N more" chip whose
 * tooltip lists each one labeled — so pods and ephemeral-storage never sit as
 * bare unlabeled numbers next to CPU/memory. Empty/unknown handling mirrors
 * QuantityInline (unavailable ≠ zero, a lower bound keeps its glyph).
 */
export function InventoryQuantityCell({
  observation,
  empty,
}: {
  observation?: CapacityQuantityObservation;
  empty: string;
}) {
  const entries = observation
    ? sortedResourceEntries(observation.resources)
    : [];
  if (entries.length === 0)
    return <EmptyQuantity observation={observation} empty={empty} />;

  const visible: string[] = [];
  const cpu = observation?.resources.cpu;
  const memory = observation?.resources.memory;
  if (cpu !== undefined) visible.push(formatQuantity("cpu", cpu));
  if (memory !== undefined) visible.push(formatQuantity("memory", memory));
  const hidden: [string, string][] = [];
  for (const [resource, value] of entries) {
    if (resource === "cpu" || resource === "memory") continue;
    if (/gpu/i.test(resource)) {
      visible.push(`gpu ${formatQuantity(resource, value)}`);
    } else {
      hidden.push([resource, value]);
    }
  }
  const hiddenTip = hidden
    .map(
      ([resource, value]) => `${resource} ${formatQuantity(resource, value)}`,
    )
    .join(" · ");

  return (
    <span className="font-mono text-xs text-theme-text-secondary">
      <WithTooltip
        tip={observation ? observationTitle(observation) : undefined}
      >
        <span>{visible.join(" · ")}</span>
      </WithTooltip>
      {hidden.length > 0 && (
        <>
          {visible.length > 0 ? " · " : ""}
          <WithTooltip tip={hiddenTip}>
            <span className="cursor-help text-theme-text-tertiary underline decoration-dotted underline-offset-2">
              {`+${hidden.length} more`}
            </span>
          </WithTooltip>
        </>
      )}
      {observation && observation.certainty !== "exact" && (
        <>
          {" "}
          <CertaintyGlyph certainty={observation.certainty} />
        </>
      )}
    </span>
  );
}

export function ActualUsageInline({
  usage,
  coverage,
}: {
  usage?: CapacityUsageObservation;
  coverage?: CapacitySourceCoverage;
}) {
  if (!usage)
    return (
      <span className="text-xs text-theme-text-tertiary">
        {missingUsageMessage(coverage)}
      </span>
    );
  if (usage.utilization.length === 0)
    return (
      <div className="space-y-1">
        {nodeMetricsAreStale(coverage) && (
          <Badge severity="warning" size="sm">
            Stale sample
          </Badge>
        )}
        <div className="text-xs text-theme-text-tertiary">
          No utilization values reported
        </div>
      </div>
    );
  const primary = usage.utilization.filter(
    (entry) => entry.resource === "cpu" || entry.resource === "memory",
  );
  const visible = primary.length > 0 ? primary : usage.utilization.slice(0, 2);
  return (
    <div className="space-y-1">
      {nodeMetricsAreStale(coverage) && (
        <div className="flex items-center gap-1.5">
          <Badge severity="warning" size="sm">
            Stale sample
          </Badge>
          <span className="text-xs text-theme-text-tertiary">
            {formatTimestamp(usage.quantity.asOf)}
          </span>
        </div>
      )}
      {visible.map((entry) => (
        <ResourceBar
          key={entry.resource}
          used={formatQuantity(entry.resource, entry.usage)}
          total={formatQuantity(entry.resource, entry.allocatable)}
          percent={entry.percent}
          colorScheme="quiet"
          layout="inline"
          label={shortResourceLabel(entry.resource)}
          tooltip={`Actual ${resourceLabel(entry.resource).toLowerCase()} usage across ${usage.coveredNodes} of ${usage.totalNodes} nodes with samples`}
        />
      ))}
    </div>
  );
}

export function pickWorstPressure(
  pressure: CapacityLimitPressure[],
): CapacityLimitPressure | undefined {
  return [...pressure].sort((left, right) => {
    if (left.overLimit !== right.overLimit) return left.overLimit ? -1 : 1;
    return (right.percent ?? -1) - (left.percent ?? -1);
  })[0];
}

export function PressureDetail({
  pressure,
}: {
  pressure: CapacityLimitPressure;
}) {
  return (
    <div>
      <div className="mb-1 flex items-center justify-between gap-2 text-xs font-medium text-theme-text-secondary">
        <span>{resourceLabel(pressure.resource)}</span>
        {pressure.overLimit && (
          <Badge severity="warning" size="sm">
            Over limit
          </Badge>
        )}
      </div>
      {pressure.percent === undefined ? (
        <div className="font-mono text-xs text-theme-text-primary">
          {formatQuantity(pressure.resource, pressure.provisioned)} /{" "}
          {formatQuantity(pressure.resource, pressure.limit)} · ratio
          unavailable
        </div>
      ) : (
        <ResourceBar
          used={formatQuantity(pressure.resource, pressure.provisioned)}
          total={formatQuantity(pressure.resource, pressure.limit)}
          percent={pressure.percent}
          colorScheme="quiet"
        />
      )}
    </div>
  );
}

export function ActualUsageDetail({
  usage,
  coverage,
}: {
  usage?: CapacityUsageObservation;
  coverage?: CapacitySourceCoverage;
}) {
  if (!usage)
    return (
      <div className="flex items-start gap-2">
        <UsageCoverageBadge coverage={coverage} />
        <p className="text-sm text-theme-text-secondary">
          {missingUsageMessage(coverage)}
        </p>
      </div>
    );
  return (
    <div>
      {nodeMetricsAreStale(coverage) && (
        <div className="mb-3">
          <Notice>
            The latest retained node metrics are stale. Values below were last
            sampled {formatTimestamp(usage.quantity.asOf)}.
          </Notice>
        </div>
      )}
      {usage.utilization.length > 0 ? (
        <div className="space-y-3">
          {usage.utilization.map((entry) => (
            <div key={entry.resource}>
              <div className="mb-1 text-xs font-medium text-theme-text-secondary">
                {resourceLabel(entry.resource)}
              </div>
              <ResourceBar
                used={formatQuantity(entry.resource, entry.usage)}
                total={formatQuantity(entry.resource, entry.allocatable)}
                percent={entry.percent}
                colorScheme="quiet"
              />
            </div>
          ))}
        </div>
      ) : (
        <p className="text-sm text-theme-text-secondary">
          The usage source returned no comparable utilization values.
        </p>
      )}
      <p className="mt-3 text-xs text-theme-text-tertiary">
        Coverage: {usage.coveredNodes} of {usage.totalNodes} nodes · sampled{" "}
        {formatTimestamp(usage.quantity.asOf)}. Percentages include only nodes
        with samples.
      </p>
    </div>
  );
}

// ============================================================================
// Small layout primitives
// ============================================================================

export function SectionCard({
  title,
  subtitle,
  actions,
  bodyClassName,
  children,
}: {
  title: ReactNode;
  subtitle?: ReactNode;
  actions?: ReactNode;
  bodyClassName?: string;
  children: ReactNode;
}) {
  return (
    <section className="overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 border-b border-theme-border px-4 py-3">
        <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
          <h2 className="text-sm font-semibold text-theme-text-primary">
            {title}
          </h2>
          {subtitle && (
            <span className="text-xs text-theme-text-tertiary">{subtitle}</span>
          )}
        </div>
        {actions}
      </div>
      <div className={bodyClassName}>{children}</div>
    </section>
  );
}

export function KpiTile({
  label,
  value,
  sub,
  certainty,
  certaintyTitle,
  attention = false,
  linkLabel,
  onClick,
}: {
  label: string;
  value: ReactNode;
  sub?: ReactNode;
  certainty?: CapacityCertainty;
  certaintyTitle?: string;
  attention?: boolean;
  linkLabel?: string;
  onClick?: () => void;
}) {
  const clickable = Boolean(onClick);
  return (
    <div
      role={clickable ? "button" : undefined}
      tabIndex={clickable ? 0 : undefined}
      onClick={onClick}
      onKeyDown={
        clickable
          ? (event) => {
              if (event.key === "Enter" || event.key === " ") {
                event.preventDefault();
                onClick?.();
              }
            }
          : undefined
      }
      className={`rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm ${
        clickable
          ? "cursor-pointer transition-colors hover:border-theme-border-light hover:bg-theme-hover/40"
          : ""
      }`}
    >
      <div className="flex items-center gap-1.5">
        <span className="text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">
          {label}
        </span>
        {certainty && certainty !== "exact" && (
          // Deviation-only: an = on every tile trains the eye to skip the one
          // glyph that matters. Exact is the quiet default; ≥ ≤ ? announce
          // themselves.
          <CertaintyGlyph certainty={certainty} title={certaintyTitle} />
        )}
        {attention && (
          <Badge severity="warning" size="sm">
            Attention
          </Badge>
        )}
      </div>
      <div className="mt-1 font-mono text-2xl font-medium tabular-nums text-theme-text-primary">
        {value}
      </div>
      {sub && (
        <div className="mt-0.5 text-xs text-theme-text-secondary">{sub}</div>
      )}
      {linkLabel && (
        <div className="mt-1.5 text-xs font-medium text-accent-text">
          {linkLabel}
        </div>
      )}
    </div>
  );
}

export function MiniCount({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-lg border border-theme-border bg-theme-base/50 px-2.5 py-2">
      <div className="font-mono text-base font-semibold tabular-nums text-theme-text-primary">
        {value}
      </div>
      <div className="text-[11px] text-theme-text-tertiary">{label}</div>
    </div>
  );
}

export function KeyValueRows({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <div className="divide-y divide-theme-border">
      {rows.map(([label, value], index) => (
        <div
          key={`${label}-${index}`}
          className="flex items-center justify-between gap-4 py-2 first:pt-0 last:pb-0"
        >
          <span className="text-sm text-theme-text-secondary">{label}</span>
          <span className="text-right font-mono text-sm text-theme-text-primary">
            {value}
          </span>
        </div>
      ))}
    </div>
  );
}

export function TokenGroup({
  title,
  values,
  empty,
}: {
  title: string;
  values: string[];
  empty: string;
}) {
  return (
    <div>
      <div className="mb-1.5 text-xs font-medium text-theme-text-tertiary">
        {title}
      </div>
      <div className="flex flex-wrap gap-1">
        {values.length > 0 ? (
          values.map((value) => (
            <Badge key={value} tone="structural" size="sm">
              {value}
            </Badge>
          ))
        ) : (
          <span className="text-sm text-theme-text-secondary">{empty}</span>
        )}
      </div>
    </div>
  );
}

export function Notice({ children }: { children: ReactNode }) {
  return (
    <div className="flex items-start gap-2 rounded-lg border border-theme-border bg-theme-surface px-3 py-2 text-sm text-theme-text-secondary">
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-theme-text-tertiary" />
      <div>{children}</div>
    </div>
  );
}

export function RefreshError({ message }: { message: string }) {
  return (
    <Notice>
      Refresh failed; showing the last successful capacity snapshot. {message}
    </Notice>
  );
}

export function ResourceLink({
  identity,
  label,
  className,
  onOpenResource,
}: {
  identity: CapacityResourceIdentity;
  label?: string;
  className?: string;
  onOpenResource: (resource: SelectedResource) => void;
}) {
  return (
    <Tooltip
      content={label ?? identity.ref.name}
      wrapperClassName="min-w-0 max-w-full"
    >
      <button
        type="button"
        className={
          className ??
          "font-mono text-xs font-medium text-accent-text hover:underline"
        }
        onClick={() => onOpenResource(identityToSelectedResource(identity))}
      >
        {label ?? identity.ref.name}
      </button>
    </Tooltip>
  );
}

export function LinkButton({
  children,
  onClick,
  title,
  className,
}: {
  children: ReactNode;
  onClick: () => void;
  title?: string;
  className?: string;
}) {
  return (
    <WithTooltip tip={title}>
      <button
        type="button"
        onClick={onClick}
        className={`text-left text-xs font-medium text-accent-text hover:underline ${className ?? ""}`}
      >
        {children}
      </button>
    </WithTooltip>
  );
}

export function InlineEmpty({
  title,
  detail,
}: {
  title: string;
  detail: string;
}) {
  return (
    <div className="px-4 py-8 text-center">
      <div className="text-sm font-medium text-theme-text-primary">{title}</div>
      <p className="mt-1 text-sm text-theme-text-secondary">{detail}</p>
    </div>
  );
}

export function EmptyState({
  icon: Icon,
  title,
  detail,
  action,
}: {
  icon: ComponentType<{ className?: string }>;
  title: string;
  detail: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="flex min-h-0 flex-1 items-center justify-center bg-theme-base p-6">
      <div className="max-w-md text-center">
        <Icon className="mx-auto h-9 w-9 text-theme-text-tertiary/50" />
        <h2 className="mt-3 text-lg font-medium text-theme-text-primary">
          {title}
        </h2>
        <p className="mt-1 text-sm text-theme-text-secondary">{detail}</p>
        {action}
      </div>
    </div>
  );
}

export function ScrollableContent({ children }: { children: ReactNode }) {
  // Expanding/collapsing cards can toggle the scrollbar; a stable gutter
  // keeps the centered column from shifting sideways when that happens.
  // The margin (not padding — scrolled content paints over scrollport
  // padding) keeps a constant breathing gap under the top bar while
  // scrolled; the inner pt shrinks so the unscrolled rhythm is unchanged.
  return (
    <div
      className="mt-2 min-h-0 flex-1 overflow-y-auto [scrollbar-gutter:stable]"
      id="rk-scroll"
    >
      <div className="mx-auto max-w-[1760px] space-y-5 px-5 pb-5 pt-3 xl:px-7">
        {children}
      </div>
    </div>
  );
}

export function CapacityFreshness({
  meta,
  query,
  connectionState,
}: {
  meta: Pick<CapacityResponseMeta, "generatedAt">;
  query: {
    dataUpdatedAt: number;
    isFetching: boolean;
    refetch: () => unknown;
  };
  connectionState: CapacityConnectionState;
}) {
  return (
    <FreshnessControl
      mode="auto"
      dataUpdatedAt={generatedAtMillis(meta.generatedAt, query.dataUpdatedAt)}
      isFetching={query.isFetching}
      onRefresh={() => {
        query.refetch();
      }}
      connectionState={connectionState}
    />
  );
}

/** Header scope badge + optional lower-bound warning, from the pods coverage. */
export function ScopeBadges({
  coverage,
  source = "pods",
}: {
  coverage: CapacityCoverageBySource;
  // Which coverage entry carries this screen's namespace scope — Activity's
  // namespaced data flows through karpenterObjectEvents, not pods; defaulting
  // it to pods would show "Cluster-wide" for a namespace-scoped view.
  source?: keyof CapacityCoverageBySource;
}) {
  const pods = coverage[source];
  const scope = pods?.scope;
  const explicit = scope === "explicit_namespaces";
  const scopeLabel = explicit
    ? `Explicit namespaces${pods?.namespaces?.length ? ` (${pods.namespaces.length})` : ""}`
    : scope === "all_authorized_namespaces"
      ? "Authorized namespaces"
      : "Cluster-wide";
  return (
    <span className="flex items-center gap-2">
      <WithTooltip tip="Pool, node and claim data is cluster-scoped. Pod and workload data covers authorized namespaces.">
        <Badge tone="structural" size="sm">
          {scopeLabel}
        </Badge>
      </WithTooltip>
      {coverageIsLowerBound(pods) && (
        <WithTooltip tip="Pod and workload data is restricted to authorized namespaces. Namespaced counts and demand are lower bounds.">
          <Badge severity="warning" size="sm">
            ≥ Namespaced data is a lower bound
          </Badge>
        </WithTooltip>
      )}
    </span>
  );
}

// ============================================================================
// Integration state gating (loading / syncing / not_detected / denied)
// ============================================================================

// The RBAC a cluster admin must grant for Capacity, ready to paste. The two
// rules mirror the two gates: nodes opens the page, Karpenter resources add
// its screens. Kept minimal on purpose — optional depth (pods, metrics,
// NodeClasses) degrades to labeled partial coverage rather than a 403.
export const CAPACITY_VIEWER_CLUSTERROLE = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: radar-capacity-viewer
rules:
- apiGroups: [""]
  resources: ["nodes"]
  verbs: ["list", "watch"] # opens the Capacity page
- apiGroups: ["karpenter.sh"]
  resources: ["nodepools", "nodeclaims"]
  verbs: ["list", "watch"] # adds the Karpenter screens
`;

// DeniedCapacityState is the 403 an operator can act on: what the denial
// means, what they can still see without these permissions, and the exact
// grant to request — a dead end converted into a next step.
export function DeniedCapacityState({ detail }: { detail: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <EmptyState
      icon={Shield}
      title="Capacity access denied"
      detail={detail}
      action={
        <div className="mt-4 text-left">
          <p className="text-xs text-theme-text-secondary">
            Your readable pods still carry their scheduler verdicts — the Pod
            drawer and Issues answer “why is this pod pending” at pod grain
            without these permissions.
          </p>
          <div className="mt-3 rounded-lg border border-theme-border bg-theme-surface">
            <div className="flex items-center justify-between border-b border-theme-border-subtle px-3 py-1.5">
              <span className="text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">
                Permissions to request
              </span>
              <LinkButton
                onClick={() => {
                  void navigator.clipboard?.writeText(
                    CAPACITY_VIEWER_CLUSTERROLE,
                  );
                  setCopied(true);
                  window.setTimeout(() => setCopied(false), 2000);
                }}
              >
                {copied ? "Copied" : "Copy"}
              </LinkButton>
            </div>
            <pre className="overflow-x-auto px-3 py-2 font-mono text-[11px] leading-relaxed text-theme-text-secondary">
              {CAPACITY_VIEWER_CLUSTERROLE}
            </pre>
          </div>
        </div>
      }
    />
  );
}

export function integrationBlock(
  response:
    | {
        state: CapacityIntegrationState;
        coverage?: CapacityCoverageBySource;
      }
    | undefined,
  error: Error | null,
  isLoading: boolean,
  loadingLabel: string,
): ReactNode | null {
  if (!response && isLoading)
    return <PaneLoader label={loadingLabel} className="min-h-0 flex-1" />;
  if (!response && error) {
    if (isForbiddenError(error))
      return <DeniedCapacityState detail={errorMessage(error)} />;
    // A 404 on the top-level capacity routes means the route doesn't exist on
    // this Radar — a pre-v1.9.0 binary behind a version-skewed host (Radar
    // Cloud), not a failure. The embedded OSS binary can never hit this.
    if (isNotFoundError(error))
      return (
        <EmptyState
          icon={Gauge}
          title="Capacity needs a newer Radar"
          detail={
            <>
              This cluster&rsquo;s Radar predates the Capacity view (added in
              Radar v1.9.0).
              <br />
              Upgrade the in-cluster Radar to enable it.
            </>
          }
        />
      );
    return (
      <EmptyState
        icon={AlertTriangle}
        title="Capacity unavailable"
        detail={errorMessage(error)}
      />
    );
  }
  if (!response)
    return <PaneLoader label={loadingLabel} className="min-h-0 flex-1" />;
  if (response.state === "syncing")
    return (
      <PaneLoader
        label={capacitySyncingLabel(response.coverage?.nodePools?.reasonCode)}
        className="min-h-0 flex-1"
      />
    );
  if (response.state === "not_detected")
    return (
      <EmptyState
        icon={Gauge}
        title="Karpenter not detected"
        detail="Capacity appears automatically when this cluster exposes Karpenter NodePools."
      />
    );
  if (response.state === "denied")
    return (
      <DeniedCapacityState detail="This user cannot read the Karpenter resources Capacity needs to diagnose this cluster." />
    );
  return null;
}

function capacitySyncingLabel(reasonCode?: string): string {
  switch (reasonCode) {
    case "cluster_connecting":
      return "Connecting to the cluster before checking Karpenter…";
    case "cluster_disconnected":
      return "Cluster disconnected; waiting to reconnect…";
    case "cluster_client_unavailable":
      return "Waiting for the Kubernetes client…";
    case "discovery_unavailable":
      return "Waiting for Kubernetes API discovery…";
    case "karpenter_discovery_partial":
      return "Karpenter API discovery is incomplete; retrying…";
    case "dynamic_cache_unavailable":
      return "Waiting for the Karpenter resource cache…";
    case "nodepools_syncing":
      return "Syncing Karpenter NodePools…";
    default:
      return "Capacity data is syncing…";
  }
}

// ============================================================================
// Keyset pagination + cursor recovery
// ============================================================================

interface CapacityPaginationState<T> {
  key: string;
  cursor?: string;
  history: (string | undefined)[];
  retainedPage?: T;
  recovered: boolean;
}

export function useCapacityPagination<T>(key: string) {
  const [state, setState] = useState<CapacityPaginationState<T>>(() => ({
    key,
    history: [],
    recovered: false,
  }));
  const emptyPage = (): CapacityPaginationState<T> => ({
    key,
    history: [],
    recovered: false,
  });
  const active = state.key === key ? state : emptyPage();
  const reset = useCallback(
    () => setState({ key, history: [], recovered: false }),
    [key],
  );
  const recover = useCallback(
    () =>
      setState((current) => ({
        key,
        history: [],
        retainedPage: current.key === key ? current.retainedPage : undefined,
        recovered: true,
      })),
    [key],
  );
  const goNext = useCallback(
    (nextCursor: string, retainedPage?: T) => {
      setState((current) => {
        const page =
          current.key === key
            ? current
            : {
                key,
                history: [] as (string | undefined)[],
                recovered: false,
              };
        return {
          key,
          cursor: nextCursor,
          history: [...page.history, page.cursor],
          retainedPage,
          recovered: false,
        };
      });
    },
    [key],
  );
  const goBack = useCallback(
    (retainedPage?: T) => {
      setState((current) => {
        const page =
          current.key === key
            ? current
            : {
                key,
                history: [] as (string | undefined)[],
                recovered: false,
              };
        if (page.history.length === 0) return page;
        return {
          key,
          cursor: page.history[page.history.length - 1],
          history: page.history.slice(0, -1),
          retainedPage,
          recovered: false,
        };
      });
    },
    [key],
  );

  return {
    cursor: active.cursor,
    history: active.history,
    retainedPage: active.retainedPage,
    recovered: active.recovered,
    reset,
    recover,
    goNext,
    goBack,
  };
}

export function useCapacityCursorRecovery(
  error: unknown,
  cursor: string | undefined,
  recover: () => void,
): boolean {
  const invalid = Boolean(cursor && isCapacityCursorInvalidError(error));

  useEffect(() => {
    if (!invalid) return;
    let cancelled = false;
    queueMicrotask(() => {
      if (!cancelled) recover();
    });
    return () => {
      cancelled = true;
    };
  }, [invalid, recover]);

  return invalid;
}

export function PageControls({
  page,
  hasPrevious,
  hasNext,
  busy,
  onPrevious,
  onNext,
}: {
  page: number;
  hasPrevious: boolean;
  hasNext: boolean;
  busy: boolean;
  onPrevious: () => void;
  onNext: () => void;
}) {
  return (
    <div className="flex items-center justify-end gap-2">
      <span className="mr-1 text-xs text-theme-text-tertiary">Page {page}</span>
      <button
        type="button"
        className="rounded-lg border border-theme-border px-3 py-1.5 text-xs font-medium text-theme-text-secondary hover:bg-theme-hover disabled:opacity-50"
        disabled={!hasPrevious || busy}
        onClick={onPrevious}
      >
        <ChevronLeft className="mr-1 inline h-3.5 w-3.5" />
        Previous
      </button>
      <button
        type="button"
        className="rounded-lg border border-theme-border px-3 py-1.5 text-xs font-medium text-theme-text-secondary hover:bg-theme-hover disabled:opacity-50"
        disabled={!hasNext || busy}
        onClick={onNext}
      >
        Next
        <ChevronRight className="ml-1 inline h-3.5 w-3.5" />
      </button>
    </div>
  );
}

// ============================================================================
// NodePool selector — a paginated <select> shared by Demand ("evaluate against")
// and Activity ("filter by pool"). The label/empty/error copy differs per caller;
// the paging behaviour (load-more cursor, invalid-cursor recovery) does not.
// ============================================================================

const POOL_PAGE_LIMIT = 100;
const LOAD_MORE_POOLS = "__load_more_nodepools__";

export function PoolSelector({
  pool,
  onChange,
  label,
  emptyLabel,
  unavailableLabel,
}: {
  pool: string | undefined;
  onChange: (pool: string | undefined) => void;
  label: string;
  emptyLabel: string;
  unavailableLabel: string;
}) {
  const [cursor, setCursor] = useState<string>();
  const [loadedPools, setLoadedPools] = useState<string[]>([]);
  const query = useCapacityPools({
    limit: POOL_PAGE_LIMIT,
    cursor,
    refetchInterval: false,
  });
  const recoverCursor = useCallback(() => {
    setCursor(undefined);
    setLoadedPools([]);
  }, []);
  const recoveringCursor = useCapacityCursorRecovery(
    query.error,
    cursor,
    recoverCursor,
  );
  const page = query.isPlaceholderData ? undefined : query.data;
  const pageNames = page?.items.map((item) => item.resource.ref.name) ?? [];
  const poolNames = Array.from(new Set([...loadedPools, ...pageNames])).sort();
  if (pool && !poolNames.includes(pool)) poolNames.unshift(pool);

  const loadingMore = Boolean(
    cursor && query.isFetching && query.isPlaceholderData,
  );
  const hasMore = Boolean(page?.page.hasMore && page.page.nextCursor);
  const statusId = "pool-selector-options-status";

  return (
    <div className="flex min-w-56 flex-col gap-1">
      <label className="flex items-center gap-2 text-xs text-theme-text-secondary">
        <span className="shrink-0 font-medium">{label}</span>
        <select
          value={pool ?? ""}
          aria-describedby={query.error ? statusId : undefined}
          aria-busy={query.isFetching}
          onChange={(event) => {
            if (event.target.value === LOAD_MORE_POOLS) {
              if (page?.page.nextCursor) {
                setLoadedPools((current) =>
                  Array.from(new Set([...current, ...pageNames])).sort(),
                );
                setCursor(page.page.nextCursor);
              }
              return;
            }
            onChange(event.target.value || undefined);
          }}
          className="min-w-0 flex-1 rounded-md border border-theme-border bg-theme-elevated px-2 py-1.5 text-xs text-theme-text-primary outline-none focus:border-skyhook-500"
        >
          <option value="">{emptyLabel}</option>
          {poolNames.map((name) => (
            <option key={name} value={name}>
              {name}
            </option>
          ))}
          {query.isLoading && <option disabled>Loading NodePools…</option>}
          {loadingMore && <option disabled>Loading more NodePools…</option>}
          {!loadingMore && hasMore && (
            <option value={LOAD_MORE_POOLS}>Load more NodePools…</option>
          )}
        </select>
      </label>
      {query.error && (
        <span
          id={statusId}
          role="status"
          className="text-right text-xs text-theme-text-tertiary"
        >
          {recoveringCursor
            ? "NodePool list changed; reloading options."
            : unavailableLabel}
        </span>
      )}
    </div>
  );
}

// ============================================================================
// Shared table cell classes (keeps every capacity table visually identical)
// ============================================================================

export const TABLE_WRAP = "overflow-x-auto";
export const TABLE_HEAD =
  "border-b border-theme-border bg-theme-base/60 text-[11px] uppercase tracking-wide text-theme-text-tertiary";
export const TH = "px-3 py-2.5 text-left font-medium whitespace-nowrap";
export const TD = "px-3 py-2.5 align-top text-sm text-theme-text-primary";
export const TBODY = "table-divide-subtle";
export const ROW_HOVER = "transition-colors hover:bg-theme-hover/50";
