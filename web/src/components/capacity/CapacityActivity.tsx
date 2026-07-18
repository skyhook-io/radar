import { useEffect, useState, type FormEvent } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import {
  Collapse,
  CollapseChevron,
  WithTooltip,
  formatDuration,
  type CapacityActivityEpisode,
  type CapacityActivityResponse,
  type CapacityResourceIdentity,
} from "@skyhook-io/k8s-ui";
import { Badge } from "@skyhook-io/k8s-ui/components/ui/Badge";
import {
  isCapacityCursorInvalidError,
  useCapacityActivity,
} from "../../api/client";
import type { SelectedResource } from "../../types";
import {
  ActivityStateBadge,
  CapacityFreshness,
  InlineEmpty,
  LinkButton,
  Notice,
  PageControls,
  ROW_HOVER,
  ScopeBadges,
  ScrollableContent,
  TABLE_HEAD,
  TABLE_WRAP,
  TBODY,
  TD,
  TH,
  activityTypeLabel,
  activityWindowPreset,
  coverageHasObservations,
  coverageIsLowerBound,
  coverageMessage,
  errorMessage,
  formatTimestamp,
  identityKey,
  identityToSelectedResource,
  integrationBlock,
  relativeTime,
  retentionLabel,
  useCapacityCursorRecovery,
  useCapacityPagination,
} from "./shared";

const WINDOW_PILLS: [number | undefined, string][] = [
  [undefined, "Retained"],
  [1, "Last hour"],
  [6, "Last 6 hours"],
  [24, "Last 24 hours"],
];

const TYPE_PILL_ORDER: CapacityActivityEpisode["type"][] = [
  "provision",
  "launch_failure",
  "registration_failure",
  "initialization_failure",
  "disruption",
  "interruption",
  "termination",
  "config_change",
];

export function CapacityActivity({
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
  const poolFilter = search.get("pool") || undefined;
  const claimFilter = search.get("claim") || undefined;
  const nodeFilter = search.get("node") || undefined;
  const reasonFilter = search.get("reason") || undefined;
  const typeFilter = search.get("type") || undefined;
  const sinceFilter = search.get("since") || undefined;
  const invalidSinceFilter = Boolean(
    sinceFilter && !Number.isFinite(Date.parse(sinceFilter)),
  );
  const requestSinceFilter = invalidSinceFilter ? undefined : sinceFilter;
  const selectedWindow = activityWindowPreset(requestSinceFilter);
  useEffect(() => {
    const params = new URLSearchParams(location.search);
    if (!params.has("workload")) return;
    params.delete("workload");
    navigate(
      {
        pathname: location.pathname,
        search: params.toString() ? `?${params.toString()}` : "",
      },
      { replace: true },
    );
  }, [location.pathname, location.search, navigate]);
  const [activityDrafts, setActivityDrafts] = useState(() => ({
    key: location.search,
    pool: poolFilter ?? "",
    reason: reasonFilter ?? "",
  }));
  const drafts =
    activityDrafts.key === location.search
      ? activityDrafts
      : {
          key: location.search,
          pool: poolFilter ?? "",
          reason: reasonFilter ?? "",
        };
  const poolDraft = drafts.pool;
  const reasonDraft = drafts.reason;
  const pagination = useCapacityPagination<CapacityActivityResponse>(
    `${location.search}`,
  );
  const query = useCapacityActivity({
    limit: 50,
    cursor: pagination.cursor,
    pool: poolFilter,
    claim: claimFilter,
    node: nodeFilter,
    reason: reasonFilter,
    type: typeFilter,
    since: requestSinceFilter,
  });
  const recoveringCursor = useCapacityCursorRecovery(
    query.error,
    pagination.cursor,
    pagination.recover,
  );
  const recoveredCursor = pagination.recovered || recoveringCursor;
  const responseData =
    query.data ?? (recoveredCursor ? pagination.retainedPage : undefined);
  const blocked = integrationBlock(
    responseData,
    query.error,
    query.isLoading,
    "Loading capacity activity…",
  );
  if (blocked) return blocked;
  const response = responseData as CapacityActivityResponse;
  const updateFilters = (pool: string, reason: string, since?: string) => {
    const params = new URLSearchParams(location.search);
    params.delete("workload");
    if (pool.trim()) params.set("pool", pool.trim());
    else params.delete("pool");
    if (reason.trim()) params.set("reason", reason.trim());
    else params.delete("reason");
    if (since) params.set("since", since);
    else params.delete("since");
    navigate(
      {
        pathname: location.pathname,
        search: params.toString() ? `?${params.toString()}` : "",
      },
      { replace: true },
    );
  };
  const applyFilters = (event: FormEvent) => {
    event.preventDefault();
    updateFilters(poolDraft, reasonDraft, sinceFilter);
  };
  const changeTypeFilter = (type?: CapacityActivityEpisode["type"]) => {
    const params = new URLSearchParams(location.search);
    params.delete("workload");
    if (type) params.set("type", type);
    else params.delete("type");
    navigate(
      {
        pathname: location.pathname,
        search: params.toString() ? `?${params.toString()}` : "",
      },
      { replace: true },
    );
  };
  const clearFilters = () => {
    const params = new URLSearchParams(location.search);
    ["pool", "claim", "node", "workload", "reason", "since", "type"].forEach(
      (key) => params.delete(key),
    );
    navigate(
      {
        pathname: location.pathname,
        search: params.toString() ? `?${params.toString()}` : "",
      },
      { replace: true },
    );
  };
  const removeFilter = (key: "claim" | "node" | "since") => {
    const params = new URLSearchParams(location.search);
    params.delete(key);
    params.delete("workload");
    navigate(
      {
        pathname: location.pathname,
        search: params.toString() ? `?${params.toString()}` : "",
      },
      { replace: true },
    );
  };
  const setWindowHours = (hours: number | undefined, now: number) => {
    updateFilters(
      poolDraft,
      reasonDraft,
      hours ? new Date(now - hours * 60 * 60 * 1000).toISOString() : undefined,
    );
  };

  const hasActiveFilters = Boolean(
    poolFilter ||
    claimFilter ||
    nodeFilter ||
    reasonFilter ||
    sinceFilter ||
    typeFilter,
  );
  const aggregate = response.aggregate;
  const aggregateIsLowerBound = coverageIsLowerBound(
    response.coverage.timeline,
  );
  const formatAggregateCount = (count: number) =>
    `${aggregateIsLowerBound ? "≥" : ""}${count}`;

  return (
    <ScrollableContent>
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="flex flex-wrap items-baseline gap-3">
            <LinkButton onClick={() => onNavigate("/capacity")}>
              ← Capacity
            </LinkButton>
            <h1 className="text-lg font-semibold text-theme-text-primary">
              Activity
            </h1>
            <span className="text-xs text-theme-text-tertiary">
              Recent Karpenter capacity events, retained for a bounded window.
            </span>
          </div>
          <div className="mt-2">
            <ScopeBadges
              coverage={response.coverage}
              source="karpenterObjectEvents"
            />
          </div>
          {coverageHasObservations(response.coverage.timeline) && (
            <p className="mt-1.5 flex flex-wrap items-center gap-x-1.5 text-xs text-theme-text-tertiary">
              <span>
                Window {formatTimestamp(response.observation.startedAt)} →{" "}
                {formatTimestamp(response.observation.endedAt)}
              </span>
              {response.observation.sources.length > 0 && (
                <span>· {response.observation.sources.join(" · ")}</span>
              )}
              <span>· {retentionLabel(response.observation.retention)}</span>
              <WithTooltip
                tip={`An observation window, not a durable audit log. ${coverageMessage(response.coverage.timeline, "Retained activity")}`}
              >
                <span aria-label="About the observation window">ⓘ</span>
              </WithTooltip>
            </p>
          )}
        </div>
        <div className="flex items-center gap-2">
          {pagination.cursor && (
            <button
              type="button"
              className="rounded-lg border border-theme-border px-3 py-1.5 text-xs font-medium text-theme-text-secondary hover:bg-theme-hover"
              onClick={pagination.reset}
            >
              ↑ Jump to newest
            </button>
          )}
          <LinkButton
            onClick={() =>
              onNavigate(
                poolFilter
                  ? `/capacity/demand?pool=${encodeURIComponent(poolFilter)}`
                  : "/capacity/demand",
              )
            }
          >
            Pending demand →
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
      {invalidSinceFilter && (
        <Notice>
          The activity window timestamp is invalid, so the retained window is
          shown instead.{" "}
          <LinkButton className="inline" onClick={() => removeFilter("since")}>
            Clear invalid filter
          </LinkButton>
        </Notice>
      )}
      {response.cursorStatus !== "valid" && (
        <Notice>
          The retained activity boundary changed (
          {response.cursorGap?.reason ?? response.cursorStatus}). The previous
          cursor can no longer produce a continuous history.{" "}
          <LinkButton className="inline" onClick={pagination.reset}>
            Return to newest activity
          </LinkButton>
        </Notice>
      )}
      {coverageIsLowerBound(response.coverage.karpenterObjectEvents) && (
        <Notice>
          ≥{" "}
          {coverageMessage(
            response.coverage.karpenterObjectEvents,
            "Activity evidence",
          )}
          . Episodes may omit events outside this user’s authorized namespaces.
        </Notice>
      )}

      <form
        className="flex flex-wrap items-end gap-2 rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm"
        onSubmit={applyFilters}
      >
        <div className="flex basis-full flex-wrap items-center gap-1.5 text-xs text-theme-text-tertiary">
          <span>Window</span>
          {WINDOW_PILLS.map(([hours, label]) => (
            <button
              key={hours ?? "retained"}
              type="button"
              aria-pressed={selectedWindow === hours}
              className={`rounded-md border px-2 py-1 ${
                selectedWindow === hours
                  ? "selection border-theme-border text-theme-text-primary"
                  : "border-transparent hover:bg-theme-hover"
              }`}
              onClick={() => setWindowHours(hours, Date.now())}
            >
              {label}
            </button>
          ))}
          {sinceFilter && <span>Since {formatTimestamp(sinceFilter)}</span>}
        </div>
        <label className="min-w-[180px] flex-1 text-xs font-medium text-theme-text-tertiary">
          NodePool
          <input
            value={poolDraft}
            onChange={(event) =>
              setActivityDrafts({ ...drafts, pool: event.target.value })
            }
            placeholder="Any pool"
            className="mt-1 w-full rounded-lg border border-theme-border bg-theme-base px-3 py-1.5 text-sm text-theme-text-primary outline-none focus:border-skyhook-500"
          />
        </label>
        <label className="min-w-[220px] flex-[1.4] text-xs font-medium text-theme-text-tertiary">
          Filter by reason or message…
          <input
            value={reasonDraft}
            onChange={(event) =>
              setActivityDrafts({ ...drafts, reason: event.target.value })
            }
            placeholder="LaunchFailed, interruption…"
            className="mt-1 w-full rounded-lg border border-theme-border bg-theme-base px-3 py-1.5 text-sm text-theme-text-primary outline-none focus:border-skyhook-500"
          />
        </label>
        <button type="submit" className="btn-brand px-3 py-1.5 text-sm">
          Apply
        </button>
        {hasActiveFilters && (
          <button
            type="button"
            className="rounded-lg border border-theme-border px-3 py-1.5 text-sm text-theme-text-secondary hover:bg-theme-hover"
            onClick={clearFilters}
          >
            Clear filters
          </button>
        )}
        {(claimFilter || nodeFilter) && (
          <div
            className="flex basis-full flex-wrap items-center gap-1.5 pt-1"
            aria-label="Active resource filters"
          >
            <span className="text-xs text-theme-text-tertiary">
              Resource filters
            </span>
            {claimFilter && (
              <ActivityFilterChip
                label={`NodeClaim: ${claimFilter}`}
                onRemove={() => removeFilter("claim")}
              />
            )}
            {nodeFilter && (
              <ActivityFilterChip
                label={`Node: ${nodeFilter}`}
                onRemove={() => removeFilter("node")}
              />
            )}
          </div>
        )}
      </form>

      {(aggregate !== undefined || typeFilter !== undefined) && (
        <div
          className="flex flex-wrap gap-1.5"
          aria-label="Filter activity by type"
        >
          {/* Counts come from the whole-window rollup (stable across the active
            type filter), not the current page. Pills without a rollup (cursor
            pages) never show a fabricated count. */}
          <button
            type="button"
            aria-pressed={typeFilter === undefined}
            className={`rounded-lg border px-3 py-1.5 text-xs font-medium transition-colors ${
              typeFilter === undefined
                ? "selection border-theme-border text-theme-text-primary"
                : "border-theme-border text-theme-text-secondary hover:bg-theme-hover"
            }`}
            onClick={() => changeTypeFilter(undefined)}
          >
            {aggregate
              ? `All · ${formatAggregateCount(aggregate.total)}`
              : "All"}
          </button>
          {TYPE_PILL_ORDER.filter(
            (type) =>
              (aggregate?.byType[type]?.total ?? 0) > 0 || type === typeFilter,
          ).map((type) => {
            const counts = aggregate?.byType[type];
            const failed = counts?.byState?.failed ?? 0;
            return (
              <button
                key={type}
                type="button"
                aria-pressed={typeFilter === type}
                className={`rounded-lg border px-3 py-1.5 text-xs font-medium transition-colors ${
                  typeFilter === type
                    ? "selection border-theme-border text-theme-text-primary"
                    : "border-theme-border text-theme-text-secondary hover:bg-theme-hover"
                }`}
                onClick={() =>
                  changeTypeFilter(typeFilter === type ? undefined : type)
                }
              >
                {counts
                  ? `${activityTypeLabel(type)} · ${formatAggregateCount(counts.total)}${
                      failed > 0
                        ? ` · ${formatAggregateCount(failed)} failed`
                        : ""
                    }`
                  : activityTypeLabel(type)}
              </button>
            );
          })}
        </div>
      )}

      {response.items.length > 0 ? (
        <div className="space-y-3">
          {response.items.map((episode, index) => (
            <ActivityEpisodeCard
              key={episode.id}
              episode={episode}
              defaultExpanded={index === 0}
              onOpenPool={onOpenPool}
              onOpenResource={onOpenResource}
            />
          ))}
        </div>
      ) : coverageHasObservations(response.coverage.timeline) ? (
        <InlineEmpty
          title="No episodes match these filters"
          detail={
            pagination.cursor
              ? "No activity episodes were retained on this page."
              : "Within the retained window, nothing matches. Evidence outside the window is not retained — absence here is not proof nothing happened earlier."
          }
        />
      ) : (
        <InlineEmpty
          title="Activity unavailable"
          detail={coverageMessage(
            response.coverage.timeline,
            "Retained activity",
          )}
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

function ActivityFilterChip({
  label,
  onRemove,
}: {
  label: string;
  onRemove: () => void;
}) {
  return (
    <button
      type="button"
      className="rounded-md border border-theme-border bg-theme-base px-2 py-1 text-xs text-theme-text-secondary hover:bg-theme-hover"
      onClick={onRemove}
      aria-label={`Remove ${label} filter`}
    >
      {label} <span aria-hidden="true">×</span>
    </button>
  );
}

function ActivityEpisodeCard({
  episode,
  defaultExpanded,
  onOpenPool,
  onOpenResource,
}: {
  episode: CapacityActivityEpisode;
  defaultExpanded: boolean;
  onOpenPool: (name: string) => void;
  onOpenResource: (resource: SelectedResource) => void;
}) {
  const [expanded, setExpanded] = useState(defaultExpanded);
  const [showProvenance, setShowProvenance] = useState(false);
  const subjects = [episode.pool, episode.claim, episode.node].filter(
    (identity): identity is CapacityResourceIdentity => Boolean(identity),
  );
  const duration =
    episode.durationSeconds !== undefined
      ? formatDuration(episode.durationSeconds * 1000, true)
      : episode.state === "open"
        ? "in progress"
        : episode.state === "observed"
          ? "point observation"
          : "—";
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
            <Badge tone="structural" size="sm">
              {activityTypeLabel(episode.type)}
            </Badge>
            <ActivityStateBadge state={episode.state} />
            {!episode.evidence.some(
              (item) => item.relationship === "direct",
            ) && (
              <WithTooltip tip="No controller-recorded cause — this episode was inferred by correlating event text. Expand for the raw evidence.">
                <Badge severity="neutral" size="sm">
                  inferred
                </Badge>
              </WithTooltip>
            )}
            <span className="min-w-0 truncate font-medium text-theme-text-primary">
              {episode.summary}
            </span>
            <span className="ml-auto font-mono text-xs text-theme-text-tertiary">
              {relativeTime(episode.startedAt)}
            </span>
            <WithTooltip tip="Duration is shown only when start and end were both observed.">
              <Badge tone="structural" size="sm">
                {duration}
              </Badge>
            </WithTooltip>
          </div>
          <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
            {subjects.map((subject) => (
              <span
                key={identityKey(subject)}
                role="link"
                tabIndex={0}
                className="cursor-pointer"
                onClick={(event) => {
                  event.stopPropagation();
                  if (subject.ref.kind === "NodePool")
                    onOpenPool(subject.ref.name);
                  else onOpenResource(identityToSelectedResource(subject));
                }}
                onKeyDown={(event) => {
                  if (event.key === "Enter" || event.key === " ") {
                    event.preventDefault();
                    event.stopPropagation();
                    if (subject.ref.kind === "NodePool")
                      onOpenPool(subject.ref.name);
                    else onOpenResource(identityToSelectedResource(subject));
                  }
                }}
              >
                <Badge tone="structural" size="sm">
                  {subject.ref.kind.toLowerCase()}/{subject.ref.name}
                </Badge>
              </span>
            ))}
            {episode.primaryReasonCode && (
              <span className="font-mono text-[11px] text-theme-text-tertiary">
                reason: {episode.primaryReasonCode}
              </span>
            )}
          </div>
        </div>
      </button>

      <Collapse open={expanded}>
        <div className="border-t border-theme-border">
          {episode.evidence.length > 0 ? (
            <div className={TABLE_WRAP}>
              <div className="flex justify-end px-4 pt-2">
                <LinkButton
                  onClick={() => setShowProvenance((current) => !current)}
                >
                  {showProvenance ? "Hide provenance ▴" : "Show provenance ▾"}
                </LinkButton>
              </div>
              <table className="w-full text-left">
                <thead className={TABLE_HEAD}>
                  <tr>
                    <th className={TH}>When</th>
                    <th className={TH}>Source</th>
                    {showProvenance && <th className={TH}>Normalized</th>}
                    <th className={TH}>Raw</th>
                    {showProvenance && <th className={TH}>Relationship</th>}
                    {showProvenance && <th className={TH}>Confidence</th>}
                    <th className={TH}>References</th>
                  </tr>
                </thead>
                <tbody className={TBODY}>
                  {episode.evidence.map((evidence, index) => (
                    <tr
                      key={`${evidence.at}-${evidence.reasonCode}-${index}`}
                      className={ROW_HOVER}
                    >
                      <td
                        className={`${TD} whitespace-nowrap font-mono text-theme-text-tertiary`}
                      >
                        {relativeTime(evidence.at)}
                      </td>
                      <td className={TD}>
                        <Badge tone="structural" size="sm">
                          {evidence.source.replace("_", " ")}
                        </Badge>
                      </td>
                      {showProvenance && (
                        <td className={`${TD} font-mono`}>
                          {evidence.reasonCode}
                        </td>
                      )}
                      <td className={`${TD} text-theme-text-secondary`}>
                        {evidence.rawReason || evidence.rawMessage ? (
                          <>
                            {evidence.rawReason
                              ? `${evidence.rawReason}: `
                              : ""}
                            {evidence.rawMessage}
                          </>
                        ) : (
                          "—"
                        )}
                      </td>
                      {showProvenance && (
                        <td className={`${TD} text-theme-text-secondary`}>
                          {evidence.relationship}
                        </td>
                      )}
                      {showProvenance && (
                        <td className={TD}>
                          <Badge tone="structural" size="sm">
                            {evidence.confidence}
                          </Badge>
                        </td>
                      )}
                      <td className={TD}>
                        {evidence.refs.length > 0 ? (
                          <div className="flex flex-wrap gap-1">
                            {evidence.refs.map((ref) => (
                              <span
                                key={identityKey(ref)}
                                role="link"
                                tabIndex={0}
                                className="cursor-pointer"
                                onClick={() =>
                                  ref.ref.kind === "NodePool"
                                    ? onOpenPool(ref.ref.name)
                                    : onOpenResource(
                                        identityToSelectedResource(ref),
                                      )
                                }
                                onKeyDown={(event) => {
                                  if (
                                    event.key === "Enter" ||
                                    event.key === " "
                                  ) {
                                    event.preventDefault();
                                    if (ref.ref.kind === "NodePool") {
                                      onOpenPool(ref.ref.name);
                                    } else {
                                      onOpenResource(
                                        identityToSelectedResource(ref),
                                      );
                                    }
                                  }
                                }}
                              >
                                <Badge tone="structural" size="sm">
                                  {ref.ref.kind.toLowerCase()}/{ref.ref.name}
                                </Badge>
                              </span>
                            ))}
                          </div>
                        ) : (
                          "—"
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p className="px-4 py-3 text-sm text-theme-text-secondary">
              No evidence records were retained for this episode.
            </p>
          )}
          {episode.evidenceMeta.truncated && (
            <p className="px-4 py-2 text-[11px] text-theme-text-tertiary">
              Showing {episode.evidenceMeta.returned} of{" "}
              {episode.evidenceMeta.total} evidence records.
            </p>
          )}
        </div>
      </Collapse>
    </article>
  );
}
