import { useMemo, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { useIssues, useSubjectIssues, type SubjectIssuesResponse } from "../../api/client";
import {
  useAPIResources,
  karpenterCapacityAvailable,
} from "../../api/apiResources";
import { useCapabilitiesContext } from "../../contexts/CapabilitiesContext";
import { useConnection } from "../../context/ConnectionContext";
import type { SelectedResource } from "../../types";
import {
  IssuesView,
  PaneLoader,
  PageHeader,
  SummaryTile,
  FreshnessControl,
  ISSUE_SEVERITIES,
  ISSUE_SEVERITY_LABEL,
  type Issue,
  type IssueResourceRef,
  type IssueSeverity,
  type SummaryTone,
} from "@skyhook-io/k8s-ui";
import { AlertTriangle } from "lucide-react";
import { IssueDiagnoseButton } from "../diagnose/LocalDiagnoseAction";

// A capacity-relevant issue links to its Karpenter diagnosis. Karpenter is
// always single-cluster (unlike Argo hub-and-spoke), so the issue and the
// Capacity view are guaranteed to be the same cluster — the deep link is
// unambiguous.
//
// Fail closed: return null unless the issue is *definitely* Karpenter's, so the
// link can't mislead. Only two signals qualify — (1) the subject IS a Karpenter
// NodePool, or (2) the backend has flagged an unschedulable pod as requiring a
// Karpenter NodePool (issue.capacity_relevant — a structural pod-spec check
// server-side, not message parsing). A generic scheduling failure (insufficient
// cpu, node-pinned, zonal PVC, a non-Karpenter managed node group) is NOT
// Karpenter's to solve and gets no link, even in a Karpenter cluster. Clusters
// without Karpenter never reach the signal checks (hasKarpenter is false).
export function capacityHrefForIssue(
  issue: Issue,
  hasKarpenter: boolean,
): string | null {
  if (!hasKarpenter) return null;
  // (1) A NodePool-subject issue (not ready, limit pressure, …) → its pool detail.
  if (issue.kind === "NodePool" && issue.group === "karpenter.sh") {
    return `/capacity/pools/${encodeURIComponent(issue.name)}`;
  }
  // (2) A pod the backend flagged as requiring a Karpenter NodePool → the Demand
  // queue, which groups pending pods by scheduling signature and shows which
  // pools can (or can't) take them. The link carries its subject (?owner=) so
  // Demand lands filtered server-side: grouped scheduling issues have the
  // workload AS their subject (grouping promotes the owner); flat pod rows
  // carry issue.owner. Fail closed to the unfiltered link when no complete
  // subject exists. No STATE filter on purpose — the issue doesn't map cleanly
  // to a single demand state (blocked vs awaiting capacity), and a state filter
  // could hide the very group being investigated. Capacity is deliberately
  // cluster-wide — no namespace view-filter forwarding.
  if (issue.capacity_relevant) {
    const owner =
      issue.owner?.kind && issue.owner.name
        ? {
            kind: issue.owner.kind,
            namespace: issue.owner.namespace ?? issue.namespace,
            name: issue.owner.name,
          }
        : issue.kind && issue.kind !== "Pod" && issue.name
          ? { kind: issue.kind, namespace: issue.namespace, name: issue.name }
          : undefined;
    if (owner?.namespace) {
      return `/capacity/demand?owner=${encodeURIComponent(`${owner.namespace}/${owner.kind}/${owner.name}`)}`;
    }
    return "/capacity/demand";
  }
  return null;
}

/** The subject a link into Issues narrows to (?kind=&group=&resource=ns/name), or null. */
export function issueSubjectFromParams(params: URLSearchParams): IssueSubject | null {
  const kind = params.get("kind");
  const resource = params.get("resource");
  if (!kind || !resource) return null;
  const group = params.get("group") ?? "";
  const slash = resource.indexOf("/");
  if (slash < 0) return { kind, group, namespace: "", name: resource };
  const name = resource.slice(slash + 1);
  return name ? { kind, group, namespace: resource.slice(0, slash), name } : null;
}

export interface IssueSubject {
  kind: string;
  /** The API group; "" for the core group. Tells a CNPG Cluster from a CAPI one. */
  group: string;
  namespace: string;
  name: string;
}

export type IssueSubjectState =
  | { state: "hidden" }
  | { state: "checking" }
  | { state: "unconfirmed"; why: string }
  | { state: "found"; issues: Issue[]; withheld: number }
  | { state: "none" };

/**
 * What can be said about the subject's issues. They come from the per-resource
 * lookup, which matches every grouped member (the list's inline members are
 * capped) by exact API group; an empty answer is "none" only when Radar reads
 * the subject's kind, nothing kept it from the evidence, and RBAC withheld no
 * issue about it.
 */
export function issueSubjectState(
  subject: IssueSubject,
  viewNamespaces: string[],
  related: { isLoading: boolean; error: unknown; data: SubjectIssuesResponse | undefined },
): IssueSubjectState {
  if (subject.namespace && viewNamespaces.length > 0 && !viewNamespaces.includes(subject.namespace)) {
    return { state: "hidden" };
  }
  if (related.error) {
    return { state: "unconfirmed", why: related.error instanceof Error ? related.error.message : String(related.error) };
  }
  const data = related.data;
  if (related.isLoading || !data) return { state: "checking" };
  if (data.issues.length > 0) return { state: "found", issues: data.issues, withheld: data.withheld?.issues ?? 0 };
  if (data.coverage === "notWatched") return { state: "unconfirmed", why: `Radar isn't watching ${subject.kind} yet` };
  if (data.coverage === "syncing") return { state: "unconfirmed", why: `Radar is still loading ${subject.kind}` };
  const withheld = data.withheld?.issues ?? 0;
  if (withheld > 0) {
    return { state: "unconfirmed", why: `${withheld} ${withheld === 1 ? "issue is" : "issues are"} about resources you can't read` };
  }
  const visibility = data.visibility;
  if (visibility?.state === "degraded" || visibility?.state === "limited") {
    return {
      state: "unconfirmed",
      why: `some evidence isn't readable${visibility.impact ? ` (${visibility.impact.replace(/\.$/, "")})` : ""}`,
    };
  }
  return { state: "none" };
}

const SEVERITY_TONE: Record<IssueSeverity, SummaryTone> = {
  critical: "error",
  warning: "warning",
};

interface IssuesPaneProps {
  namespaces: string[];
  onNavigateToResource: (resource: SelectedResource) => void;
  /** Brings a namespace into the view filter: added to it, or (where Radar's scope allows only one) switched to it. */
  showNamespace?: { mode: "add" | "switch"; show: (namespace: string) => void };
}

// The per-cluster Issues surface. Renders the same shared triage queue
// (IssuesView) the Hub fleet view uses — single cluster here, so no cluster
// label and in-app (client-side) resource navigation. Classification +
// owner-grouping come pre-computed from radar's /api/issues
// (internal/issues.Compose → Classify → Group). Filtering is the host's job
// (IssuesView is a pure list); single-cluster gets a light severity filter via
// the header status tiles (clickable → filter), matching the Applications /
// GitOps header-tile pattern rather than Hub's fleet facet sidebar.
export function IssuesPane({
  namespaces,
  onNavigateToResource,
  showNamespace,
}: IssuesPaneProps) {
  const { data, isLoading, error, dataUpdatedAt, refetch } =
    useIssues(namespaces);
  const { connection } = useConnection();
  const navigate = useNavigate();
  const apiResources = useAPIResources();
  const hasKarpenter = karpenterCapacityAvailable(
    useCapabilitiesContext().karpenter,
    apiResources.data,
  );
  const [severityFilter, setSeverityFilter] = useState<Set<IssueSeverity>>(
    new Set(),
  );

  const [searchParams, setSearchParams] = useSearchParams();
  const subject = useMemo(() => issueSubjectFromParams(searchParams), [searchParams]);
  const subjectHidden =
    !!subject?.namespace && namespaces.length > 0 && !namespaces.includes(subject.namespace);
  const related = useSubjectIssues(subject, !subjectHidden);
  const subjectState = subject
    ? issueSubjectState(subject, namespaces, related)
    : null;
  const pageIssues = useMemo(() => data?.issues ?? [], [data]);
  // With a subject set, the tiles and the list both describe the subject.
  const scopeIssues = subjectState
    ? subjectState.state === "found" ? subjectState.issues : []
    : pageIssues;
  const totals = useMemo(() => {
    const t: Record<IssueSeverity, number> = { critical: 0, warning: 0 };
    for (const i of scopeIssues) t[i.severity] = (t[i.severity] ?? 0) + 1;
    return t;
  }, [scopeIssues]);
  const shown = severityFilter.size
    ? scopeIssues.filter((i) => severityFilter.has(i.severity))
    : scopeIssues;
  const clearSubject = () =>
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        next.delete("kind");
        next.delete("group");
        next.delete("resource");
        return next;
      },
      { replace: true },
    );

  const toggleSeverity = (s: IssueSeverity) =>
    setSeverityFilter((prev) => {
      const next = new Set(prev);
      if (next.has(s)) next.delete(s);
      else next.add(s);
      return next;
    });

  const onResourceClick = (ref: IssueResourceRef) =>
    onNavigateToResource({
      kind: ref.kind,
      namespace: ref.namespace ?? "",
      name: ref.name,
      group: ref.group ?? "",
    });

  if (isLoading) {
    return <PaneLoader label="Loading issues…" className="flex-1" />;
  }

  if (error) {
    return (
      <div className="flex-1 flex items-center justify-center text-theme-text-secondary">
        <p>Failed to load issues</p>
      </div>
    );
  }

  return (
    <div className="flex-1 flex flex-col min-h-0 p-4 gap-4 overflow-auto">
      <PageHeader
        icon={AlertTriangle}
        title="Issues"
        description="Live cluster problems — crashes, scheduling failures, bad references — grouped by the resource they affect."
        actions={
          <>
            <FreshnessControl
              mode="auto"
              dataUpdatedAt={subject && !subjectHidden ? related.dataUpdatedAt : dataUpdatedAt}
              onRefresh={() => {
                if (subject && !subjectHidden) related.refetch();
                else refetch();
              }}
              connectionState={connection.state}
            />
            {scopeIssues.length > 0 && (
              <>
                <SummaryTile
                  label={scopeIssues.length === 1 ? "issue" : "issues"}
                  value={scopeIssues.length}
                />
                {ISSUE_SEVERITIES.map((s) =>
                  totals[s] > 0 || severityFilter.has(s) ? (
                    <SummaryTile
                      key={s}
                      label={ISSUE_SEVERITY_LABEL[s]}
                      value={totals[s]}
                      tone={SEVERITY_TONE[s]}
                      active={severityFilter.has(s)}
                      onClick={() => toggleSeverity(s)}
                    />
                  ) : null,
                )}
              </>
            )}
          </>
        }
      />

      {/* Visibility honesty: when RBAC reads are incomplete, an empty queue may
          mean "can't see" rather than "nothing broken" — say so up front so the
          empty state isn't mistaken for a clean bill of health. */}
      {data?.visibility?.impact && (
        <div className="flex items-start gap-2 rounded-lg border border-theme-border bg-theme-elevated px-3 py-2 text-xs text-theme-text-secondary">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
          <span>
            Limited visibility — {data.visibility.impact} Results may be
            incomplete.
          </span>
        </div>
      )}

      {/* Truncation honesty: when more issues matched than were returned, say
          so — don't present a capped list as the complete picture. */}
      {!subject && data?.total_matched != null &&
        data.total_matched > (data.issues?.length ?? 0) && (
          <p className="text-xs text-theme-text-tertiary">
            Showing {data.issues?.length ?? 0} of {data.total_matched} issues
            (capped) — narrow by namespace to see the rest.
          </p>
        )}

      {subject && subjectState && (
        <div className="flex flex-wrap items-center gap-2 text-xs text-theme-text-secondary">
          {subjectState.state === "hidden" ? (
            <>
              <span>
                {subject.kind}{" "}
                <span className="font-mono">{subject.namespace}/{subject.name}</span>{" "}
                is in namespace <span className="font-mono">{subject.namespace}</span>, which your
                namespace filter hides.
              </span>
              {showNamespace && (
                <button
                  type="button"
                  onClick={() => showNamespace.show(subject.namespace)}
                  className="text-accent-text hover:underline"
                >
                  {showNamespace.mode === "add" ? "Add" : "Switch to"}{" "}
                  <span className="font-mono">{subject.namespace}</span>
                  {showNamespace.mode === "add" ? " to the view" : ""}
                </button>
              )}
            </>
          ) : (
            <span>
              {subjectState.state === "checking" ? "Checking issues about" : "Showing issues about"}{" "}
              {subject.kind}{" "}
              <span className="font-mono">
                {subject.namespace ? `${subject.namespace}/` : ""}
                {subject.name}
              </span>
              {subjectState.state === "none" && " — none now"}
              {subjectState.state === "unconfirmed" && ` — can't confirm: ${subjectState.why}`}
              {subjectState.state === "found" && subjectState.withheld > 0 &&
                ` — ${subjectState.withheld} more about resources you can't read`}
            </span>
          )}
          <button type="button" onClick={clearSubject} className="text-accent-text hover:underline">
            Show all issues
          </button>
        </div>
      )}

      {/* Filtered-empty is NOT the healthy empty state: when a severity filter
          hides every row but issues still exist, say "no matches" rather than
          letting IssuesView render its "nothing broken" terminal state. */}
      {subject && scopeIssues.length === 0 ? null : severityFilter.size > 0 && scopeIssues.length > 0 && shown.length === 0 ? (
        <div className="flex flex-col items-center gap-2 py-12 text-center text-sm text-theme-text-secondary">
          <p>No issues match the selected severity.</p>
          <button
            type="button"
            onClick={() => setSeverityFilter(new Set())}
            className="text-xs text-skyhook-600 hover:text-skyhook-500 dark:text-skyhook-400"
          >
            Clear filter
          </button>
        </div>
      ) : (
        /* anyData = the query resolved, i.e. the cluster is reachable; an empty
           list then means "nothing broken" rather than "not connected". */
        <IssuesView
          issues={shown}
          anyData={!!data}
          onResourceClick={onResourceClick}
          renderActions={({ issue }) => {
            const capacityHref = capacityHrefForIssue(issue, hasKarpenter);
            return (
              <div className="flex items-center gap-2">
                {capacityHref && (
                  <button
                    type="button"
                    onClick={() => navigate(capacityHref)}
                    className="rounded-md border border-theme-border px-2 py-1 text-xs font-medium text-accent-text transition-colors hover:bg-theme-hover"
                  >
                    View in Capacity →
                  </button>
                )}
                <IssueDiagnoseButton
                  kind={issue.kind}
                  group={issue.group}
                  namespace={issue.namespace ?? ""}
                  name={issue.name}
                />
              </div>
            );
          }}
        />
      )}
    </div>
  );
}
