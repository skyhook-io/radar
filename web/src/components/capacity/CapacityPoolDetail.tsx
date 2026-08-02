import { useState, type ReactNode } from "react";
import { CircleHelp, Layers3 } from "lucide-react";
import {
  WithTooltip,
  type CapacityLedger,
  type CapacityMemberListResponse,
  type CapacityMemberType,
  type CapacityPoolMember,
  type CapacityPoolObservation,
  type CapacityQuantityObservation,
  type CapacitySourceCoverage,
} from "@skyhook-io/k8s-ui";
import { Badge } from "@skyhook-io/k8s-ui/components/ui/Badge";
import {
  isCapacityCursorInvalidError,
  isNotFoundError,
  useCapacityPoolDetail,
  useCapacityPoolMembers,
} from "../../api/client";
import type { SelectedResource } from "../../types";
import { refToSelectedResource } from "../../utils/navigation";
import {
  ActualUsageDetail,
  ActualUsageInline,
  CapacityFreshness,
  CapacityIssueEvidence,
  CertaintyGlyph,
  ClaimStageBadge,
  conditionSummary,
  ConditionBadge,
  coverageHasObservations,
  coverageIsDenied,
  coverageIsLowerBound,
  coverageMessage,
  DeniedBadge,
  EmptyState,
  errorMessage,
  formatQuantity,
  formatTaint,
  humanizeCode,
  identityKey,
  identityToSelectedResource,
  InlineEmpty,
  integrationBlock,
  KeyValueRows,
  LinkButton,
  memberCoverageSource,
  MiniCount,
  NodeReadyBadge,
  Notice,
  observationTitle,
  PoolReadyBadge,
  POOL_SECTIONS,
  PressureDetail,
  quantityResourceRank,
  QuantityInline,
  RefreshError,
  resourceLabel,
  ResourceLink,
  ROW_HOVER,
  ScopeBadges,
  ScrollableContent,
  SectionCard,
  TABLE_HEAD,
  TABLE_WRAP,
  TBODY,
  TD,
  TH,
  TokenGroup,
  useCapacityCursorRecovery,
  useCapacityPagination,
  type CapacityConnectionState,
  type PoolSection,
} from "./shared";

export function CapacityPoolDetail({
  name,
  section,
  connectionState,
  onNavigate,
  onOpenResource,
}: {
  name: string;
  section: PoolSection;
  connectionState: CapacityConnectionState;
  onNavigate: (path: string) => void;
  onOpenResource: (resource: SelectedResource) => void;
}) {
  const query = useCapacityPoolDetail(name);
  const poolPath = `/capacity/pools/${encodeURIComponent(name)}`;

  if (isNotFoundError(query.error)) {
    return (
      <ScrollableContent>
        <div className="mx-auto max-w-xl">
          <SectionCard title="Not found" bodyClassName="px-4 py-4">
            <p className="text-sm text-theme-text-secondary">
              NodePool “<span className="font-mono">{name}</span>” no longer
              exists. It may have been deleted since this link was created.
              Claims it owned appear as orphans in the Overview until they
              drain.
            </p>
            <LinkButton
              className="mt-3"
              onClick={() => onNavigate("/capacity")}
            >
              ← Back to Capacity overview
            </LinkButton>
          </SectionCard>
        </div>
      </ScrollableContent>
    );
  }

  const blocked = integrationBlock(
    query.data,
    query.error,
    query.isLoading,
    `Loading ${name}…`,
  );
  if (blocked) return blocked;

  const response = query.data;
  if (response && !coverageHasObservations(response.coverage.nodePools)) {
    return (
      <EmptyState
        icon={Layers3}
        title="NodePool inventory unavailable"
        detail={coverageMessage(
          response.coverage.nodePools,
          "NodePool inventory",
        )}
      />
    );
  }
  const pool = response?.pool;
  if (!response || !pool) {
    return (
      <EmptyState
        icon={CircleHelp}
        title="NodePool unavailable"
        detail="The Capacity API did not return an observation for this NodePool."
      />
    );
  }

  const staleGeneration =
    pool.observedGeneration !== undefined &&
    pool.observedGeneration !== pool.generation;

  return (
    <ScrollableContent>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <LinkButton onClick={() => onNavigate("/capacity")}>
          ← Overview
        </LinkButton>
        <Badge tone="structural" size="sm">
          NodePool
        </Badge>
        <h1 className="font-mono text-base font-semibold text-theme-text-primary">
          {pool.resource.ref.name}
        </h1>
        <PoolReadyBadge ready={pool.ready} />
        {pool.mode !== "unknown" && (
          <Badge tone="structural" size="sm">
            {pool.mode}
          </Badge>
        )}
        <WithTooltip tip="metadata.generation vs status.observedGeneration">
          <span className="font-mono text-xs text-theme-text-tertiary">
            generation {pool.generation}
            {pool.observedGeneration !== undefined
              ? ` · observed ${pool.observedGeneration}`
              : ""}
          </span>
        </WithTooltip>
        {staleGeneration && (
          <WithTooltip tip="The controller has not yet observed the latest spec. Status below may describe the previous generation.">
            <Badge severity="warning" size="sm">
              Stale generation
            </Badge>
          </WithTooltip>
        )}
        <div className="flex-1" />
        <ScopeBadges coverage={response.coverage} />
        <LinkButton
          onClick={() =>
            onNavigate(`/capacity/activity?pool=${encodeURIComponent(name)}`)
          }
        >
          Pool activity →
        </LinkButton>
        <LinkButton
          title="Raw resource, YAML, events"
          onClick={() =>
            onOpenResource(identityToSelectedResource(pool.resource))
          }
        >
          Resource drawer →
        </LinkButton>
        <CapacityFreshness
          meta={response}
          query={query}
          connectionState={connectionState}
        />
      </div>

      {pool.nodeClass && (
        <div className="flex items-center gap-2 text-xs text-theme-text-secondary">
          <span>NodeClass</span>
          <LinkButton
            className="font-mono text-xs font-medium text-accent-text hover:underline"
            onClick={() =>
              onOpenResource(refToSelectedResource(pool.nodeClass!.reference))
            }
          >
            {pool.nodeClass.reference.kind}/{pool.nodeClass.reference.name}
          </LinkButton>
          <PoolReadyBadge ready={pool.nodeClass.ready} />
        </div>
      )}

      {query.error && <RefreshError message={errorMessage(query.error)} />}

      <div className="flex gap-1 border-b border-theme-border">
        {POOL_SECTIONS.map((tab) => (
          <button
            key={tab.id}
            type="button"
            aria-selected={section === tab.id}
            onClick={() =>
              onNavigate(
                tab.id === "summary" ? poolPath : `${poolPath}/${tab.id}`,
              )
            }
            className={`border-b-2 px-3 py-2 text-sm font-medium transition-colors ${
              section === tab.id
                ? "border-skyhook-500 text-theme-text-primary"
                : "border-transparent text-theme-text-secondary hover:text-theme-text-primary"
            }`}
          >
            {tab.label}
          </button>
        ))}
      </div>

      {section === "summary" && (
        <PoolSummaryTab
          pool={pool}
          onNavigate={onNavigate}
          onOpenResource={onOpenResource}
        />
      )}
      {section === "workloads" && (
        <PoolWorkloadsTab name={name} onOpenResource={onOpenResource} />
      )}
      {section === "members" && (
        <PoolMembersTab name={name} onOpenResource={onOpenResource} />
      )}
      {section === "configuration" && (
        <PoolConfigurationTab pool={pool} onOpenResource={onOpenResource} />
      )}
    </ScrollableContent>
  );
}

// ---------------------------------------------------------------- ledger

const LEDGER_COLUMNS: {
  key: keyof CapacityLedger | "usage";
  header: string;
  tooltip: string;
}[] = [
  {
    key: "configuredLimit",
    header: "Configured limit",
    tooltip: "Declared ceiling on total provisioned capacity (spec.limits)",
  },
  {
    key: "provisioned",
    header: "Provisioned",
    tooltip:
      "Capacity Karpenter has committed for this pool (status.resources) — includes claims still launching",
  },
  {
    key: "inFlightCapacity",
    header: "In flight",
    tooltip:
      "Capacity of claims launched but not yet registered. Already counted inside Provisioned — shown separately to expose the launching share, not to be added on top",
  },
  {
    key: "limitHeadroom",
    header: "Limit headroom",
    tooltip:
      "Configured limit minus provisioned — Karpenter's actual provisioning gate (in-flight claims are already inside provisioned)",
  },
  {
    key: "allocatable",
    header: "Node allocatable",
    tooltip: "What kubelets offer the scheduler after reservations",
  },
  {
    key: "scheduledRequests",
    header: "Scheduled requests",
    tooltip: "Sum of scheduled pod requests on this pool's nodes",
  },
  {
    key: "aggregateUnallocatedRequests",
    header: "Unallocated ⚠",
    tooltip: "Allocatable minus requests, summed across nodes. NOT bin-packed.",
  },
  {
    key: "usage",
    header: "Actual usage",
    tooltip:
      "Point-in-time usage from metrics — efficiency signal, never scheduler headroom",
  },
];

function ledgerResourceKeys(ledger: CapacityLedger): string[] {
  const keys = new Set<string>();
  const collect = (obs?: CapacityQuantityObservation) => {
    if (obs) Object.keys(obs.resources).forEach((key) => keys.add(key));
  };
  collect(ledger.allocatable);
  collect(ledger.provisioned);
  collect(ledger.configuredLimit);
  collect(ledger.scheduledRequests);
  collect(ledger.aggregateUnallocatedRequests);
  collect(ledger.inFlightCapacity);
  ledger.actualUsage?.utilization.forEach((entry) => keys.add(entry.resource));
  return [...keys].sort(
    (left, right) =>
      quantityResourceRank(left) - quantityResourceRank(right) ||
      left.localeCompare(right),
  );
}

function LedgerValue({
  observation,
  resource,
  prefix,
}: {
  observation?: CapacityQuantityObservation;
  resource: string;
  prefix?: string;
}) {
  const raw = observation?.resources[resource];
  if (!observation || raw === undefined)
    return <span className="text-theme-text-tertiary">—</span>;
  return (
    <WithTooltip tip={observationTitle(observation)}>
      <span>
        {prefix}
        {formatQuantity(resource, raw)}
        {observation.certainty !== "exact" && (
          <>
            {" "}
            <CertaintyGlyph certainty={observation.certainty} />
          </>
        )}
      </span>
    </WithTooltip>
  );
}

function LedgerCell({
  column,
  ledger,
  resource,
  denied,
  coverage,
}: {
  column: (typeof LEDGER_COLUMNS)[number];
  ledger: CapacityLedger;
  resource: string;
  denied: boolean;
  coverage: CapacityPoolObservation["coverage"];
}) {
  if (column.key === "usage") {
    const usage = ledger.actualUsage;
    const entry = usage?.utilization.find((u) => u.resource === resource);
    if (!usage || !entry) {
      // Distinguish "metrics ran, this pool had no sample" (a real zero-ish
      // state, e.g. scaled to zero) from "metrics were never observed" — the
      // latter must not read as a benign No samples.
      if (coverageIsDenied(coverage.nodeMetrics))
        return (
          <WithTooltip tip="Metrics access denied — usage cannot be computed. This is not zero.">
            <span className="text-theme-text-tertiary">Unavailable</span>
          </WithTooltip>
        );
      if (!coverageHasObservations(coverage.nodeMetrics))
        return <span className="text-theme-text-tertiary">Not observed</span>;
      return <span className="text-theme-text-tertiary">No samples</span>;
    }
    const sampledSubset = usage.coveredNodes < usage.totalNodes;
    return (
      <WithTooltip
        tip={
          sampledSubset
            ? `${observationTitle(usage.quantity)} — only ${usage.coveredNodes} of ${usage.totalNodes} nodes had samples; the percentage covers the sampled nodes' allocatable, not the whole pool.`
            : observationTitle(usage.quantity)
        }
      >
        <span>
          {formatQuantity(resource, entry.usage)}
          {usage.quantity.certainty !== "exact" && (
            <>
              {" "}
              <CertaintyGlyph certainty={usage.quantity.certainty} />
            </>
          )}{" "}
          <span className="text-theme-text-tertiary">
            ({Math.round(entry.percent)}%
            {sampledSubset
              ? ` of ${usage.coveredNodes}/${usage.totalNodes} sampled`
              : ""}
            )
          </span>
        </span>
      </WithTooltip>
    );
  }

  if (column.key === "configuredLimit") {
    const obs = ledger.configuredLimit;
    if (!obs || obs.resources[resource] === undefined)
      return (
        <WithTooltip tip="No configured ceiling on this dimension. Not unlimited — provider quota and availability remain external constraints.">
          <span className="text-theme-text-tertiary">No limit</span>
        </WithTooltip>
      );
    return <LedgerValue observation={obs} resource={resource} />;
  }

  if (column.key === "inFlightCapacity") {
    const obs = ledger.inFlightCapacity;
    if (!obs || obs.resources[resource] === undefined) {
      // No in-flight figure. If NodeClaims weren't observed we can't claim
      // "none in flight" — that would read a missing source as a healthy zero.
      if (!coverageHasObservations(coverage.nodeClaims))
        return (
          <WithTooltip tip="NodeClaims were not observed — in-flight capacity is unknown, not zero.">
            <span className="text-theme-text-tertiary">Unknown</span>
          </WithTooltip>
        );
      if (obs && obs.certainty === "unknown")
        return (
          <WithTooltip tip="Claims are launching but none report capacity yet — the in-flight quantity is unknown, not zero.">
            <span className="text-theme-text-tertiary">
              In flight — unknown
            </span>
          </WithTooltip>
        );
      return (
        <WithTooltip tip="No capacity currently in flight">
          <span className="text-theme-text-tertiary">—</span>
        </WithTooltip>
      );
    }
    return <LedgerValue observation={obs} resource={resource} prefix="+" />;
  }

  if (column.key === "limitHeadroom") {
    const obs = ledger.limitHeadroom;
    if (!obs || obs.resources[resource] === undefined)
      return (
        <WithTooltip tip="Headroom is undefined without a configured limit">
          <span className="text-theme-text-tertiary">—</span>
        </WithTooltip>
      );
    return <LedgerValue observation={obs} resource={resource} />;
  }

  if (
    (column.key === "scheduledRequests" ||
      column.key === "aggregateUnallocatedRequests") &&
    denied
  ) {
    return (
      <WithTooltip tip="Pod access denied — cannot be computed. This is not zero.">
        <span className="text-theme-text-tertiary">Unavailable</span>
      </WithTooltip>
    );
  }

  if (
    (column.key === "scheduledRequests" ||
      column.key === "aggregateUnallocatedRequests") &&
    !coverageHasObservations(coverage.pods)
  ) {
    return (
      <WithTooltip tip="Pods were not observed — this value cannot be computed. This is not zero.">
        <span className="text-theme-text-tertiary">Not observed</span>
      </WithTooltip>
    );
  }

  if (
    (column.key === "allocatable" ||
      column.key === "aggregateUnallocatedRequests") &&
    !coverageHasObservations(coverage.nodes)
  ) {
    return (
      <WithTooltip tip="Nodes were not observed — this value cannot be computed. This is not zero.">
        <span className="text-theme-text-tertiary">Unavailable</span>
      </WithTooltip>
    );
  }

  const observation = ledger[column.key] as
    CapacityQuantityObservation | undefined;
  return <LedgerValue observation={observation} resource={resource} />;
}

function PoolSummaryTab({
  pool,
  onNavigate,
  onOpenResource,
}: {
  pool: CapacityPoolObservation;
  onNavigate: (path: string) => void;
  onOpenResource: (resource: SelectedResource) => void;
}) {
  const denied = coverageIsDenied(pool.coverage.pods);
  const resources = ledgerResourceKeys(pool.ledger);
  // Overview signals label their links "Diagnose pool", so a broken pool's
  // dominant arrival is diagnostic: lead with the diagnosis instead of a
  // ledger of true zeros. Issues are warning-or-worse by construction;
  // informational posture (facts on a healthy scale-to-zero pool) deliberately
  // keeps the healthy ordering — reordering on a normal state feels unstable.
  const diagnosisFirst = pool.issues.length > 0;
  const scaledToZero = pool.nodes?.total === 0;
  const issuesCard = (
    <SectionCard title="Issues & conditions" bodyClassName="px-4 py-4">
      <PostureView
        pool={pool}
        onOpenResource={onOpenResource}
        onNavigate={onNavigate}
        onOpenMembers={() =>
          onNavigate(
            `/capacity/pools/${encodeURIComponent(pool.resource.ref.name)}/members`,
          )
        }
      />
    </SectionCard>
  );

  return (
    <div className="space-y-4">
      {diagnosisFirst && issuesCard}
      <SectionCard
        title="Capacity ledger"
        subtitle="every column is a different concept — they are never combined into one number"
      >
        {scaledToZero && (
          <p className="px-4 pt-3 text-xs text-theme-text-secondary">
            No nodes exist in this pool right now — the observed columns are
            true zeros, and Configured limit is the ceiling provisioning would
            run against.
          </p>
        )}
        <div className={TABLE_WRAP}>
          <table className="w-full min-w-[1180px] text-left">
            <thead className={TABLE_HEAD}>
              <tr>
                <th className={TH}>Resource</th>
                {LEDGER_COLUMNS.map((column) => (
                  <th key={column.key} className={TH}>
                    <WithTooltip tip={column.tooltip}>
                      <span>{column.header}</span>
                    </WithTooltip>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className={TBODY}>
              {resources.length === 0 ? (
                <tr>
                  <td
                    className={`${TD} text-theme-text-tertiary`}
                    colSpan={LEDGER_COLUMNS.length + 1}
                  >
                    No ledger dimensions were reported for this pool.
                  </td>
                </tr>
              ) : (
                resources.map((resource) => (
                  <tr key={resource}>
                    <td className={`${TD} whitespace-nowrap font-medium`}>
                      {resourceLabel(resource)}
                    </td>
                    {LEDGER_COLUMNS.map((column) => (
                      <td
                        key={column.key}
                        className={`${TD} whitespace-nowrap font-mono text-xs`}
                      >
                        <LedgerCell
                          column={column}
                          ledger={pool.ledger}
                          resource={resource}
                          denied={denied}
                          coverage={pool.coverage}
                        />
                      </td>
                    ))}
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
        {pool.ledger.limitPressure.length > 0 && (
          <div className="grid gap-4 border-t border-theme-border-subtle px-4 py-3 sm:grid-cols-2 lg:grid-cols-3">
            {pool.ledger.limitPressure.map((pressure) => (
              <PressureDetail key={pressure.resource} pressure={pressure} />
            ))}
          </div>
        )}
        <p className="border-t border-theme-border-subtle px-4 py-3 text-[11px] leading-relaxed text-theme-text-tertiary">
          Sources: nodepool spec/status · node allocatable · pod list requests ·
          metrics API. Granularity: aggregate per pool; unallocated is aggregate
          subtraction, <b>not bin-packed</b>, and does not prove another pod can
          schedule. Certainty glyphs: <span className="font-mono">=</span> exact
          · <span className="font-mono">≥</span> lower bound ·{" "}
          <span className="font-mono">≤</span> upper bound (a difference
          computed from a lower-bound input) ·{" "}
          <span className="font-mono">?</span> unknown. Hover any value for its
          definition, certainty and source.
        </p>
      </SectionCard>

      <div className="grid gap-4 lg:grid-cols-2">
        <SectionCard title="Lifecycle" bodyClassName="space-y-4 px-4 py-4">
          <LifecycleGrid pool={pool} denied={denied} />
        </SectionCard>

        <SectionCard title="Composition" bodyClassName="px-4 py-4">
          <CompositionView pool={pool} />
        </SectionCard>

        <SectionCard title="Workload attribution" bodyClassName="px-4 py-4">
          <WorkloadAttribution
            pool={pool}
            denied={denied}
            onNavigate={onNavigate}
            onOpenResource={onOpenResource}
          />
        </SectionCard>

        <SectionCard title="Actual usage" bodyClassName="px-4 py-4">
          <ActualUsageDetail
            usage={pool.ledger.actualUsage}
            coverage={pool.coverage.nodeMetrics}
          />
        </SectionCard>
      </div>

      {!diagnosisFirst && issuesCard}
    </div>
  );
}

function LifecycleGrid({
  pool,
  denied,
}: {
  pool: CapacityPoolObservation;
  denied: boolean;
}) {
  return (
    <>
      <div>
        <div className="mb-2 text-xs font-medium text-theme-text-tertiary">
          Nodes
        </div>
        {pool.nodes ? (
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
            <MiniCount label="Ready" value={pool.nodes.ready} />
            <MiniCount label="Not ready" value={pool.nodes.notReady} />
            <MiniCount label="Cordoned" value={pool.nodes.cordoned} />
            <MiniCount label="Terminating" value={pool.nodes.terminating} />
          </div>
        ) : (
          <p className="text-sm text-theme-text-secondary">
            {coverageMessage(pool.coverage.nodes, "Node lifecycle")}
          </p>
        )}
      </div>
      <div>
        <div className="mb-2 text-xs font-medium text-theme-text-tertiary">
          Claims
        </div>
        {pool.claims ? (
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
            <MiniCount label="Pending" value={pool.claims.pending} />
            <MiniCount label="Launched" value={pool.claims.launched} />
            <MiniCount label="Registered" value={pool.claims.registered} />
            <MiniCount label="Initialized" value={pool.claims.initialized} />
            <MiniCount label="Ready" value={pool.claims.ready} />
            <MiniCount label="Failed" value={pool.claims.failed} />
            <MiniCount label="Terminating" value={pool.claims.terminating} />
          </div>
        ) : (
          <p className="text-sm text-theme-text-secondary">
            {coverageMessage(pool.coverage.nodeClaims, "Claim lifecycle")}
          </p>
        )}
      </div>
      <div>
        <div className="mb-1 text-xs font-medium text-theme-text-tertiary">
          Scheduled pods on this pool
        </div>
        <div className="font-mono text-sm text-theme-text-primary">
          <ScheduledPodCount
            count={pool.workloads?.scheduledPodCount}
            denied={denied}
            coverage={pool.coverage.pods}
          />
        </div>
      </div>
    </>
  );
}

/** Pod-derived count: namespace-scoped pod coverage makes it a lower bound, so
 *  it never renders bare. */
function ScheduledPodCount({
  count,
  denied,
  coverage,
}: {
  count?: number;
  denied: boolean;
  coverage?: CapacitySourceCoverage;
}) {
  if (denied) return <>Unknown — Pod access denied</>;
  if (count === undefined) return <>—</>;
  if (!coverageIsLowerBound(coverage)) return <>{count}</>;
  return (
    <WithTooltip tip={coverageMessage(coverage, "Scheduled pod count")}>
      <span>≥ {count}</span>
    </WithTooltip>
  );
}

function CompositionView({ pool }: { pool: CapacityPoolObservation }) {
  const composition = pool.composition;
  if (!pool.nodes || !composition)
    return (
      <p className="text-sm text-theme-text-secondary">
        {coverageMessage(pool.coverage.nodes, "Fleet composition")}
      </p>
    );
  const bucketValues = (
    buckets: { value: string; count: number }[],
  ): string[] => buckets.map((bucket) => `${bucket.value} · ${bucket.count}`);
  return (
    <div className="space-y-3">
      <TokenGroup
        title="Capacity type"
        values={bucketValues(composition.capacityTypes)}
        empty="None reported"
      />
      <TokenGroup
        title="Zones"
        values={composition.zones.map((bucket) => bucket.value)}
        empty="None reported"
      />
      <TokenGroup
        title="Instance types"
        values={composition.instanceTypes.map((bucket) => bucket.value)}
        empty="None — scaled to zero"
      />
      <TokenGroup
        title="Architectures"
        values={composition.architectures.map((bucket) => bucket.value)}
        empty="None reported"
      />
      <TokenGroup
        title="Images"
        values={composition.images.map((bucket) => bucket.value)}
        empty="None reported"
      />
    </div>
  );
}

function WorkloadAttribution({
  pool,
  denied,
  onNavigate,
  onOpenResource,
}: {
  pool: CapacityPoolObservation;
  denied: boolean;
  onNavigate: (path: string) => void;
  onOpenResource: (resource: SelectedResource) => void;
}) {
  if (denied) return <DeniedBadge />;
  const workloads = pool.workloads;
  if (!coverageHasObservations(pool.coverage.workloads) || !workloads)
    return (
      <p className="text-sm text-theme-text-secondary">
        {coverageMessage(pool.coverage.workloads, "Workload attribution")}
      </p>
    );
  const poolName = pool.resource.ref.name;
  return (
    <div className="space-y-2">
      {workloads.topScheduled.length === 0 ? (
        <p className="text-sm text-theme-text-secondary">
          No workloads were attributed to this pool.
        </p>
      ) : (
        workloads.topScheduled.map((workload) => (
          <button
            key={identityKey({ ref: workload.owner })}
            type="button"
            className="flex w-full items-baseline justify-between gap-2 border-b border-theme-border-subtle py-1.5 text-left last:border-b-0 hover:text-accent-text"
            onClick={() =>
              onOpenResource(refToSelectedResource(workload.owner))
            }
          >
            <span className="min-w-0 flex-1 truncate font-mono text-xs text-theme-text-primary">
              {workload.owner.kind}/{workload.owner.name}
            </span>
            <span className="shrink-0 text-xs text-theme-text-tertiary">
              {workload.owner.namespace}
            </span>
            <span className="shrink-0 font-mono text-xs">
              {workload.podCount} pods
            </span>
            <span className="shrink-0">
              <QuantityInline observation={workload.requests} empty="—" />
            </span>
          </button>
        ))
      )}
      {workloads.workloadCount > workloads.topScheduled.length && (
        <LinkButton
          onClick={() =>
            onNavigate(
              `/capacity/pools/${encodeURIComponent(poolName)}/workloads`,
            )
          }
        >
          All {workloads.workloadCount} workloads →
        </LinkButton>
      )}
      <div className="mt-2 border-t border-theme-border-subtle pt-2">
        <div className="mb-0.5 text-xs text-theme-text-tertiary">
          Pending demand
        </div>
        {workloads.pendingEligibleGroupCount > 0 ? (
          <>
            <div className="text-xs text-theme-text-secondary">
              {workloads.pendingEligibleGroupCount}{" "}
              {workloads.pendingEligibleGroupCount === 1
                ? "group awaiting capacity is"
                : "groups awaiting capacity are"}{" "}
              declared eligible for this pool.
            </div>
            <LinkButton
              className="mt-0.5"
              onClick={() =>
                onNavigate(
                  `/capacity/demand?pool=${encodeURIComponent(poolName)}`,
                )
              }
            >
              Evaluate in Demand →
            </LinkButton>
          </>
        ) : (
          <div className="text-xs text-theme-text-secondary">
            No pending pods are declared eligible for this pool.
          </div>
        )}
      </div>
    </div>
  );
}

// A NodePool-not-ready row whose stated reason is the NodeClass issue shown
// beside it adds nothing: the header chip already carries the state, and the
// root cause's Inspect link is exactly its "Next" step — the cascade collapses
// to its root. A standalone pool-unready (any NodeClass-independent reason)
// keeps its row; this narrows to the one derivable echo.
function isNodeClassCascadeEcho(
  issue: { reason: string; message?: string },
  all: { reason: string }[],
): boolean {
  return (
    issue.reason === "NodePoolNotReady" &&
    (issue.message ?? "").includes("NodeClassReady") &&
    all.some((other) => other.reason === "NodeClassNotReady")
  );
}

// Issue reasons whose durable evidence lives in the pool's provision episodes
// (failed and ended launches). Their cards deep-link into Activity pre-filtered
// — the whole diagnosis journey stays one click wide.
const PROVISION_EPISODE_REASONS = new Set([
  "NodePoolNodeRegistrationUnhealthy",
  "NodeClaimProvisioningFailed",
]);

function PostureView({
  pool,
  onOpenResource,
  onNavigate,
  onOpenMembers,
}: {
  pool: CapacityPoolObservation;
  onOpenResource: (resource: SelectedResource) => void;
  onNavigate: (path: string) => void;
  onOpenMembers: () => void;
}) {
  const visibleIssues = pool.issues.filter(
    (issue) => !isNodeClassCascadeEcho(issue, pool.issues),
  );
  // Raw conditions are the receipts. While issues cover them they collapse
  // behind a toggle (the Activity provenance pattern); on a healthy pool they
  // ARE the content and stay visible.
  const conditionsCollapsible = visibleIssues.length > 0;
  const [showConditions, setShowConditions] = useState(false);
  const hasSignals =
    pool.facts.length > 0 ||
    visibleIssues.length > 0 ||
    pool.conditions.length > 0;
  if (!hasSignals)
    return (
      <p className="text-sm text-theme-text-secondary">
        No capacity-specific posture facts or controller conditions were
        reported.
      </p>
    );
  return (
    <div className="space-y-4">
      {(pool.facts.length > 0 || visibleIssues.length > 0) && (
        <div className="space-y-1.5">
          {visibleIssues.map((issue) => {
            const subject = {
              group: issue.group,
              kind: issue.kind,
              namespace: issue.namespace,
              name: issue.name,
            };
            const subjectIsPool =
              subject.group === pool.resource.ref.group &&
              subject.kind === pool.resource.ref.kind &&
              (subject.namespace ?? "") ===
                (pool.resource.ref.namespace ?? "") &&
              subject.name === pool.resource.ref.name;
            return (
              <div key={issue.id} className="flex items-start gap-2 text-sm">
                <Badge
                  severity={issue.severity === "critical" ? "error" : "warning"}
                  size="sm"
                >
                  {issue.severity}
                </Badge>
                <div className="text-theme-text-secondary">
                  <span className="font-medium text-theme-text-primary">
                    {humanizeCode(issue.reason)}
                  </span>
                  {issue.message ? ` — ${issue.message}` : ""}
                  {!subjectIsPool && (
                    <div className="mt-1">
                      <LinkButton
                        onClick={() =>
                          onOpenResource(refToSelectedResource(subject))
                        }
                      >
                        Inspect {subject.kind}/{subject.name} →
                      </LinkButton>
                    </div>
                  )}
                  {PROVISION_EPISODE_REASONS.has(issue.reason) && (
                    <div className="mt-1">
                      <LinkButton
                        onClick={() =>
                          onNavigate(
                            `/capacity/activity?pool=${encodeURIComponent(pool.resource.ref.name)}&type=provision`,
                          )
                        }
                      >
                        View provisioning episodes →
                      </LinkButton>
                    </div>
                  )}
                  {issue.cause && (
                    <p className="mt-1 text-xs">
                      <span className="font-medium text-theme-text-primary">
                        Cause:
                      </span>{" "}
                      {issue.cause}
                    </p>
                  )}
                  {issue.action && (
                    <p className="mt-1 text-xs">
                      <span className="font-medium text-theme-text-primary">
                        Next:
                      </span>{" "}
                      {issue.action}
                    </p>
                  )}
                  <CapacityIssueEvidence
                    issue={issue}
                    onOpenResource={onOpenResource}
                    onOpenMembers={onOpenMembers}
                  />
                </div>
              </div>
            );
          })}
          {pool.facts.map((fact) => (
            <div
              key={`${fact.code}-${fact.summary}`}
              className="flex items-start gap-2 text-sm"
            >
              <span className="mt-0.5 text-theme-text-tertiary">·</span>
              <span className="text-theme-text-secondary">
                <span className="font-medium text-theme-text-primary">
                  {fact.summary}
                </span>
                {fact.detail ? ` — ${fact.detail}` : ""}
              </span>
            </div>
          ))}
        </div>
      )}
      {pool.conditions.length > 0 && (
        <div>
          {conditionsCollapsible ? (
            <LinkButton
              className="mb-1.5 text-xs"
              onClick={() => setShowConditions((current) => !current)}
            >
              {showConditions ? "Hide" : "Show"} controller conditions (
              {pool.conditions.length})
            </LinkButton>
          ) : (
            <div className="mb-1.5 text-xs font-medium text-theme-text-tertiary">
              Controller conditions
            </div>
          )}
          {(!conditionsCollapsible || showConditions) && (
            <div className="space-y-1.5">
              {pool.conditions.map((condition) => (
                <div
                  key={condition.type}
                  className="flex flex-wrap items-baseline gap-2 text-xs"
                >
                  <ConditionBadge status={condition.status} />
                  <span className="font-medium text-theme-text-primary">
                    {condition.type}
                  </span>
                  {condition.reason && (
                    <span className="text-theme-text-secondary">
                      {condition.reason}
                    </span>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- members

function MemberPage({
  name,
  type,
  title,
  empty,
  minWidth,
  head,
  renderRow,
}: {
  name: string;
  type: CapacityMemberType;
  title: string;
  empty: string;
  minWidth: number;
  head: ReactNode;
  renderRow: (
    item: CapacityPoolMember,
    coverage: CapacityMemberListResponse["coverage"],
  ) => ReactNode;
}) {
  const pagination = useCapacityPagination<CapacityMemberListResponse>(
    `${name}\u0000${type}`,
  );
  const query = useCapacityPoolMembers(name, type, {
    limit: 50,
    cursor: pagination.cursor,
  });
  const recoveringCursor = useCapacityCursorRecovery(
    query.error,
    pagination.cursor,
    pagination.recover,
  );
  const recoveredCursor = pagination.recovered || recoveringCursor;
  const response =
    query.data ?? (recoveredCursor ? pagination.retainedPage : undefined);
  const sourceCoverage = response?.coverage[memberCoverageSource(type)];

  const lowerBound =
    response &&
    coverageIsLowerBound(sourceCoverage) &&
    response.items.length > 0;

  return (
    <SectionCard
      title={title}
      subtitle={
        // "Page 1" is noise unless pagination is actually happening.
        response && (pagination.history.length > 0 || response.page.hasMore)
          ? `Page ${pagination.history.length + 1}`
          : undefined
      }
    >
      {recoveredCursor && (
        <div className="border-b border-theme-border-subtle px-4 py-2">
          <Notice>Capacity data changed; showing the latest results.</Notice>
        </div>
      )}
      {query.error &&
        response &&
        !isCapacityCursorInvalidError(query.error) && (
          <div className="border-b border-theme-border-subtle px-4 py-2">
            <RefreshError message={errorMessage(query.error)} />
          </div>
        )}
      {lowerBound && (
        <div className="border-b border-theme-border-subtle px-4 py-2">
          <Notice>{coverageMessage(sourceCoverage, title)}.</Notice>
        </div>
      )}
      {!response && query.isLoading && (
        <div className="px-4 py-6 text-sm text-theme-text-tertiary">
          Loading {title}…
        </div>
      )}
      {!response && query.error && (
        <InlineEmpty
          title={`${title} unavailable`}
          detail={errorMessage(query.error)}
        />
      )}
      {response && response.state === "syncing" && (
        <InlineEmpty
          title="Inventory is syncing"
          detail="Counts below will fill in as the watch catches up."
        />
      )}
      {response && response.state === "denied" && (
        <InlineEmpty
          title="Inventory denied"
          detail={coverageMessage(sourceCoverage, title)}
        />
      )}
      {response &&
        (response.state === "available" || response.state === "not_detected") &&
        (response.items.length === 0 ? (
          <InlineEmpty
            title={
              coverageHasObservations(sourceCoverage)
                ? `No ${title}`
                : `${title} unavailable`
            }
            detail={
              coverageHasObservations(sourceCoverage)
                ? empty
                : coverageMessage(sourceCoverage, title)
            }
          />
        ) : (
          <>
            <div className={TABLE_WRAP}>
              <table
                className="w-full text-left"
                style={{ minWidth: `${minWidth}px` }}
              >
                <thead className={TABLE_HEAD}>{head}</thead>
                <tbody className={TBODY}>
                  {response.items.map((item) =>
                    renderRow(item, response.coverage),
                  )}
                </tbody>
              </table>
            </div>
            {(pagination.history.length > 0 || response.page.hasMore) && (
              <div className="flex items-center justify-end gap-2 border-t border-theme-border-subtle px-4 py-2.5">
                <button
                  type="button"
                  className="rounded-lg border border-theme-border px-3 py-1.5 text-xs font-medium text-theme-text-secondary hover:bg-theme-hover disabled:opacity-50"
                  disabled={pagination.history.length === 0 || query.isFetching}
                  onClick={() => pagination.goBack(response)}
                >
                  Previous
                </button>
                <button
                  type="button"
                  className="rounded-lg border border-theme-border px-3 py-1.5 text-xs font-medium text-theme-text-secondary hover:bg-theme-hover disabled:opacity-50"
                  disabled={
                    !response.page.hasMore ||
                    !response.page.nextCursor ||
                    query.isFetching
                  }
                  onClick={() =>
                    response.page.nextCursor &&
                    pagination.goNext(response.page.nextCursor, response)
                  }
                >
                  Next
                </button>
              </div>
            )}
          </>
        ))}
    </SectionCard>
  );
}

function PoolWorkloadsTab({
  name,
  onOpenResource,
}: {
  name: string;
  onOpenResource: (resource: SelectedResource) => void;
}) {
  return (
    <MemberPage
      name={name}
      type="workload"
      title="Workloads on this pool"
      empty="No workloads were observed on this NodePool."
      minWidth={860}
      head={
        <tr>
          <th className={TH}>Kind</th>
          <th className={TH}>Workload</th>
          <th className={TH}>Namespace</th>
          <th className={TH}>Pods</th>
          <th className={TH}>Aggregate requests</th>
          <th className={TH}>
            <WithTooltip tip="Distinct nodes this workload's scheduled pods run on">
              <span>Nodes used</span>
            </WithTooltip>
          </th>
        </tr>
      }
      renderRow={(item) => {
        const workload = item.workload;
        if (!workload) return null;
        const owner = workload.owner;
        return (
          <tr key={identityKey(item.resource)} className={ROW_HOVER}>
            <td className={TD}>
              {owner.kind ? (
                <Badge tone="structural" size="sm">
                  {owner.kind}
                </Badge>
              ) : (
                <Badge severity="neutral" size="sm">
                  Unowned
                </Badge>
              )}
            </td>
            <td className={`${TD} max-w-[320px]`}>
              <WithTooltip tip={owner.name}>
                <button
                  type="button"
                  className="block max-w-full truncate font-mono text-xs font-medium text-accent-text hover:underline"
                  onClick={() => onOpenResource(refToSelectedResource(owner))}
                >
                  {owner.name}
                </button>
              </WithTooltip>
            </td>
            <td className={`${TD} font-mono text-xs`}>
              {owner.namespace ?? item.resource.ref.namespace ?? "—"}
            </td>
            <td className={`${TD} font-mono`}>{workload.podCount}</td>
            <td className={`${TD} whitespace-nowrap`}>
              <QuantityInline observation={workload.requests} empty="—" />
            </td>
            <td className={`${TD} font-mono`}>
              <WithTooltip tip="Count of distinct nodes running this workload's scheduled pods">
                <span>{workload.nodesMeta.total}</span>
              </WithTooltip>
            </td>
          </tr>
        );
      }}
    />
  );
}

function PoolMembersTab({
  name,
  onOpenResource,
}: {
  name: string;
  onOpenResource: (resource: SelectedResource) => void;
}) {
  return (
    <div className="space-y-4">
      <MemberPage
        name={name}
        type="node"
        title="Nodes"
        empty="No nodes — this pool may be scaled to zero. For a dynamic pool this is a valid state."
        minWidth={1240}
        head={
          <tr>
            <th className={TH}>Node</th>
            <th className={TH}>Readiness</th>
            <th className={TH}>Instance</th>
            <th className={TH}>Type</th>
            <th className={TH}>Zone</th>
            <th className={TH}>Arch</th>
            <th className={TH}>Allocatable</th>
            <th className={TH}>Requests</th>
            <th className={TH}>Usage</th>
            <th className={TH}>
              <WithTooltip tip="Shown only when Pods were actually observed">
                <span>Pods</span>
              </WithTooltip>
            </th>
          </tr>
        }
        renderRow={(item, coverage) => {
          const node = item.node;
          if (!node) return null;
          const podsDeniedNode = coverageIsDenied(coverage.pods);
          const podsLowerBound = coverageIsLowerBound(coverage.pods);
          return (
            <tr key={identityKey(item.resource)} className={ROW_HOVER}>
              <td className={`${TD} max-w-[220px]`}>
                <ResourceLink
                  identity={item.resource}
                  className="block max-w-full truncate font-mono text-xs font-medium text-accent-text hover:underline"
                  onOpenResource={onOpenResource}
                />
              </td>
              <td className={TD}>
                <NodeReadyBadge ready={node.ready} cordoned={node.cordoned} />
              </td>
              <td className={`${TD} font-mono text-xs`}>
                {node.instanceType ?? "—"}
              </td>
              <td className={`${TD} font-mono text-xs`}>
                {node.capacityType ?? "—"}
              </td>
              <td className={`${TD} font-mono text-xs`}>{node.zone ?? "—"}</td>
              <td className={`${TD} font-mono text-xs`}>
                {node.architecture ?? "—"}
              </td>
              <td className={`${TD} whitespace-nowrap`}>
                <QuantityInline observation={node.allocatable} empty="—" />
              </td>
              <td className={`${TD} whitespace-nowrap`}>
                {podsDeniedNode ? (
                  <WithTooltip tip="Pod access denied — not zero">
                    <span className="text-theme-text-tertiary">
                      Unavailable
                    </span>
                  </WithTooltip>
                ) : (
                  <QuantityInline
                    observation={node.scheduledRequests}
                    empty="—"
                  />
                )}
              </td>
              <td className={`${TD} whitespace-nowrap`}>
                <ActualUsageInline
                  usage={node.actualUsage}
                  coverage={coverage.nodeMetrics}
                />
              </td>
              <td className={`${TD} font-mono`}>
                <WithTooltip
                  tip={
                    podsDeniedNode
                      ? "Pod access denied — the count is unknown, not zero"
                      : "Observed via pod list"
                  }
                >
                  <span>
                    {podsDeniedNode
                      ? "Unknown"
                      : node.podCount === undefined
                        ? "—"
                        : `${podsLowerBound ? "≥ " : ""}${node.podCount}`}
                  </span>
                </WithTooltip>
              </td>
            </tr>
          );
        }}
      />
      <MemberPage
        name={name}
        type="claim"
        title="NodeClaims"
        empty="No claims. For a dynamic pool at zero this is a valid state."
        minWidth={1080}
        head={
          <tr>
            <th className={TH}>Claim</th>
            <th className={TH}>Lifecycle</th>
            <th className={TH}>
              <WithTooltip tip="The controller-reported node name is preserved even when the Node object is not readable">
                <span>Node</span>
              </WithTooltip>
            </th>
            <th className={TH}>Status capacity</th>
            <th className={TH}>Conditions</th>
          </tr>
        }
        renderRow={(item, coverage) => {
          const claim = item.claim;
          if (!claim) return null;
          return (
            <tr key={identityKey(item.resource)} className={ROW_HOVER}>
              <td className={TD}>
                <ResourceLink
                  identity={item.resource}
                  onOpenResource={onOpenResource}
                />
              </td>
              <td className={TD}>
                <ClaimStageBadge stage={claim.stage} />
              </td>
              <td className={`${TD} max-w-[220px]`}>
                {claim.node ? (
                  <ResourceLink
                    identity={claim.node}
                    className="block max-w-full truncate font-mono text-xs font-medium text-accent-text hover:underline"
                    onOpenResource={onOpenResource}
                  />
                ) : claim.nodeName ? (
                  <WithTooltip
                    tip={coverageMessage(coverage.nodes, "Node details")}
                  >
                    <span className="font-mono text-xs text-theme-text-secondary">
                      {claim.nodeName}
                    </span>
                  </WithTooltip>
                ) : (
                  <span className="text-theme-text-tertiary">—</span>
                )}
              </td>
              <td className={`${TD} whitespace-nowrap`}>
                <QuantityInline observation={claim.capacity} empty="—" />
              </td>
              <td className={`${TD} text-xs text-theme-text-secondary`}>
                {conditionSummary(claim.conditions)}
              </td>
            </tr>
          );
        }}
      />
    </div>
  );
}

// ---------------------------------------------------------------- configuration

function PoolConfigurationTab({
  pool,
  onOpenResource,
}: {
  pool: CapacityPoolObservation;
  onOpenResource: (resource: SelectedResource) => void;
}) {
  const config = pool.configuration;
  const disruption = pool.disruption;
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <SectionCard title="Effective configuration" bodyClassName="px-4 py-4">
        <KeyValueRows
          rows={[
            ["Mode", pool.mode],
            ["Weight", config.weight?.toString() ?? "—"],
            [
              "Static replicas",
              config.replicas !== undefined
                ? config.replicas.toString()
                : "— (dynamic pool)",
            ],
            ["Expiry", config.expireAfter ?? "—"],
            ["Termination grace", config.terminationGracePeriod ?? "—"],
          ]}
        />
        {pool.nodeClass && (
          <div className="mt-3 border-t border-theme-border-subtle pt-3">
            <div className="mb-1 text-xs font-medium text-theme-text-tertiary">
              NodeClass
            </div>
            <div className="flex items-center gap-2">
              <LinkButton
                className="font-mono text-xs font-medium text-accent-text hover:underline"
                onClick={() =>
                  onOpenResource(
                    refToSelectedResource(pool.nodeClass!.reference),
                  )
                }
              >
                {pool.nodeClass.reference.kind}/{pool.nodeClass.reference.name}
              </LinkButton>
              <PoolReadyBadge ready={pool.nodeClass.ready} />
              {pool.nodeClass.ready === undefined && (
                <span className="text-xs text-theme-text-tertiary">
                  {coverageMessage(
                    pool.coverage.nodeClasses,
                    "NodeClass readiness",
                  )}
                </span>
              )}
            </div>
          </div>
        )}
      </SectionCard>

      <SectionCard title="Requirements" bodyClassName="px-0 py-0">
        {config.requirements.length === 0 ? (
          <InlineEmpty
            title="No requirements"
            detail="This NodePool declares no scheduling requirements."
          />
        ) : (
          <div className={TABLE_WRAP}>
            <table className="w-full min-w-[520px] text-left">
              <thead className={TABLE_HEAD}>
                <tr>
                  <th className={TH}>Key</th>
                  <th className={TH}>Op</th>
                  <th className={TH}>Values</th>
                  <th className={TH}>minValues</th>
                </tr>
              </thead>
              <tbody className={TBODY}>
                {config.requirements.map((requirement) => (
                  <tr key={`${requirement.key}-${requirement.operator}`}>
                    <td className={`${TD} font-mono text-xs`}>
                      {requirement.key}
                    </td>
                    <td className={`${TD} text-xs`}>{requirement.operator}</td>
                    <td className={`${TD} font-mono text-xs`}>
                      {requirement.values.join(", ") || "—"}
                    </td>
                    <td className={`${TD} font-mono text-xs`}>
                      {requirement.minValues ?? "—"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </SectionCard>

      <SectionCard title="Disruption" bodyClassName="px-4 py-4">
        <KeyValueRows
          rows={[
            ["Consolidation policy", disruption.consolidationPolicy ?? "—"],
            ["Consolidate after", disruption.consolidateAfter ?? "—"],
          ]}
        />
        {disruption.budgets.length > 0 && (
          <div className="mt-3 border-t border-theme-border-subtle pt-3">
            <div className="mb-1.5 text-xs font-medium text-theme-text-tertiary">
              Budgets
            </div>
            <div className="space-y-1">
              {disruption.budgets.map((budget, index) => (
                <div
                  key={index}
                  className="font-mono text-xs text-theme-text-secondary"
                >
                  {budget.nodes}
                  {budget.reasons.length > 0
                    ? ` · ${budget.reasons.join(", ")}`
                    : ""}
                  {budget.schedule ? ` · ${budget.schedule}` : ""}
                  {budget.duration ? ` · ${budget.duration}` : ""}
                </div>
              ))}
            </div>
          </div>
        )}
      </SectionCard>

      <SectionCard title="Labels & taints" bodyClassName="space-y-3 px-4 py-4">
        <TokenGroup
          title="Labels"
          values={Object.entries(config.labels).map(
            ([key, value]) => `${key}=${value}`,
          )}
          empty="None"
        />
        <TokenGroup
          title="Taints"
          values={config.taints.map(formatTaint)}
          empty="None"
        />
        <TokenGroup
          title="Startup taints"
          values={config.startupTaints.map(formatTaint)}
          empty="None"
        />
      </SectionCard>
    </div>
  );
}
