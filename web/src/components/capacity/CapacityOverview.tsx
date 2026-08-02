import { useEffect, useMemo, useRef, useState, type RefObject } from "react";
import { Layers3, X } from "lucide-react";
import {
  Collapse,
  CollapseChevron,
  StatusDot,
  WithTooltip,
  mapHealthToTone,
  type CapacityAutoscalerBackoff,
  type CapacityAutoscalerChildObservation,
  type CapacityBoundedResultMeta,
  type CapacityCertainty,
  type CapacityClaimLifecycleSummary,
  type CapacityCoverageBySource,
  type CapacityDemandState,
  type CapacityGroupSummary,
  type CapacityIntegrationState,
  type CapacityManagerSummary,
  type CapacityOverviewResponse,
  type CapacityOverviewSummary,
  type CapacityPoolSummary,
  type CapacitySourceCoverage,
} from "@skyhook-io/k8s-ui";
import { ClusterSchedulingCard } from "./ClusterSchedulingCard";
import { Badge } from "@skyhook-io/k8s-ui/components/ui/Badge";
import type { useCapacityOverview } from "../../api/client";
import type { SelectedResource } from "../../types";
import {
  actionSeverity,
  CapacityFreshness,
  capacityManagerLabel,
  coverageHasObservations,
  coverageIsDenied,
  coverageIsLowerBound,
  CertaintyGlyph,
  formatQuantity,
  coverageMessage,
  DeniedBadge,
  EmptyState,
  errorMessage,
  formatTimestamp,
  humanizeCode,
  identityToSelectedResource,
  InlineEmpty,
  integrationBlock,
  InventoryQuantityCell,
  KpiTile,
  LinkButton,
  managerStatusTone,
  Notice,
  pickWorstPressure,
  PoolReadyBadge,
  poolReadinessDetail,
  RefreshError,
  relativeTime,
  ROW_HOVER,
  ScopeBadges,
  ScrollableContent,
  SectionCard,
  shortResourceLabel,
  sortedResourceEntries,
  TABLE_HEAD,
  TABLE_WRAP,
  TBODY,
  TD,
  TH,
  worstManagerStatus,
  type CapacityConnectionState,
} from "./shared";

const EXPLAIN_CARDS: { term: string; body: string }[] = [
  {
    term: "Capacity managers",
    body: 'The controllers that add and remove nodes — Karpenter, the GKE or AKS cluster autoscaler, or the standalone Cluster Autoscaler. Detection is coverage-gated: whenever the autoscaler status cannot be read — permissions, watch scope, or an unparseable payload — Radar says detection is unavailable rather than reporting none. "None detected" means the status ConfigMap was looked for in kube-system and is not there.',
  },
  {
    term: "Node groups",
    body: "Logical groups an operator recognizes — a Karpenter NodePool, a GKE node pool, an EKS managed node group, an AKS agent pool. Identity comes from node labels or CRD evidence, never from parsing provider group names. Nodes with no group identity are counted apart — never called static.",
  },
  {
    term: "Cluster scheduling capacity",
    body: "Scheduled requests against node allocatable across every observed node, not just Karpenter's. Requests are what the scheduler consumes, never live usage, and the bar never takes a health color. In-flight capacity stays Karpenter-only — no other manager reports claims before they register.",
  },
  {
    term: "Configured limit",
    body: "A ceiling declared on the NodePool. Says nothing about what exists right now. A missing limit is not unlimited capacity — provider quota still applies.",
  },
  {
    term: "Provisioned / allocatable",
    body: "Capacity of nodes that exist. Allocatable is what the kubelet offers to the scheduler after reservations.",
  },
  {
    term: "Scheduled requests",
    body: "What the scheduler has placed. This is what consumes scheduling capacity — not live usage.",
  },
  {
    term: "Negative-priority requests",
    body: "The subset of scheduled requests coming from pods with a priority below zero — potential preemption victims. Default-priority workloads can reclaim their capacity, subject to preemption policy, placement, and disruption constraints. A measured priority fact, never added to scheduled requests: Radar does not claim these are an overprovisioning buffer.",
  },
  {
    term: "Actual usage",
    body: "Point-in-time consumption from metrics. An efficiency signal. Never scheduler headroom.",
  },
  {
    term: "Unallocated",
    body: "Allocatable minus requests, summed across nodes. Not bin-packed: it does not prove another pod can schedule.",
  },
  {
    term: "In flight",
    body: "Capacity of NodeClaims Karpenter has requested that have not registered as nodes yet. Drawn beyond the allocatable edge — the scheduler cannot use it until registration.",
  },
  {
    term: "Pending demand",
    body: "Requests of unscheduled pods. Shown as a count, never to scale — demand can exceed the whole fleet. Demand is what Karpenter still has to answer for, not usage.",
  },
];

const SEVERITY_RANK: Record<string, number> = {
  error: 0,
  warning: 1,
  info: 2,
  neutral: 3,
};

export function coverageCertainty(
  coverage?: CapacitySourceCoverage,
): CapacityCertainty {
  if (coverageIsDenied(coverage)) return "unknown";
  // Only an actually-observed source can be exact or a lower bound. Syncing,
  // unavailable, error, or absent coverage is unknown — never a false "=".
  if (!coverageHasObservations(coverage)) return "unknown";
  if (coverageIsLowerBound(coverage)) return "lower_bound";
  return "exact";
}

/**
 * Certainty of the node-groups count. A group with no nodes is only visible
 * through its NodePool, so unreadable NodePools hide scale-to-zero groups
 * entirely and make the count a lower bound even when every node was observed.
 */
export function nodeGroupsCertainty(
  coverage: CapacityCoverageBySource,
  state: CapacityIntegrationState,
): {
  certainty: CapacityCertainty;
  title: string;
} {
  const fromNodes = coverageCertainty(coverage.nodes);
  const nodePools = coverage.nodePools;
  // With Karpenter absent there are no NodePools to hide behind, so nothing is
  // invisible and the count keeps the nodes' own certainty.
  const nodePoolsHidden =
    state !== "not_detected" &&
    nodePools !== undefined &&
    !coverageHasObservations(nodePools);
  if (fromNodes === "unknown" || !nodePoolsHidden)
    return {
      certainty: fromNodes,
      title: coverageMessage(coverage.nodes, "Node groups"),
    };
  return {
    certainty: "lower_bound",
    title: `${coverageMessage(nodePools, "NodePool inventory")} — NodePool groups holding no nodes are invisible, so this count is a lower bound.`,
  };
}

/** Honest one-liner for an autoscaler-status source that could not be read.
 *  Null when the source WAS observed — including the true "the autoscaler
 *  publishes nothing here" observation, which is the only reading that may
 *  render as "None detected". */
export function autoscalerDetectionFailure(
  coverage?: CapacitySourceCoverage,
): string | null {
  if (!coverage) return null;
  if (coverage.status === "denied")
    return "Autoscaler status is hidden by permissions, so managers could not be detected.";
  if (coverage.status !== "unavailable" && coverage.status !== "error")
    return null;
  switch (coverage.reasonCode) {
    case "autoscaler_status_not_published":
      return null;
    case "autoscaler_status_cache_scope":
      return "The autoscaler status ConfigMap is outside the namespaces Radar watches.";
    case "autoscaler_status_cache_unavailable":
      return "The autoscaler status ConfigMap could not be read.";
    case "autoscaler_status_read_failed":
      return "Reading the autoscaler status ConfigMap failed.";
    case "autoscaler_status_parse_failed":
      return "The autoscaler status ConfigMap could not be parsed.";
    default:
      return coverage.reason ?? "The autoscaler status could not be observed.";
  }
}

// The autoscaler publishes to one fixed object; the server reports the
// namespace it probed on the not-published path.
const AUTOSCALER_STATUS_NAMESPACE = "kube-system";

/** Scope for the "None detected" reading. Absence of the status ConfigMap is
 *  only evidence about the one namespace it can live in — unscoped, the tile
 *  would read as a cluster-wide claim that no autoscaler exists. */
export function autoscalerNotPublishedDetail(
  coverage?: CapacitySourceCoverage,
): string | null {
  if (coverage?.reasonCode !== "autoscaler_status_not_published") return null;
  const namespaces = coverage.namespaces?.length
    ? coverage.namespaces.join(", ")
    : AUTOSCALER_STATUS_NAMESPACE;
  return `No cluster-autoscaler-status ConfigMap in ${namespaces}.`;
}

/** One-manager breakdown for the Node groups subline when no Karpenter pools
 *  own the groups (e.g. a GKE/EKS-managed cluster). Null when no manager was
 *  attributed. */
function managerBreakdown(managers: CapacityManagerSummary[]): string | null {
  if (managers.length === 0) return null;
  if (managers.length === 1)
    return `${managers[0].groupCount} ${capacityManagerLabel(managers[0].manager)}`;
  return `across ${managers.length} managers`;
}

/** Node groups tile subline. Headline is the unified group count; the subline
 *  keeps the Karpenter readiness signal when pools exist, else describes the
 *  managers that own the groups, else says none/unavailable — never a
 *  fabricated zero. */
function nodeGroupsSubline(
  summary: CapacityOverviewSummary,
  data: CapacityOverviewResponse,
  coverage: CapacityCoverageBySource,
): string {
  if ((summary.poolCount ?? 0) > 0)
    return `${summary.poolCount} Karpenter · ${poolReadinessDetail(
      data.pools,
      data.poolsTruncated,
    )}`;
  if (data.groups.length === 0) {
    if (!coverageHasObservations(coverage.nodes))
      return coverageMessage(coverage.nodes, "Node inventory");
    // Karpenter simply is not installed. That is a true observation, so it must
    // not borrow the "unavailable" phrasing that means we tried and could not
    // see — the server leaves this coverage reasonless precisely because there
    // was nothing to fail at.
    if (data.state === "not_detected") return "Karpenter not detected";
    // Unreadable NodePools cannot be summarized as "none identified" — the
    // pools may exist and simply be invisible to this identity.
    return summary.poolCount === undefined
      ? coverageMessage(coverage.nodePools, "NodePool inventory")
      : "none identified";
  }
  return managerBreakdown(summary.managers) ?? "logical groups across managers";
}

/** Nodes tile subline. Decomposes the total: nodes inside Karpenter pools, plus
 *  nodes with no group identity. Falls back to the coverage message when the
 *  total is unavailable or nothing decomposes (unavailable ≠ zero). */
function nodesSubline(
  summary: CapacityOverviewSummary,
  coverage: CapacityCoverageBySource,
): string {
  if (summary.nodeCount === undefined)
    return coverageMessage(coverage.nodes, "Node inventory");
  const parts: string[] = [];
  if ((summary.poolCount ?? 0) > 0)
    parts.push(
      `${summary.nodeCount - (summary.unpooledNodeCount ?? 0)} in Karpenter pools`,
    );
  if ((summary.unattributedNodeCount ?? 0) > 0)
    parts.push(`${summary.unattributedNodeCount} unattributed`);
  return parts.length > 0
    ? parts.join(" · ")
    : coverageMessage(coverage.nodes, "Node inventory");
}

interface OverviewSignal {
  key: string;
  severity: "error" | "warning" | "info" | "neutral";
  title: string;
  detail: string;
  /** Named subjects (pools) rendered as individual links into their evidence. */
  subjects?: { label: string; open: () => void }[];
  action?: { label: string; run: () => void };
}

export function CapacityOverview({
  query,
  connectionState,
  onOpenPool,
  onOpenResource,
  onNavigate,
}: {
  query: ReturnType<typeof useCapacityOverview>;
  connectionState: CapacityConnectionState;
  onOpenPool: (name: string) => void;
  onOpenResource: (resource: SelectedResource) => void;
  onNavigate: (path: string) => void;
}) {
  const [showExplain, setShowExplain] = useState(false);
  const inventoryRef = useRef<HTMLDivElement>(null);
  const openExplainer = () => setShowExplain(true);

  // A Karpenter-less cluster — or one whose NodePools this identity cannot read
  // — still has capacity worth showing: its nodes, node groups owned by other
  // managers, the autoscaler ConfigMap. Render the overview whenever there is an
  // observable surface; only fall through to the "not detected" / "access
  // denied" block when there is genuinely nothing to show. Loading / error /
  // syncing still short-circuit through integrationBlock unchanged. Node
  // visibility is the page gate on the server, so a truly node-blind caller
  // never reaches here with a surface — the fetch 403s and integrationBlock
  // renders the denial.
  const response = query.data;
  const hasObservableSurface =
    !!response &&
    (coverageHasObservations(response.coverage.nodes) ||
      response.groups.length > 0 ||
      response.orphanAutoscalerGroups.length > 0 ||
      response.summary.managers.length > 0);
  const stateWithSurface =
    (response?.state === "not_detected" || response?.state === "denied") &&
    hasObservableSurface;

  if (!stateWithSurface) {
    const blocked = integrationBlock(
      query.data,
      query.error,
      query.isLoading,
      "Loading capacity overview…",
    );
    if (blocked) return blocked;
  }

  const data = query.data as CapacityOverviewResponse;
  // Karpenter-specific tiles/sections key off this, not off the presence of any
  // capacity data — a not-detected cluster with observable nodes still renders.
  const karpenterActive = data.state === "available";
  // The NodePool-inventory guard is a Karpenter-detected concern (Karpenter is
  // present but its NodePools can't be read). A not-detected cluster has no
  // NodePool coverage by definition — that is not an error to block on.
  if (karpenterActive && !coverageHasObservations(data.coverage.nodePools)) {
    return (
      <EmptyState
        icon={Layers3}
        title="NodePool inventory unavailable"
        detail={coverageMessage(data.coverage.nodePools, "NodePool inventory")}
      />
    );
  }

  const summary = data.summary;
  const coverage = data.coverage;
  const podsDeniedFlag = coverageIsDenied(coverage.pods);
  const unavailableRefresh = query.error ? errorMessage(query.error) : null;
  // Cluster scope is the honest headline once the server measures every node;
  // the Karpenter-only ledger is the fallback until it does — and only while
  // Karpenter is actually a manager here (never a fabricated Karpenter bar on a
  // cluster that has none).
  const clusterScheduling = summary.clusterScheduling;
  const schedulingForBar =
    clusterScheduling ?? (karpenterActive ? summary.scheduling : undefined);
  const schedulingScope = clusterScheduling ? "cluster" : "karpenter";
  const groupsCertainty = nodeGroupsCertainty(coverage, data.state);

  const signals = buildSignals(summary, { onOpenPool, onNavigate });

  return (
    <ScrollableContent>
      <div>
        <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
          <div>
            <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
              <h1 className="text-lg font-semibold text-theme-text-primary">
                Capacity overview
              </h1>
              <span className="text-xs text-theme-text-tertiary">
                cluster capacity posture · read-only diagnosis
              </span>
            </div>
            <button
              type="button"
              onClick={() => setShowExplain(true)}
              className="mt-1 text-xs font-medium text-accent-text hover:underline"
            >
              Scheduling capacity ≠ actual usage — how to read these numbers
            </button>
          </div>
          <div className="flex items-center gap-2">
            <ScopeBadges coverage={coverage} />
            <CapacityFreshness
              meta={data}
              query={query}
              connectionState={connectionState}
            />
          </div>
        </div>
      </div>
      <ExplainDialog open={showExplain} onClose={() => setShowExplain(false)} />

      {unavailableRefresh && <RefreshError message={unavailableRefresh} />}

      {coverageIsDenied(coverage.nodePools) && (
        <Notice>
          Karpenter view unavailable — your identity cannot list NodePools. The
          cluster capacity below is derived from nodes and pods only; the
          NodePool detail, Demand, and Activity screens stay unavailable.
        </Notice>
      )}

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-3 xl:grid-cols-5">
        <ManagersTile
          managers={summary.managers}
          groups={data.groups}
          autoscalerStatus={coverage.autoscalerStatus}
          nodesObserved={coverageHasObservations(coverage.nodes)}
          karpenterMode={data.provider.controllerMode}
        />
        <KpiTile
          label="Node groups"
          value={data.groups.length}
          sub={nodeGroupsSubline(summary, data, coverage)}
          certainty={groupsCertainty.certainty}
          certaintyTitle={groupsCertainty.title}
          attention={data.pools.some((pool) => pool.ready === false)}
          linkLabel="View inventory ↓"
          onClick={() =>
            inventoryRef.current?.scrollIntoView({
              behavior: "smooth",
              block: "start",
            })
          }
        />
        <KpiTile
          label="Nodes"
          value={summary.nodeCount ?? "—"}
          sub={nodesSubline(summary, coverage)}
          certainty={coverageCertainty(coverage.nodes)}
          certaintyTitle={coverageMessage(coverage.nodes, "Node inventory")}
        />
        {karpenterActive && (
          <KpiTile
            label="NodeClaims"
            value={summary.claimCount ?? "—"}
            sub={
              claimStagesDetail(
                summary.claimStages,
                summary.orphanedClaimCount,
              ) ?? coverageMessage(coverage.nodeClaims, "Claim inventory")
            }
            certainty={coverageCertainty(coverage.nodeClaims)}
            certaintyTitle={coverageMessage(
              coverage.nodeClaims,
              "Claim inventory",
            )}
            attention={(summary.claimStages?.failed ?? 0) > 0}
          />
        )}
        {podsDeniedFlag ? (
          <KpiTile
            label="Pending pods"
            value="Unavailable"
            sub="Pod access denied — not zero"
            certainty="unknown"
            certaintyTitle="Unknown — the current identity cannot list Pods. Absence of data is not zero."
          />
        ) : (
          <KpiTile
            label="Pending pods"
            value={summary.pendingPodCount ?? "—"}
            sub="scheduling demand, not usage"
            certainty={coverageCertainty(coverage.pods)}
            certaintyTitle={coverageMessage(coverage.pods, "Pending demand")}
            attention={(summary.pendingPodCount ?? 0) > 0}
            // Without Karpenter there is no Demand screen, but a pending count
            // must never dead-end: Issues carries the scheduler verdicts on
            // every cluster.
            linkLabel={karpenterActive ? "Open Demand →" : "View issues →"}
            onClick={
              karpenterActive
                ? () => onNavigate("/capacity/demand")
                : () => onNavigate("/issues")
            }
          />
        )}
      </div>

      <ClusterSchedulingCard
        scheduling={schedulingForBar}
        pending={podsDeniedFlag ? undefined : summary.aggregateDemand}
        onExplain={openExplainer}
        scope={schedulingScope}
      />

      <PendingDemandChips
        summary={summary}
        podsDenied={podsDeniedFlag}
        podsObserved={coverageHasObservations(coverage.pods)}
        podsPartial={coverageIsLowerBound(coverage.pods)}
      />

      {/* Operational signals are Karpenter-scoped: the actions come from the
          Karpenter action ledger and every link lands on a Karpenter-gated
          screen (Demand / Activity / pool diagnosis). On a Karpenter-less
          cluster the section — and its dead-end links — is omitted rather than
          shown empty. */}
      {karpenterActive && (
        <SectionCard
          title="Operational signals"
          subtitle="prioritized · each links to its diagnosis"
          actions={
            <div className="flex items-center gap-4">
              <LinkButton onClick={() => onNavigate("/capacity/demand")}>
                {summary.pendingPodCount !== undefined && !podsDeniedFlag
                  ? `All demand · ${summary.pendingPodCount} →`
                  : "All demand →"}
              </LinkButton>
              <LinkButton onClick={() => onNavigate("/capacity/activity")}>
                Activity timeline →
              </LinkButton>
            </div>
          }
          bodyClassName="divide-y divide-theme-border-subtle"
        >
          {signals.length === 0 ? (
            <InlineEmpty
              title="No operational signals"
              detail="Radar reported no capacity-affecting actions for this cluster."
            />
          ) : (
            signals.map((signal) => (
              <div
                key={signal.key}
                className="flex items-start gap-3 px-4 py-2.5 hover:bg-theme-hover/40"
              >
                <Badge
                  severity={signal.severity}
                  size="sm"
                  className="min-w-[3rem] justify-center"
                >
                  {signalSeverityLabel(signal.severity)}
                </Badge>
                <div className="min-w-0 flex-1">
                  <div className="text-sm font-medium text-theme-text-primary">
                    {signal.title}
                  </div>
                  <div className="flex flex-wrap items-baseline gap-x-2 text-xs text-theme-text-secondary">
                    {signal.subjects?.map((subject) => (
                      <button
                        key={subject.label}
                        type="button"
                        onClick={subject.open}
                        className="font-mono text-accent-text hover:underline"
                      >
                        {subject.label}
                      </button>
                    ))}
                    {signal.detail && <span>{signal.detail}</span>}
                  </div>
                </div>
                {signal.action && (
                  <LinkButton
                    onClick={signal.action.run}
                    className="shrink-0 self-center"
                  >
                    {signal.action.label} →
                  </LinkButton>
                )}
              </div>
            ))
          )}
        </SectionCard>
      )}

      <NodeGroupsSection
        sectionRef={inventoryRef}
        groups={data.groups}
        pools={data.pools}
        coverage={coverage}
        poolCount={summary.poolCount}
        unattributedNodeCount={summary.unattributedNodeCount ?? 0}
        onOpenPool={onOpenPool}
        onOpenResource={onOpenResource}
      />

      <OrphanAutoscalerSection
        orphans={data.orphanAutoscalerGroups}
        meta={data.orphanAutoscalerGroupsMeta}
      />
    </ScrollableContent>
  );
}

// ============================================================================
// Capacity managers KPI tile
// ============================================================================

const GROUP_PLATFORM_LABELS: Record<string, string> = {
  karpenter: "Karpenter NodePool",
  gke: "GKE node pool",
  eks: "EKS managed node group",
  aks: "AKS agent pool",
  kops: "kOps instance group",
  doks: "DOKS node pool",
  ack: "ACK node pool",
  lke: "LKE node pool",
  scaleway: "Scaleway pool",
};

export function groupPlatformLabel(platform: string): string {
  return GROUP_PLATFORM_LABELS[platform] ?? humanizeCode(platform);
}

function ManagersTile({
  managers,
  groups,
  autoscalerStatus,
  nodesObserved,
  karpenterMode,
}: {
  managers: CapacityManagerSummary[];
  groups: CapacityGroupSummary[];
  autoscalerStatus?: CapacitySourceCoverage;
  nodesObserved: boolean;
  karpenterMode?: string;
}) {
  const worst = worstManagerStatus(managers);
  // Denied, cache-scoped, unreadable and unparseable all mean the same thing to
  // an operator: detection did not run. Only a source that WAS read may report
  // "none detected".
  const detectionFailure = autoscalerDetectionFailure(autoscalerStatus);
  const notPublishedDetail = autoscalerNotPublishedDetail(autoscalerStatus);
  const unvalidated = new Set(
    groups
      .filter((group) => group.manager && !group.managerValidated)
      .map((group) => group.manager),
  );
  const observedAt = coverageHasObservations(autoscalerStatus)
    ? autoscalerStatus?.observedAt
    : undefined;
  return (
    <div className="rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
      <div className="flex items-center gap-1.5">
        <span className="text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">
          Capacity managers
        </span>
        {worst === "degraded" && (
          <Badge severity="warning" size="sm">
            Attention
          </Badge>
        )}
      </div>
      <div className="mt-1.5">
        {managers.length === 0 ? (
          detectionFailure ? (
            <>
              <div className="text-sm font-medium text-theme-text-primary">
                Detection unavailable
              </div>
              <div className="mt-0.5 text-xs text-theme-text-tertiary">
                {detectionFailure}
              </div>
            </>
          ) : nodesObserved ? (
            <>
              <div className="text-sm text-theme-text-secondary">
                None detected
              </div>
              {notPublishedDetail && (
                <div className="mt-0.5 text-xs text-theme-text-tertiary">
                  {notPublishedDetail}
                </div>
              )}
            </>
          ) : (
            <div className="text-xs text-theme-text-tertiary">
              Not yet observed
            </div>
          )
        ) : (
          <div className="flex flex-col gap-1">
            {managers.map((manager) => (
              <ManagerLine
                key={manager.manager}
                manager={manager}
                karpenterMode={karpenterMode}
                validated={!unvalidated.has(manager.manager)}
              />
            ))}
            {detectionFailure && (
              <div className="mt-0.5 text-[11px] text-theme-text-tertiary">
                {`Autoscaler detection is partially unavailable. ${detectionFailure}`}
              </div>
            )}
            {observedAt && (
              <WithTooltip
                tip={`The autoscaler publishes its own timestamp; a quiet cluster republishes rarely, so an old reading is context, not breakage. Published ${formatTimestamp(observedAt)}.`}
              >
                <div className="mt-0.5 text-[11px] text-theme-text-tertiary">
                  as of {relativeTime(observedAt)}
                </div>
              </WithTooltip>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

/** Quiet marker for a manager whose node→group attribution Radar has not yet
 *  validated against a live cluster of that platform. */
function UnvalidatedHint() {
  return (
    <WithTooltip tip="Attribution for this platform is not yet validated on live clusters — group membership here is best effort.">
      <span className="cursor-help text-[10px] text-theme-text-tertiary underline decoration-dotted underline-offset-2">
        unvalidated
      </span>
    </WithTooltip>
  );
}

function ManagerLine({
  manager,
  karpenterMode,
  validated,
}: {
  manager: CapacityManagerSummary;
  karpenterMode?: string;
  validated: boolean;
}) {
  const tone = managerStatusTone(manager.status);
  const showDetail = manager.status === "degraded" && manager.detail;
  // Auto Mode is the notable case (AWS runs the controller — its logs and
  // knobs are out of reach); self-managed is the default and earns no ink.
  const modeSuffix =
    manager.manager === "karpenter" && karpenterMode === "eks_auto"
      ? " · EKS Auto Mode"
      : "";
  return (
    <div>
      <div className="flex items-baseline justify-between gap-2">
        <span className="flex min-w-0 items-center gap-1.5">
          <StatusDot tone={tone} />
          <span className="truncate text-sm text-theme-text-primary">
            {capacityManagerLabel(manager.manager)}
            {modeSuffix}
          </span>
          {!validated && <UnvalidatedHint />}
        </span>
        <span className="shrink-0 font-mono text-xs text-theme-text-tertiary">
          {manager.groupCount} {manager.groupCount === 1 ? "group" : "groups"}
        </span>
      </div>
      {showDetail && (
        <div className="mt-0.5 pl-3 text-[11px] text-theme-text-tertiary">
          {manager.detail}
        </div>
      )}
    </div>
  );
}

// ============================================================================
// Node groups inventory (every manager) + autoscaler-child sub-tables
// ============================================================================

function NodeGroupsSection({
  sectionRef,
  groups,
  pools,
  coverage,
  poolCount,
  unattributedNodeCount,
  onOpenPool,
  onOpenResource,
}: {
  sectionRef: RefObject<HTMLDivElement | null>;
  groups: CapacityGroupSummary[];
  pools: CapacityPoolSummary[];
  coverage: CapacityCoverageBySource;
  poolCount?: number;
  unattributedNodeCount: number;
  onOpenPool: (name: string) => void;
  onOpenResource: (resource: SelectedResource) => void;
}) {
  const nodesObserved = coverageHasObservations(coverage.nodes);
  // A group with no attributed manager means one of two very different things,
  // and only the detection source can tell them apart.
  const detectionFailure = autoscalerDetectionFailure(
    coverage.autoscalerStatus,
  );
  const poolByName = useMemo(() => {
    const map = new Map<string, CapacityPoolSummary>();
    for (const pool of pools) map.set(pool.resource.ref.name, pool);
    return map;
  }, [pools]);

  return (
    <div ref={sectionRef} className="scroll-mt-4">
      <SectionCard
        title="Node groups"
        subtitle="logical groups across every capacity manager"
        bodyClassName=""
      >
        {groups.length === 0 ? (
          nodesObserved ? (
            <InlineEmpty
              title="No node groups identified"
              detail={
                (poolCount ?? 0) > 0
                  ? "Nodes carry no group-identity labels and no Karpenter NodePool owns them yet."
                  : "Nodes carry no group-identity labels."
              }
            />
          ) : (
            <InlineEmpty
              title="Node groups unavailable"
              detail={coverageMessage(coverage.nodes, "Node inventory")}
            />
          )
        ) : (
          <>
            <div className={TABLE_WRAP}>
              <table className="w-full min-w-[980px] text-left">
                <thead className={TABLE_HEAD}>
                  <tr>
                    <th className={TH}>Group</th>
                    <th className={TH}>Status</th>
                    <th className={TH}>Manager</th>
                    <th className={TH}>Nodes</th>
                    <th className={TH}>
                      <WithTooltip tip="Allocatable the kubelet offers the scheduler across this group's nodes">
                        <span>Allocatable</span>
                      </WithTooltip>
                    </th>
                    <th className={TH}>
                      <WithTooltip tip="Sum of scheduled pod requests on this group's nodes">
                        <span>Scheduled requests</span>
                      </WithTooltip>
                    </th>
                    <th className={TH}>Scaling</th>
                  </tr>
                </thead>
                <tbody className={TBODY}>
                  {groups.map((group) => (
                    <GroupRow
                      key={group.id}
                      group={group}
                      pool={
                        group.manager === "karpenter"
                          ? poolByName.get(group.name)
                          : undefined
                      }
                      detectionFailure={detectionFailure}
                      onOpenPool={onOpenPool}
                      onOpenResource={onOpenResource}
                    />
                  ))}
                </tbody>
              </table>
            </div>
            {unattributedNodeCount > 0 && (
              <div className="border-t border-theme-border-subtle px-4 py-2.5">
                <WithTooltip tip="No group-identity label and no Karpenter ownership — Radar does not guess group membership from node names.">
                  <span className="text-xs text-theme-text-tertiary">
                    {`${unattributedNodeCount} ${
                      unattributedNodeCount === 1 ? "node" : "nodes"
                    } without group identity`}
                  </span>
                </WithTooltip>
              </div>
            )}
          </>
        )}
      </SectionCard>
    </div>
  );
}

function ExplainDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const dialogRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKeyDown);
    dialogRef.current?.focus();
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [open, onClose]);
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div
        className="absolute inset-0 bg-black/60 backdrop-blur-sm"
        onClick={onClose}
      />
      <div
        ref={dialogRef}
        tabIndex={-1}
        role="dialog"
        aria-label="How to read capacity numbers"
        className="dialog relative mx-4 flex max-h-[85vh] w-full max-w-3xl flex-col outline-none"
      >
        <div className="flex shrink-0 items-center justify-between border-b border-theme-border/50 px-6 py-4">
          <div className="flex items-center gap-3">
            <Layers3 className="h-5 w-5 text-accent-text" />
            <h3 className="text-lg font-semibold text-theme-text-primary">
              How to read these numbers
            </h3>
          </div>
          <button
            onClick={onClose}
            aria-label="Close"
            className="rounded-md p-1.5 transition-colors hover:bg-theme-elevated"
          >
            <X className="h-5 w-5 text-theme-text-tertiary" />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-6 py-5">
          <p className="text-sm text-theme-text-secondary">
            Scheduling capacity ≠ actual usage. Requests are what the scheduler
            consumes; usage is an efficiency signal — the two never mix, and no
            bar here is a health score.
          </p>
          <div className="mt-4 grid gap-3 sm:grid-cols-2">
            {EXPLAIN_CARDS.map((card) => (
              <div key={card.term} className="text-xs leading-relaxed">
                <div className="font-semibold text-theme-text-primary">
                  {card.term}
                </div>
                <div className="mt-0.5 text-theme-text-tertiary">
                  {card.body}
                </div>
              </div>
            ))}
          </div>
          <p className="mt-4 border-t border-theme-border-subtle pt-3 text-[11px] text-theme-text-tertiary">
            Certainty on each value — <span className="font-mono">=</span> exact
            · <span className="font-mono">≥</span> lower bound ·{" "}
            <span className="font-mono">≤</span> upper bound (a difference
            computed from a lower-bound input) ·{" "}
            <span className="font-mono">?</span> unknown. Hover a glyph for its
            coverage detail.
          </p>
        </div>
      </div>
    </div>
  );
}

/** Worst-of rollup of the autoscaler's own per-child health so the collapsed
 *  row carries the "expand me" signal: backoff outranks unhealthy outranks
 *  healthy; health strings pass through verbatim. Null when no child reports
 *  anything — never a fabricated "Healthy". */
function worstChildHealth(
  children: CapacityAutoscalerChildObservation[],
): { label: string; tone: "healthy" | "alert"; tip?: string } | null {
  const backoff = children.find((child) => child.backoff);
  if (backoff) {
    const message = backoff.backoff?.errorMessage;
    return {
      label: "Backoff",
      tone: "alert",
      tip: `${backoff.id}${message ? `: ${message}` : ""}`,
    };
  }
  const unhealthy = children.find(
    (child) => child.health && child.health.toLowerCase() !== "healthy",
  );
  if (unhealthy) {
    return { label: unhealthy.health ?? "", tone: "alert", tip: unhealthy.id };
  }
  if (!children.some((child) => child.health)) return null;
  return { label: "Healthy", tone: "healthy" };
}

function GroupRow({
  group,
  pool,
  detectionFailure,
  onOpenPool,
  onOpenResource,
}: {
  group: CapacityGroupSummary;
  pool?: CapacityPoolSummary;
  /** Why manager detection could not run, when it could not. Non-null forbids
   *  the "none detected" conclusion for a group with no attributed manager. */
  detectionFailure: string | null;
  onOpenPool: (name: string) => void;
  onOpenResource: (resource: SelectedResource) => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const hasChildren = group.children.length > 0;
  const isKarpenter = group.manager === "karpenter";
  const worstPressure = pool
    ? pickWorstPressure(pool.ledger.limitPressure)
    : undefined;
  const childHealth = worstChildHealth(group.children);
  // Server-rendered prose — joined, never sorted or parsed.
  const scalingText = group.scaling.map((fact) => fact.summary).join(" · ");

  return (
    <>
      <tr
        className={`${ROW_HOVER}${hasChildren ? " cursor-pointer" : ""}`}
        onClick={
          hasChildren
            ? (event) => {
                // The whole row toggles the children, but clicks on inner
                // links/buttons (pool link, Inspect, tooltips) keep their own
                // behavior.
                const target = event.target as HTMLElement;
                if (target.closest("button, a, [role='link']")) return;
                setExpanded((value) => !value);
              }
            : undefined
        }
      >
        <td className={TD}>
          <div className="flex items-center gap-1.5">
            {hasChildren ? (
              <button
                type="button"
                aria-expanded={expanded}
                aria-label={expanded ? "Collapse children" : "Expand children"}
                onClick={() => setExpanded((value) => !value)}
                className="rounded p-0.5 hover:bg-theme-hover"
              >
                <CollapseChevron open={expanded} className="h-3.5 w-3.5" />
              </button>
            ) : (
              // Names align across rows with and without children.
              <span aria-hidden="true" className="w-[23px] shrink-0" />
            )}
            {isKarpenter ? (
              <LinkButton
                onClick={() => onOpenPool(group.name)}
                className="font-mono text-xs font-medium text-accent-text hover:underline"
              >
                {group.name}
              </LinkButton>
            ) : (
              <span className="font-mono text-xs text-theme-text-primary">
                {group.name}
              </span>
            )}
          </div>
        </td>
        <td className={TD}>
          <div className="flex flex-wrap items-center gap-1.5">
            {pool && <PoolReadyBadge ready={pool.ready} />}
            {worstPressure?.overLimit && (
              <Badge severity="warning" size="sm">
                Over limit
              </Badge>
            )}
            {childHealth && (
              <WithTooltip
                tip={
                  childHealth.tip ??
                  "Worst of the autoscaler's own per-group health reports."
                }
              >
                <span className="flex items-center gap-1 text-xs text-theme-text-secondary">
                  <StatusDot tone={childHealth.tone} />
                  {childHealth.label}
                </span>
              </WithTooltip>
            )}
            {pool && pool.issueCount > 0 && (
              <Badge severity="warning" size="sm">
                {pool.issueCount} {pool.issueCount === 1 ? "issue" : "issues"}
              </Badge>
            )}
            {isKarpenter && pool && (
              <LinkButton
                onClick={() =>
                  onOpenResource(identityToSelectedResource(pool.resource))
                }
                title="Open the raw NodePool in the resource drawer"
                className="text-theme-text-tertiary"
              >
                Inspect
              </LinkButton>
            )}
            {!pool && !childHealth && !worstPressure?.overLimit && (
              <span className="text-xs text-theme-text-tertiary">—</span>
            )}
          </div>
        </td>
        <td className={TD}>
          {group.manager ? (
            <span className="flex flex-wrap items-center gap-1.5">
              <Badge tone="structural" size="sm">
                {capacityManagerLabel(group.manager)}
              </Badge>
              {!group.managerValidated && <UnvalidatedHint />}
            </span>
          ) : detectionFailure ? (
            <WithTooltip tip={detectionFailure}>
              <span className="text-xs text-theme-text-tertiary">
                detection unavailable
              </span>
            </WithTooltip>
          ) : (
            <WithTooltip tip="No autoscaler was observed for this group — the platform identity comes from its node labels.">
              <span className="text-xs text-theme-text-tertiary">
                {groupPlatformLabel(group.platform)}
              </span>
            </WithTooltip>
          )}
        </td>
        <td
          className={`${TD} whitespace-nowrap font-mono text-xs text-theme-text-secondary`}
        >
          {`${group.readyNodeCount}/${group.nodeCount}`}
        </td>
        <td className={TD}>
          <InventoryQuantityCell
            observation={group.allocatable}
            empty="Not observed"
          />
        </td>
        <td className={TD}>
          <InventoryQuantityCell
            observation={group.scheduledRequests}
            empty="Not observed"
          />
        </td>
        <td className={`${TD} text-xs text-theme-text-secondary`}>
          {scalingText || <span className="text-theme-text-tertiary">—</span>}
        </td>
      </tr>
      {hasChildren && (
        <tr>
          <td colSpan={7} className="p-0">
            <Collapse open={expanded}>
              <ChildSubTable group={group} />
            </Collapse>
          </td>
        </tr>
      )}
    </>
  );
}

const CHILD_TH = "px-3 py-1.5 text-left font-medium whitespace-nowrap";
const CHILD_TD = "px-3 py-1.5 align-top text-xs text-theme-text-primary";

function ChildSubTable({ group }: { group: CapacityGroupSummary }) {
  return (
    <div className="border-t border-theme-border-subtle bg-theme-base/40 px-4 py-3">
      <div className="mb-1.5 text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">
        As the autoscaler reports it
      </div>
      <div className="overflow-x-auto">
        <table className="w-full min-w-[720px] text-left">
          <thead className="text-[11px] uppercase tracking-wide text-theme-text-tertiary">
            <tr>
              <th className={CHILD_TH}>Group</th>
              <th className={CHILD_TH}>Min–max</th>
              <th className={CHILD_TH}>Target</th>
              <th className={CHILD_TH}>Ready / total</th>
              <th className={CHILD_TH}>Health</th>
              <th className={CHILD_TH}>Backoff</th>
              <th className={CHILD_TH}>Observed</th>
            </tr>
          </thead>
          <tbody className={TBODY}>
            {group.children.map((child) => (
              <tr key={child.id} className={ROW_HOVER}>
                <td className={`${CHILD_TD} font-mono`}>
                  <WithTooltip tip={child.name}>
                    <span>{child.id}</span>
                  </WithTooltip>
                </td>
                <td
                  className={`${CHILD_TD} font-mono text-theme-text-secondary`}
                >
                  {childMinMax(child)}
                </td>
                <td className={CHILD_TD}>
                  <ChildTarget target={child.target} />
                </td>
                <td
                  className={`${CHILD_TD} font-mono text-theme-text-secondary`}
                >
                  {childReadyTotal(child)}
                </td>
                <td className={CHILD_TD}>
                  <ChildHealth health={child.health} />
                </td>
                <td className={CHILD_TD}>
                  <ChildBackoff backoff={child.backoff} />
                </td>
                <td className={CHILD_TD}>
                  <ChildObserved asOf={child.asOf} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {group.childrenMeta.truncated && (
        <p className="mt-2 text-[11px] text-theme-text-tertiary">
          {`Showing ${group.childrenMeta.returned} of ${group.childrenMeta.total} child groups.`}
        </p>
      )}
    </div>
  );
}

// ============================================================================
// Autoscaler-known groups with no joinable node (scale-to-zero included)
// ============================================================================

function OrphanAutoscalerSection({
  orphans,
  meta,
}: {
  orphans: CapacityAutoscalerChildObservation[];
  meta: CapacityBoundedResultMeta;
}) {
  if (orphans.length === 0) return null;
  return (
    <SectionCard
      title="Known to the autoscaler, unattributed"
      subtitle="Groups the autoscaler reports that no observed node joins — scale-to-zero groups live here until nodes appear."
      bodyClassName=""
    >
      <div className={TABLE_WRAP}>
        <table className="w-full min-w-[720px] text-left">
          <thead className={TABLE_HEAD}>
            <tr>
              <th className={TH}>Group</th>
              <th className={TH}>Min–max</th>
              <th className={TH}>Target</th>
              <th className={TH}>Health</th>
              <th className={TH}>Observed</th>
            </tr>
          </thead>
          <tbody className={TBODY}>
            {orphans.map((child) => (
              <tr key={child.id} className={ROW_HOVER}>
                <td className={`${TD} font-mono text-xs`}>
                  <WithTooltip tip={child.name}>
                    <span>{child.id}</span>
                  </WithTooltip>
                </td>
                <td
                  className={`${TD} whitespace-nowrap font-mono text-xs text-theme-text-secondary`}
                >
                  {childMinMax(child)}
                </td>
                <td className={TD}>
                  <ChildTarget target={child.target} />
                </td>
                <td className={TD}>
                  <ChildHealth health={child.health} />
                </td>
                <td className={TD}>
                  <ChildObserved asOf={child.asOf} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {meta.truncated && (
        <div className="border-t border-theme-border-subtle px-4 py-2.5 text-[11px] text-theme-text-tertiary">
          {`Showing ${meta.returned} of ${meta.total} groups.`}
        </div>
      )}
    </SectionCard>
  );
}

// ---------------------------------------------------------------------------
// Autoscaler-child cell renderers (shared by group children + orphan groups)
// ---------------------------------------------------------------------------

function childMinMax(child: CapacityAutoscalerChildObservation): string {
  if (child.minSize === undefined && child.maxSize === undefined) return "—";
  return `${child.minSize ?? "?"}–${child.maxSize ?? "?"}`;
}

function childReadyTotal(child: CapacityAutoscalerChildObservation): string {
  if (child.readyNodes === undefined && child.totalNodes === undefined)
    return "—";
  return `${child.readyNodes ?? "?"}/${child.totalNodes ?? "?"}`;
}

function ChildTarget({ target }: { target?: number }) {
  // A published 0 is a real target (scale-to-zero), not an absence — render it.
  if (target === undefined)
    return <span className="text-theme-text-tertiary">—</span>;
  return (
    <span className="font-mono text-theme-text-primary tabular-nums">
      {target}
    </span>
  );
}

function ChildHealth({ health }: { health?: string }) {
  if (!health) return <span className="text-theme-text-tertiary">—</span>;
  return (
    <span className="flex items-center gap-1.5">
      <StatusDot tone={mapHealthToTone(health)} />
      <span className="text-theme-text-secondary">{health}</span>
    </span>
  );
}

function ChildBackoff({ backoff }: { backoff?: CapacityAutoscalerBackoff }) {
  // The legacy status format reports a group as backing off without any error
  // class, code or message. A dash there would read as "not backing off" — the
  // detail is missing, the backoff is not.
  if (!backoff) return <span className="text-theme-text-tertiary">—</span>;
  const message = backoff.errorMessage;
  const label = backoff.errorCode ?? backoff.errorClass;
  return (
    <WithTooltip
      tip={
        message ??
        label ??
        "The autoscaler reports this group in backoff without a reason."
      }
    >
      <Badge severity="alert" size="sm">
        {label ?? "Backoff"}
      </Badge>
    </WithTooltip>
  );
}

/** Relative freshness; an absent `asOf` is "not probed" — never a zero time
 *  and never "now" (the payload published null, meaning we never checked). */
function ChildObserved({ asOf }: { asOf?: string }) {
  if (!asOf)
    return <span className="text-theme-text-tertiary">not probed</span>;
  return (
    <span className="text-theme-text-secondary">{relativeTime(asOf)}</span>
  );
}

function PendingDemandChips({
  summary,
  podsDenied,
  podsObserved,
  podsPartial,
}: {
  summary: CapacityOverviewSummary;
  podsDenied: boolean;
  podsObserved: boolean;
  /** Pod coverage is a lower bound (namespace-scoped or partial) — an empty
   *  result then means "none observed in scope", never a global "none". */
  podsPartial: boolean;
}) {
  // The "pods" resource duplicates the dedicated "Pod slots" chip below.
  const entries = summary.aggregateDemand
    ? sortedResourceEntries(summary.aggregateDemand.resources).filter(
        ([resource]) => resource !== "pods",
      )
    : [];
  const demandCertainty = summary.aggregateDemand?.certainty;
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="text-xs text-theme-text-tertiary">
        Active pending requests (scheduling demand, not usage):
      </span>
      {podsDenied ? (
        <DeniedBadge />
      ) : !podsObserved ? (
        // Pods not yet observed (syncing/unavailable) — absence is not zero.
        <span className="text-xs text-theme-text-tertiary">Not observed</span>
      ) : entries.length === 0 ? (
        <span className="text-xs text-theme-text-tertiary">
          {podsPartial ? "None observed in scope" : "None pending"}
        </span>
      ) : (
        <>
          {entries.map(([resource, value]) => (
            <Badge key={resource} tone="structural" size="sm">
              <span className="font-mono">
                {`${shortResourceLabel(resource)} ${formatQuantity(resource, value)}`}
              </span>
            </Badge>
          ))}
          {summary.pendingPodCount !== undefined && (
            <Badge tone="structural" size="sm">
              <span className="font-mono">{`Pod slots ${summary.pendingPodCount}`}</span>
            </Badge>
          )}
          {demandCertainty && demandCertainty !== "exact" && (
            <CertaintyGlyph certainty={demandCertainty} />
          )}
        </>
      )}
    </div>
  );
}

/** "8 ready · 1 launched · 1 failed · +1 orphaned" — nonzero stages in
 *  lifecycle order, failed kept late for adjacency with the orphan note.
 *  Null when the stage rollup is absent so the tile can fall back to its
 *  coverage message (unavailable ≠ zero claims). */
export function claimStagesDetail(
  stages: CapacityClaimLifecycleSummary | undefined,
  orphaned: number | undefined,
): string | null {
  if (!stages) return null;
  const parts: string[] = [];
  const push = (count: number, label: string) => {
    if (count > 0) parts.push(`${count} ${label}`);
  };
  push(stages.ready, "ready");
  push(stages.initialized, "initialized");
  push(stages.registered, "registered");
  push(stages.launched, "launched");
  push(stages.pending, "pending");
  push(stages.failed, "failed");
  push(stages.terminating, "terminating");
  if (orphaned) parts.push(`+${orphaned} orphaned`);
  if (parts.length === 0) return stages.total === 0 ? "none yet" : null;
  return parts.join(" · ");
}

function signalSeverityLabel(severity: OverviewSignal["severity"]): string {
  if (severity === "error") return "Alert";
  if (severity === "warning") return "Warn";
  if (severity === "info") return "Info";
  return "Note";
}

function buildSignals(
  summary: CapacityOverviewSummary,
  handlers: {
    onOpenPool: (name: string) => void;
    onNavigate: (path: string) => void;
  },
): OverviewSignal[] {
  const signals: OverviewSignal[] = summary.actions.map((action) => {
    const demandState: CapacityDemandState | undefined =
      action.code === "pending_demand_blocked"
        ? "blocked"
        : action.code === "pending_demand_resource_pressure"
          ? "awaiting_capacity"
          : action.code === "pending_demand_unclassified"
            ? "unknown"
            : undefined;
    const subjects = action.pools.slice(0, 3).map((pool) => ({
      label: pool.ref.name,
      open: () => handlers.onOpenPool(pool.ref.name),
    }));
    const detailParts: string[] = [];
    // The wire caps subjects at 25 (`truncated`), so the visible array length
    // must never masquerade as the total — an action over 50 pools is
    // "+22+ more", not "+22 more".
    const remainder = action.pools.length - subjects.length;
    if (remainder > 0)
      detailParts.push(`+${remainder}${action.truncated ? "+" : ""} more`);
    if (action.count > 0)
      detailParts.push(
        `${action.count} ${action.count === 1 ? "occurrence" : "occurrences"}`,
      );
    const firstPool = action.pools[0];
    const action_: OverviewSignal["action"] = demandState
      ? {
          label: "View demand",
          run: () =>
            handlers.onNavigate(
              `/capacity/demand?state=${encodeURIComponent(demandState)}`,
            ),
        }
      : firstPool
        ? {
            label: "Diagnose pool",
            run: () => handlers.onOpenPool(firstPool.ref.name),
          }
        : undefined;
    return {
      key: `action:${action.code}`,
      severity: actionSeverity(action.highestSeverity),
      title: humanizeCode(action.code),
      detail:
        detailParts.join(" · ") ||
        (subjects.length > 0
          ? ""
          : "Reported by the Capacity API for this cluster."),
      subjects: subjects.length > 0 ? subjects : undefined,
      action: action_,
    };
  });

  const orphaned = summary.orphanedClaimCount ?? 0;
  const unpooled = summary.unpooledNodeCount ?? 0;
  if (orphaned > 0 || unpooled > 0) {
    const parts: string[] = [];
    if (orphaned > 0)
      parts.push(
        `${orphaned} ${orphaned === 1 ? "claim references" : "claims reference"} a deleted NodePool`,
      );
    if (unpooled > 0)
      parts.push(
        `${unpooled} ${unpooled === 1 ? "node is" : "nodes are"} outside Karpenter pools`,
      );
    signals.push({
      key: "inventory:outside-pools",
      severity: "neutral",
      title: "Inventory outside live pools",
      // "Both" only when two facts are actually listed — one orphaned-claim or
      // one unpooled-node fact reads as a miscount otherwise.
      detail: `${parts.join("; ")}. ${
        parts.length > 1
          ? "Both may be intentional."
          : "This may be intentional."
      }`,
    });
  }

  return signals.sort(
    (left, right) =>
      SEVERITY_RANK[left.severity] - SEVERITY_RANK[right.severity],
  );
}
