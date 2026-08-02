import { useCallback, useEffect, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import {
  Collapse,
  CollapseChevron,
  SearchBox,
  WithTooltip,
  formatDuration,
  type CapacityActivityEpisode,
  type CapacityActivityResponse,
  type CapacityResourceIdentity,
  FilterPill,
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
  PoolSelector,
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
  const qParam = search.get("q") || undefined;
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
  const [searchInput, setSearchInput] = useState(qParam ?? "");
  // Deferred writes (the debounced URL mirror of the search) must merge onto
  // the CURRENT params, not a snapshot captured when the timer was armed —
  // otherwise a window/pool change made mid-type would be dropped when the
  // write lands.
  const searchRef = useRef(location.search);
  searchRef.current = location.search;
  const pathRef = useRef(location.pathname);
  pathRef.current = location.pathname;
  const setParam = useCallback(
    (key: "pool" | "q" | "since", value: string | undefined) => {
      const params = new URLSearchParams(searchRef.current);
      params.delete("workload");
      const trimmed = value?.trim();
      if (trimmed) params.set(key, trimmed);
      else params.delete(key);
      navigate(
        {
          pathname: pathRef.current,
          search: params.toString() ? `?${params.toString()}` : "",
        },
        { replace: true },
      );
    },
    [navigate],
  );
  // The search filters the loaded episodes client-side, instantly. The URL
  // `q` param is a shareability mirror only — it never drives a server fetch.
  // Reflect external changes (Clear filters, back/forward) into the buffer;
  // both guards compare trimmed so the mirror landing mid-type can never eat
  // a trailing space the user is still extending.
  useEffect(() => {
    setSearchInput((current) =>
      current.trim() === (qParam ?? "") ? current : (qParam ?? ""),
    );
  }, [qParam]);
  useEffect(() => {
    if (searchInput.trim() === (qParam ?? "")) return;
    const timer = setTimeout(() => setParam("q", searchInput), 350);
    return () => clearTimeout(timer);
  }, [searchInput, qParam, setParam]);
  // The URL mirror of the search must not reset pagination — only the
  // server-side filters do.
  const paginationParams = new URLSearchParams(location.search);
  paginationParams.delete("q");
  const pagination = useCapacityPagination<CapacityActivityResponse>(
    paginationParams.toString(),
  );
  const query = useCapacityActivity({
    limit: 50,
    cursor: pagination.cursor,
    pool: poolFilter,
    claim: claimFilter,
    node: nodeFilter,
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
    // Reset the buffer directly — otherwise a pending debounce timer would
    // rewrite `q` right after the URL was cleared.
    setSearchInput("");
    const params = new URLSearchParams(location.search);
    ["pool", "claim", "node", "workload", "q", "since", "type"].forEach((key) =>
      params.delete(key),
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
    setParam(
      "since",
      hours ? new Date(now - hours * 60 * 60 * 1000).toISOString() : undefined,
    );
  };

  const hasActiveFilters = Boolean(
    poolFilter ||
    claimFilter ||
    nodeFilter ||
    searchInput.trim() ||
    sinceFilter ||
    typeFilter,
  );
  const searchTerm = searchInput.trim().toLowerCase();
  const visibleItems = searchTerm
    ? response.items.filter((episode) =>
        episodeMatchesSearch(episode, searchTerm),
      )
    : response.items;
  const aggregate = response.aggregate;
  // Episodes are built from both the retained timeline and Karpenter's object
  // events; either source being partial makes every rollup count a floor.
  const aggregateIsLowerBound =
    coverageIsLowerBound(response.coverage.timeline) ||
    coverageIsLowerBound(response.coverage.karpenterObjectEvents);
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
              <WithTooltip
                tip={`${
                  response.observation.sources.length > 0
                    ? `Sources: ${response.observation.sources.join(", ")} · `
                    : ""
                }${retentionLabel(response.observation.retention)}. An observation window, not a durable audit log. ${coverageMessage(response.coverage.timeline, "Retained activity")}`}
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

      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <div className="flex flex-wrap items-center gap-1.5 text-xs text-theme-text-tertiary">
          <span>Window</span>
          {WINDOW_PILLS.map(([hours, label]) => (
            <FilterPill
              key={hours ?? "retained"}
              label={label}
              active={selectedWindow === hours}
              onClick={() => setWindowHours(hours, Date.now())}
            />
          ))}
          {sinceFilter && <span>Since {formatTimestamp(sinceFilter)}</span>}
        </div>
        <PoolSelector
          key={response.clusterContext.contextName}
          pool={poolFilter}
          onChange={(pool) => setParam("pool", pool)}
          label="NodePool"
          emptyLabel="Any pool"
          unavailableLabel="NodePool options unavailable; activity remains available."
        />
        <SearchBox
          value={searchInput}
          onChange={setSearchInput}
          scope="global"
          shortcutId="capacity-activity-search"
          placeholder="LaunchFailed, node name…"
          className="w-60 2xl:w-72"
        />
        {(claimFilter || nodeFilter) && (
          <div
            className="flex flex-wrap items-center gap-1.5"
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
        {hasActiveFilters && (
          <button
            type="button"
            className="rounded-lg border border-theme-border px-3 py-1.5 text-sm text-theme-text-secondary hover:bg-theme-hover"
            onClick={clearFilters}
          >
            Clear filters
          </button>
        )}
      </div>

      {(aggregate !== undefined || typeFilter !== undefined) && (
        <div
          className="flex flex-wrap gap-1.5"
          aria-label="Filter activity by type"
        >
          {/* Counts come from the whole-window rollup (stable across the active
            type filter), not the current page. Pills without a rollup (cursor
            pages) never show a fabricated count. */}
          <FilterPill
            label={
              aggregate
                ? `All · ${formatAggregateCount(aggregate.total)}`
                : "All"
            }
            active={typeFilter === undefined}
            onClick={() => changeTypeFilter(undefined)}
          />
          {TYPE_PILL_ORDER.filter(
            (type) =>
              (aggregate?.byType[type]?.total ?? 0) > 0 || type === typeFilter,
          ).map((type) => {
            const counts = aggregate?.byType[type];
            const failed = counts?.byState?.failed ?? 0;
            return (
              <FilterPill
                key={type}
                label={
                  counts
                    ? `${activityTypeLabel(type)} · ${formatAggregateCount(counts.total)}${
                        failed > 0
                          ? ` · ${formatAggregateCount(failed)} failed`
                          : ""
                      }`
                    : activityTypeLabel(type)
                }
                active={typeFilter === type}
                onClick={() =>
                  changeTypeFilter(typeFilter === type ? undefined : type)
                }
              />
            );
          })}
        </div>
      )}

      {visibleItems.length > 0 ? (
        <div className="space-y-3">
          {visibleItems.map((episode, index) => (
            <ActivityEpisodeCard
              key={episode.id}
              episode={episode}
              defaultExpanded={index === 0 && !searchTerm}
              onOpenPool={onOpenPool}
              onOpenResource={onOpenResource}
            />
          ))}
        </div>
      ) : response.items.length > 0 ? (
        <InlineEmpty
          title="No loaded episodes match this search"
          detail="The search narrows the episodes loaded on this page. Matches may exist on other pages — page through, or clear the search."
        />
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

function episodeMatchesSearch(
  episode: CapacityActivityEpisode,
  term: string,
): boolean {
  const haystacks = [
    episode.summary,
    activityTypeLabel(episode.type),
    episode.state,
    episode.primaryReasonCode,
    ...[episode.pool, episode.claim, episode.node]
      .filter((identity): identity is CapacityResourceIdentity =>
        Boolean(identity),
      )
      .flatMap((identity) => [identity.ref.kind, identity.ref.name]),
    ...episode.evidence.flatMap((evidence) => [
      evidence.reasonCode,
      evidence.rawReason,
      evidence.rawMessage,
    ]),
  ];
  return haystacks.some((value) => value?.toLowerCase().includes(term));
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
