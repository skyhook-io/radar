import { describe, expect, it } from "vitest";

import {
  investigationActivitySourceDomId,
  investigationEvidenceGroupWithoutSources,
  investigationEvidenceStepIdsByTurn,
  investigationEvidenceSourceDomId,
  investigationEvidenceSourceId,
  projectInvestigationEvidence,
  resolveInvestigationRootCauseEvidence,
  type InvestigationEvidenceGroup,
  type InvestigationEvidenceTimelineItem,
  type InvestigationEvidenceTurn,
} from "./investigationEvidence";

const target = {
  kind: "Deployment",
  group: "apps",
  namespace: "shop",
  name: "api",
};

function evidenceRef(scope: string, nonce: string): string {
  return `ev_${scope.repeat(26)}_${nonce.repeat(26)}`;
}

function tool(
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
    ...patch,
  };
}

function project(...timelines: InvestigationEvidenceTimelineItem[][]) {
  const turns: InvestigationEvidenceTurn[] = timelines.map((timeline) => ({
    timeline,
  }));
  return projectInvestigationEvidence(turns, target);
}

function groupsOf(
  groups: InvestigationEvidenceGroup[],
  kind: InvestigationEvidenceGroup["kind"],
) {
  return groups.filter((group) => group.kind === kind);
}

const deployment = {
  apiVersion: "apps/v1",
  kind: "Deployment",
  metadata: { namespace: "shop", name: "api" },
  status: { readyReplicas: 0, replicas: 1 },
};

const warningEvent = {
  reason: "BackOff",
  message: "Back-off restarting failed container",
  type: "Warning",
  count: 4,
  lastTimestamp: "2026-09-02T10:00:00Z",
};

const criticalIssue = {
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

function completeLogProof(container = "api") {
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

describe("investigation evidence source identity", () => {
  it("is stable, DOM-safe, and shared by both pane anchors", () => {
    const id = investigationEvidenceSourceId(2, "call/α:1");
    expect(id).toBe("turn-2-step-call_x2f__x3b1__x3a_1");
    expect(id).toMatch(/^[A-Za-z0-9_-]+$/);
    expect(investigationActivitySourceDomId(id)).toBe(
      `investigation-activity-${id}`,
    );
    expect(investigationEvidenceSourceDomId(id)).toBe(
      `investigation-evidence-${id}`,
    );
  });

  it("keeps escaped characters distinct from literal escape text", () => {
    expect(investigationEvidenceSourceId(0, "/")).not.toBe(
      investigationEvidenceSourceId(0, "_x2f_"),
    );
    expect(investigationEvidenceSourceId(0, "/")).toBe("turn-0-step-_x2f_");
    expect(investigationEvidenceSourceId(0, "_x2f_")).toBe(
      "turn-0-step-_x5f_x2f_x5f_",
    );
  });

  it("keeps the empty token distinct from its literal sentinel", () => {
    expect(investigationEvidenceSourceId(0, "")).not.toBe(
      investigationEvidenceSourceId(0, "_empty_"),
    );
    expect(investigationEvidenceSourceId(0, "")).toBe("turn-0-step-_empty_");
    expect(investigationEvidenceSourceId(0, "_empty_")).toBe(
      "turn-0-step-_x5f_empty_x5f_",
    );
    expect(investigationEvidenceSourceId(0, "call-123")).toBe(
      "turn-0-step-call-123",
    );
  });
});

describe("root-cause evidence resolution", () => {
  it("resolves server-linked refs in model order and snapshots the exact typed source", () => {
    const firstRef = evidenceRef("a", "b");
    const secondRef = evidenceRef("a", "c");
    const projection = project([
      tool(
        "diagnose-1",
        "diagnose",
        {
          resource: deployment,
          resourceContext: { tier: "basic" },
          relatedIssues: [criticalIssue],
        },
        { evidenceRef: firstRef },
      ),
      tool(
        "events-1",
        "get_events",
        { events: [warningEvent] },
        { evidenceRef: secondRef },
      ),
    ]);

    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [secondRef, firstRef] },
      0,
    );

    expect(resolution.status).toBe("linked");
    expect(resolution.links.map((link) => link.source.stepId)).toEqual([
      "events-1",
      "diagnose-1",
    ]);
    expect(resolution.links.every((link) => link.group)).toBe(true);
    expect(
      resolution.links[1].group?.observations.every(
        (observation) => observation.source.stepId === "diagnose-1",
      ),
    ).toBe(true);
    expect(resolution.links[1].originalGroupId).toBe(
      projection.sources.find((source) => source.stepId === "diagnose-1")
        ?.primaryGroupId,
    );
  });

  it("keeps an unadapted successful Radar check linkable to Activity", () => {
    const ref = evidenceRef("a", "b");
    const projection = project([
      tool(
        "metrics-1",
        "query_prometheus",
        { result: [1] },
        { evidenceRef: ref },
      ),
    ]);

    expect(projection.groups).toHaveLength(0);
    expect(projection.citableSources).toHaveLength(1);
    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    expect(resolution).toMatchObject({
      status: "linked",
      links: [{ source: { stepId: "metrics-1" }, additionalGroupCount: 0 }],
    });
    expect(resolution.links[0].group).toBeUndefined();
    expect(
      investigationEvidenceStepIdsByTurn(projection, resolution).get(0),
    ).toEqual(new Set(["metrics-1"]));
  });

  it("fails closed for malformed, unmatched, prior-turn, failed, or partial refs", () => {
    const currentRef = evidenceRef("a", "b");
    const priorRef = evidenceRef("c", "d");
    const failedRef = evidenceRef("a", "e");
    const partialRef = evidenceRef("a", "f");
    const projection = project(
      [tool("old", "get_resource", deployment, { evidenceRef: priorRef })],
      [
        tool("current", "get_resource", deployment, {
          evidenceRef: currentRef,
        }),
        tool("failed", "get_resource", deployment, {
          evidenceRef: failedRef,
          isError: true,
        }),
        tool("partial", "get_resource", deployment, {
          evidenceRef: partialRef,
          truncated: true,
        }),
      ],
    );

    for (const evidence of [
      { status: "linked" as const, refs: [evidenceRef("a", "z")] },
      { status: "linked" as const, refs: [priorRef] },
      { status: "linked" as const, refs: [failedRef] },
      { status: "linked" as const, refs: [partialRef] },
      { status: "linked" as const, refs: [currentRef, currentRef] },
      { status: "linked" as const, refs: [] },
      { status: "linked" as const, refs: ["invalid"] },
    ]) {
      expect(
        resolveInvestigationRootCauseEvidence(projection, evidence, 1),
      ).toEqual({ status: "invalid", links: [] });
    }
    expect(
      resolveInvestigationRootCauseEvidence(
        projection,
        { status: "linked", refs: [currentRef, evidenceRef("a", "z")] },
        1,
      ),
    ).toEqual({ status: "invalid", links: [] });

    const duplicateSourceProjection = project([
      tool("first", "get_resource", deployment, {
        evidenceRef: currentRef,
      }),
      tool("second", "get_resource", deployment, {
        evidenceRef: currentRef,
      }),
    ]);
    expect(
      resolveInvestigationRootCauseEvidence(
        duplicateSourceProjection,
        { status: "linked", refs: [currentRef] },
        0,
      ),
    ).toEqual({ status: "invalid", links: [] });
  });

  it("counts ineligible duplicate refs only in the assessment turn", () => {
    const ref = evidenceRef("a", "b");
    const currentDuplicate = project(
      [tool("prior", "get_resource", deployment, { evidenceRef: ref })],
      [
        tool("current", "get_resource", deployment, { evidenceRef: ref }),
        tool("failed-duplicate", "get_resource", deployment, {
          evidenceRef: ref,
          isError: true,
        }),
      ],
    );
    expect(currentDuplicate.evidenceRefSources).toHaveLength(3);
    expect(
      resolveInvestigationRootCauseEvidence(
        currentDuplicate,
        { status: "linked", refs: [ref] },
        1,
      ),
    ).toEqual({ status: "invalid", links: [] });

    const priorDuplicateOnly = project(
      [
        tool("prior-first", "get_resource", deployment, { evidenceRef: ref }),
        tool("prior-second", "get_resource", deployment, {
          evidenceRef: ref,
          isError: true,
        }),
      ],
      [tool("current-only", "get_resource", deployment, { evidenceRef: ref })],
    );
    expect(
      resolveInvestigationRootCauseEvidence(
        priorDuplicateOnly,
        { status: "linked", refs: [ref] },
        1,
      ),
    ).toMatchObject({
      status: "linked",
      links: [{ source: { stepId: "current-only" } }],
    });
  });

  it("rejects a replay that claims refs from different investigation scopes are linked", () => {
    const firstRef = evidenceRef("a", "b");
    const secondRef = evidenceRef("c", "d");
    const projection = project([
      tool("first", "get_resource", deployment, { evidenceRef: firstRef }),
      tool("second", "get_resource", deployment, { evidenceRef: secondRef }),
    ]);

    expect(
      resolveInvestigationRootCauseEvidence(
        projection,
        { status: "linked", refs: [firstRef, secondRef] },
        0,
      ),
    ).toEqual({ status: "invalid", links: [] });
  });

  it("preserves explicit missing and invalid server states without inventing links", () => {
    const projection = project([]);
    expect(
      resolveInvestigationRootCauseEvidence(projection, undefined, 0),
    ).toEqual({ status: "missing", links: [] });
    expect(
      resolveInvestigationRootCauseEvidence(
        projection,
        { status: "missing" },
        0,
      ),
    ).toEqual({ status: "missing", links: [] });
    expect(
      resolveInvestigationRootCauseEvidence(
        projection,
        { status: "invalid" },
        0,
      ),
    ).toEqual({ status: "invalid", links: [] });
  });
});

describe("semantic diagnose evidence projection", () => {
  it("promotes only classified smoking guns and audits every partiality signal", () => {
    const result = project([
      tool("diagnose-1", "diagnose", {
        resource: deployment,
        resourceContext: {
          tier: "diagnostic",
          issueSummary: {
            count: 1,
            highestSeverity: "critical",
            topReason: "CrashLoopBackOff",
          },
          // Static posture stays attached to Resource; it is never its own Key.
          auditSummary: {
            count: 3,
            highestSeverity: "high",
            topFinding: "run-as-root",
          },
          referencedBy: { total: 8, items: [], truncated: true },
          omitted: [{ field: "uses.secrets", reason: "rbac_denied" }],
        },
        pods: 2,
        relatedIssues: [criticalIssue],
        startupBlockers: [
          {
            kind: "Pod",
            name: "api-abc",
            reason: "ImagePullBackOff",
            severity: "critical",
            message: "The image tag does not exist.",
          },
        ],
        crashCause: [
          {
            pods: ["api-abc"],
            container: "api",
            state: "terminated",
            reason: "Error",
            exitCode: 1,
            logLine: "FATAL: missing DATABASE_URL",
            logSource: "previous",
            logLineSelection: "fatal_pattern",
          },
        ],
        crashCauseTruncated: true,
        logsCurrent: [
          {
            pod: "api-abc",
            container: "api",
            logs: {
              lines: ["ERROR database unavailable"],
              totalLines: 30,
              matchedLines: 1,
              fallback: false,
            },
          },
          {
            pod: "api-def",
            container: "api",
            logs: {
              lines: ["server started"],
              totalLines: 1,
              matchedLines: 0,
              fallback: true,
            },
          },
        ],
        logCoverage: {
          resolvedPods: 5,
          selectedPods: 2,
          selectionTruncated: true,
          shownLines: 2,
          totalLines: 8,
          contentTruncated: true,
        },
        events: [warningEvent],
        eventsTotalGroups: 3,
        recentChanges: [
          {
            kind: "Deployment",
            namespace: "shop",
            name: "api",
            changeType: "update",
            timestamp: "2026-09-02T09:55:00Z",
            summary: "image changed",
          },
        ],
        recentChangesSaturated: true,
        dnsContext: { signals: ["lookup failures in application logs"] },
        narrowHint: "log fan-out selected 2 of 5 pods",
        warnings: ["Managed by Helm; edit the source of truth."],
      }),
    ]);

    expect(groupsOf(result.groups, "issue")[0].latest).toMatchObject({
      tier: "key",
      relevance: "target",
    });
    expect(groupsOf(result.groups, "startup")[0].latest.tier).toBe("key");
    expect(groupsOf(result.groups, "crash")[0].latest.tier).toBe("key");
    expect(groupsOf(result.groups, "startup")[0].latest.relevance).toBe(
      "producer-related",
    );
    expect(groupsOf(result.groups, "crash")[0].latest.relevance).toBe(
      "producer-related",
    );
    // The detailed critical Issue owns the Key slot; its aggregate resource
    // rollup stays supporting rather than counting the same signal twice.
    expect(groupsOf(result.groups, "resource")[0].latest.tier).toBe(
      "supporting",
    );
    expect(groupsOf(result.groups, "resource")[0].latest.data).toMatchObject({
      type: "resource",
      warnings: ["Managed by Helm; edit the source of truth."],
    });
    expect(
      groupsOf(result.groups, "logs").map((group) => group.latest.tier),
    ).toEqual(["supporting", "context"]);
    expect(groupsOf(result.groups, "dns")).toHaveLength(1);
    expect(
      result.groups.some((group) => group.identity.includes("audit")),
    ).toBe(false);

    const limitationText = result.limitations
      .map((item) => item.message)
      .join("\n");
    expect(limitationText).toContain("selected 2 of 5");
    expect(limitationText).toContain("retained 2 of 8");
    expect(limitationText).toContain("Additional crash-cause");
    expect(limitationText).toContain("returned 1 of 3 event groups");
    expect(limitationText).toContain("rbac denied");
    expect(limitationText).toContain("Referenced-by relationships");
    expect(limitationText).toContain("recent-change result limit");
    expect(result.coverage).toEqual({
      attempted: 1,
      projected: 1,
      limited: 1,
      checked: 0,
    });
    expect(result.sources[0].primaryGroupId).toBe(
      groupsOf(result.groups, "issue")[0].id,
    );
  });

  it("uses a critical live issueSummary as Key when no detailed issue duplicates it", () => {
    const result = project([
      tool("resource-summary", "diagnose", {
        resource: deployment,
        resourceContext: {
          tier: "basic",
          issueSummary: {
            count: 2,
            highestSeverity: "critical",
            topReason: "Workload unavailable",
          },
        },
        pods: 0,
        events: [],
        recentChanges: [],
      }),
    ]);
    expect(groupsOf(result.groups, "resource")[0].latest.tier).toBe("key");
  });

  it("keeps an explicit replica shortfall or failing controller condition visible as Supporting", () => {
    const result = project([
      tool("diagnose-state", "diagnose", {
        resource: deployment,
        resourceContext: {
          tier: "basic",
          workloadSummary: {
            replicas: { desired: 2, ready: 1, available: 1, unavailable: 1 },
          },
          statusSummary: {
            conditions: [
              {
                type: "ReplicaFailure",
                status: "True",
                reason: "FailedCreate",
              },
            ],
          },
        },
        pods: 1,
        events: [],
        recentChanges: [],
      }),
    ]);
    expect(groupsOf(result.groups, "resource")[0].latest).toMatchObject({
      tier: "supporting",
      tone: "warning",
    });
  });
});

describe("strict evidence adapters", () => {
  it("recognizes GitOps diagnose without inventing workload collection receipts", () => {
    const result = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool("gitops", "diagnose", {
              resource: {
                apiVersion: "argoproj.io/v1alpha1",
                kind: "Application",
                metadata: { namespace: "argocd", name: "shop" },
              },
              pods: 0,
              gitopsDiagnosis: {
                tool: "argocd",
                sync: "OutOfSync",
                health: "Degraded",
              },
              relatedIssues: [],
            }),
          ],
        },
      ],
      {
        kind: "Application",
        group: "argoproj.io",
        namespace: "argocd",
        name: "shop",
      },
    );

    expect(groupsOf(result.groups, "resource")).toHaveLength(1);
    expect(groupsOf(result.groups, "resource")[0].latest).toMatchObject({
      tier: "supporting",
      tone: "warning",
      data: {
        type: "resource",
        gitOpsDiagnosis: { sync: "OutOfSync", health: "Degraded" },
      },
    });
    expect(groupsOf(result.groups, "receipt")).toHaveLength(0);
    expect(result.limitations).toHaveLength(0);
  });

  it("renders the current network diagnose contract instead of rejecting it as a workload", () => {
    const result = project([
      tool("network", "diagnose", {
        subject: { kind: "Service", namespace: "shop", name: "api" },
        verdict: "broken",
        reason: "No ready endpoints",
        diagnosis: {
          class: "fault",
          severity: "critical",
          summary: "The Service has no ready endpoints.",
          route: "api:80",
          nextAction: "Inspect the selected Pods.",
        },
        summary: {
          tested: 1,
          passed: 0,
          failed: 1,
          derived: 0,
          skipped: 1,
          headline: "One route failed and one could not be tested.",
        },
        routes: [
          {
            route: "api:80",
            target: "api:8080",
            outcome: "unreachable",
            failedLayer: "tcp",
            confidence: "real",
            evidence: "connection refused",
          },
        ],
        notTested: [{ route: "api:443", reason: "TLS secret unavailable" }],
      }),
    ]);

    const network = groupsOf(result.groups, "network")[0];
    expect(network.latest).toMatchObject({
      tier: "context",
      relevance: "broader",
      tone: "error",
      summary: "The Service has no ready endpoints.",
    });
    expect(result.limitations).toEqual([
      expect.objectContaining({
        source: "Network path coverage",
        kind: "unknown",
      }),
    ]);
  });

  it("supports all current get_resource producer modes, including safe Secret detail", () => {
    const result = project([
      tool("bare", "get_resource", deployment),
      tool("context", "get_resource", {
        resource: deployment,
        resourceContext: { tier: "basic" },
      }),
      tool("extras", "get_resource", {
        resource: deployment,
        resourceContext: { tier: "basic" },
        events: [warningEvent],
        recentChanges: [],
      }),
      tool("secret-bare", "get_resource", {
        kind: "Secret",
        name: "api-credentials",
        namespace: "shop",
        type: "Opaque",
        keys: ["MONGO_PASSWORD", "API_TOKEN"],
      }),
      tool("secret-wrapped", "get_resource", {
        resource: {
          kind: "Secret",
          name: "api-credentials",
          namespace: "shop",
          type: "Opaque",
          keys: ["MONGO_PASSWORD", "API_TOKEN"],
        },
        resourceContext: { tier: "basic" },
      }),
    ]);

    const resources = groupsOf(result.groups, "resource");
    expect(resources).toHaveLength(2);
    const deploymentGroup = resources.find(
      (group) =>
        group.latest.data.type === "resource" &&
        group.latest.data.resource.kind === "Deployment",
    )!;
    const secretGroup = resources.find(
      (group) =>
        group.latest.data.type === "resource" &&
        group.latest.data.resource.kind === "Secret",
    )!;
    expect(deploymentGroup.observations.map((item) => item.revision)).toEqual([
      1, 2, 3,
    ]);
    expect(
      deploymentGroup.observations.map((item) => item.source.stepId),
    ).toEqual(["bare", "context", "extras"]);
    expect(secretGroup.latest.data).toMatchObject({
      type: "resource",
      resource: {
        apiVersion: "v1",
        kind: "Secret",
        metadata: { namespace: "shop", name: "api-credentials" },
        keys: ["MONGO_PASSWORD", "API_TOKEN"],
      },
    });
    expect(secretGroup.observations.map((item) => item.source.stepId)).toEqual([
      "secret-bare",
      "secret-wrapped",
    ]);
    expect(groupsOf(result.groups, "events")).toHaveLength(1);
    expect(groupsOf(result.groups, "receipt")).toHaveLength(1);
  });

  it("keeps namespace inventory adverse rows as broader context", () => {
    const result = project([
      tool("inventory", "list_resources", [
        {
          kind: "Deployment",
          namespace: "shop",
          name: "api",
          status: "0/2 Ready",
          ready: "0/2",
          issue: "CrashLoopBackOff",
          summaryContext: { health: "unhealthy", issueCount: 1 },
        },
        {
          kind: "Deployment",
          namespace: "shop",
          name: "worker",
          status: "Running",
          summaryContext: { health: "healthy" },
        },
      ]),
    ]);
    const inventory = groupsOf(result.groups, "inventory")[0].latest;
    expect(inventory).toMatchObject({
      tier: "context",
      relevance: "broader",
      tone: "warning",
    });
    expect(inventory.data).toMatchObject({
      type: "inventory",
      resources: [
        expect.objectContaining({ kind: "Deployment", name: "api" }),
        expect.objectContaining({ kind: "Deployment", name: "worker" }),
      ],
    });
  });

  it("keeps unmatched rows from broad issue queries out of lead evidence", () => {
    const unrelated = {
      ...criticalIssue,
      id: "issue-db-crash",
      name: "db",
      reason: "DatabaseCrashLoop",
    };
    const result = project([
      tool(
        "issues-broad",
        "issues",
        { issues: [unrelated], total: 1, total_matched: 1 },
        { summary: JSON.stringify({ namespace: "shop" }) },
      ),
    ]);

    const observation = groupsOf(result.groups, "issue")[0].latest;
    expect(observation).toMatchObject({
      tier: "context",
      data: { type: "issue", relevance: "broader" },
    });
  });

  it("treats API group as part of exact target identity", () => {
    const coreServiceTarget = {
      kind: "Service",
      group: "",
      namespace: "shop",
      name: "api",
    };
    const knative = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool(
              "knative-service",
              "get_resource",
              {
                apiVersion: "serving.knative.dev/v1",
                kind: "Service",
                metadata: { namespace: "shop", name: "api" },
              },
              {
                summary: JSON.stringify({
                  group: "serving.knative.dev",
                  kind: "Service",
                  namespace: "shop",
                  name: "api",
                }),
              },
            ),
          ],
        },
      ],
      coreServiceTarget,
    );
    expect(groupsOf(knative.groups, "resource")[0].latest).toMatchObject({
      relevance: "broader",
      tier: "context",
    });

    const core = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool(
              "core-service",
              "get_resource",
              {
                apiVersion: "v1",
                kind: "Service",
                metadata: { namespace: "shop", name: "api" },
              },
              {
                summary: JSON.stringify({
                  group: "",
                  kind: "Service",
                  namespace: "shop",
                  name: "api",
                }),
              },
            ),
          ],
        },
      ],
      coreServiceTarget,
    );
    expect(groupsOf(core.groups, "resource")[0].latest.relevance).toBe(
      "target",
    );

    const mismatchedIssue = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool("knative-issue", "issues", {
              issues: [
                {
                  ...criticalIssue,
                  id: "knative-service-issue",
                  group: "serving.knative.dev",
                  kind: "Service",
                },
              ],
              total: 1,
              total_matched: 1,
            }),
          ],
        },
      ],
      coreServiceTarget,
    );
    expect(groupsOf(mismatchedIssue.groups, "issue")[0].latest.relevance).toBe(
      "broader",
    );
  });

  it("treats an unexpressible event group as unspecified while honoring explicit groups", () => {
    const eventsFor = (group?: string) =>
      project([
        tool(
          `events-${group ?? "unspecified"}`,
          "get_events",
          { events: [warningEvent] },
          {
            summary: JSON.stringify({
              ...(group === undefined ? {} : { group }),
              kind: "Deployment",
              namespace: "shop",
              name: "api",
            }),
          },
        ),
      ]);

    for (const group of [undefined, "apps"] as const) {
      expect(
        groupsOf(eventsFor(group).groups, "events")[0].latest,
      ).toMatchObject({
        relevance: "target",
        tier: "supporting",
      });
    }
    expect(groupsOf(eventsFor("").groups, "events")[0].latest).toMatchObject({
      relevance: "broader",
      tier: "context",
    });
    expect(
      groupsOf(eventsFor("serving.knative.dev").groups, "events")[0].latest,
    ).toMatchObject({ relevance: "broader", tier: "context" });
  });

  it("labels direct Pod startup, crash, and log evidence as target evidence", () => {
    const podTarget = {
      kind: "Pod",
      group: "",
      namespace: "shop",
      name: "api-abc",
    };
    const result = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool(
              "diagnose-pod",
              "diagnose",
              {
                resource: {
                  apiVersion: "v1",
                  kind: "Pod",
                  metadata: { namespace: "shop", name: "api-abc" },
                },
                resourceContext: { tier: "basic" },
                startupBlockers: [
                  {
                    kind: "Pod",
                    name: "api-abc",
                    reason: "ImagePullBackOff",
                    severity: "critical",
                    message: "The image could not be pulled.",
                  },
                ],
                crashCause: [
                  {
                    pods: ["api-abc"],
                    container: "api",
                    state: "terminated",
                    reason: "Error",
                    exitCode: 1,
                    logLine: "FATAL: configuration missing",
                    logSource: "previous",
                    logLineSelection: "fatal_pattern",
                  },
                ],
                logsCurrent: [
                  {
                    pod: "api-abc",
                    container: "api",
                    logs: {
                      lines: ["FATAL: configuration missing"],
                      totalLines: 1,
                      matchedLines: 1,
                      fallback: false,
                    },
                  },
                ],
              },
              {
                summary: JSON.stringify({
                  group: "",
                  kind: "Pod",
                  namespace: "shop",
                  name: "api-abc",
                }),
              },
            ),
          ],
        },
      ],
      podTarget,
    );

    for (const kind of ["startup", "crash", "logs"] as const) {
      expect(groupsOf(result.groups, kind)[0].latest.relevance).toBe("target");
    }
  });

  it("does not let a later broad query overwrite producer-established issue relevance", () => {
    const relatedPodIssue = {
      ...criticalIssue,
      id: "issue-api-pod-crash",
      kind: "Pod",
      group: "",
      name: "api-abc",
    };
    const result = project([
      tool("diagnose-target", "diagnose", {
        resource: deployment,
        resourceContext: { tier: "basic" },
        relatedIssues: [relatedPodIssue],
      }),
      tool("issues-broad", "issues", {
        issues: [relatedPodIssue],
        total: 1,
        total_matched: 1,
      }),
    ]);

    const group = groupsOf(result.groups, "issue")[0];
    expect(group.observations).toHaveLength(2);
    expect(group.observations[1]).toMatchObject({
      relevance: "broader",
      tier: "context",
      changedFromPrevious: false,
      source: { stepId: "issues-broad" },
    });
    expect(group.latest).toMatchObject({
      relevance: "producer-related",
      tier: "key",
      source: { stepId: "diagnose-target" },
    });

    const reversed = project([
      tool("issues-broad", "issues", {
        issues: [relatedPodIssue],
        total: 1,
        total_matched: 1,
      }),
      tool("diagnose-target", "diagnose", {
        resource: deployment,
        resourceContext: { tier: "basic" },
        relatedIssues: [relatedPodIssue],
      }),
    ]);
    expect(groupsOf(reversed.groups, "issue")[0].latest).toMatchObject({
      relevance: "producer-related",
      tier: "key",
      source: { stepId: "diagnose-target" },
    });
  });

  it("keeps a changed broad payload in history without laundering it into the lead card", () => {
    const relatedPodIssue = {
      ...criticalIssue,
      id: "issue-api-pod-crash",
      kind: "Pod",
      group: "",
      name: "api-abc",
    };
    const result = project([
      tool("diagnose-target", "diagnose", {
        resource: deployment,
        resourceContext: { tier: "basic" },
        relatedIssues: [relatedPodIssue],
      }),
      tool("issues-broad", "issues", {
        issues: [
          {
            ...relatedPodIssue,
            reason: "ImagePullBackOff",
            message: "A later broad read saw an image pull failure.",
          },
        ],
        total: 1,
        total_matched: 1,
      }),
    ]);

    const group = groupsOf(result.groups, "issue")[0];
    expect(group.observations[1]).toMatchObject({
      changedFromPrevious: true,
      title: "ImagePullBackOff",
      relevance: "broader",
    });
    expect(group.latest).toMatchObject({
      title: "CrashLoopBackOff",
      relevance: "producer-related",
      source: { stepId: "diagnose-target" },
    });
  });

  it("partitions weak log, startup, and crash identities by proof scope", () => {
    const evidence = {
      resourceContext: { tier: "basic" },
      startupBlockers: [
        {
          kind: "Pod",
          name: "api-abc",
          reason: "Unschedulable",
          severity: "critical",
          message: "No matching nodes",
        },
      ],
      crashCause: [
        {
          pods: ["api-abc"],
          container: "api",
          state: "terminated",
          reason: "Error",
          exitCode: 1,
          logLine: "FATAL: missing config",
          logSource: "previous",
          logLineSelection: "fatal_pattern",
        },
      ],
      logsCurrent: [
        {
          pod: "api-abc",
          container: "api",
          logs: {
            lines: ["FATAL: missing config"],
            totalLines: 1,
            matchedLines: 1,
            fallback: false,
          },
        },
      ],
    };
    const result = project([
      tool("diagnose-target", "diagnose", {
        ...evidence,
        resource: deployment,
      }),
      tool(
        "diagnose-sibling",
        "diagnose",
        {
          ...evidence,
          resource: {
            ...deployment,
            metadata: { namespace: "other", name: "worker" },
          },
        },
        {
          summary: JSON.stringify({
            kind: "Deployment",
            namespace: "other",
            name: "worker",
          }),
        },
      ),
    ]);

    for (const kind of ["logs", "startup", "crash"] as const) {
      const groups = groupsOf(result.groups, kind);
      expect(groups).toHaveLength(2);
      expect(groups.map((group) => group.latest.relevance).sort()).toEqual([
        "broader",
        "producer-related",
      ]);
    }
  });

  it("keeps same-relevance log reads from different namespaces separate", () => {
    const logs = {
      lines: ["FATAL: missing config"],
      totalLines: 1,
      matchedLines: 1,
      fallback: false,
    };
    const result = project([
      tool("logs-shop", "get_pod_logs", logs, {
        summary: JSON.stringify({
          namespace: "shop",
          name: "api-abc",
          container: "api",
        }),
      }),
      tool("logs-other", "get_pod_logs", logs, {
        summary: JSON.stringify({
          namespace: "other",
          name: "api-abc",
          container: "api",
        }),
      }),
    ]);

    const groups = groupsOf(result.groups, "logs");
    expect(groups).toHaveLength(2);
    expect(groups.every((group) => group.latest.relevance === "broader")).toBe(
      true,
    );
    expect(groups.map((group) => group.observations.length)).toEqual([1, 1]);
  });

  it("does not promote a sibling diagnose bundle as evidence for the target", () => {
    const sibling = {
      ...deployment,
      metadata: { namespace: "shop", name: "worker" },
    };
    const siblingIssue = {
      ...criticalIssue,
      id: "issue-worker-crash",
      name: "worker",
      reason: "WorkerCrashLoop",
    };
    const result = project([
      tool(
        "diagnose-sibling",
        "diagnose",
        {
          resource: sibling,
          resourceContext: {
            tier: "basic",
            issueSummary: {
              count: 1,
              highestSeverity: "critical",
              topReason: "WorkerCrashLoop",
            },
          },
          relatedIssues: [siblingIssue],
          events: [warningEvent],
          logsCurrent: [
            {
              pod: "worker-abc",
              container: "worker",
              logs: {
                lines: ["FATAL worker crashed"],
                totalLines: 1,
                matchedLines: 1,
                fallback: false,
              },
            },
          ],
        },
        {
          summary: JSON.stringify({
            kind: "Deployment",
            namespace: "shop",
            name: "worker",
          }),
        },
      ),
    ]);

    expect(result.groups).not.toHaveLength(0);
    expect(
      result.groups.every((group) => group.latest.tier === "context"),
    ).toBe(true);
    expect(
      result.groups.every((group) => group.latest.relevance === "broader"),
    ).toBe(true);
  });

  it("caps generic resource, event, log, and list results without proven target scope", () => {
    const sibling = {
      ...deployment,
      metadata: { namespace: "shop", name: "worker" },
    };
    const result = project([
      tool(
        "resource-sibling",
        "get_resource",
        {
          resource: sibling,
          resourceContext: {
            tier: "basic",
            issueSummary: {
              count: 1,
              highestSeverity: "critical",
              topReason: "Worker unavailable",
            },
          },
        },
        {
          summary: JSON.stringify({
            kind: "Deployment",
            namespace: "shop",
            name: "worker",
          }),
        },
      ),
      tool(
        "events-namespace",
        "get_events",
        { events: [warningEvent] },
        { summary: JSON.stringify({ namespace: "shop" }) },
      ),
      tool(
        "logs-child",
        "get_pod_logs",
        {
          lines: ["FATAL child Pod crashed"],
          totalLines: 1,
          matchedLines: 1,
          fallback: false,
        },
        {
          summary: JSON.stringify({
            namespace: "shop",
            name: "api-abc",
            container: "api",
          }),
        },
      ),
      tool(
        "list-namespace",
        "list_resources",
        [
          {
            kind: "Deployment",
            namespace: "shop",
            name: "api",
            summaryContext: { health: "unhealthy", issueCount: 1 },
          },
        ],
        { summary: JSON.stringify({ namespace: "shop" }) },
      ),
    ]);

    for (const kind of ["resource", "events", "logs", "inventory"] as const) {
      const observation = groupsOf(result.groups, kind)[0].latest;
      expect(observation).toMatchObject({
        tier: "context",
        relevance: "broader",
      });
    }
  });

  it("merges repeated observations without inflating evidence count", () => {
    const first = { ...criticalIssue, severity: "warning" };
    const second = { ...criticalIssue, message: "Now affecting all replicas." };
    const result = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool("issues-a", "issues", {
              issues: [first],
              total: 1,
              total_matched: 1,
            }),
          ],
        },
        {
          verify: true,
          timeline: [
            tool("issues-b", "issues", {
              issues: [second],
              total: 1,
              total_matched: 1,
            }),
          ],
        },
      ],
      target,
    );

    const issues = groupsOf(result.groups, "issue");
    expect(issues).toHaveLength(1);
    expect(issues[0].observations).toHaveLength(2);
    expect(issues[0].observations.map((item) => item.revision)).toEqual([1, 2]);
    expect(issues[0].observations.map((item) => item.source.id)).toEqual([
      "turn-0-step-issues-a",
      "turn-1-step-issues-b",
    ]);
    expect(issues[0].observations.map((item) => item.source.phase)).toEqual([
      "initial",
      "verification",
    ]);
    expect(
      issues[0].observations.map((item) => item.changedFromPrevious),
    ).toEqual([false, true]);
    expect(issues[0].latest.tier).toBe("key");
    expect(issues[0].latest.source.args).toContain('"namespace":"shop"');
  });

  it("recomputes residual chronology after a cited verification is promoted", () => {
    const ref = evidenceRef("a", "b");
    const result = projectInvestigationEvidence(
      [
        {
          status: "done",
          timeline: [
            tool("initial", "issues", {
              issues: [criticalIssue],
              total: 1,
              total_matched: 1,
            }),
          ],
        },
        {
          status: "done",
          verify: true,
          timeline: [
            tool(
              "verification",
              "diagnose",
              {
                resource: deployment,
                resourceContext: { tier: "basic" },
                relatedIssues: [
                  {
                    ...criticalIssue,
                    message: "The verification still sees the failure.",
                  },
                ],
              },
              { evidenceRef: ref },
            ),
          ],
        },
      ],
      target,
    );
    const group = groupsOf(result.groups, "issue")[0];
    const verificationSource = result.sources.find(
      (source) => source.stepId === "verification",
    )!;
    const residual = investigationEvidenceGroupWithoutSources(
      group,
      new Set([verificationSource.id]),
    );

    expect(
      group.observations.map((observation) => observation.historical),
    ).toEqual([true, false]);
    expect(residual).toMatchObject({
      historical: true,
      firstOrder: 0,
      latest: {
        revision: 1,
        changedFromPrevious: false,
        source: { stepId: "initial" },
      },
    });
  });

  it("does not promote malformed change correlation or neutral DNS configuration", () => {
    const change = {
      kind: "Deployment",
      namespace: "shop",
      name: "api",
      changeType: "update",
      timestamp: "2026-09-02T09:55:00Z",
      summary: "image changed",
    };
    const result = project([
      tool("strict-semantics", "diagnose", {
        resource: deployment,
        resourceContext: { tier: "basic" },
        pods: 0,
        recentChanges: [change],
        changeContext: {
          changed: "false",
          what: "not a valid producer value",
        },
        dnsContext: {
          signals: [
            "api uses dnsPolicy=None",
            "api sets dnsConfig.nameservers",
          ],
        },
        events: [],
      }),
    ]);

    expect(groupsOf(result.groups, "changes")[0].latest.tier).toBe("context");
    expect(groupsOf(result.groups, "dns")[0].latest).toMatchObject({
      tier: "context",
      tone: "info",
      title: "DNS configuration",
    });
    expect(result.limitations).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ source: "Change correlation" }),
      ]),
    );
  });

  it("only moves domains actually rechecked by verification into Earlier evidence", () => {
    const relatedPodIssue = {
      ...criticalIssue,
      id: "issue-api-pod-crash",
      kind: "Pod",
      group: "",
      name: "api-abc",
    };
    const initial = {
      resource: deployment,
      resourceContext: { tier: "basic" },
      pods: 1,
      relatedIssues: [relatedPodIssue],
      crashCause: [
        {
          pods: ["api-abc"],
          container: "api",
          state: "terminated",
          reason: "Error",
          exitCode: 1,
          logLine: "FATAL: missing DATABASE_URL",
          logSource: "previous",
          logLineSelection: "fatal_pattern",
        },
      ],
      events: [warningEvent],
      recentChanges: [],
    };
    const resourceOnly = projectInvestigationEvidence(
      [
        { status: "done", timeline: [tool("initial", "diagnose", initial)] },
        {
          status: "done",
          verify: true,
          timeline: [tool("resource-only", "get_resource", deployment)],
        },
      ],
      target,
    );
    expect(groupsOf(resourceOnly.groups, "issue")[0].historical).toBe(false);
    expect(groupsOf(resourceOnly.groups, "crash")[0].historical).toBe(false);

    const targetOnlyEventVerification = projectInvestigationEvidence(
      [
        { status: "done", timeline: [tool("initial", "diagnose", initial)] },
        {
          status: "done",
          verify: true,
          timeline: [
            tool("resource-events", "get_resource", {
              resource: deployment,
              resourceContext: { tier: "basic" },
              events: [],
            }),
          ],
        },
      ],
      target,
    );
    expect(
      groupsOf(targetOnlyEventVerification.groups, "events")[0].historical,
    ).toBe(false);

    const fullVerification = projectInvestigationEvidence(
      [
        { status: "done", timeline: [tool("initial", "diagnose", initial)] },
        {
          status: "done",
          timeline: [
            tool("broad-reread", "issues", {
              issues: [relatedPodIssue],
              total: 1,
              total_matched: 1,
            }),
          ],
        },
        {
          status: "done",
          verify: true,
          timeline: [
            tool("verified", "diagnose", {
              resource: {
                ...deployment,
                status: { readyReplicas: 1, replicas: 1 },
              },
              resourceContext: {
                tier: "basic",
                workloadSummary: {
                  replicas: {
                    desired: 1,
                    ready: 1,
                    available: 1,
                    unavailable: 0,
                  },
                },
              },
              pods: 1,
              ...completeLogProof(),
              events: [],
              recentChanges: [],
            }),
          ],
        },
      ],
      target,
    );
    const verifiedIssue = groupsOf(fullVerification.groups, "issue")[0];
    expect(
      verifiedIssue.observations.map((observation) => observation.relevance),
    ).toEqual(["producer-related", "broader"]);
    expect(verifiedIssue.historical).toBe(true);
    expect(groupsOf(fullVerification.groups, "crash")[0].historical).toBe(true);
    expect(groupsOf(fullVerification.groups, "events")[0].historical).toBe(
      true,
    );
    expect(groupsOf(fullVerification.groups, "resource")[0].historical).toBe(
      false,
    );

    const siblingVerification = projectInvestigationEvidence(
      [
        { status: "done", timeline: [tool("initial", "diagnose", initial)] },
        {
          status: "done",
          verify: true,
          timeline: [
            tool(
              "verified-sibling",
              "diagnose",
              {
                resource: {
                  ...deployment,
                  metadata: { namespace: "shop", name: "worker" },
                },
                pods: 1,
                events: [],
                recentChanges: [],
              },
              {
                summary: JSON.stringify({
                  kind: "Deployment",
                  namespace: "shop",
                  name: "worker",
                }),
              },
            ),
          ],
        },
      ],
      target,
    );
    expect(groupsOf(siblingVerification.groups, "issue")[0].historical).toBe(
      false,
    );
    expect(groupsOf(siblingVerification.groups, "crash")[0].historical).toBe(
      false,
    );
  });

  it("retires only semantic domains the verification actually covered", () => {
    const initial = {
      resource: deployment,
      pods: 1,
      relatedIssues: [criticalIssue],
      startupBlockers: [
        {
          kind: "Pod",
          name: "api-abc",
          reason: "ImagePullBackOff",
          severity: "critical",
          message: "The image could not be pulled.",
        },
      ],
      crashCause: [
        {
          pods: ["api-abc"],
          container: "api",
          state: "terminated",
          reason: "Error",
          exitCode: 1,
          logLine: "FATAL: missing DATABASE_URL",
          logSource: "previous",
          logLineSelection: "fatal_pattern",
        },
      ],
      dnsContext: { signals: ["DNS timeout in application logs"] },
      events: [],
      recentChanges: [],
    };
    const result = projectInvestigationEvidence(
      [
        { status: "done", timeline: [tool("initial", "diagnose", initial)] },
        {
          status: "done",
          verify: true,
          timeline: [
            tool("partial-verification", "diagnose", {
              resource: {
                ...deployment,
                status: { readyReplicas: 1, replicas: 1 },
              },
              pods: 1,
              relatedIssues: [],
              startupBlockers: [],
              logsError: "pods/log is forbidden",
              events: [],
              recentChanges: [],
            }),
          ],
        },
      ],
      target,
    );

    expect(groupsOf(result.groups, "issue")[0].historical).toBe(true);
    expect(groupsOf(result.groups, "startup")[0].historical).toBe(true);
    expect(groupsOf(result.groups, "crash")[0].historical).toBe(false);
    expect(groupsOf(result.groups, "dns")[0].historical).toBe(false);
  });

  it.each([
    ["pod selection was truncated", { selectionTruncated: true }],
    ["log content was truncated", { contentTruncated: true }],
  ])("does not retire crash evidence when %s", (_label, coveragePatch) => {
    const proof = completeLogProof();
    const result = projectInvestigationEvidence(
      [
        {
          status: "done",
          timeline: [
            tool("initial", "diagnose", {
              resource: deployment,
              pods: 1,
              crashCause: [
                {
                  pods: ["api-abc"],
                  container: "api",
                  state: "terminated",
                  reason: "Error",
                  exitCode: 1,
                  logLine: "FATAL: missing DATABASE_URL",
                  logSource: "previous",
                  logLineSelection: "fatal_pattern",
                },
              ],
            }),
          ],
        },
        {
          status: "done",
          verify: true,
          timeline: [
            tool("verification", "diagnose", {
              resource: deployment,
              pods: 1,
              ...proof,
              logCoverage: {
                ...proof.logCoverage,
                ...coveragePatch,
              },
            }),
          ],
        },
      ],
      target,
    );

    expect(groupsOf(result.groups, "crash")[0].historical).toBe(false);
  });

  it.each([
    ["one container", { container: "sidecar" }, "sidecar"],
    ["a recent time window", { since: "5m" }, "api"],
    ["a shorter log tail", { tail_lines: 20 }, "api"],
  ])(
    "does not retire target-wide crash evidence after checking only %s",
    (_label, queryPatch, container) => {
      const result = projectInvestigationEvidence(
        [
          {
            status: "done",
            timeline: [
              tool("initial", "diagnose", {
                resource: deployment,
                pods: 1,
                crashCause: [
                  {
                    pods: ["api-abc"],
                    container: "api",
                    state: "terminated",
                    reason: "Error",
                    exitCode: 1,
                    logLine: "FATAL: missing DATABASE_URL",
                    logSource: "previous",
                    logLineSelection: "fatal_pattern",
                  },
                ],
              }),
            ],
          },
          {
            status: "done",
            verify: true,
            timeline: [
              tool(
                "narrow-verification",
                "diagnose",
                {
                  resource: deployment,
                  pods: 1,
                  ...completeLogProof(container),
                },
                {
                  summary: JSON.stringify({
                    group: "apps",
                    kind: "Deployment",
                    namespace: "shop",
                    name: "api",
                    ...queryPatch,
                  }),
                },
              ),
            ],
          },
        ],
        target,
      );

      expect(groupsOf(result.groups, "crash")[0].historical).toBe(false);
    },
  );

  it("keeps retirement monotonic across later verification of another proof scope", () => {
    const initialIssue = {
      resource: deployment,
      resourceContext: { tier: "basic" },
      pods: 1,
      relatedIssues: [criticalIssue],
      events: [],
      recentChanges: [],
    };
    const result = projectInvestigationEvidence(
      [
        {
          status: "done",
          timeline: [tool("initial", "diagnose", initialIssue)],
        },
        {
          status: "done",
          verify: true,
          timeline: [
            tool("issue-cleared", "diagnose", {
              resource: {
                ...deployment,
                status: { readyReplicas: 1, replicas: 1 },
              },
              resourceContext: { tier: "basic" },
              pods: 1,
              relatedIssues: [],
              events: [],
              recentChanges: [],
            }),
          ],
        },
        {
          status: "done",
          verify: true,
          timeline: [tool("later-resource", "get_resource", deployment)],
        },
      ],
      target,
    );

    expect(groupsOf(result.groups, "issue")[0].historical).toBe(true);
  });

  it("retires an exact-target issue observed by issues after target diagnosis verifies it", () => {
    const result = projectInvestigationEvidence(
      [
        {
          status: "done",
          timeline: [
            tool(
              "issue-query",
              "issues",
              {
                issues: [criticalIssue],
                total: 1,
                total_matched: 1,
              },
              {
                summary: JSON.stringify({
                  group: "apps",
                  kind: "Deployment",
                  namespace: "shop",
                  name: "api",
                }),
              },
            ),
          ],
        },
        {
          status: "done",
          verify: true,
          timeline: [
            tool("verified", "diagnose", {
              resource: {
                ...deployment,
                status: { readyReplicas: 1, replicas: 1 },
              },
              resourceContext: { tier: "basic" },
              pods: 1,
              relatedIssues: [],
              events: [],
              recentChanges: [],
            }),
          ],
        },
      ],
      target,
    );

    expect(groupsOf(result.groups, "issue")[0]).toMatchObject({
      historical: true,
      latest: { relevance: "target" },
    });
  });

  it("treats Argo Rollout diagnose and verification as workload evidence", () => {
    const rolloutTarget = {
      kind: "Rollout",
      group: "argoproj.io",
      namespace: "shop",
      name: "api",
    };
    const rollout = {
      apiVersion: "argoproj.io/v1alpha1",
      kind: "Rollout",
      metadata: { namespace: "shop", name: "api" },
      status: { readyReplicas: 1, replicas: 1 },
    };
    const rolloutIssue = {
      ...criticalIssue,
      id: "issue-rollout-crash",
      kind: "Rollout",
      group: "argoproj.io",
    };
    const result = projectInvestigationEvidence(
      [
        {
          status: "done",
          timeline: [
            tool("rollout-initial", "diagnose", {
              resource: rollout,
              relatedIssues: [rolloutIssue],
              pods: 1,
              events: [],
              recentChanges: [],
            }),
          ],
        },
        {
          status: "done",
          verify: true,
          timeline: [
            tool("rollout-verified", "diagnose", {
              resource: rollout,
              relatedIssues: [],
              pods: 1,
              events: [],
              recentChanges: [],
            }),
          ],
        },
      ],
      rolloutTarget,
    );

    expect(groupsOf(result.groups, "issue")[0].historical).toBe(true);
    expect(
      groupsOf(result.groups, "receipt").some(
        (group) =>
          group.latest.data.type === "receipt" &&
          group.latest.data.checked === "issues",
      ),
    ).toBe(true);
  });

  it("retires previous-log evidence only for the pod and container rechecked", () => {
    const result = projectInvestigationEvidence(
      [
        {
          status: "done",
          timeline: [
            tool("initial", "diagnose", {
              resource: deployment,
              resourceContext: { tier: "basic" },
              pods: 2,
              logsPrevious: [
                {
                  pod: "api-abc",
                  container: "api",
                  logs: {
                    lines: ["FATAL: missing DATABASE_URL"],
                    totalLines: 1,
                    matchedLines: 1,
                    fallback: false,
                  },
                },
                {
                  pod: "api-def",
                  container: "api",
                  logs: {
                    lines: ["ERROR: database unavailable"],
                    totalLines: 1,
                    matchedLines: 1,
                    fallback: false,
                  },
                },
              ],
              events: [],
              recentChanges: [],
            }),
          ],
        },
        {
          status: "done",
          verify: true,
          timeline: [
            tool("verified", "diagnose", {
              resource: deployment,
              resourceContext: { tier: "basic" },
              pods: 2,
              expectedPreviousLogAbsences: [
                { pod: "api-abc", container: "api" },
              ],
              logsPrevious: [
                {
                  pod: "api-abc",
                  container: "api",
                  error: "previous terminated container not found",
                },
              ],
              events: [],
              recentChanges: [],
            }),
          ],
        },
      ],
      target,
    );

    const logGroups = groupsOf(result.groups, "logs");
    expect(
      logGroups.find(
        (group) =>
          group.latest.data.type === "logs" &&
          group.latest.data.pod === "api-abc",
      )?.historical,
    ).toBe(true);
    expect(
      logGroups.find(
        (group) =>
          group.latest.data.type === "logs" &&
          group.latest.data.pod === "api-def",
      )?.historical,
    ).toBe(false);
  });

  it("keeps unknown tools in Activity and limits invalid known contracts", () => {
    const result = project([
      tool("other", "query_prometheus", { data: "ignored" }),
      tool("bad-events", "get_events", { events: [{ nope: true }] }),
    ]);
    expect(result.sources.map((source) => source.stepId)).toEqual([
      "bad-events",
    ]);
    expect(result.groups).toHaveLength(0);
    expect(result.limitations).toHaveLength(1);
    expect(result.limitations[0].message).toContain(
      "current structured evidence contract",
    );
  });

  it("never parses a transcript-truncated result", () => {
    const result = project([
      tool(
        "cut",
        "issues",
        { issues: [criticalIssue], total: 1, total_matched: 1 },
        {
          truncated: true,
        },
      ),
    ]);
    expect(result.groups).toHaveLength(0);
    expect(result.limitations[0]).toMatchObject({ kind: "truncated" });
    expect(result.limitations[0].message).toContain("did not parse");
  });

  it("projects non-empty older results but never turns unknown outcomes into receipts", () => {
    const result = project([
      tool(
        "old-data",
        "get_events",
        { events: [warningEvent] },
        { isError: undefined },
      ),
      tool("old-empty", "get_events", { events: [] }, { isError: undefined }),
    ]);
    expect(groupsOf(result.groups, "events")).toHaveLength(1);
    expect(groupsOf(result.groups, "receipt")).toHaveLength(0);
    expect(result.limitations).toHaveLength(1);
    expect(
      result.limitations[0].sources.map((source) => source.stepId),
    ).toEqual(["old-data", "old-empty"]);
    expect(result.coverage.checked).toBe(0);
  });
});

describe("honest zero and partial-result states", () => {
  it("treats empty pod logs as unknown, never Checked", () => {
    const result = project([
      tool("logs", "get_pod_logs", {
        lines: [],
        totalLines: 0,
        matchedLines: 0,
        fallback: false,
      }),
    ]);
    expect(groupsOf(result.groups, "logs")).toHaveLength(0);
    expect(groupsOf(result.groups, "receipt")).toHaveLength(0);
    expect(result.limitations[0]).toMatchObject({ kind: "unknown" });
    expect(result.coverage.checked).toBe(0);
  });

  it("keeps benign filtered request logs in Context", () => {
    const result = project([
      tool("logs", "get_pod_logs", {
        lines: [
          "GET /api/issues?severity=critical%2Cwarning HTTP/1.1 200",
          "GET /api/health HTTP/1.1 200",
        ],
        totalLines: 50,
        matchedLines: 2,
        fallback: false,
      }),
    ]);

    expect(groupsOf(result.groups, "logs")[0].latest).toMatchObject({
      tier: "context",
      tone: "neutral",
    });
  });

  it("classifies ANSI-colored failures after normalizing their log text", () => {
    const podTarget = {
      kind: "Pod",
      group: "",
      namespace: "shop",
      name: "api-abc",
    };
    const result = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool(
              "logs",
              "get_pod_logs",
              {
                lines: ["\u001b[31mERROR\u001b[0m Authentication failed"],
                totalLines: 1,
                matchedLines: 1,
                fallback: false,
              },
              {
                summary: JSON.stringify({
                  group: "",
                  kind: "Pod",
                  namespace: "shop",
                  name: "api-abc",
                }),
              },
            ),
          ],
        },
      ],
      podTarget,
    );
    const observation = groupsOf(result.groups, "logs")[0].latest;

    expect(observation).toMatchObject({ tier: "supporting", tone: "warning" });
    expect(observation.data).toMatchObject({
      type: "logs",
      logs: { lines: ["ERROR Authentication failed"] },
    });
  });

  it("renders producer-proven previous-log absence as a compact Checked receipt", () => {
    const result = project([
      tool("diagnose", "diagnose", {
        resource: deployment,
        resourceContext: { tier: "basic" },
        pods: 1,
        expectedPreviousLogAbsences: [{ pod: "api-abc", container: "api" }],
        logsPrevious: [
          {
            pod: "api-abc",
            container: "api",
            error: "previous terminated container not found",
          },
        ],
        events: [],
        recentChanges: [],
      }),
    ]);
    expect(
      groupsOf(result.groups, "receipt").some(
        (group) =>
          group.latest.data.type === "receipt" &&
          group.latest.data.checked === "logs",
      ),
    ).toBe(true);
    expect(
      result.limitations.some((item) => item.source === "api-abc / api"),
    ).toBe(false);
  });

  it("keeps uncorrelated changes in Context and promotes producer correlation only", () => {
    const change = {
      kind: "Deployment",
      namespace: "shop",
      name: "api",
      changeType: "update",
      timestamp: "2026-09-02T09:55:00Z",
      summary: "image changed",
    };
    const result = project([
      tool("plain", "get_changes", { changes: [change] }),
      tool("correlated", "diagnose", {
        resource: deployment,
        resourceContext: { tier: "basic" },
        pods: 0,
        recentChanges: [change],
        changeContext: {
          changed: true,
          what: "pod template changed before the failure",
          evidence: "ReplicaSet revision advanced within the issue window.",
        },
        events: [],
      }),
    ]);
    expect(
      groupsOf(result.groups, "changes").map((group) => group.latest.tier),
    ).toEqual(["context", "supporting"]);
  });

  it("creates receipts only for confirmed complete zero-result reads", () => {
    const result = project([
      tool("diagnose", "diagnose", {
        resource: deployment,
        resourceContext: { tier: "basic" },
        pods: 0,
        events: [],
        recentChanges: [],
      }),
      tool("events", "get_events", { events: [] }),
      tool("issues", "issues", { issues: [], total: 0, total_matched: 0 }),
      tool("changes", "get_changes", { changes: [] }),
      tool("inventory", "list_resources", []),
    ]);
    expect(
      groupsOf(result.groups, "receipt").map((group) => group.latest.data),
    ).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ checked: "events" }),
        expect.objectContaining({ checked: "issues" }),
        expect.objectContaining({ checked: "changes" }),
      ]),
    );
    // list_resources uses [] for some RBAC-filtered reads, so its emptiness is
    // never a positive receipt even when the transport itself succeeded. The
    // narrow event/change producers have the same ambiguity today; the targeted
    // semantic diagnose bundle is authoritative because access is checked before
    // that workload path runs.
    expect(
      groupsOf(result.groups, "receipt").some(
        (group) =>
          group.latest.data.type === "receipt" &&
          group.latest.data.checked === "inventory",
      ),
    ).toBe(false);
    expect(result.limitations.map((item) => item.source)).toEqual(
      expect.arrayContaining([
        "Events",
        "Recent changes",
        "Resource inventory",
      ]),
    );
    expect(result.coverage).toEqual({
      attempted: 5,
      projected: 2,
      limited: 3,
      checked: 1,
    });
    expect(
      groupsOf(result.groups, "receipt").find(
        (group) =>
          group.latest.data.type === "receipt" &&
          group.latest.data.checked === "events",
      )?.latest,
    ).toMatchObject({
      title: "No matching warning events",
      data: {
        message: "The warning-event query completed and returned no groups.",
      },
    });
  });

  it("treats RBAC-limited recent changes as incomplete without exposing counts", () => {
    const result = project([
      tool("diagnose", "diagnose", {
        resource: deployment,
        resourceContext: { tier: "basic" },
        pods: 0,
        events: [],
        recentChanges: [],
        recentChangesCoverageLimited: true,
      }),
    ]);

    expect(
      groupsOf(result.groups, "receipt").some(
        (group) =>
          group.latest.data.type === "receipt" &&
          group.latest.data.checked === "changes",
      ),
    ).toBe(false);
    expect(result.limitations).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          source: "Recent changes",
          kind: "unknown",
          message: expect.stringContaining(
            "could not confirm permission to read every referenced source",
          ),
        }),
      ]),
    );
    expect(JSON.stringify(result)).not.toContain("2 recent changes");
  });

  it("does not trust malformed recent-change coverage metadata", () => {
    const result = project([
      tool("diagnose", "diagnose", {
        resource: deployment,
        resourceContext: { tier: "basic" },
        pods: 0,
        events: [],
        recentChanges: [],
        recentChangesCoverageLimited: "yes",
      }),
    ]);

    expect(
      groupsOf(result.groups, "receipt").some(
        (group) =>
          group.latest.data.type === "receipt" &&
          group.latest.data.checked === "changes",
      ),
    ).toBe(false);
    expect(result.limitations).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ source: "Recent changes" }),
      ]),
    );
  });

  it("does not call a partial empty read Checked", () => {
    const result = project([
      tool("issues", "issues", {
        issues: [],
        total: 0,
        total_matched: 0,
        visibility: {
          state: "degraded",
          impact: "Radar cannot read core workload resources.",
        },
      }),
      tool("changes", "get_changes", {
        changes: [],
        sourcesErrored: ["helm: permission denied"],
      }),
      tool("events", "get_events", {
        events: [],
        narrowHint: "returned 0 of 12 groups",
      }),
    ]);
    expect(groupsOf(result.groups, "receipt")).toHaveLength(0);
    expect(result.coverage.checked).toBe(0);
    expect(result.coverage.limited).toBe(3);
  });

  it("preserves neighborhood evidence and every declared coverage gap", () => {
    const result = project([
      tool("neighbors", "get_neighborhood", {
        root: {
          kind: "Deployment",
          group: "apps",
          namespace: "shop",
          name: "api",
        },
        subgraph: {
          nodes: [
            {
              id: "deployment/shop/api",
              kind: "Deployment",
              name: "api",
              status: "unhealthy",
              data: { namespace: "shop" },
            },
          ],
          edges: [],
        },
        truncated: true,
        narrowHint: "subgraph capped at 25 nodes",
        omitted: [
          { field: "uses.secrets", reason: "rbac_denied" },
          { field: "referencedBy", reason: "budget_exceeded" },
        ],
      }),
    ]);
    expect(groupsOf(result.groups, "relationships")[0].latest).toMatchObject({
      tier: "context",
      relevance: "target",
    });
    expect(result.limitations.map((item) => item.kind)).toEqual([
      "truncated",
      "unknown",
      "truncated",
    ]);
  });

  it("projects the bounded get_topology summary without inventing causality", () => {
    const result = project([
      tool("topology", "get_topology", {
        namespaces: [
          {
            namespace: "shop",
            chains: ["Ingress/store → Service/api → Deployment/api"],
          },
        ],
        problems: ["Deployment api: unhealthy"],
        stats: { nodes: 3, edges: 2 },
      }),
    ]);
    const topology = groupsOf(result.groups, "topology")[0].latest;
    expect(topology).toMatchObject({
      tier: "context",
      relevance: "broader",
      tone: "warning",
    });
    expect(topology.data).toMatchObject({
      type: "topology",
      stats: { nodes: 3, edges: 2 },
      problems: ["Deployment api: unhealthy"],
      warnings: [],
    });
    expect(topology.title).toBe("Resource topology");
    expect(result.limitations).toHaveLength(0);
  });

  it("keeps summary topology scale metadata as explicit coverage limits", () => {
    const result = project([
      tool("topology", "get_topology", {
        namespaces: [],
        stats: { nodes: 0, edges: 0 },
        warnings: [
          "Cluster too large for all-namespace topology. Filter to a specific namespace.",
        ],
        largeCluster: true,
        hiddenKinds: ["ConfigMap", "PersistentVolumeClaim"],
        requiresNamespaceFilter: true,
        estimatedNodes: 2400,
      }),
    ]);

    const topology = groupsOf(result.groups, "topology")[0].latest;
    expect(topology.tier).toBe("context");
    expect(topology.data).toMatchObject({
      type: "topology",
      stats: { nodes: 0, edges: 0 },
      warnings: [
        "Cluster too large for all-namespace topology. Filter to a specific namespace.",
      ],
    });
    expect(result.limitations).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          source: "Topology coverage",
          kind: "truncated",
        }),
        expect.objectContaining({
          source: "Topology scale",
          kind: "truncated",
          message: expect.stringContaining("namespace="),
        }),
      ]),
    );
    expect(
      result.limitations.find((item) => item.source === "Topology scale")
        ?.message,
    ).toContain("ConfigMap, PersistentVolumeClaim");
    expect(
      result.limitations.find((item) => item.source === "Topology scale")
        ?.message,
    ).toContain("2400 estimated nodes");
  });

  it("keeps graph topology collapse and discovery gaps explicit", () => {
    const result = project([
      tool("topology", "get_topology", {
        nodes: [
          {
            id: "deployment/shop/api",
            kind: "Deployment",
            name: "api",
            status: "healthy",
          },
        ],
        edges: [],
        largeCluster: true,
        hiddenKinds: ["ConfigMap"],
        estimatedNodes: 2600,
        summaryMode: true,
        crdDiscoveryStatus: "discovering",
      }),
    ]);

    expect(groupsOf(result.groups, "topology")).toHaveLength(1);
    expect(result.limitations).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          source: "Topology scale",
          kind: "truncated",
          message: expect.stringContaining("Summary mode collapsed"),
        }),
        expect.objectContaining({
          source: "Custom Resource topology",
          kind: "unknown",
          message: expect.stringContaining("still in progress"),
        }),
      ]),
    );
  });

  it("rejects malformed optional topology coverage metadata", () => {
    const result = project([
      tool("topology", "get_topology", {
        namespaces: [],
        stats: { nodes: 0, edges: 0 },
        summaryMode: "false",
      }),
    ]);

    expect(groupsOf(result.groups, "topology")).toHaveLength(0);
    expect(result.limitations).toEqual([
      expect.objectContaining({
        source: "Topology coverage metadata",
        kind: "unknown",
      }),
    ]);
  });
});
