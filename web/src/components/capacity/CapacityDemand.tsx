import { useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { AlertTriangle } from "lucide-react";
import {
  Collapse,
  CollapseChevron,
  WithTooltip,
  type CapacityDemandGroup,
  type CapacityDemandResponse,
  type CapacityDemandState,
  type CapacityEvaluationEvidence,
  type CapacityPoolEvaluation,
  type CapacitySchedulingSignature,
} from "@skyhook-io/k8s-ui";
import { Badge } from "@skyhook-io/k8s-ui/components/ui/Badge";
import { FilterPill } from "@skyhook-io/k8s-ui";
import {
  isCapacityCursorInvalidError,
  isNotFoundError,
  useCapacityDemand,
} from "../../api/client";
import type { SelectedResource } from "../../types";
import { refToSelectedResource } from "../../utils/navigation";
import {
  CapacityFreshness,
  CapacityIssueEvidence,
  DemandStateBadge,
  EmptyState,
  InlineEmpty,
  LinkButton,
  Notice,
  PageControls,
  PoolEvaluationBadge,
  PoolSelector,
  QuantityInline,
  ResourceLink,
  ROW_HOVER,
  ScopeBadges,
  ScrollableContent,
  TABLE_HEAD,
  TABLE_WRAP,
  TBODY,
  TD,
  TH,
  coverageHasObservations,
  coverageIsLowerBound,
  coverageMessage,
  demandStateLabel,
  errorMessage,
  formatTimestamp,
  humanizeCode,
  identityKey,
  integrationBlock,
  namespaceCoverageDescription,
  quantityText,
  useCapacityCursorRecovery,
  useCapacityPagination,
} from "./shared";

const STATE_PILLS: [CapacityDemandState | undefined, string][] = [
  [undefined, "All states"],
  ["waiting_for_scheduler", "Waiting for scheduler"],
  ["held", "Held by scheduling gate"],
  ["awaiting_capacity", "Awaiting capacity"],
  ["blocked", "Blocked"],
  ["unknown", "Unknown"],
];

export function updateDemandSearchParam(
  search: string,
  key: "pool" | "state" | "owner" | "pod",
  value: string | undefined,
): string {
  const params = new URLSearchParams(search);
  if (value) params.set(key, value);
  else params.delete(key);
  const next = params.toString();
  return next ? `?${next}` : "";
}

// demandGroupVerdict synthesizes the one-line answer for the archetypal case:
// every evaluated pool ruled the group out. The dominant leading reason across
// incompatible pools headlines; the predicate tables below stay the receipts.
// Anything short of all-pools-incompatible keeps the neutral counts line —
// declared compatibility is not a scheduling guarantee, and a synthesized
// verdict must not overclaim past what the evidence supports.
export function demandGroupVerdict(group: CapacityDemandGroup): {
  explanation: string;
  predicateCount: number;
  poolCount: number;
} | null {
  const counts = group.poolEvaluationCounts;
  if (counts.declaredCompatible > 0 || counts.incompatible === 0) return null;
  const leading = group.poolEvaluations
    .filter((e) => e.result === "incompatible" && e.evidence.length > 0)
    .map((e) => e.evidence[0]);
  if (leading.length === 0) return null;
  const byPredicate = new Map<string, { count: number; explanation: string }>();
  for (const item of leading) {
    const entry = byPredicate.get(item.predicate);
    if (entry) entry.count += 1;
    else byPredicate.set(item.predicate, { count: 1, explanation: item.explanation });
  }
  let best: { count: number; explanation: string } | undefined;
  for (const entry of byPredicate.values()) {
    if (!best || entry.count > best.count) best = entry;
  }
  if (!best?.explanation) return null;
  return {
    explanation: best.explanation,
    predicateCount: best.count,
    poolCount: counts.incompatible,
  };
}

export function CapacityDemand({
  connectionState,
  onOpenPool,
  onOpenResource,
  onNavigate,
}: {
  connectionState: "connected" | "disconnected" | "connecting";
  onOpenPool: (name: string) => void;
  onOpenResource: (resource: SelectedResource) => void;
  onNavigate: (path: string) => void;
}) {
  const location = useLocation();
  const navigate = useNavigate();
  const search = new URLSearchParams(location.search);
  const stateValue = search.get("state");
  const stateFilter =
    stateValue &&
    [
      "waiting_for_scheduler",
      "held",
      "awaiting_capacity",
      "blocked",
      "unknown",
    ].includes(stateValue)
      ? (stateValue as CapacityDemandState)
      : undefined;
  const poolFilter = search.get("pool") || undefined;
  const ownerFilter = search.get("owner") || undefined;
  const ownerFilterName = ownerFilter?.split("/").slice(1).join(" ");
  const podFilter = search.get("pod") || undefined;
  const podFilterLabel = podFilter
    ? `pod ${podFilter.split("/")[1] ?? podFilter}'s workload`
    : undefined;
  const subjectFilterLabel = ownerFilterName ?? podFilterLabel;
  const subjectScoped = Boolean(ownerFilter || podFilter);
  const pagination = useCapacityPagination<CapacityDemandResponse>(
    `${location.search}`,
  );
  const query = useCapacityDemand({
    limit: 25,
    cursor: pagination.cursor,
    state: stateFilter,
    pool: poolFilter,
    owner: ownerFilter,
    pod: podFilter,
  });
  const recoveringCursor = useCapacityCursorRecovery(
    query.error,
    pagination.cursor,
    pagination.recover,
  );
  const recoveredCursor = pagination.recovered || recoveringCursor;
  const responseData =
    query.data ?? (recoveredCursor ? pagination.retainedPage : undefined);
  const updateSearchParam = (
    key: "pool" | "state" | "owner" | "pod",
    value: string | undefined,
  ) => {
    navigate(
      {
        pathname: location.pathname,
        search: updateDemandSearchParam(location.search, key, value),
      },
      { replace: true },
    );
  };
  const clearPoolFilter = () => updateSearchParam("pool", undefined);
  const clearOwnerFilter = () => updateSearchParam("owner", undefined);
  const clearPodFilter = () => updateSearchParam("pod", undefined);
  if (poolFilter && !responseData && isNotFoundError(query.error)) {
    return (
      <EmptyState
        icon={AlertTriangle}
        title="NodePool not found"
        detail={`This link evaluates demand against NodePool “${poolFilter}”, which no longer exists. It may have been removed or belongs to another cluster context.`}
        action={
          <button
            type="button"
            className="btn-brand mt-4 rounded-lg px-3 py-1.5 text-sm"
            onClick={clearPoolFilter}
          >
            Show all pending demand
          </button>
        }
      />
    );
  }
  const blocked = integrationBlock(
    responseData,
    query.error,
    query.isLoading,
    "Loading pending capacity demand…",
  );
  if (blocked) return blocked;
  const response = responseData as CapacityDemandResponse;

  const changeFilter = (next: CapacityDemandState | undefined) =>
    updateSearchParam("state", next);

  // Page-local counts (numerator of "showing X of N").
  const pagePods = response.items.reduce(
    (total, group) => total + group.podCount,
    0,
  );
  const pageGroups = response.items.length;
  // Authoritative denominator from the server rollup — the full observed
  // population, computed after RBAC/namespace scoping + Karpenter classification
  // but BEFORE the state filter and pagination. Never the current page. Absent
  // while Pod data isn't observed yet — then we show no total rather than a
  // misleading zero.
  const rollup = response.summary;
  // Namespace-scoped pod coverage hides whole groups, so every rollup count is
  // a floor — the pills must say so rather than read as totals.
  const countsAreLowerBound = coverageIsLowerBound(response.coverage.pods);
  const denom = rollup
    ? stateFilter
      ? rollup.byState[stateFilter]
      : rollup.total
    : undefined;
  const noun = stateFilter
    ? `${demandStateLabel(stateFilter).toLowerCase()} pods`
    : "pending pods";
  const nounFor = (count: number) =>
    count === 1 ? noun.replace(/pods$/, "pod") : noun;
  // Under an owner filter the rollup denominator describes ALL observed
  // demand, not this workload — "showing X of Y" would imply Y pods exist for
  // this owner. Owner-scoped views get honest page-local counts instead.
  const headerSummary = subjectScoped
    ? pagePods > 0 || pageGroups > 0
      ? `${pagePods}${response.page.hasMore ? "+" : ""} ${nounFor(pagePods)} in ${pageGroups}${response.page.hasMore ? "+" : ""} ${pageGroups === 1 ? "group" : "groups"} for this workload`
      : null
    : denom
      ? denom.podCount === pagePods && denom.groupCount === pageGroups
        ? `${denom.podCount} ${nounFor(denom.podCount)} in ${denom.groupCount} ${denom.groupCount === 1 ? "group" : "groups"}`
        : `showing ${pagePods} of ${denom.podCount} ${nounFor(denom.podCount)} in ${pageGroups} of ${denom.groupCount} groups`
      : null;

  return (
    <ScrollableContent>
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="flex flex-wrap items-baseline gap-3">
            <LinkButton onClick={() => onNavigate("/capacity")}>
              ← Capacity
            </LinkButton>
            <h1 className="text-lg font-semibold text-theme-text-primary">
              Demand
            </h1>
            <span className="text-xs text-theme-text-tertiary">
              {headerSummary
                ? `Pending pods grouped by scheduling signature · ${headerSummary}`
                : "Pending pods grouped by scheduling signature"}
            </span>
          </div>
          <div className="mt-2">
            <ScopeBadges coverage={response.coverage} />
          </div>
          <p className="mt-1.5 text-xs text-theme-text-tertiary">
            Evaluations cover Karpenter NodePools only — “blocked” means no
            NodePool can take it, not that no node can.
          </p>
        </div>
        <div className="flex items-center gap-3">
          <LinkButton
            onClick={() =>
              onNavigate(
                poolFilter
                  ? `/capacity/activity?pool=${encodeURIComponent(poolFilter)}`
                  : "/capacity/activity",
              )
            }
          >
            Activity timeline →
          </LinkButton>
          <CapacityFreshness
            meta={response}
            query={query}
            connectionState={connectionState}
          />
        </div>
      </div>

      {recoveredCursor && (
        <Notice>Capacity data changed; showing the latest results.</Notice>
      )}
      {query.error && !isCapacityCursorInvalidError(query.error) && (
        <Notice>
          Refresh failed; showing the last successful capacity snapshot.{" "}
          {errorMessage(query.error)}
        </Notice>
      )}
      {coverageIsLowerBound(response.coverage.pods) && (
        <Notice>
          ≥ {coverageMessage(response.coverage.pods, "Pending pod demand")}.
          Groups outside {namespaceCoverageDescription(response.coverage.pods)}{" "}
          are invisible — every count on this page is a lower bound, not a
          total.
        </Notice>
      )}
      {ownerFilter && (
        <Notice>
          Showing pending demand for{" "}
          <span className="font-medium text-theme-text-primary">
            {ownerFilterName}
          </span>{" "}
          only — the state counts below describe all observed demand, not just
          this workload.{" "}
          <LinkButton className="inline" onClick={clearOwnerFilter}>
            Show all demand
          </LinkButton>
        </Notice>
      )}
      {podFilter && (
        <Notice>
          Showing pending demand for{" "}
          <span className="font-medium text-theme-text-primary">
            {podFilterLabel}
          </span>{" "}
          only — the state counts below describe all observed demand, not just
          this workload.{" "}
          <LinkButton className="inline" onClick={clearPodFilter}>
            Show all demand
          </LinkButton>
        </Notice>
      )}
      {poolFilter && (
        <Notice>
          Evaluated against NodePool{" "}
          <span className="font-medium text-theme-text-primary">
            {poolFilter}
          </span>{" "}
          — each group's evaluation shows this pool's perspective; states and
          counts stay fleet-wide.{" "}
          <LinkButton className="inline" onClick={clearPoolFilter}>
            Evaluate against all NodePools
          </LinkButton>
        </Notice>
      )}

      <div className="flex flex-wrap items-end justify-between gap-3">
        <div
          className="flex flex-wrap gap-1.5"
          aria-label="Filter demand by state"
        >
          {STATE_PILLS.map(([state, label]) => {
            // Counts come from the server rollup (stable across the active filter),
            // not the current page. Omitted entirely when summary is absent — a
            // pill never shows a fabricated "· 0".
            const counts = rollup
              ? state === undefined
                ? rollup.total
                : rollup.byState[state]
              : undefined;
            return (
              <FilterPill
                key={state ?? "all"}
                label={
                  counts
                    ? `${label} · ${countsAreLowerBound ? "≥" : ""}${counts.podCount}`
                    : label
                }
                active={stateFilter === state}
                onClick={() => changeFilter(state)}
              />
            );
          })}
        </div>
        <PoolSelector
          key={`${response.clusterContext.contextName}`}
          pool={poolFilter}
          onChange={(pool) => updateSearchParam("pool", pool)}
          label="Evaluate against"
          emptyLabel="All NodePools"
          unavailableLabel="NodePool options unavailable; demand remains available."
        />
      </div>

      {response.items.length > 0 ? (
        <div className="space-y-3">
          {response.items.map((group, index) => (
            <DemandGroupCard
              key={group.id}
              group={group}
              defaultExpanded={index === 0}
              onOpenPool={onOpenPool}
              onOpenResource={onOpenResource}
            />
          ))}
        </div>
      ) : coverageHasObservations(response.coverage.pods) ? (
        subjectScoped ? (
          <InlineEmpty
            title={`No pending demand for ${subjectFilterLabel}`}
            detail="Its pods may have scheduled since this link was created — this is a true zero across all pending demand, not a paging artifact."
          />
        ) : (
          stateFilter ? (
            <InlineEmpty
              title={`No groups in “${demandStateLabel(stateFilter)}”`}
              detail="Pending demand, if any, is in other states — the pills above carry the counts. A true zero, not a paging artifact."
            />
          ) : countsAreLowerBound ? (
            <InlineEmpty
              title="No pending demand observed"
              detail="Nothing is pending in the namespaces you can read — pods outside them are not visible here."
            />
          ) : (
            <InlineEmpty
              title="Nothing is pending"
              detail="Every pod the scheduler knows about is placed — a true zero, not a paging artifact."
            />
          )
        )
      ) : (
        <InlineEmpty
          title="Demand unavailable"
          detail={coverageMessage(response.coverage.pods, "Pending pod demand")}
        />
      )}

      {(pagination.history.length > 0 || response.page.hasMore) && (
        <PageControls
          page={pagination.history.length + 1}
          hasPrevious={pagination.history.length > 0}
          hasNext={response.page.hasMore && Boolean(response.page.nextCursor)}
          busy={query.isFetching}
          onPrevious={() => pagination.goBack(response)}
          onNext={() =>
            response.page.nextCursor &&
            pagination.goNext(response.page.nextCursor, response)
          }
        />
      )}
    </ScrollableContent>
  );
}

/** What actually binds these pods into one group, in words. The fingerprint
 *  hash identifies the group but tells an operator nothing. */
function signatureSummary(signature: CapacitySchedulingSignature): string {
  const parts: string[] = ["identical per-pod requests"];
  const constraints = signature.constraintsMeta.total;
  const tolerations = signature.tolerationsMeta.total;
  if (constraints > 0)
    parts.push(
      `${constraints} ${constraints === 1 ? "selector" : "selectors"}`,
    );
  if (tolerations > 0)
    parts.push(
      `${tolerations} ${tolerations === 1 ? "toleration" : "tolerations"}`,
    );
  if (constraints === 0 && tolerations === 0)
    parts.push("no selectors or tolerations");
  return `Grouped by ${parts.join(" · ")}`;
}

function DemandGroupCard({
  group,
  defaultExpanded,
  onOpenPool,
  onOpenResource,
}: {
  group: CapacityDemandGroup;
  defaultExpanded: boolean;
  onOpenPool: (name: string) => void;
  onOpenResource: (resource: SelectedResource) => void;
}) {
  const [expanded, setExpanded] = useState(defaultExpanded);
  const counts = group.poolEvaluationCounts;
  const compatSummary = `${counts.declaredCompatible} compatible · ${counts.incompatible} incompatible · ${counts.unknown} unknown`;
  const verdict = demandGroupVerdict(group);

  return (
    <article className="overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
      <button
        type="button"
        aria-expanded={expanded}
        className={`flex w-full items-start gap-2 px-4 py-3 text-left ${ROW_HOVER}`}
        onClick={() => setExpanded((current) => !current)}
      >
        <CollapseChevron open={expanded} className="mt-0.5 h-4 w-4" />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="min-w-[90px]">
              <DemandStateBadge state={group.state} />
            </span>
            {group.owner ? (
              <>
                <Badge tone="structural" size="sm">
                  {group.owner.kind}
                </Badge>
                <span
                  role="link"
                  tabIndex={0}
                  className="truncate font-medium text-accent-text hover:underline"
                  onClick={(event) => {
                    event.stopPropagation();
                    onOpenResource(refToSelectedResource(group.owner!));
                  }}
                  onKeyDown={(event) => {
                    if (event.key === "Enter" || event.key === " ") {
                      event.preventDefault();
                      event.stopPropagation();
                      onOpenResource(refToSelectedResource(group.owner!));
                    }
                  }}
                >
                  {group.owner.name}
                </span>
              </>
            ) : (
              <span className="font-medium text-theme-text-primary">
                Pod without a workload owner
              </span>
            )}
            <span className="font-mono text-xs text-theme-text-tertiary">
              ns/{group.namespace}
            </span>
            <Badge tone="structural" size="sm">
              {group.podCount} {group.podCount === 1 ? "pod" : "pods"}
            </Badge>
            {group.nominatedPodCount != null && group.nominatedPodCount > 0 && (
              <WithTooltip
                tip={`${group.nominatedPodCount} ${
                  group.nominatedPodCount === 1 ? "pod holds" : "pods hold"
                } a scheduler node nomination — preemption in progress. Nomination is best-effort and can go stale; it does not change the group's state.`}
              >
                <Badge tone="note" size="sm" className="cursor-help">
                  {`${group.nominatedPodCount} nominated`}
                </Badge>
              </WithTooltip>
            )}
            <span className="font-mono text-xs text-theme-text-secondary">
              {quantityText(group.aggregateRequests) ?? "No requests reported"}
            </span>
          </div>
          <div className="mt-1 flex flex-wrap items-baseline gap-x-2 text-xs text-theme-text-tertiary">
            <span>
              first {formatTimestamp(group.firstSeen)} · last{" "}
              {formatTimestamp(group.lastSeen)}
            </span>
            {group.schedulerReasons.length > 0 && (
              <span className="min-w-0 truncate text-theme-text-secondary">
                e.g. {group.schedulerReasons[0].message}
              </span>
            )}
          </div>
        </div>
      </button>

      <Collapse open={expanded}>
        <div className="border-t border-theme-border">
          <div className="px-4 pt-3 text-xs text-theme-text-tertiary">
            Pools:{" "}
            <span className="text-theme-text-secondary">{compatSummary}</span>{" "}
            <WithTooltip tip="Declared-compatible means the pool's declared requirements, taints and resources do not rule out these pods. It is not a scheduling, capacity or availability guarantee.">
              <span className="cursor-help text-theme-text-tertiary">
                — declared compatibility, not a scheduling guarantee
              </span>
            </WithTooltip>
          </div>

          {verdict && (
            <div className="px-4 pt-2 text-sm">
              <span className="font-medium text-warning-text">
                No pool can take this group
              </span>{" "}
              <span className="text-theme-text-secondary">
                — {verdict.explanation}
                {verdict.poolCount > 1
                  ? ` (${verdict.predicateCount} of ${verdict.poolCount} incompatible pools)`
                  : ""}
              </span>
            </div>
          )}

          {group.issues.length > 0 && (
            <div className="px-4 pt-3">
              <div className="rounded-lg border border-theme-border bg-theme-base/50 px-3 py-3">
                <h3 className="text-xs font-medium uppercase tracking-wide text-theme-text-tertiary">
                  Capacity diagnosis
                </h3>
                <div className="mt-2 space-y-2">
                  {group.issues.map((issue) => (
                    <div
                      key={issue.id}
                      className="flex items-start gap-2 text-xs"
                    >
                      <Badge
                        severity={
                          issue.severity === "critical" ? "error" : "warning"
                        }
                        size="sm"
                      >
                        {issue.severity}
                      </Badge>
                      <div className="min-w-0 text-theme-text-secondary">
                        <p>
                          <span className="font-medium text-theme-text-primary">
                            {humanizeCode(issue.reason)}
                          </span>
                          {issue.message ? ` — ${issue.message}` : ""}
                        </p>
                        {issue.cause && (
                          <p className="mt-1">Cause: {issue.cause}</p>
                        )}
                        {issue.action && (
                          <p className="mt-1">Next: {issue.action}</p>
                        )}
                        <CapacityIssueEvidence
                          issue={issue}
                          onOpenResource={onOpenResource}
                        />
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            </div>
          )}

          <div className="grid gap-x-6 gap-y-4 px-4 py-4 lg:grid-cols-2">
            <div className="space-y-4">
              <div>
                <h3 className="text-xs font-medium uppercase tracking-wide text-theme-text-tertiary">
                  Scheduling signature
                </h3>
                <WithTooltip tip={`Grouping fingerprint: ${group.fingerprint}`}>
                  <div className="mt-1.5 w-fit cursor-help text-[11px] text-theme-text-tertiary">
                    {signatureSummary(group.schedulingSignature)}
                  </div>
                </WithTooltip>
                <div className="mt-2 text-xs font-medium text-theme-text-tertiary">
                  Per pod
                </div>
                <div className="mt-1">
                  <QuantityInline
                    observation={group.perPodRequests}
                    empty="No requests reported"
                  />
                </div>
                <div className="mt-3 text-xs font-medium text-theme-text-tertiary">
                  Selectors
                </div>
                <div className="mt-1 flex flex-wrap gap-1">
                  {group.schedulingSignature.constraints.length > 0 ? (
                    group.schedulingSignature.constraints.map((constraint) => (
                      <Badge
                        key={`${constraint.predicate}-${constraint.sourcePath}`}
                        tone="structural"
                        size="sm"
                      >
                        {constraint.key || constraint.predicate}
                        {constraint.operator ? ` ${constraint.operator}` : ""}
                        {constraint.values.length > 0
                          ? ` ${constraint.values.join(", ")}`
                          : ""}
                      </Badge>
                    ))
                  ) : (
                    <span className="text-sm text-theme-text-secondary">
                      None
                    </span>
                  )}
                </div>
                {group.schedulingSignature.constraintsMeta.truncated && (
                  <p className="mt-1.5 text-[11px] text-theme-text-tertiary">
                    Showing {group.schedulingSignature.constraintsMeta.returned}{" "}
                    of {group.schedulingSignature.constraintsMeta.total}{" "}
                    constraints.
                  </p>
                )}
                <div className="mt-3 text-xs font-medium text-theme-text-tertiary">
                  Tolerations
                </div>
                <div className="mt-1 flex flex-wrap gap-1">
                  {group.schedulingSignature.tolerations.length > 0 ? (
                    group.schedulingSignature.tolerations.map(
                      (toleration, index) => (
                        <Badge
                          key={`${toleration.key ?? "*"}-${toleration.effect ?? "*"}-${index}`}
                          tone="structural"
                          size="sm"
                        >
                          {toleration.key || "*"}
                          {toleration.value ? `=${toleration.value}` : ""}
                          {toleration.effect ? ` · ${toleration.effect}` : ""}
                        </Badge>
                      ),
                    )
                  ) : (
                    <span className="text-sm text-theme-text-secondary">
                      None
                    </span>
                  )}
                </div>
                {group.schedulingSignature.tolerationsMeta.truncated && (
                  <p className="mt-1.5 text-[11px] text-theme-text-tertiary">
                    Showing {group.schedulingSignature.tolerationsMeta.returned}{" "}
                    of {group.schedulingSignature.tolerationsMeta.total}{" "}
                    tolerations.
                  </p>
                )}
              </div>
            </div>

            <div className="space-y-4">
              <div>
                <h3 className="text-xs font-medium uppercase tracking-wide text-theme-text-tertiary">
                  Evidence
                </h3>
                {group.schedulerReasons.length > 0 ? (
                  <div className="mt-2 space-y-2">
                    {group.schedulerReasons.map((reason) => (
                      <div
                        key={`${reason.source}-${reason.code}`}
                        className="rounded-lg border border-theme-border bg-theme-base/50 px-3 py-2"
                      >
                        <div className="flex items-center justify-between gap-2">
                          <span className="font-mono text-xs font-medium text-theme-text-primary">
                            {reason.code}
                          </span>
                          <Badge tone="structural" size="sm">
                            {reason.count}{" "}
                            {reason.count === 1 ? "event" : "events"}
                          </Badge>
                        </div>
                        {reason.message && (
                          <p className="mt-1 text-xs text-theme-text-secondary">
                            {reason.message}
                          </p>
                        )}
                        <p className="mt-1 text-[11px] text-theme-text-tertiary">
                          {reason.source} · latest{" "}
                          {formatTimestamp(reason.lastSeen)}
                        </p>
                      </div>
                    ))}
                  </div>
                ) : (
                  <p className="mt-2 text-sm text-theme-text-secondary">
                    No scheduler reason was observed for this group.
                  </p>
                )}
                {group.schedulerReasonsMeta.truncated && (
                  <p className="mt-1.5 text-[11px] text-theme-text-tertiary">
                    Showing {group.schedulerReasonsMeta.returned} of{" "}
                    {group.schedulerReasonsMeta.total} scheduler reasons.
                  </p>
                )}
              </div>

              {(group.pods.length > 0 || group.podsMeta.truncated) && (
                <div>
                  <h3 className="text-xs font-medium uppercase tracking-wide text-theme-text-tertiary">
                    Pods
                  </h3>
                  <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1">
                    {group.pods.map((pod) => (
                      <ResourceLink
                        key={identityKey(pod)}
                        identity={pod}
                        onOpenResource={onOpenResource}
                      />
                    ))}
                    {group.podsMeta.truncated && (
                      <span className="text-[11px] text-theme-text-tertiary">
                        showing {group.podsMeta.returned} of{" "}
                        {group.podsMeta.total}
                      </span>
                    )}
                  </div>
                </div>
              )}
            </div>
          </div>

          <div className="border-t border-theme-border-subtle px-4 py-4">
            <div className="flex items-center justify-between gap-3">
              <h3 className="text-xs font-medium uppercase tracking-wide text-theme-text-tertiary">
                Pool evaluations
              </h3>
              <span className="text-[11px] text-theme-text-tertiary">
                — declared compatibility, with evidence
              </span>
            </div>
            {group.poolEvaluations.length > 0 ? (
              <div className="mt-2 space-y-2">
                {group.poolEvaluations.map((evaluation) => (
                  <PoolEvaluationCard
                    key={`${evaluation.pool.group ?? ""}/${evaluation.pool.name}`}
                    evaluation={evaluation}
                    onOpenPool={onOpenPool}
                  />
                ))}
                {group.poolEvaluationsMeta.truncated && (
                  <p className="text-[11px] text-theme-text-tertiary">
                    Showing {group.poolEvaluationsMeta.returned} of{" "}
                    {group.poolEvaluationsMeta.total} evaluations.
                  </p>
                )}
              </div>
            ) : (
              <p className="mt-2 text-sm text-theme-text-secondary">
                No NodePools were available for evaluation.
              </p>
            )}
          </div>
        </div>
      </Collapse>
    </article>
  );
}

function PoolEvaluationCard({
  evaluation,
  onOpenPool,
}: {
  evaluation: CapacityPoolEvaluation;
  onOpenPool: (name: string) => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const predicateCount =
    evaluation.evidence.length + evaluation.unknownPredicates.length;
  return (
    <div className="overflow-hidden rounded-lg border border-theme-border bg-theme-base/40">
      <button
        type="button"
        aria-expanded={expanded}
        className={`flex w-full items-center gap-2 px-3 py-2.5 text-left ${ROW_HOVER}`}
        onClick={() => setExpanded((current) => !current)}
      >
        <CollapseChevron open={expanded} className="h-4 w-4" />
        <span
          role="link"
          tabIndex={0}
          className="font-mono text-sm font-medium text-accent-text hover:underline"
          onClick={(event) => {
            event.stopPropagation();
            onOpenPool(evaluation.pool.name);
          }}
          onKeyDown={(event) => {
            if (event.key === "Enter" || event.key === " ") {
              event.preventDefault();
              event.stopPropagation();
              onOpenPool(evaluation.pool.name);
            }
          }}
        >
          {evaluation.pool.name}
        </span>
        <PoolEvaluationBadge result={evaluation.result} />
        <span className="ml-auto text-[11px] text-theme-text-tertiary">
          {predicateCount} {predicateCount === 1 ? "predicate" : "predicates"}
          {evaluation.evidenceMeta.truncated ? " · truncated" : ""}
        </span>
      </button>
      <Collapse open={expanded}>
        <div className="border-t border-theme-border">
          {evaluation.evidence.length > 0 ||
          evaluation.unknownPredicates.length > 0 ? (
            <div className={TABLE_WRAP}>
              <table className="w-full text-left">
                <thead className={TABLE_HEAD}>
                  <tr>
                    <th className={TH}>Predicate</th>
                    <th className={TH}>Observed</th>
                    <th className={TH}>Expected</th>
                    <th className={TH}>Confidence</th>
                    <th className={TH}>Explanation</th>
                    <th className={TH}>Source</th>
                  </tr>
                </thead>
                <tbody className={TBODY}>
                  {evaluation.evidence.map((evidence, index) => (
                    <EvidenceRow
                      key={`${evidence.predicate}-${index}`}
                      evidence={evidence}
                    />
                  ))}
                  {evaluation.unknownPredicates.map((predicate) => (
                    <tr key={`unknown-${predicate}`} className={ROW_HOVER}>
                      <td className={`${TD} font-mono`}>{predicate}</td>
                      <td
                        className={`${TD} font-mono text-theme-text-tertiary`}
                        colSpan={4}
                      >
                        ? unknown — not evaluated
                      </td>
                      <td className={`${TD} text-theme-text-tertiary`}>—</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p className="px-3 py-2.5 text-sm text-theme-text-secondary">
              No incompatibility evidence was recorded.
            </p>
          )}
          {evaluation.evidenceMeta.truncated && (
            <p className="px-3 py-2 text-[11px] text-theme-text-tertiary">
              Showing {evaluation.evidenceMeta.returned} of{" "}
              {evaluation.evidenceMeta.total} incompatibility checks.
            </p>
          )}
          {evaluation.unknownPredicatesMeta.truncated && (
            <p className="px-3 pb-2 text-[11px] text-theme-text-tertiary">
              Showing {evaluation.unknownPredicatesMeta.returned} of{" "}
              {evaluation.unknownPredicatesMeta.total} unknown predicates.
            </p>
          )}
        </div>
      </Collapse>
    </div>
  );
}

function EvidenceRow({ evidence }: { evidence: CapacityEvaluationEvidence }) {
  return (
    <tr className={ROW_HOVER}>
      <td className={`${TD} font-mono`}>{evidence.predicate}</td>
      <td className={`${TD} font-mono text-theme-text-secondary`}>
        {evidence.observedValues.length > 0
          ? evidence.observedValues.join(", ")
          : "—"}
      </td>
      <td className={`${TD} font-mono text-theme-text-secondary`}>
        {evidence.expectedValues.length > 0
          ? evidence.expectedValues.join(", ")
          : "—"}
      </td>
      <td className={TD}>
        <Badge tone="structural" size="sm">
          {evidence.confidence}
        </Badge>
      </td>
      <td className={`${TD} text-theme-text-secondary`}>
        {evidence.explanation}
      </td>
      <td className={`${TD} font-mono text-theme-text-tertiary`}>
        {evidence.sourcePath || "—"}
      </td>
    </tr>
  );
}
