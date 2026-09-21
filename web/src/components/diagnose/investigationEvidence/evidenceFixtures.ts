// Shared fixtures for the evidence suites. A projection test needs a target,
// a well-formed tool result and a turn to hang them on before it can assert
// anything, so these live apart from any one suite that uses them.

import {
  projectInvestigationEvidence,
  type InvestigationEvidenceGroup,
  type InvestigationEvidenceTimelineItem,
  type InvestigationEvidenceTurn,
} from "./index";

export const target = {
  kind: "Deployment",
  group: "apps",
  namespace: "shop",
  name: "api",
};

export function evidenceRef(scope: string, nonce: string): string {
  return `ev_${scope.repeat(26)}_${nonce.repeat(26)}`;
}

export function tool(
  id: string,
  name: string,
  result: unknown,
  patch: Partial<
    Extract<InvestigationEvidenceTimelineItem, { kind: "tool" }>
  > = {},
): Extract<InvestigationEvidenceTimelineItem, { kind: "tool" }> {
  return {
    kind: "tool",
    id,
    tool: name,
    status: "done",
    summary: JSON.stringify({ namespace: "shop", name: "api" }),
    result: typeof result === "string" ? result : JSON.stringify(result),
    isError: false,
    radarEvidence: true,
    ...patch,
  };
}

export function project(...timelines: InvestigationEvidenceTimelineItem[][]) {
  const turns: InvestigationEvidenceTurn[] = timelines.map((timeline) => ({
    timeline,
  }));
  return projectInvestigationEvidence(turns, target);
}

export function groupsOf(
  groups: InvestigationEvidenceGroup[],
  kind: InvestigationEvidenceGroup["kind"],
) {
  return groups.filter((group) => group.kind === kind);
}

export const deployment = {
  apiVersion: "apps/v1",
  kind: "Deployment",
  metadata: { namespace: "shop", name: "api" },
  status: { readyReplicas: 0, replicas: 1 },
};

export const warningEvent = {
  reason: "BackOff",
  message: "Back-off restarting failed container",
  type: "Warning",
  count: 4,
  lastTimestamp: "2026-09-02T10:00:00Z",
};

export const criticalIssue = {
  id: "issue-api-crash",
  severity: "critical",
  source: "problem",
  category: "crashloop",
  category_group: "runtime",
  grouping_scope: "workload",
  kind: "Deployment",
  group: "apps",
  namespace: "shop",
  name: "api",
  reason: "CrashLoopBackOff",
  message: "The API container keeps restarting.",
};

export function completeLogProof(container = "api") {
  return {
    logsCurrent: [
      {
        pod: "api-abc",
        container,
        logs: {
          lines: ["server ready"],
          totalLines: 1,
          matchedLines: 0,
          fallback: true,
        },
      },
    ],
    logsPrevious: [
      {
        pod: "api-abc",
        container,
        logs: {
          lines: null,
          totalLines: 0,
          matchedLines: 0,
          fallback: false,
        },
        error: "failed to get logs: previous terminated container not found",
      },
    ],
    expectedPreviousLogAbsences: [{ pod: "api-abc", container }],
    logCoverage: {
      resolvedPods: 1,
      selectedPods: 1,
      shownLines: 1,
      totalLines: 1,
      shownPods: 1,
      totalPods: 1,
    },
  };
}

export const workloadLogsPayload = {
  workload: "deployments/shop/api",
  pods: 2,
  logs: [
    {
      pod: "api-68c7b766dc-fmphn",
      container: "api",
      logs: {
        lines: [
          "2026-09-07T08:00:20.234687659Z [31m[Nest] 1  - [39m09/07/2026, 8:00:20 AM [31m  ERROR[39m [38;5;3m[MongooseModule] [39m[31mUnable to connect to the database. Retrying (9)...[39m",
          "2026-09-07T08:00:20.234756499Z MongoServerError: Authentication failed.",
        ],
        totalLines: 50,
        matchedLines: 4,
        fallback: false,
      },
    },
    {
      pod: "api-68c7b766dc-fmphn",
      container: "istio-proxy",
      logs: {
        lines: ["2026-09-07T08:00:19Z info Envoy proxy is ready"],
        totalLines: 12,
        matchedLines: 0,
        fallback: true,
      },
    },
    {
      pod: "api-68c7b766dc-q2zxr",
      container: "api",
      logs: {
        lines: [
          "2026-09-07T08:00:21Z MongoServerError: Authentication failed.",
        ],
        totalLines: 50,
        matchedLines: 1,
        fallback: false,
      },
    },
  ],
  narrowHint:
    "at least one pod's log stream tailed to 50 lines (cap reached) — narrow with since= (e.g. 10m), grep= regex, container=, or raise tail_lines",
  warnings: [
    "1 of 2 pod(s) have container restarts on record; the error(s) that killed prior containers are in the previous instance's logs — call again with `previous: true` to see them.",
  ],
};

export const workloadLogsArgs = JSON.stringify({
  namespace: "shop",
  name: "api",
  kind: "deployment",
  tail_lines: 50,
});
