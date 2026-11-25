import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { issueRowKey, type Issue, type IssueExpansion } from "@skyhook-io/k8s-ui";
import { useIssueCorrelation, useIssueRowChanges, type CorrelatedIssuesResponse } from "../../api/client";
import { RadarFeatureUnsupportedError } from "../../api/radarFeatures";
import { useTimelineEventLinks } from "../../context/TimelineNavigation";

interface CorrelationQuery {
  data: CorrelatedIssuesResponse | undefined;
  error: unknown;
  isFetching: boolean;
  dataUpdatedAt: number;
  refetch: () => unknown;
}

/** Maps the correlation request for the open issue onto what the row renders.
 *  Data wins over a later refresh error: the row keeps the last answer and says
 *  the refresh failed and when that answer arrived. */
export function issueExpansionFromQuery(issue: Issue, query: CorrelationQuery): IssueExpansion {
  const onRetry = () => { void query.refetch(); };
  const retrying = query.isFetching;
  if (query.data) {
    const status = query.data.correlation_status;
    if (status === "user_forbidden" || status === "read_error") {
      return { recentChanges: { status: "unavailable", reason: status }, onRetry, retrying };
    }
    // An issue Radar doesn't correlate comes back unchanged, with no
    // correlation and no status; the row then shows no Recent changes section.
    const fresh = status === "not_found"
      ? undefined
      : query.data.issues?.find((candidate) => candidate.id === issue.id);
    if (!fresh) return { recentChanges: { status: "not_found" } };
    // An ineligible issue comes back as the list has it. Overriding the row with
    // it would only freeze the row on that copy once later refreshes fail.
    if (!fresh.correlation && !fresh.correlated_changes && !fresh.no_recent_changes) {
      return { recentChanges: { status: "loaded" } };
    }
    return {
      issue: fresh,
      recentChanges: { status: "loaded" },
      onRetry,
      retrying,
      ...(query.error ? { refreshFailed: { fetchedAt: query.dataUpdatedAt } } : {}),
    };
  }
  if (query.error instanceof RadarFeatureUnsupportedError) {
    return { recentChanges: { status: "unsupported", upgrade: query.error.radarUpgrade } };
  }
  if (query.error) return { recentChanges: { status: "failed" }, onRetry, retrying };
  return { recentChanges: { status: "loading" } };
}

/**
 * Wires a host's issue list (IssuesView or ResourceIssuesSection) to the
 * correlation request: the list reports its open row, this fetches for that
 * one issue, and the open row renders the fresh answer. `onNotFound` refreshes
 * the host's list when the open issue is no longer in the subject's answer.
 */
export function useIssueExpansion(onNotFound?: () => void) {
  const [openIssue, setOpenIssue] = useState<Issue | null>(null);
  const query = useIssueCorrelation(openIssue, openIssue !== null);
  const { data, error, isFetching, refetch, dataUpdatedAt } = query;
  const expansion = useMemo(
    () => (openIssue ? issueExpansionFromQuery(openIssue, { data, error, isFetching, dataUpdatedAt, refetch }) : undefined),
    [openIssue, data, error, isFetching, dataUpdatedAt, refetch],
  );

  const notFoundRef = useRef(onNotFound);
  notFoundRef.current = onNotFound;
  const notFound = expansion?.recentChanges.status === "not_found";
  useEffect(() => {
    if (notFound) notFoundRef.current?.();
  }, [notFound, dataUpdatedAt]);

  const openKey = openIssue ? issueRowKey(openIssue) : null;
  const expandedIssue = useCallback(
    (issue: Issue) => (openKey !== null && issueRowKey(issue) === openKey ? expansion : undefined),
    [openKey, expansion],
  );
  const timelineLinks = useTimelineEventLinks();
  return { onOpenIssueChange: setOpenIssue, expandedIssue, timelineLinks };
}

/**
 * Wires a host's issue list to the batched row lookup: the list reports the
 * rows on screen, this fetches the one change each of them names.
 */
export function useIssueRowNotes() {
  const [visible, setVisible] = useState<Issue[]>([]);
  const rowChanges = useIssueRowChanges(visible);
  return { rowChanges, onVisibleIssuesChange: setVisible };
}
