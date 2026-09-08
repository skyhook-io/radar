import { describe, expect, it } from "vitest";

import {
  investigationActivitySourceDomId,
  investigationEvidenceStepIdsByTurn,
  investigationEvidenceSourceDomId,
  investigationEvidenceSourceId,
  investigationEvidenceSubjectRef,
  metricsScope,
  projectInvestigationEvidence,
  resolveInvestigationRootCauseEvidence,
  type InvestigationEvidenceGroup,
  type InvestigationEvidenceTarget,
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
    radarEvidence: true,
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

describe("investigationEvidenceSubjectRef", () => {
  it("uses resource identities stated by the captured evidence", () => {
    expect(
      investigationEvidenceSubjectRef({
        type: "resource",
        resource: {
          apiVersion: "apps/v1",
          kind: "Deployment",
          metadata: { namespace: "shop", name: "api" },
        },
        warnings: [],
      }),
    ).toEqual({
      kind: "Deployment",
      group: "apps",
      namespace: "shop",
      name: "api",
    });
    expect(
      investigationEvidenceSubjectRef({
        type: "startup",
        blocker: {
          kind: "Pod",
          name: "api-123",
          reason: "ImagePullBackOff",
          severity: "critical",
          message: "The image could not be pulled.",
        },
        subject: { kind: "Pod", namespace: "shop", name: "api-123" },
      }),
    ).toEqual({ kind: "Pod", namespace: "shop", name: "api-123" });
    expect(
      investigationEvidenceSubjectRef({
        type: "logs",
        pod: "api-123",
        container: "api",
        namespace: "shop",
        previous: false,
        warnings: [],
      }),
    ).toEqual({ kind: "Pod", namespace: "shop", name: "api-123" });
    expect(
      investigationEvidenceSubjectRef({
        type: "relationships",
        root: { kind: "Service", namespace: "shop", name: "api" },
        nodes: [],
        edges: [],
        truncated: false,
      }),
    ).toEqual({ kind: "Service", namespace: "shop", name: "api" });
    expect(
      investigationEvidenceSubjectRef({
        type: "resource",
        resource: {
          apiVersion: "v1",
          kind: "Node",
          metadata: { name: "worker-1" },
        },
        warnings: [],
      }),
    ).toEqual({ kind: "Node", name: "worker-1" });
    expect(
      investigationEvidenceSubjectRef({
        type: "resource",
        resource: {
          apiVersion: "v1",
          kind: "Node",
          metadata: { namespace: "kube-system", name: "worker-1" },
        },
        warnings: [],
      }),
    ).toBeUndefined();
  });

  it("does not invent a destination for ambiguous pod evidence", () => {
    expect(
      investigationEvidenceSubjectRef({
        type: "logs",
        pod: "api-123",
        container: "api",
        previous: false,
        warnings: [],
      }),
    ).toBeUndefined();
    expect(
      investigationEvidenceSubjectRef({
        type: "resource",
        resource: {
          apiVersion: "apps/v1",
          kind: "Deployment",
          metadata: { name: "api" },
        },
        warnings: [],
      }),
    ).toBeUndefined();
    expect(
      investigationEvidenceSubjectRef({
        type: "crash",
        crash: {
          pods: ["api-1", "api-2"],
          container: "api",
          state: "waiting",
          exitCode: 1,
          logLine: "FATAL",
          logSource: "current",
          logLineSelection: "fatal_pattern",
        },
        namespace: "shop",
      }),
    ).toBeUndefined();
    expect(
      investigationEvidenceSubjectRef({
        type: "receipt",
        checked: "issues",
        scope: "shop",
        message: "No issues found",
      }),
    ).toBeUndefined();
  });

  it("suppresses missing namespaces only for known namespaced resources", () => {
    expect(
      investigationEvidenceSubjectRef({
        type: "resource",
        resource: {
          apiVersion: "v1",
          kind: "Secret",
          metadata: { name: "api-token" },
        },
        warnings: [],
      }),
    ).toBeUndefined();
    expect(
      investigationEvidenceSubjectRef({
        type: "resource",
        resource: {
          apiVersion: "example.io/v1",
          kind: "Widget",
          metadata: { name: "global-widget" },
        },
        warnings: [],
      }),
    ).toEqual({
      kind: "Widget",
      group: "example.io",
      name: "global-widget",
    });
    expect(
      investigationEvidenceSubjectRef({
        type: "resource",
        resource: {
          apiVersion: "example.io/v1",
          kind: "Deployment",
          metadata: { name: "global-deployment" },
        },
        warnings: [],
      }),
    ).toEqual({
      kind: "Deployment",
      group: "example.io",
      name: "global-deployment",
    });
  });

  it("rejects malformed optional identity fields instead of creating a link", () => {
    const malformed = [
      {
        type: "issue",
        issue: { ...criticalIssue, namespace: { value: "shop" } },
      },
      {
        type: "resource",
        resource: {
          ...deployment,
          metadata: { ...deployment.metadata, namespace: ["shop"] },
        },
        warnings: [],
      },
      {
        type: "network",
        network: {
          subject: {
            kind: "Service",
            namespace: 42,
            name: "api",
          },
          route: "Service shop/api",
          outcome: "observed",
        },
      },
      {
        type: "relationships",
        root: {
          kind: "Deployment",
          group: ["apps"],
          namespace: "shop",
          name: "api",
        },
        nodes: [],
        edges: [],
        truncated: false,
      },
    ];

    for (const data of malformed) {
      expect(
        investigationEvidenceSubjectRef(
          data as unknown as InvestigationEvidenceGroup["latest"]["data"],
        ),
      ).toBeUndefined();
    }
  });

  it("carries the namespace from a pod-logs query into its evidence subject", () => {
    const withNamespace = project([
      tool(
        "logs",
        "get_pod_logs",
        {
          lines: ["FATAL database unavailable"],
          totalLines: 1,
          matchedLines: 1,
          fallback: false,
        },
        {
          summary: JSON.stringify({
            namespace: "shop",
            name: "api-123",
            container: "api",
          }),
        },
      ),
    ]);
    const withoutNamespace = project([
      tool(
        "logs",
        "get_pod_logs",
        {
          lines: ["FATAL database unavailable"],
          totalLines: 1,
          matchedLines: 1,
          fallback: false,
        },
        { summary: JSON.stringify({ name: "api-123", container: "api" }) },
      ),
    ]);

    expect(
      investigationEvidenceSubjectRef(withNamespace.groups[0].latest.data),
    ).toEqual({ kind: "Pod", namespace: "shop", name: "api-123" });
    expect(
      investigationEvidenceSubjectRef(withoutNamespace.groups[0].latest.data),
    ).toBeUndefined();
  });
});

describe("investigation evidence provenance", () => {
  it("does not project a foreign MCP result whose bare name collides with Radar", () => {
    const forgedRef = evidenceRef("a", "z");
    for (const radarEvidence of [undefined, false]) {
      const projection = project([
        tool("foreign", "get_resource", deployment, {
          evidenceRef: forgedRef,
          radarEvidence,
        }),
      ]);

      expect(projection.groups).toHaveLength(0);
      expect(projection.sources).toHaveLength(0);
      expect(projection.evidenceRefSources).toHaveLength(0);
      expect(projection.citableSources).toHaveLength(0);
      expect(projection.limitations).toHaveLength(0);
      expect(projection.coverage.attempted).toBe(0);
    }
  });

  it("projects only the server-validated result when real and colliding foreign calls are mixed", () => {
    const projection = project([
      tool("foreign", "get_resource", deployment, { radarEvidence: false }),
      tool("radar", "get_resource", deployment),
    ]);

    expect(projection.sources.map((source) => source.stepId)).toEqual([
      "radar",
    ]);
    expect(projection.groups).toHaveLength(1);
    expect(projection.coverage.attempted).toBe(1);
  });

  it("keeps a validated failed check as a limitation but never citable evidence", () => {
    const ref = evidenceRef("a", "b");
    const projection = project([
      tool("denied", "get_resource", "permission denied", {
        evidenceRef: ref,
        isError: true,
      }),
    ]);

    expect(projection.groups).toHaveLength(0);
    expect(projection.sources.map((source) => source.stepId)).toEqual([
      "denied",
    ]);
    expect(projection.evidenceRefSources).toHaveLength(1);
    expect(projection.citableSources).toHaveLength(0);
    expect(projection.limitations).toEqual([
      expect.objectContaining({
        source: "Resource details",
        kind: "error",
        message: "permission denied",
      }),
    ]);
  });

  it("surfaces a validated truncated result as a limit without trusting its retained payload", () => {
    const ref = evidenceRef("a", "b");
    const validated = project([
      tool("partial", "get_resource", '{"resource":', {
        evidenceRef: ref,
        truncated: true,
      }),
    ]);

    expect(validated.sources.map((source) => source.stepId)).toEqual([
      "partial",
    ]);
    expect(validated.groups).toHaveLength(0);
    expect(validated.citableSources).toHaveLength(0);
    expect(validated.limitations).toEqual([
      expect.objectContaining({
        source: "Resource details",
        kind: "truncated",
      }),
    ]);

    const unvalidated = project([
      tool("foreign-partial", "get_resource", '{"resource":', {
        evidenceRef: ref,
        radarEvidence: false,
        truncated: true,
      }),
    ]);
    expect(unvalidated.sources).toHaveLength(0);
    expect(unvalidated.groups).toHaveLength(0);
    expect(unvalidated.citableSources).toHaveLength(0);
    expect(unvalidated.limitations).toHaveLength(0);
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
    expect(resolution.links.every((link) => link.originalGroupId)).toBe(true);
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
        "discover_metrics",
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
      links: [{ source: { stepId: "metrics-1" } }],
    });
    expect(resolution.links[0].originalGroupId).toBeUndefined();
    expect(
      investigationEvidenceStepIdsByTurn(projection).get(0),
    ).toBeUndefined();
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
    expect(groupsOf(result.groups, "startup")[0].latest.data).toMatchObject({
      type: "startup",
      subject: { kind: "Pod", namespace: "shop", name: "api-abc" },
    });
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
    expect(limitationText).toContain("includes 2 of 8 selected log lines");
    expect(limitationText).toContain("not the container's full log history");
    expect(limitationText).toContain("Additional crash-cause");
    expect(limitationText).toContain("received 1 of 3 event groups");
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
  it("renders resource scopes as identities, not argument order", () => {
    const result = project([
      tool(
        "events",
        "get_events",
        { events: [warningEvent] },
        {
          summary: JSON.stringify({
            group: "apps",
            kind: "Deployment",
            namespace: "shop",
            name: "api",
          }),
        },
      ),
    ]);
    const events = groupsOf(result.groups, "events")[0].latest;

    expect(events.summary).toBe(
      "BackOff: Back-off restarting failed container · Deployment shop/api",
    );
    expect(events.data).toMatchObject({
      type: "events",
      scope: "Deployment shop/api",
    });
  });

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
        recentChangesSaturated: false,
        recentChangesCoverageLimited: false,
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

  it("keeps get_resource recent-change completeness explicit", () => {
    const complete = project([
      tool("complete", "get_resource", {
        resource: deployment,
        recentChanges: [],
        recentChangesSaturated: false,
        recentChangesCoverageLimited: false,
      }),
    ]);
    expect(
      groupsOf(complete.groups, "receipt").some(
        (group) =>
          group.latest.data.type === "receipt" &&
          group.latest.data.checked === "changes",
      ),
    ).toBe(true);
    expect(complete.limitations).toHaveLength(0);

    const capped = project([
      tool("capped", "get_resource", {
        resource: deployment,
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
        recentChangesCoverageLimited: false,
      }),
    ]);
    expect(groupsOf(capped.groups, "changes")).toHaveLength(1);
    expect(groupsOf(capped.groups, "receipt")).toHaveLength(0);
    expect(capped.limitations).toEqual([
      expect.objectContaining({
        source: "Recent changes",
        kind: "truncated",
        message: expect.stringContaining("result limit was reached"),
      }),
    ]);

    const filtered = project([
      tool("filtered", "get_resource", {
        resource: deployment,
        recentChanges: [],
        recentChangesSaturated: false,
        recentChangesCoverageLimited: true,
      }),
    ]);
    expect(groupsOf(filtered.groups, "receipt")).toHaveLength(0);
    expect(filtered.limitations).toEqual([
      expect.objectContaining({
        source: "Recent changes",
        kind: "unknown",
        message: expect.stringContaining("Change history for"),
        presentation: "history",
      }),
    ]);

    const cappedAfterFiltering = project([
      tool("capped-filtered", "get_resource", {
        resource: deployment,
        recentChangesSaturated: true,
        recentChangesCoverageLimited: true,
      }),
    ]);
    expect(groupsOf(cappedAfterFiltering.groups, "receipt")).toHaveLength(0);
    expect(cappedAfterFiltering.limitations).toEqual([
      expect.objectContaining({
        source: "Recent changes",
        kind: "truncated",
      }),
      expect.objectContaining({
        source: "Recent changes",
        kind: "unknown",
        message: expect.stringContaining("Change history for"),
        presentation: "history",
      }),
    ]);

    for (const metadata of [
      {},
      {
        recentChangesSaturated: "false",
        recentChangesCoverageLimited: false,
      },
      {
        recentChangesSaturated: false,
        recentChangesCoverageLimited: "false",
      },
      { recentChangesSaturated: false },
      { recentChangesCoverageLimited: false },
    ]) {
      const untrusted = project([
        tool("untrusted", "get_resource", {
          resource: deployment,
          recentChanges: [],
          ...metadata,
        }),
      ]);
      expect(groupsOf(untrusted.groups, "receipt")).toHaveLength(0);
      expect(untrusted.limitations).toEqual([
        expect.objectContaining({
          source: "Recent changes",
          kind: "unknown",
        }),
      ]);
    }
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

  it("compares resource revisions without their tool-specific context", () => {
    const result = project([
      tool("diagnose", "diagnose", {
        resource: deployment,
        resourceContext: {
          tier: "diagnostic",
          workloadSummary: { replicas: { desired: 1, ready: 0 } },
        },
        gitOpsDiagnosis: { tool: "argocd", sync: "OutOfSync" },
      }),
      tool("read", "get_resource", {
        resource: deployment,
        context: { tier: "basic" },
      }),
      tool("changed", "get_resource", {
        resource: { ...deployment, status: { readyReplicas: 1, replicas: 1 } },
        context: { tier: "basic" },
      }),
    ]);
    const group = groupsOf(result.groups, "resource")[0];
    expect(group.observations.map((item) => item.changedFromPrevious)).toEqual([
      false,
      false,
      true,
    ]);
    expect(group.observations.map((item) => item.source.stepId)).toEqual([
      "diagnose",
      "read",
      "changed",
    ]);
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
    expect(
      group.observations.map((observation) => observation.historical),
    ).toEqual([true, false]);
    expect(group.historical).toBe(false);
    expect(group.latest.source.stepId).toBe("verification");
    expect(group.observations[0].source.stepId).toBe("initial");
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

  it("keeps exact API identity for recent changes and rejects malformed identity", () => {
    const exactChange = {
      apiVersion: "apps/v1",
      kind: "Deployment",
      namespace: "shop",
      name: "api",
      changeType: "update",
      timestamp: "2026-09-02T09:55:00Z",
    };
    const exact = project([
      tool("exact-change", "get_changes", { changes: [exactChange] }),
    ]);
    const changes = groupsOf(exact.groups, "changes")[0].latest.data;

    expect(changes.type).toBe("changes");
    if (changes.type === "changes") {
      expect(changes.changes[0].apiVersion).toBe("apps/v1");
    }

    const malformed = project([
      tool("malformed-change", "get_changes", {
        changes: [{ ...exactChange, apiVersion: "" }],
      }),
    ]);
    expect(groupsOf(malformed.groups, "changes")).toHaveLength(0);
    expect(malformed.limitations).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ source: "Recent changes" }),
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
      tool("other", "discover_metrics", { data: "ignored" }),
      tool("bad-events", "get_events", { events: [{ nope: true }] }),
    ]);
    expect(result.sources.map((source) => source.stepId)).toEqual([
      "bad-events",
    ]);
    expect(result.groups).toHaveLength(0);
    expect(result.limitations).toHaveLength(1);
    expect(result.limitations[0].source).toBe("Kubernetes events");
    expect(result.limitations[0].message).toContain(
      "couldn't summarize this investigation step",
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
    expect(result.limitations[0].source).toBe("Issue scan");
    expect(result.limitations[0].message).toContain(
      "Only part of this investigation result was saved",
    );
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
          what: "pod_template",
          evidence: "ReplicaSet revision advanced within the issue window.",
        },
        events: [],
      }),
    ]);
    expect(
      groupsOf(result.groups, "changes").map((group) => group.latest.tier),
    ).toEqual(["context", "supporting"]);
    expect(groupsOf(result.groups, "changes")[1].latest).toMatchObject({
      summary: "The workload's Pod template changed",
      data: {
        changeContext: {
          what: "The workload's Pod template changed",
        },
      },
    });
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
          message: expect.stringContaining("namespace-scoped topology search"),
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

// The get_workload_logs response shape, with the stream rows fetchPodLogs
// emits per pod and container.
const workloadLogsPayload = {
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

const workloadLogsArgs = JSON.stringify({
  namespace: "shop",
  name: "api",
  kind: "deployment",
  tail_lines: 50,
});

describe("workload logs adapter", () => {
  it("keeps one multi-container result as one source with several observations", () => {
    const ref = evidenceRef("a", "b");
    const projection = project([
      tool("wl-logs", "get_workload_logs", workloadLogsPayload, {
        summary: workloadLogsArgs,
        evidenceRef: ref,
      }),
    ]);
    const logs = groupsOf(projection.groups, "logs");
    expect(logs.map((group) => group.identity)).toEqual([
      "logs:current:api-68c7b766dc-fmphn:api",
      "logs:current:api-68c7b766dc-fmphn:istio-proxy",
      "logs:current:api-68c7b766dc-q2zxr:api",
    ]);
    expect(projection.sources).toHaveLength(1);
    expect(projection.coverage).toMatchObject({
      attempted: 1,
      projected: 1,
      limited: 1,
    });
    expect(new Set(logs.map((group) => group.latest.source.id)).size).toBe(1);
    // Diagnose relates a target workload to its pods the same way.
    expect(logs.map((group) => group.latest.relevance)).toEqual([
      "producer-related",
      "producer-related",
      "producer-related",
    ]);
    expect(logs[0].latest.tier).toBe("supporting");
    expect(logs[0].latest.tone).toBe("warning");
    expect(logs[0].latest.data).toMatchObject({
      type: "logs",
      namespace: "shop",
      previous: false,
      warnings: workloadLogsPayload.warnings,
    });
    expect(
      (logs[0].latest.data as { logs?: { lines: string[] } }).logs?.lines[0],
    ).not.toContain("[");
    expect(logs[1].latest.tier).toBe("context");
    expect(investigationEvidenceSubjectRef(logs[2].latest.data)).toEqual({
      kind: "Pod",
      namespace: "shop",
      name: "api-68c7b766dc-q2zxr",
    });

    const resolution = resolveInvestigationRootCauseEvidence(
      projection,
      { status: "linked", refs: [ref] },
      0,
    );
    expect(resolution.status).toBe("linked");
    expect(resolution.links[0].source.stepId).toBe("wl-logs");
    expect(resolution.links[0].originalGroupId).toBe(logs[0].id);
    expect(projection.sources[0].primaryGroupId).toBe(logs[0].id);
    expect(
      projection.limitations.map((limitation) => limitation.source),
    ).toEqual(["Workload logs"]);
    expect(projection.limitations[0].kind).toBe("truncated");
  });

  it("accepts every workload kind the producer resolves, including Rollouts", () => {
    const rollout = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool(
              "wl-rollout",
              "get_workload_logs",
              { ...workloadLogsPayload, workload: "rollouts/shop/api" },
              {
                summary: JSON.stringify({
                  namespace: "shop",
                  name: "api",
                  kind: "rollout",
                }),
              },
            ),
          ],
        },
      ],
      { kind: "Rollout", group: "argoproj.io", namespace: "shop", name: "api" },
    );
    expect(
      groupsOf(rollout.groups, "logs").map((group) => group.latest.relevance),
    ).toEqual(["producer-related", "producer-related", "producer-related"]);
    expect(rollout.limitations.map((limitation) => limitation.kind)).toEqual([
      "truncated",
    ]);
  });

  it("scopes a sibling workload's logs as broader and a Pod target's own row as target", () => {
    const sibling = project([
      tool(
        "wl-sibling",
        "get_workload_logs",
        { ...workloadLogsPayload, workload: "deployments/shop/api-worker" },
        {
          summary: JSON.stringify({
            namespace: "shop",
            name: "api-worker",
            kind: "deployment",
          }),
        },
      ),
    ]);
    expect(
      groupsOf(sibling.groups, "logs").map((group) => group.latest.relevance),
    ).toEqual(["broader", "broader", "broader"]);
    expect(
      groupsOf(sibling.groups, "logs").map((group) => group.latest.tier),
    ).toEqual(["context", "context", "context"]);

    const podTarget = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool("wl-pod", "get_workload_logs", workloadLogsPayload, {
              summary: workloadLogsArgs,
            }),
          ],
        },
      ],
      {
        kind: "Pod",
        group: "",
        namespace: "shop",
        name: "api-68c7b766dc-q2zxr",
      },
    );
    expect(
      groupsOf(podTarget.groups, "logs").map((group) => group.latest.relevance),
    ).toEqual(["broader", "broader", "target"]);
  });

  it("reports a failed read as a limitation and no pods as a scoped receipt", () => {
    const failed = project([
      tool(
        "wl-error",
        "get_workload_logs",
        {
          workload: "deployments/shop/api",
          pods: 2,
          logsError: "no kube client in request context",
        },
        { summary: workloadLogsArgs },
      ),
    ]);
    expect(failed.groups).toHaveLength(0);
    expect(failed.limitations).toEqual([
      expect.objectContaining({
        source: "Workload logs",
        message: "no kube client in request context",
        kind: "error",
      }),
    ]);

    const empty = project([
      tool(
        "wl-empty",
        "get_workload_logs",
        {
          workload: "jobs/shop/api",
          pods: 0,
          logs: "No pods found for this Job yet. Check scheduling, admission, or controller events.",
          emptyReason: "no-pods",
          emptyMessage:
            "No pods found for this Job yet. Check scheduling, admission, or controller events.",
          command: "kubectl logs job/api -n shop",
        },
        {
          summary: JSON.stringify({
            namespace: "shop",
            name: "api",
            kind: "job",
          }),
        },
      ),
    ]);
    const receipt = groupsOf(empty.groups, "receipt")[0].latest;
    expect(receipt.tier).toBe("context");
    expect(receipt.relevance).toBe("broader");
    expect(receipt.data).toMatchObject({
      type: "receipt",
      checked: "logs",
      scope: "Job shop/api",
      message:
        "No pods found for this Job yet. Check scheduling, admission, or controller events.",
    });
    expect(empty.limitations).toHaveLength(0);

    const unconfirmed = project([
      tool(
        "wl-unconfirmed",
        "get_workload_logs",
        { workload: "deployments/shop/api", pods: 0, logs: "no pods" },
        { summary: workloadLogsArgs, isError: undefined },
      ),
    ]);
    expect(groupsOf(unconfirmed.groups, "receipt")).toHaveLength(0);
  });

  it("rejects malformed payloads and per-stream errors honestly", () => {
    const malformed = project([
      tool(
        "wl-bad",
        "get_workload_logs",
        { workload: "pods/shop/api", pods: 1, logs: [] },
        { summary: workloadLogsArgs },
      ),
      tool(
        "wl-stream-error",
        "get_workload_logs",
        {
          workload: "deployments/shop/api",
          pods: 1,
          logs: [
            {
              pod: "api-68c7b766dc-fmphn",
              container: "api",
              error: "failed to get logs: container is waiting to start",
            },
          ],
        },
        { summary: workloadLogsArgs },
      ),
      tool(
        "wl-null",
        "get_workload_logs",
        { workload: "deployments/shop/api", pods: 3, logs: null },
        { summary: workloadLogsArgs },
      ),
      tool(
        "wl-zero-with-streams",
        "get_workload_logs",
        {
          workload: "deployments/shop/api",
          pods: 0,
          logs: workloadLogsPayload.logs,
        },
        { summary: workloadLogsArgs },
      ),
    ]);
    expect(malformed.groups).toHaveLength(0);
    expect(
      malformed.limitations.map((limitation) => [
        limitation.source,
        limitation.kind,
      ]),
    ).toEqual([
      ["Workload logs", "unknown"],
      ["api-68c7b766dc-fmphn / api", "error"],
      ["Workload logs", "unknown"],
    ]);
    expect(malformed.limitations[0].sources.map((item) => item.stepId)).toEqual(
      ["wl-bad", "wl-zero-with-streams"],
    );
    expect(malformed.limitations[0].message).toContain(
      "couldn't summarize this investigation step",
    );
    expect(malformed.limitations[2].message).toContain("3 resolved pods");
  });
});

const firingInstance = {
  state: "firing",
  activeAt: "2026-09-07T07:58:00Z",
  value: "1e+00",
  labels: {
    alertname: "KubePodCrashLooping",
    namespace: "shop",
    pod: "api-68c7b766dc-fmphn",
    container: "api",
    severity: "warning",
  },
};

function alertingRule(
  patch: Record<string, unknown> = {},
): Record<string, unknown> {
  return {
    group: "kubernetes-apps",
    type: "alerting",
    name: "KubePodCrashLooping",
    query:
      'max_over_time(kube_pod_container_status_waiting_reason{reason="CrashLoopBackOff"}[5m]) >= 1',
    state: "firing",
    health: "ok",
    labels: { severity: "warning" },
    annotations: {
      summary: "Pod is crash looping.",
      description:
        "Pod {{ $labels.namespace }}/{{ $labels.pod }} ({{ $labels.container }}) is in waiting state (reason: CrashLoopBackOff).",
    },
    alerts: [firingInstance],
    ...patch,
  };
}

const rulesArgs = JSON.stringify({ state: "firing" });

/** A diagnose read that ties the given pods to the target as its log streams. */
function diagnoseWithPods(...pods: string[]) {
  return tool("diagnose-pods", "diagnose", {
    resource: deployment,
    resourceContext: { tier: "basic" },
    logsCurrent: pods.map((pod) => ({
      pod,
      container: "api",
      logs: {
        lines: ["ready"],
        totalLines: 1,
        matchedLines: 0,
        fallback: true,
      },
    })),
  });
}

describe("prometheus rules adapter", () => {
  it("relates a rule to the target through its instances, never the rule alone", () => {
    const projection = project([
      diagnoseWithPods("api-68c7b766dc-fmphn"),
      tool(
        "rules",
        "get_prometheus_rules",
        {
          count: 4,
          rules: [
            alertingRule(),
            alertingRule({
              name: "KubeDeploymentReplicasMismatch",
              labels: { severity: "warning", namespace: "shop" },
              alerts: [
                {
                  state: "firing",
                  labels: {
                    namespace: "shop",
                    deployment: "api-worker",
                  },
                },
              ],
            }),
            alertingRule({
              name: "TargetDown",
              group: "general",
              labels: { severity: "critical" },
              alerts: [
                {
                  state: "firing",
                  labels: { namespace: "monitoring", job: "node-exporter" },
                },
              ],
            }),
            alertingRule({
              name: "SiblingCrash",
              alerts: [
                {
                  state: "firing",
                  labels: {
                    namespace: "shop",
                    pod: "api-worker-5d8f7c9b6-x2k9p",
                  },
                },
              ],
            }),
          ],
        },
        { summary: rulesArgs },
      ),
    ]);
    const alerts = groupsOf(projection.groups, "alerts");
    expect(alerts.map((group) => group.latest.relevance)).toEqual([
      "target",
      "broader",
      "broader",
      "broader",
    ]);
    expect(alerts.map((group) => group.latest.tier)).toEqual([
      "supporting",
      "context",
      "context",
      "context",
    ]);
    expect(alerts[0].latest.tone).toBe("alert");
    expect(alerts[2].latest.tone).toBe("error");
    expect(alerts[0].latest.title).toBe("KubePodCrashLooping firing");
    expect(alerts[0].latest.summary).toBe(
      "1 active instance, 1 naming Deployment shop/api · kubernetes-apps",
    );
    expect(alerts[0].latest.data).toMatchObject({
      type: "alerts",
      rule: { group: "kubernetes-apps", state: "firing", health: "ok" },
      instances: [{ namesTarget: true, value: "1e+00" }],
      annotations: { summary: "Pod is crash looping." },
    });
    expect(alerts[3].latest.data).toMatchObject({
      instances: [{ namesTarget: false }],
    });
    expect(
      groupsOf(projection.groups, "receipt").filter((group) =>
        group.identity.startsWith("alerts:"),
      ),
    ).toHaveLength(0);
    expect(
      projection.limitations.filter((limitation) =>
        limitation.source.startsWith("Alert"),
      ),
    ).toHaveLength(0);
  });

  it("uses rule-level labels only as producer-related context", () => {
    const projection = project([
      tool(
        "rules",
        "get_prometheus_rules",
        {
          count: 1,
          rules: [
            alertingRule({
              name: "ApiDown",
              labels: {
                severity: "warning",
                namespace: "shop",
                deployment: "api",
              },
              alerts: [
                {
                  state: "firing",
                  labels: { namespace: "shop", instance: "10.0.0.4:8080" },
                },
              ],
            }),
          ],
        },
        { summary: rulesArgs },
      ),
    ]);
    const alert = groupsOf(projection.groups, "alerts")[0].latest;
    expect(alert.relevance).toBe("producer-related");
    expect(alert.tier).toBe("supporting");
    expect(alert.summary).toBe("1 active instance · kubernetes-apps");
    expect(groupsOf(projection.groups, "receipt")).toHaveLength(0);
  });

  it("matches workload labels per kind and pod labels only through producer-established pods", () => {
    const run = (
      target: InvestigationEvidenceTarget,
      labels: object,
      capturedPods: string[] = [],
    ) =>
      groupsOf(
        projectInvestigationEvidence(
          [
            {
              timeline: [
                ...(capturedPods.length > 0
                  ? [
                      tool("diagnose-pods", "diagnose", {
                        resource: {
                          ...deployment,
                          kind: target.kind,
                          metadata: {
                            namespace: target.namespace,
                            name: target.name,
                          },
                        },
                        resourceContext: { tier: "basic" },
                        logsCurrent: capturedPods.map((pod) => ({
                          pod,
                          container: "app",
                          logs: {
                            lines: ["ready"],
                            totalLines: 1,
                            matchedLines: 0,
                            fallback: true,
                          },
                        })),
                      }),
                    ]
                  : []),
                tool(
                  "rules",
                  "get_prometheus_rules",
                  {
                    count: 1,
                    rules: [
                      alertingRule({ alerts: [{ state: "firing", labels }] }),
                    ],
                  },
                  { summary: rulesArgs },
                ),
              ],
            },
          ],
          target,
        ).groups,
        "alerts",
      )[0].latest.relevance;
    const statefulSet = { ...target, kind: "StatefulSet", name: "db" };
    expect(run(target, { namespace: "shop", deployment: "api" })).toBe(
      "target",
    );
    expect(run(target, { namespace: "other", deployment: "api" })).toBe(
      "broader",
    );
    expect(run(target, { namespace: "shop", statefulset: "api" })).toBe(
      "broader",
    );
    expect(
      run(target, {
        namespace: "shop",
        workload: "api",
        workload_type: "deployment",
      }),
    ).toBe("target");
    expect(
      run(target, {
        namespace: "shop",
        workload: "api",
        workload_type: "statefulset",
      }),
    ).toBe("broader");
    // A controller-shaped pod name proves nothing on its own: a sibling
    // ReplicaSet `api-worker` produces `api-worker-abcde` too.
    expect(
      run(target, { namespace: "shop", pod: "api-68c7b766dc-fmphn" }),
    ).toBe("broader");
    expect(
      run(target, { namespace: "shop", pod: "api-68c7b766dc-fmphn" }, [
        "api-68c7b766dc-fmphn",
      ]),
    ).toBe("target");
    expect(
      run(target, { namespace: "shop", pod: "api-worker-abcde" }, [
        "api-68c7b766dc-fmphn",
      ]),
    ).toBe("broader");
    expect(run(statefulSet, { namespace: "shop", pod: "db-0" }, ["db-0"])).toBe(
      "target",
    );
    expect(
      run(statefulSet, { namespace: "other", pod: "db-0" }, ["db-0"]),
    ).toBe("broader");
    expect(
      run(target, { pod: "api-68c7b766dc-fmphn" }, ["api-68c7b766dc-fmphn"]),
    ).toBe("broader");
  });

  it("issues a scoped receipt only for a complete, unfiltered, evaluated active-state query", () => {
    const unrelated = alertingRule({
      name: "TargetDown",
      alerts: [{ state: "firing", labels: { namespace: "monitoring" } }],
    });
    const receipt = (
      payload: Record<string, unknown>,
      args: Record<string, unknown>,
    ) =>
      groupsOf(
        project([
          tool("rules", "get_prometheus_rules", payload, {
            summary: JSON.stringify(args),
          }),
        ]).groups,
        "receipt",
      );

    const firing = receipt(
      { count: 1, rules: [unrelated] },
      { state: "firing" },
    );
    expect(firing).toHaveLength(1);
    expect(firing[0].latest).toMatchObject({
      tier: "checked",
      relevance: "target",
      title: "No firing alert rules name this Deployment",
      summary: "Deployment shop/api",
      data: {
        type: "receipt",
        checked: "alerts",
        scope: "Deployment shop/api",
        message:
          "1 alerting rule returned (filter: state=firing), every instance in another namespace; none names Deployment shop/api.",
      },
    });

    expect(
      receipt(
        {
          count: 0,
          rules: [],
          note: "no rules matched — drop filters to see what exists, or the backend may have no rules configured",
        },
        { state: "pending" },
      )[0].latest.data,
    ).toMatchObject({
      message: "0 alerting rules returned (filter: state=pending).",
    });

    // A name or group filter never looked at the other rules.
    expect(
      receipt(
        { count: 1, rules: [unrelated] },
        { state: "firing", name: "Target" },
      ),
    ).toHaveLength(0);
    expect(
      receipt({ count: 0, rules: [] }, { state: "firing", group: "general" }),
    ).toHaveLength(0);
    // An instance this projection cannot place is not evidence of absence.
    expect(
      receipt(
        {
          count: 1,
          rules: [
            alertingRule({
              name: "Vague",
              alerts: [
                { state: "firing", labels: { instance: "10.0.0.4:9100" } },
              ],
            }),
          ],
        },
        { state: "firing" },
      ),
    ).toHaveLength(0);
    expect(
      receipt(
        {
          count: 1,
          rules: [
            alertingRule({
              name: "SameNamespace",
              alerts: [
                {
                  state: "firing",
                  labels: { namespace: "shop", pod: "other-abc" },
                },
              ],
            }),
          ],
        },
        { state: "firing" },
      ),
    ).toHaveLength(0);
    expect(receipt({ count: 1, rules: [unrelated] }, {})).toHaveLength(0);
    expect(
      receipt({ count: 1, rules: [unrelated] }, { state: "inactive" }),
    ).toHaveLength(0);
    expect(
      receipt(
        { count: 1, rules: [alertingRule({ type: "recording", name: "x" })] },
        { type: "record", state: "firing" },
      ),
    ).toHaveLength(0);
    expect(
      receipt(
        {
          count: 1,
          rules: [unrelated],
          truncated: true,
          note: "narrow with name, group, state, or type filters",
        },
        { state: "firing" },
      ),
    ).toHaveLength(0);
    expect(
      receipt(
        { count: 1, rules: [{ ...unrelated, health: "err" }] },
        { state: "firing" },
      ),
    ).toHaveLength(0);
    expect(
      receipt(
        { count: 1, rules: [{ ...unrelated, health: "degraded" }] },
        { state: "firing" },
      ),
    ).toHaveLength(0);
    expect(
      receipt(
        { count: 1, rules: [{ ...unrelated, health: undefined }] },
        { state: "firing" },
      ),
    ).toHaveLength(0);
  });

  it("advances the card through alert state transitions and keeps distinct rules apart", () => {
    const firingForTarget = alertingRule();
    const resolved = alertingRule({ state: "inactive", alerts: [] });
    const firingElsewhere = alertingRule({
      alerts: [
        {
          state: "firing",
          labels: { namespace: "shop", pod: "api-worker-5d8f7c9b6-x2k9p" },
        },
      ],
    });
    const run = (...later: Record<string, unknown>[]) =>
      groupsOf(
        project(
          [
            diagnoseWithPods("api-68c7b766dc-fmphn"),
            tool(
              "rules-1",
              "get_prometheus_rules",
              { count: 1, rules: [firingForTarget] },
              { summary: rulesArgs },
            ),
          ],
          [
            tool(
              "rules-2",
              "get_prometheus_rules",
              { count: later.length, rules: later },
              { summary: JSON.stringify({}) },
            ),
          ],
        ).groups,
        "alerts",
      );

    const [resolvedGroup] = run(resolved);
    expect(resolvedGroup.observations).toHaveLength(2);
    expect(resolvedGroup.latest).toMatchObject({
      title: "KubePodCrashLooping inactive",
      relevance: "target",
      tier: "context",
      tone: "neutral",
      summary: "No active instances · kubernetes-apps",
    });
    expect(resolvedGroup.latest).toBe(resolvedGroup.chronologicalLatest);
    expect(resolvedGroup.historical).toBe(false);

    const [movedGroup] = run(firingElsewhere);
    expect(movedGroup.observations).toHaveLength(2);
    expect(movedGroup.latest.relevance).toBe("broader");
    expect(movedGroup.latest.summary).toBe(
      "1 active instance · kubernetes-apps",
    );

    const distinct = groupsOf(
      project([
        tool(
          "rules",
          "get_prometheus_rules",
          {
            count: 2,
            rules: [
              alertingRule({ labels: { severity: "warning", team: "a" } }),
              alertingRule({
                query: "vector(1)",
                labels: { severity: "critical", team: "b" },
              }),
            ],
          },
          { summary: rulesArgs },
        ),
      ]).groups,
      "alerts",
    );
    expect(distinct).toHaveLength(2);
    expect(distinct.every((group) => group.observations.length === 1)).toBe(
      true,
    );

    // Identical definitions from different rule files arrive as identical
    // rows: two rules this read cannot tell apart, so neither inherits the
    // other's history on a later read.
    const twin = () => alertingRule({ alerts: [], state: "inactive" });
    const twins = groupsOf(
      project(
        [
          diagnoseWithPods("api-68c7b766dc-fmphn"),
          tool(
            "rules-1",
            "get_prometheus_rules",
            { count: 2, rules: [alertingRule(), twin()] },
            { summary: rulesArgs },
          ),
        ],
        [
          tool(
            "rules-2",
            "get_prometheus_rules",
            { count: 2, rules: [alertingRule(), twin()] },
            { summary: rulesArgs },
          ),
        ],
      ).groups,
      "alerts",
    );
    expect(twins).toHaveLength(4);
    expect(twins.every((group) => group.observations.length === 1)).toBe(true);
    expect(twins.map((group) => group.latest.relevance)).toEqual([
      "target",
      "broader",
      "target",
      "broader",
    ]);
    expect(twins.map((group) => group.latest.title)).toEqual([
      "KubePodCrashLooping firing",
      "KubePodCrashLooping inactive",
      "KubePodCrashLooping firing",
      "KubePodCrashLooping inactive",
    ]);
  });

  it("never proves absence by namespace for a cluster-scoped target", () => {
    const node = { kind: "Node", group: "", name: "worker-1" };
    const run = (rules: Record<string, unknown>[]) =>
      groupsOf(
        projectInvestigationEvidence(
          [
            {
              timeline: [
                tool(
                  "rules",
                  "get_prometheus_rules",
                  { count: rules.length, rules },
                  { summary: rulesArgs },
                ),
              ],
            },
          ],
          node,
        ).groups,
        "receipt",
      );
    expect(
      run([
        alertingRule({
          name: "KubeNodeNotReady",
          alerts: [
            {
              state: "firing",
              labels: { node: "worker-1", namespace: "kube-system" },
            },
          ],
        }),
      ]),
    ).toHaveLength(0);
    expect(run([])).toHaveLength(1);
    expect(run([])[0].latest.title).toBe(
      "No firing alert rules name this Node",
    );
  });

  it("keeps recording rules off the card list and flags truncation and evaluation health", () => {
    const projection = project([
      tool(
        "rules",
        "get_prometheus_rules",
        {
          count: 3,
          rules: [
            alertingRule({
              type: "recording",
              name: "namespace:container_cpu_usage_seconds_total:sum_rate",
              alerts: undefined,
              state: undefined,
            }),
            alertingRule({ health: "err", alerts: [] }),
            alertingRule({
              name: "KubeContainerWaiting",
              state: "inactive",
              health: "unknown",
              alerts: [],
            }),
          ],
          truncated: true,
          note: "narrow with name, group, state, or type filters",
        },
        { summary: rulesArgs },
      ),
    ]);
    const alerts = groupsOf(projection.groups, "alerts");
    expect(alerts.map((group) => group.latest.title)).toEqual([
      "KubePodCrashLooping firing",
      "KubeContainerWaiting inactive",
    ]);
    expect(alerts[1].latest).toMatchObject({
      tier: "context",
      tone: "neutral",
      relevance: "broader",
      summary: "No active instances · kubernetes-apps",
    });
    expect(
      projection.limitations.map((limitation) => [
        limitation.source,
        limitation.kind,
      ]),
    ).toEqual([
      ["Alert rules", "truncated"],
      ["Alert rule KubePodCrashLooping", "error"],
      ["Alert rule KubeContainerWaiting", "unknown"],
    ]);
    expect(projection.limitations[0].message).toContain(
      "narrow with name, group, state, or type filters",
    );
    expect(groupsOf(projection.groups, "receipt")).toHaveLength(0);
  });

  it("does not let sampled values churn revision history, and rejects malformed rules", () => {
    const later = {
      ...firingInstance,
      value: "2e+00",
      activeAt: "2026-09-07T08:10:00Z",
    };
    const projection = project(
      [
        tool(
          "rules-1",
          "get_prometheus_rules",
          { count: 1, rules: [alertingRule()] },
          { summary: rulesArgs },
        ),
      ],
      [
        tool(
          "rules-2",
          "get_prometheus_rules",
          { count: 1, rules: [alertingRule({ alerts: [later] })] },
          { summary: rulesArgs },
        ),
      ],
    );
    const group = groupsOf(projection.groups, "alerts")[0];
    expect(group.observations).toHaveLength(2);
    expect(group.observations[1].changedFromPrevious).toBe(false);

    const malformed = project([
      tool(
        "rules-bad",
        "get_prometheus_rules",
        { count: 1, rules: [{ name: "NoGroup", type: "alerting" }] },
        { summary: rulesArgs },
      ),
      tool(
        "rules-bad-instance",
        "get_prometheus_rules",
        { count: 1, rules: [alertingRule({ alerts: [{ labels: {} }] })] },
        { summary: rulesArgs },
      ),
    ]);
    expect(malformed.groups).toHaveLength(0);
    expect(malformed.limitations).toHaveLength(1);
    expect(malformed.limitations[0].sources).toHaveLength(2);
    expect(malformed.limitations[0].source).toBe("Alert rules");

    const envelope = project([
      tool(
        "rules-count",
        "get_prometheus_rules",
        { count: 2, rules: [alertingRule()] },
        { summary: rulesArgs },
      ),
      tool(
        "rules-truncated",
        "get_prometheus_rules",
        { count: 0, rules: [], truncated: "true" },
        { summary: rulesArgs },
      ),
      tool(
        "rules-no-query",
        "get_prometheus_rules",
        { count: 1, rules: [alertingRule({ query: undefined })] },
        { summary: rulesArgs },
      ),
    ]);
    expect(envelope.groups).toHaveLength(0);
    expect(envelope.limitations[0].sources).toHaveLength(3);
  });
});

const helmRelease = {
  name: "shop",
  namespace: "shop",
  chart: "shop",
  chartVersion: "1.4.2",
  appVersion: "2.0.0",
  status: "deployed",
  revision: 7,
  updated: "2026-09-07T07:00:00Z",
  description: "Upgrade complete",
  resources: [
    {
      kind: "Deployment",
      apiVersion: "apps/v1",
      name: "api",
      namespace: "shop",
      status: "Running",
      ready: "0/1",
    },
    {
      kind: "Service",
      apiVersion: "v1",
      name: "api",
      namespace: "shop",
      status: "Active",
    },
  ],
};

describe("helm release adapter", () => {
  it("relates a release through the resources it manages", () => {
    const projection = project([
      tool("helm", "get_helm_release", helmRelease, {
        summary: JSON.stringify({ namespace: "shop", name: "shop" }),
      }),
      tool(
        "helm-other",
        "get_helm_release",
        {
          ...helmRelease,
          name: "ingress",
          resources: [
            {
              kind: "Deployment",
              apiVersion: "apps/v1",
              name: "api",
              namespace: "ingress",
            },
          ],
        },
        { summary: JSON.stringify({ namespace: "shop", name: "ingress" }) },
      ),
    ]);
    const releases = groupsOf(projection.groups, "helm");
    expect(releases.map((group) => group.latest.relevance)).toEqual([
      "producer-related",
      "broader",
    ]);
    expect(releases[0].latest).toMatchObject({
      tier: "context",
      tone: "info",
      title: "Helm release shop/shop",
      summary: "shop 1.4.2 · deployed · revision 7",
    });
    expect(investigationEvidenceSubjectRef(releases[0].latest.data)).toEqual({
      kind: "HelmRelease",
      group: "helm.sh",
      namespace: "shop",
      name: "shop",
    });
  });

  it("never fills a missing owned-resource API version from the target", () => {
    const projection = project([
      tool(
        "helm-no-version",
        "get_helm_release",
        {
          ...helmRelease,
          resources: [{ kind: "Deployment", name: "api", namespace: "shop" }],
        },
        { summary: JSON.stringify({ namespace: "shop", name: "shop" }) },
      ),
      tool(
        "helm-other-group",
        "get_helm_release",
        {
          ...helmRelease,
          name: "shop-x",
          resources: [
            {
              kind: "Deployment",
              apiVersion: "example.io/v1",
              name: "api",
              namespace: "shop",
            },
          ],
        },
        { summary: JSON.stringify({ namespace: "shop", name: "shop-x" }) },
      ),
    ]);
    expect(
      groupsOf(projection.groups, "helm").map(
        (group) => group.latest.relevance,
      ),
    ).toEqual(["broader", "broader"]);
  });

  it("keys a release by its storage namespace and withholds a link it cannot express", () => {
    const projection = project([
      tool(
        "helm-a",
        "get_helm_release",
        { ...helmRelease, storageNamespace: "flux-a" },
        { summary: JSON.stringify({ namespace: "shop", name: "shop" }) },
      ),
      tool(
        "helm-b",
        "get_helm_release",
        { ...helmRelease, storageNamespace: "flux-b", revision: 9 },
        { summary: JSON.stringify({ namespace: "shop", name: "shop" }) },
      ),
    ]);
    const releases = groupsOf(projection.groups, "helm");
    expect(releases.map((group) => group.identity)).toEqual([
      "helm:flux-a:shop:shop",
      "helm:flux-b:shop:shop",
    ]);
    expect(releases[0].latest.data).toMatchObject({
      release: { storageNamespace: "flux-a" },
    });
    expect(investigationEvidenceSubjectRef(releases[0].latest.data)).toBe(
      undefined,
    );
  });

  it("treats a non-deployed status, a health issue, or a failed operation as adverse", () => {
    const projection = project([
      tool(
        "helm-failed",
        "get_helm_release",
        { ...helmRelease, status: "failed", description: "Upgrade failed" },
        { summary: JSON.stringify({ namespace: "shop", name: "shop" }) },
      ),
      tool(
        "helm-issue",
        "get_helm_release",
        {
          ...helmRelease,
          name: "shop-cache",
          resourceHealth: "unhealthy",
          healthIssue: "Deployment shop/cache has 0/1 ready replicas",
          healthSummary: "1 of 2 resources unhealthy",
        },
        { summary: JSON.stringify({ namespace: "shop", name: "shop-cache" }) },
      ),
      tool(
        "helm-stuck",
        "get_helm_release",
        {
          ...helmRelease,
          name: "shop-jobs",
          status: "pending-upgrade",
          lastOperation: {
            kind: "pending",
            status: "stuck_pending",
            source: "helm_status",
            confidence: "high",
            message: "Revision 8 has been pending-upgrade for 42m",
            revision: 8,
          },
          valuesError:
            'Radar Cloud role "viewer" cannot view Helm release values (requires member or higher)',
        },
        { summary: JSON.stringify({ namespace: "shop", name: "shop-jobs" }) },
      ),
    ]);
    const releases = groupsOf(projection.groups, "helm");
    expect(
      releases.map((group) => [group.latest.tier, group.latest.tone]),
    ).toEqual([
      ["supporting", "error"],
      ["supporting", "warning"],
      ["supporting", "warning"],
    ]);
    expect(releases[1].latest.summary).toBe(
      "deployed · Deployment shop/cache has 0/1 ready replicas",
    );
    expect(releases[2].latest.data).toMatchObject({
      type: "helm",
      release: {
        lastOperation: { kind: "pending", status: "stuck_pending" },
      },
    });
    expect(projection.limitations).toEqual([
      expect.objectContaining({ source: "Helm values", kind: "error" }),
    ]);
  });

  it("rejects malformed release payloads", () => {
    const projection = project([
      tool(
        "helm-bad",
        "get_helm_release",
        { ...helmRelease, revision: "7" },
        { summary: JSON.stringify({ namespace: "shop", name: "shop" }) },
      ),
      tool(
        "helm-bad-resource",
        "get_helm_release",
        { ...helmRelease, resources: [{ kind: "Deployment" }] },
        { summary: JSON.stringify({ namespace: "shop", name: "shop" }) },
      ),
      tool(
        "helm-bad-operation",
        "get_helm_release",
        { ...helmRelease, lastOperation: { kind: "rollback" } },
        { summary: JSON.stringify({ namespace: "shop", name: "shop" }) },
      ),
    ]);
    expect(projection.groups).toHaveLength(0);
    expect(
      projection.limitations.map((limitation) => limitation.source),
    ).toEqual(["Helm release", "Helm resources", "Helm operation"]);
  });
});

const deploymentWithServiceAccount = {
  ...deployment,
  spec: { template: { spec: { serviceAccountName: "api-sa" } } },
};

const permissionsArgs = JSON.stringify({
  kind: "ServiceAccount",
  namespace: "shop",
  name: "api-sa",
  verb: "get",
  resource: "secrets",
});

describe("subject permissions adapter", () => {
  it("binds an access check to the target's ServiceAccount seen in the same turn", () => {
    const check = {
      subject: { kind: "ServiceAccount", namespace: "shop", name: "api-sa" },
      accessCheck: {
        verb: "get",
        resource: "secrets",
        namespace: "shop",
        allowed: false,
        reason: "",
      },
    };
    const projection = project([
      tool("diagnose", "diagnose", {
        resource: deploymentWithServiceAccount,
        resourceContext: { tier: "basic" },
      }),
      tool("perm", "get_subject_permissions", check, {
        summary: permissionsArgs,
      }),
      tool(
        "perm-other",
        "get_subject_permissions",
        {
          subject: { kind: "ServiceAccount", namespace: "shop", name: "other" },
          accessCheck: {
            verb: "list",
            group: "apps",
            resource: "deployments",
            subresource: "scale",
            namespace: "",
            allowed: true,
            reason: 'RBAC: allowed by ClusterRoleBinding "admin"',
          },
        },
        {
          summary: JSON.stringify({
            kind: "ServiceAccount",
            namespace: "shop",
            name: "other",
          }),
        },
      ),
    ]);
    const permissions = groupsOf(projection.groups, "permissions");
    expect(permissions.map((group) => group.latest.relevance)).toEqual([
      "target",
      "broader",
    ]);
    expect(permissions[0].latest).toMatchObject({
      tier: "supporting",
      tone: "warning",
      title: "Service Account shop/api-sa cannot get secrets",
      summary: "No RBAC rule allows it · namespace shop",
    });
    expect(permissions[0].latest.data).toMatchObject({
      type: "permissions",
      accessCheck: { allowed: false, denied: false },
    });
    expect(permissions[1].latest).toMatchObject({
      tier: "context",
      tone: "info",
      title: "Service Account shop/other can list deployments/scale.apps",
      summary: 'RBAC: allowed by ClusterRoleBinding "admin" · cluster-wide',
    });
    expect(investigationEvidenceSubjectRef(permissions[0].latest.data)).toEqual(
      { kind: "ServiceAccount", namespace: "shop", name: "api-sa" },
    );
  });

  it("uses the producer's serviceAccount reference and the default account when the template omits one", () => {
    const withContext = project([
      tool("diagnose", "diagnose", {
        resource: deployment,
        resourceContext: {
          tier: "diagnostic",
          uses: {
            serviceAccount: {
              kind: "ServiceAccount",
              namespace: "shop",
              name: "ctx-sa",
            },
          },
        },
      }),
      tool(
        "perm",
        "get_subject_permissions",
        {
          subject: {
            kind: "ServiceAccount",
            namespace: "shop",
            name: "ctx-sa",
          },
          bindings: [],
          flatRules: [],
        },
        { summary: permissionsArgs },
      ),
    ]);
    expect(
      groupsOf(withContext.groups, "permissions")[0].latest.relevance,
    ).toBe("target");

    const withDefault = project([
      tool("resource", "get_resource", {
        ...deployment,
        spec: { template: { spec: {} } },
      }),
      tool(
        "perm",
        "get_subject_permissions",
        {
          subject: {
            kind: "ServiceAccount",
            namespace: "shop",
            name: "default",
          },
          bindings: [],
          flatRules: [],
        },
        { summary: permissionsArgs },
      ),
    ]);
    expect(
      groupsOf(withDefault.groups, "permissions")[0].latest.relevance,
    ).toBe("target");

    const priorTurn = project(
      [
        tool("diagnose", "diagnose", {
          resource: deploymentWithServiceAccount,
          resourceContext: { tier: "basic" },
        }),
      ],
      [
        tool(
          "perm",
          "get_subject_permissions",
          {
            subject: {
              kind: "ServiceAccount",
              namespace: "shop",
              name: "api-sa",
            },
            bindings: [],
            flatRules: [],
          },
          { summary: permissionsArgs },
        ),
      ],
    );
    expect(groupsOf(priorTurn.groups, "permissions")[0].latest.relevance).toBe(
      "broader",
    );

    // The newest captured read of the target decides which account it runs as.
    const rotated = project([
      tool("resource-old", "get_resource", deploymentWithServiceAccount),
      tool("resource-new", "get_resource", {
        ...deployment,
        spec: { template: { spec: { serviceAccountName: "api-sa-v2" } } },
      }),
      tool(
        "perm-old",
        "get_subject_permissions",
        {
          subject: {
            kind: "ServiceAccount",
            namespace: "shop",
            name: "api-sa",
          },
          bindings: [],
          flatRules: [],
        },
        { summary: permissionsArgs },
      ),
      tool(
        "perm-new",
        "get_subject_permissions",
        {
          subject: {
            kind: "ServiceAccount",
            namespace: "shop",
            name: "api-sa-v2",
          },
          bindings: [],
          flatRules: [],
        },
        { summary: permissionsArgs },
      ),
    ]);
    expect(
      groupsOf(rotated.groups, "permissions").map(
        (group) => group.latest.relevance,
      ),
    ).toEqual(["broader", "target"]);
  });

  it("reads a PodSpec only from kinds that carry one", () => {
    const check = (name: string) => ({
      subject: { kind: "ServiceAccount", namespace: "shop", name },
      accessCheck: {
        verb: "get",
        resource: "secrets",
        namespace: "shop",
        allowed: false,
      },
    });
    const applicationSet = project([
      tool("resource", "get_resource", {
        apiVersion: "argoproj.io/v1alpha1",
        kind: "ApplicationSet",
        metadata: { namespace: "shop", name: "api" },
        spec: { template: { spec: { project: "default" } } },
      }),
      tool("perm", "get_subject_permissions", check("default"), {
        summary: permissionsArgs,
      }),
    ]);
    expect(
      groupsOf(applicationSet.groups, "permissions")[0].latest.relevance,
    ).toBe("broader");

    const cronJob = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool("resource", "get_resource", {
              apiVersion: "batch/v1",
              kind: "CronJob",
              metadata: { namespace: "shop", name: "api" },
              spec: {
                jobTemplate: {
                  spec: {
                    template: { spec: { serviceAccountName: "cron-sa" } },
                  },
                },
              },
            }),
            tool("perm", "get_subject_permissions", check("cron-sa"), {
              summary: permissionsArgs,
            }),
          ],
        },
      ],
      { kind: "CronJob", group: "batch", namespace: "shop", name: "api" },
    );
    expect(groupsOf(cronJob.groups, "permissions")[0].latest.relevance).toBe(
      "target",
    );
  });

  it("keeps a denial for an unrelated principal out of the lead evidence", () => {
    const projection = project([
      tool("diagnose", "diagnose", {
        resource: deploymentWithServiceAccount,
        resourceContext: { tier: "basic" },
      }),
      tool(
        "perm-cross",
        "get_subject_permissions",
        {
          subject: {
            kind: "ServiceAccount",
            namespace: "unrelated",
            name: "other",
          },
          accessCheck: {
            verb: "get",
            resource: "secrets",
            namespace: "unrelated",
            allowed: false,
          },
        },
        {
          summary: JSON.stringify({
            kind: "ServiceAccount",
            namespace: "unrelated",
            name: "other",
          }),
        },
      ),
    ]);
    const denial = groupsOf(projection.groups, "permissions")[0].latest;
    expect(denial.relevance).toBe("broader");
    expect(denial.tier).toBe("context");
    expect(denial.tone).toBe("warning");
  });

  it("renders the subject response as counts and keeps every coverage caveat", () => {
    const projection = project([
      tool(
        "perm",
        "get_subject_permissions",
        {
          subject: {
            kind: "ServiceAccount",
            namespace: "shop",
            name: "api-sa",
          },
          bindings: [
            {
              bindingKind: "RoleBinding",
              bindingNamespace: "shop",
              bindingName: "api-reader",
              roleKind: "Role",
              roleNamespace: "shop",
              roleName: "reader",
              rulesCount: 3,
            },
            {
              bindingKind: "ClusterRoleBinding",
              bindingName: "system:basic-user",
              roleKind: "ClusterRole",
              roleName: "system:basic-user",
              rulesCount: 1,
              inheritedFromGroup: "system:authenticated",
            },
          ],
          flatRules: [
            { verbs: ["get"], resources: ["pods"] },
            { verbs: ["list"], resources: ["pods"] },
          ],
          truncated: true,
          usedByPods: ["shop/api-68c7b766dc-fmphn"],
          podsTotal: 3,
          narrowHint:
            "rule list truncated — the subject has more rules than shown; do not treat this list as the subject's complete permissions",
        },
        { summary: permissionsArgs },
      ),
      tool(
        "perm-missing",
        "get_subject_permissions",
        {
          subject: { kind: "ServiceAccount", namespace: "shop", name: "ghost" },
          bindings: [],
          flatRules: [],
          subjectWarning:
            'no ServiceAccount "ghost" exists in namespace "shop" — this empty result reflects a subject that was never found, not an account without permissions. Check the name and namespace.',
        },
        { summary: permissionsArgs },
      ),
    ]);
    const permissions = groupsOf(projection.groups, "permissions");
    expect(permissions[0].latest).toMatchObject({
      tier: "context",
      tone: "info",
      relevance: "broader",
      title: "Permissions of Service Account shop/api-sa",
      summary: "2 bindings · 2+ effective rules",
    });
    expect(permissions[0].latest.data).toMatchObject({
      type: "permissions",
      flatRulesCount: 2,
      truncated: true,
      usedByPods: ["shop/api-68c7b766dc-fmphn"],
      podsTotal: 3,
    });
    expect(permissions[1].latest.summary).toBe(
      "0 bindings · 0 effective rules",
    );
    expect(
      projection.limitations.map((limitation) => [
        limitation.source,
        limitation.kind,
      ]),
    ).toEqual([
      ["Permissions", "truncated"],
      ["Permissions", "truncated"],
      ["Permissions", "unknown"],
    ]);
    expect(projection.limitations[0].message).toContain("rule list truncated");
    expect(projection.limitations[1].message).toBe(
      "Only 1 of 3 pods running as this subject were listed.",
    );
  });

  it("rejects malformed permission payloads", () => {
    const projection = project([
      tool(
        "perm-bad",
        "get_subject_permissions",
        { subject: { kind: "ServiceAccount" }, bindings: [], flatRules: [] },
        { summary: permissionsArgs },
      ),
      tool(
        "perm-bad-check",
        "get_subject_permissions",
        {
          subject: { kind: "ServiceAccount", namespace: "shop", name: "x" },
          accessCheck: { verb: "get", resource: "secrets", namespace: "shop" },
        },
        { summary: permissionsArgs },
      ),
      tool(
        "perm-bad-binding",
        "get_subject_permissions",
        {
          subject: { kind: "ServiceAccount", namespace: "shop", name: "x" },
          bindings: [{ bindingKind: "RoleBinding" }],
          flatRules: [],
        },
        { summary: permissionsArgs },
      ),
      tool(
        "perm-bad-rule",
        "get_subject_permissions",
        {
          subject: { kind: "ServiceAccount", namespace: "shop", name: "x" },
          bindings: [],
          flatRules: [{ resources: ["pods"] }],
        },
        { summary: permissionsArgs },
      ),
    ]);
    expect(projection.groups).toHaveLength(0);
    expect(
      projection.limitations.map((limitation) => limitation.source),
    ).toEqual(["Permissions", "Access check"]);
    expect(projection.limitations[0].sources).toHaveLength(3);
  });
});

describe("card leads and deep-link subjects", () => {
  it("leads an events card with the newest warning's reason and message", () => {
    const projection = project([
      tool(
        "events",
        "get_events",
        {
          events: [
            {
              reason: "Scheduled",
              message: "Successfully assigned shop/api-abc to node-1",
              type: "Normal",
              count: 1,
              lastTimestamp: "2026-09-02T11:00:00Z",
            },
            warningEvent,
            {
              reason: "Unhealthy",
              message: "Readiness probe failed: connection refused",
              type: "Warning",
              count: 12,
              lastTimestamp: "2026-09-02T10:30:00Z",
            },
          ],
        },
        {
          summary: JSON.stringify({
            kind: "Deployment",
            namespace: "shop",
            name: "api",
          }),
        },
      ),
    ]);
    expect(groupsOf(projection.groups, "events")[0].latest.summary).toBe(
      "Unhealthy: Readiness probe failed: connection refused · 3 event groups · Deployment shop/api",
    );
  });

  it("carries the producer's change subject so a card can open its Timeline", () => {
    const change = {
      kind: "Deployment",
      apiVersion: "apps/v1",
      namespace: "shop",
      name: "api",
      changeType: "update",
      timestamp: "2026-09-02T09:00:00Z",
    };
    const projection = project([
      tool(
        "changes",
        "get_changes",
        { changes: [change] },
        {
          summary: JSON.stringify({
            kind: "Deployment",
            namespace: "shop",
            name: "api",
          }),
        },
      ),
      tool(
        "changes-ns",
        "get_changes",
        { changes: [change] },
        { summary: JSON.stringify({ namespace: "shop" }) },
      ),
      tool("diagnose", "diagnose", {
        resource: deployment,
        resourceContext: { tier: "basic" },
        recentChanges: [change],
      }),
      tool("resource", "get_resource", {
        resource: {
          ...deployment,
          metadata: { namespace: "shop", name: "web" },
        },
        resourceContext: { tier: "basic" },
        recentChanges: [change],
        recentChangesSaturated: false,
        recentChangesCoverageLimited: false,
      }),
    ]);
    const changes = groupsOf(projection.groups, "changes").map(
      (group) => (group.latest.data as { subject?: unknown }).subject,
    );
    expect(changes).toEqual([
      { kind: "Deployment", namespace: "shop", name: "api" },
      undefined,
      { kind: "Deployment", namespace: "shop", name: "api" },
      { kind: "Deployment", namespace: "shop", name: "web" },
    ]);
  });
});

describe("query_prometheus evidence", () => {
  const rangeSeries = [
    {
      labels: { pod: "api-7f6-abc" },
      dataPoints: [
        { timestamp: 1_788_679_043, value: 169_377_792 },
        { timestamp: 1_788_679_068, value: 171_401_216 },
      ],
    },
  ];
  const targetSelectors = [
    {
      metric: "container_memory_working_set_bytes",
      matchers: [
        { label: "namespace", op: "=", value: "shop" },
        { label: "pod", op: "=~", value: "api-.*" },
      ],
    },
  ];
  function promResult(patch: Record<string, unknown> = {}) {
    return {
      query:
        'sum(container_memory_working_set_bytes{namespace="shop",pod=~"api-.*"})',
      type: "range",
      start: "2026-09-06T07:17:23Z",
      end: "2026-09-06T09:17:23Z",
      step: "25s",
      resultType: "matrix",
      seriesCount: 1,
      series: rangeSeries,
      selectors: targetSelectors,
      ...patch,
    };
  }

  it("charts a range query scoped to the target as target evidence", () => {
    const projection = project([
      tool("prom", "query_prometheus", promResult(), {
        summary: JSON.stringify({ query: "sum(...)", type: "range" }),
      }),
    ]);
    const [group] = groupsOf(projection.groups, "metrics");
    expect(group).toBeDefined();
    expect(group.latest.relevance).toBe("target");
    expect(group.latest.tier).toBe("supporting");
    expect(group.latest.tone).toBe("neutral");
    expect(group.latest.title).toBe("Prometheus metrics");
    expect(group.latest.summary).toBe("1 series · 2h window · 25s step");
    const data = group.latest.data;
    if (data.type !== "metrics") throw new Error("expected metrics");
    expect(data.mode).toBe("range");
    expect(data.origin).toBe("query");
    expect(data.subject).toEqual({
      kind: "Deployment",
      group: "apps",
      namespace: "shop",
      name: "api",
    });
    expect(data.unit).toBe("bytes");
    expect(data.series).toEqual(rangeSeries);
    expect(investigationEvidenceSubjectRef(data)).toEqual(data.subject);
    expect(projection.coverage.projected).toBe(1);
  });

  it("claims a unit only for a single bare metric that states one", () => {
    const projection = project([
      tool(
        "bare",
        "query_prometheus",
        promResult({
          query:
            'container_memory_working_set_bytes{namespace="shop",pod=~"api-.*"}',
        }),
      ),
      tool(
        "rate",
        "query_prometheus",
        promResult({
          query:
            'rate(container_cpu_usage_seconds_total{namespace="shop",pod=~"api-.*"}[5m])',
          selectors: [
            {
              metric: "container_cpu_usage_seconds_total",
              matchers: targetSelectors[0].matchers,
            },
          ],
        }),
      ),
    ]);
    const units = groupsOf(projection.groups, "metrics").map((group) =>
      group.latest.data.type === "metrics" ? group.latest.data.unit : "?",
    );
    expect(units).toEqual(["bytes", ""]);
  });

  it("renders an instant query as values, not a chart", () => {
    const projection = project([
      tool(
        "instant",
        "query_prometheus",
        promResult({
          type: "instant",
          start: undefined,
          end: undefined,
          step: undefined,
          resultType: "vector",
          series: [
            {
              labels: { pod: "api-7f6-abc" },
              dataPoints: [{ timestamp: 1_788_679_068, value: 3 }],
            },
          ],
        }),
      ),
    ]);
    const [group] = groupsOf(projection.groups, "metrics");
    expect(group.latest.title).toBe("Prometheus values");
    expect(group.latest.summary).toBe("1 series");
    expect(group.latest.data.type === "metrics" && group.latest.data.mode).toBe(
      "instant",
    );
  });

  it("reports a truncated result as a limitation with its cardinality, never as a chart", () => {
    const projection = project([
      tool(
        "big",
        "query_prometheus",
        promResult({
          series: [],
          truncated: true,
          summary: {
            seriesCount: 812,
            totalDataPoints: 194_880,
            labelCardinality: { pod: 812, container: 3 },
            suggestion: "topk(5, ...)",
          },
          note: "Result exceeded the 96 KiB cap.",
        }),
      ),
    ]);
    expect(groupsOf(projection.groups, "metrics")).toHaveLength(0);
    expect(projection.limitations).toHaveLength(1);
    expect(projection.limitations[0].kind).toBe("truncated");
    expect(projection.limitations[0].message).toContain("812 series");
    expect(projection.limitations[0].message).toContain(
      "812 distinct pod values",
    );
    expect(projection.limitations[0].message).toContain("Result exceeded");
    expect(projection.coverage.limited).toBe(1);
  });

  it("keeps a query that matched nothing as a fact about the window", () => {
    const projection = project([
      tool("none", "query_prometheus", promResult({ series: [] })),
    ]);
    const [group] = groupsOf(projection.groups, "metrics");
    expect(group.latest.summary).toBe(
      "No series matched · 2h window · 25s step",
    );
  });

  it("classifies scope from the producer's selectors, never from the expression text", () => {
    const withNamespace = (
      metric: string,
      extra: Array<{ label: string; op: string; value: string }> = [],
    ) => ({
      metric,
      matchers: [{ label: "namespace", op: "=", value: "shop" }, ...extra],
    });
    const cases: Array<[string, Record<string, unknown>, string]> = [
      [
        "namespace only",
        { selectors: [withNamespace("up")] },
        "producer-related",
      ],
      [
        "workload label",
        {
          selectors: [
            withNamespace("kube_deployment_status_replicas_available", [
              { label: "deployment", op: "=", value: "api" },
            ]),
          ],
        },
        "target",
      ],
      [
        "exact pod name prefix",
        {
          selectors: [
            withNamespace("kube_pod_container_status_restarts_total", [
              { label: "pod", op: "=", value: "api-7f6-abc" },
            ]),
          ],
        },
        "target",
      ],
      [
        "container alone",
        {
          selectors: [
            withNamespace("container_memory_working_set_bytes", [
              { label: "container", op: "=", value: "api" },
            ]),
          ],
        },
        "producer-related",
      ],
      [
        "sibling workload",
        {
          selectors: [
            withNamespace("kube_deployment_status_replicas_available", [
              { label: "deployment", op: "=", value: "worker" },
            ]),
          ],
        },
        "producer-related",
      ],
      [
        "pod regex with an optional dash",
        {
          selectors: [
            withNamespace("kube_pod_container_status_restarts_total", [
              { label: "pod", op: "=~", value: "api-?.*" },
            ]),
          ],
        },
        "producer-related",
      ],
      [
        "pod regex alternation behind the target prefix",
        {
          selectors: [
            withNamespace("kube_pod_container_status_restarts_total", [
              { label: "pod", op: "=~", value: "api-.*|worker-.*" },
            ]),
          ],
        },
        "producer-related",
      ],
      [
        "workload alternation",
        {
          selectors: [
            withNamespace("kube_deployment_status_replicas_available", [
              { label: "deployment", op: "=~", value: "api|worker" },
            ]),
          ],
        },
        "producer-related",
      ],
      [
        "statefulset label on a deployment target",
        {
          selectors: [
            withNamespace("kube_statefulset_status_replicas_ready", [
              { label: "statefulset", op: "=", value: "api" },
            ]),
          ],
        },
        "producer-related",
      ],
      [
        "braced target numerator over a bare cluster denominator",
        {
          query:
            'sum(container_memory_working_set_bytes{namespace="shop",pod=~"api-.*"}) / scalar(sum(machine_memory_bytes))',
          selectors: [
            ...targetSelectors,
            { metric: "machine_memory_bytes", matchers: [] },
          ],
        },
        "broader",
      ],
      [
        "other namespace",
        {
          selectors: [
            {
              metric: "up",
              matchers: [
                { label: "namespace", op: "=", value: "other" },
                { label: "pod", op: "=~", value: "api-.*" },
              ],
            },
          ],
        },
        "broader",
      ],
      ["empty selectors", { selectors: [] }, "broader"],
      [
        "unknown selectors",
        { selectors: targetSelectors, selectorsUnknown: true },
        "broader",
      ],
    ];
    for (const [name, patch, expected] of cases) {
      const projection = project([
        tool(`case-${name}`, "query_prometheus", promResult(patch)),
      ]);
      const [group] = groupsOf(projection.groups, "metrics");
      expect(group?.latest.relevance, name).toBe(expected);
      expect(
        group?.latest.data.type === "metrics"
          ? group.latest.data.subject
          : "missing",
        name,
      ).toEqual(
        expected === "target"
          ? {
              kind: "Deployment",
              group: "apps",
              namespace: "shop",
              name: "api",
            }
          : undefined,
      );
      expect(group?.latest.tier, name).toBe(
        expected === "broader" ? "context" : "supporting",
      );
    }
  });

  it("treats an unescaped dot in a workload regex as naming more than the target", () => {
    const dotted = { ...target, name: "api.v2" };
    const selector = (value: string) => [
      {
        metric: "kube_deployment_status_replicas_available",
        matchers: [
          { label: "namespace", op: "=", value: "shop" },
          { label: "deployment", op: "=~", value },
        ],
      },
    ];
    expect(metricsScope(dotted, selector("api.v2"), false)).toBe(
      "producer-related",
    );
    expect(metricsScope(dotted, selector("api\\.v2"), false)).toBe("target");
    expect(
      metricsScope(
        dotted,
        [
          {
            metric: "up",
            matchers: [
              { label: "namespace", op: "=", value: "shop" },
              { label: "pod", op: "=~", value: "api\\.v2-.*" },
            ],
          },
        ],
        false,
      ),
    ).toBe("target");
    expect(
      metricsScope(
        dotted,
        [
          {
            metric: "up",
            matchers: [
              { label: "namespace", op: "=", value: "shop" },
              { label: "pod", op: "=~", value: "api.v2-.*" },
            ],
          },
        ],
        false,
      ),
    ).toBe("producer-related");
  });

  it("names a Pod target only by its exact name and accepts an anchored namespace regex", () => {
    const pod = { ...target, kind: "Pod", group: "", name: "api-7f6-abc" };
    const selector = (
      namespace: { op: string; value: string },
      podMatcher: { op: string; value: string },
    ) => [
      {
        metric: "container_memory_working_set_bytes",
        matchers: [
          { label: "namespace", ...namespace },
          { label: "pod", ...podMatcher },
        ],
      },
    ];
    const exactNs = { op: "=", value: "shop" };
    expect(
      metricsScope(
        pod,
        selector(exactNs, { op: "=~", value: "api-7f6-abc-.*" }),
        false,
      ),
    ).toBe("producer-related");
    expect(
      metricsScope(
        pod,
        selector(exactNs, { op: "=", value: "api-7f6-abc" }),
        false,
      ),
    ).toBe("target");
    expect(
      metricsScope(
        pod,
        selector(exactNs, { op: "=~", value: "api-7f6-abc" }),
        false,
      ),
    ).toBe("target");
    expect(
      metricsScope(
        pod,
        selector(exactNs, { op: "=", value: "api-7f6-abc-extra" }),
        false,
      ),
    ).toBe("producer-related");
    expect(
      metricsScope(
        target,
        selector({ op: "=~", value: "shop" }, { op: "=~", value: "api-.*" }),
        false,
      ),
    ).toBe("target");
    expect(
      metricsScope(
        target,
        selector(
          { op: "=~", value: "shop|other" },
          { op: "=~", value: "api-.*" },
        ),
        false,
      ),
    ).toBe("broader");
    expect(
      metricsScope(
        target,
        selector(
          { op: "!=", value: "kube-system" },
          { op: "=~", value: "api-.*" },
        ),
        false,
      ),
    ).toBe("broader");
  });

  it("rejects a result without the producer's selector inventory", () => {
    const projection = project([
      tool("old", "query_prometheus", promResult({ selectors: undefined })),
      tool(
        "bad-series",
        "query_prometheus",
        promResult({
          series: [{ labels: {}, dataPoints: [{ timestamp: "x", value: 1 }] }],
        }),
      ),
    ]);
    expect(groupsOf(projection.groups, "metrics")).toHaveLength(0);
    expect(projection.limitations.map((item) => item.kind)).toEqual([
      "unknown",
    ]);
    expect(projection.limitations[0].source).toBe("Prometheus query");
    expect(projection.limitations[0].sources).toHaveLength(2);
  });

  it("merges the same query into one card with revisions", () => {
    const projection = project(
      [tool("first", "query_prometheus", promResult())],
      [
        tool(
          "second",
          "query_prometheus",
          promResult({
            series: [
              {
                labels: { pod: "api-7f6-abc" },
                dataPoints: [{ timestamp: 1_788_679_043, value: 1 }],
              },
            ],
          }),
        ),
      ],
    );
    const groups = groupsOf(projection.groups, "metrics");
    expect(groups).toHaveLength(1);
    expect(groups[0].observations).toHaveLength(2);
    expect(groups[0].observations[1].changedFromPrevious).toBe(true);
  });
});

describe("resource cards scaled by an HPA", () => {
  const scaled = (state: string) =>
    tool("workload", "diagnose", {
      resource: {
        apiVersion: "apps/v1",
        kind: "Deployment",
        metadata: { namespace: "shop", name: "api" },
      },
      resourceContext: {
        tier: "basic",
        workloadSummary: { replicas: { desired: 5, ready: 5, available: 5 } },
        scaledBy: [
          {
            kind: "HorizontalPodAutoscaler",
            namespace: "shop",
            name: "api-hpa",
            hpaSummary: {
              state,
              summary: "summary from Radar",
              bounds: { min: 1, max: 5, current: 5, desired: 5 },
            },
          },
        ],
      },
    });

  it("lifts a healthy workload whose autoscaler cannot act", () => {
    const result = project([scaled("metrics_unavailable")]);
    const card = groupsOf(result.groups, "resource")[0].latest;
    expect(card.tier).toBe("supporting");
    expect(card.tone).toBe("warning");
    expect(card.summary).toBe("5/5 replicas ready · HPA: Metrics unavailable");
  });

  it("treats a maxed-out autoscaler as adverse", () => {
    const card = groupsOf(
      project([scaled("limited_max")]).groups,
      "resource",
    )[0].latest;
    expect(card.tone).toBe("warning");
    expect(card.summary).toBe("5/5 replicas ready · HPA: Maxed");
  });

  it("leaves an autoscaler that is simply scaling as context", () => {
    const card = groupsOf(project([scaled("scaling_up")]).groups, "resource")[0]
      .latest;
    expect(card.tier).toBe("context");
    expect(card.tone).toBe("neutral");
    expect(card.summary).toBe("5/5 replicas ready");
  });
});

describe("diagnose metrics evidence", () => {
  const window = {
    start: "2026-09-06T07:00:00Z",
    end: "2026-09-06T08:00:00Z",
    step: "1m2s",
  };
  const samples = [
    { timestamp: Date.parse(window.start) / 1000, value: 0.003457 },
    { timestamp: Date.parse(window.end) / 1000, value: 0.004 },
  ];
  function vitals(patch: Record<string, unknown> = {}) {
    return {
      window,
      pods: 2,
      series: [
        {
          category: "cpu",
          unit: "cores",
          query:
            "sum(rate(container_cpu_usage_seconds_total{container!='',namespace='shop',pod=~'^(api-a|api-b)$'}[5m]))",
          series: [{ labels: {}, dataPoints: samples }],
        },
        {
          category: "memory",
          unit: "bytes",
          query:
            "sum(max by (pod,namespace,container) (container_memory_working_set_bytes{container!='',namespace='shop',pod=~'^(api-a|api-b)$'}))",
          series: [{ labels: {}, dataPoints: samples }],
        },
        {
          category: "restarts",
          unit: "count",
          query:
            "sum(round(increase(kube_pod_container_status_restarts_total{namespace='shop',pod=~'^(api-a|api-b)$'}[1h])))",
          series: [],
        },
      ],
      ...patch,
    };
  }
  const args = { kind: "Deployment", namespace: "shop", name: "api" };

  it("charts each captured category as target evidence with the diagnosed resource as subject", () => {
    const projection = project([
      tool(
        "diag",
        "diagnose",
        { resource: deployment, pods: 2, metrics: vitals() },
        { summary: JSON.stringify(args) },
      ),
    ]);
    const groups = groupsOf(projection.groups, "metrics");
    expect(groups.map((group) => group.latest.title)).toEqual([
      "CPU usage · Deployment shop/api",
      "Memory working set · Deployment shop/api",
      "Restarts · Deployment shop/api",
    ]);
    for (const group of groups) {
      expect(group.latest.relevance).toBe("target");
      expect(group.latest.tier).toBe("supporting");
      expect(group.latest.tone).toBe("neutral");
      const data = group.latest.data;
      if (data.type !== "metrics") throw new Error("expected metrics");
      expect(data.origin).toBe("diagnose");
      expect(data.mode).toBe("range");
      expect(data.start).toBe(window.start);
      expect(data.end).toBe(window.end);
      expect(data.step).toBe(window.step);
      expect(data.pods).toBe(2);
      expect(data.partial).toBe(false);
      expect(data.truncated).toBe(false);
      expect(data.subject).toEqual({
        kind: "Deployment",
        group: "apps",
        namespace: "shop",
        name: "api",
      });
      expect(investigationEvidenceSubjectRef(data)).toEqual(data.subject);
    }
    const [cpu, , restarts] = groups;
    expect(cpu.latest.summary).toBe("2 pods · 60m window · 1m2s step");
    const cpuData = cpu.latest.data;
    if (cpuData.type !== "metrics") throw new Error("expected metrics");
    expect(cpuData.unit).toBe("cores");
    expect(cpuData.label).toBe("CPU usage");
    expect(cpuData.query).toContain("pod=~'^(api-a|api-b)$'");
    expect(cpuData.series).toEqual([{ labels: {}, dataPoints: samples }]);
    expect(restarts.latest.summary).toBe(
      "No samples in the window · 60m window · 1m2s step",
    );
    expect(
      projection.limitations.filter(
        (item) => item.source === "Workload metrics",
      ),
    ).toHaveLength(0);
  });

  it("merges a re-diagnose of the same workload into the same chart per category", () => {
    const later = vitals({
      window: { ...window, end: "2026-09-06T08:30:00Z" },
    });
    const projection = project(
      [
        tool(
          "diag-1",
          "diagnose",
          { resource: deployment, pods: 2, metrics: vitals() },
          { summary: JSON.stringify(args) },
        ),
      ],
      [
        tool(
          "diag-2",
          "diagnose",
          { resource: deployment, pods: 2, metrics: later },
          { summary: JSON.stringify(args) },
        ),
      ],
    );
    const groups = groupsOf(projection.groups, "metrics");
    expect(groups).toHaveLength(3);
    expect(groups.every((group) => group.observations.length === 2)).toBe(true);
    const data = groups[0].latest.data;
    if (data.type !== "metrics") throw new Error("expected metrics");
    expect(data.end).toBe("2026-09-06T08:30:00Z");
  });

  it("reports a producer error as a limitation and still charts what answered", () => {
    const projection = project([
      tool(
        "diag",
        "diagnose",
        {
          resource: deployment,
          pods: 2,
          metrics: vitals({
            series: [vitals().series[1]],
            error:
              "cpu: prom: query error from prometheus: cpu exploded (execution)",
          }),
        },
        { summary: JSON.stringify(args) },
      ),
    ]);
    expect(groupsOf(projection.groups, "metrics")).toHaveLength(1);
    const limitation = projection.limitations.find(
      (item) => item.source === "Workload metrics",
    );
    expect(limitation?.kind).toBe("error");
    expect(limitation?.message).toContain("cpu exploded");
  });

  it("reports an unreachable Prometheus without inventing a chart", () => {
    const projection = project([
      tool(
        "diag",
        "diagnose",
        {
          resource: deployment,
          pods: 2,
          metrics: vitals({
            series: [],
            error:
              "prometheus unreachable: dial tcp 10.0.0.9:9090: connection refused",
          }),
        },
        { summary: JSON.stringify(args) },
      ),
    ]);
    expect(groupsOf(projection.groups, "metrics")).toHaveLength(0);
    const limitation = projection.limitations.find(
      (item) => item.source === "Workload metrics",
    );
    expect(limitation?.kind).toBe("error");
    expect(limitation?.message).toBe(
      "prometheus unreachable: dial tcp 10.0.0.9:9090: connection refused",
    );
  });

  it("says nothing when diagnose carried no metrics field", () => {
    const projection = project([
      tool(
        "diag",
        "diagnose",
        { resource: deployment, pods: 2 },
        { summary: JSON.stringify(args) },
      ),
    ]);
    expect(groupsOf(projection.groups, "metrics")).toHaveLength(0);
    expect(
      projection.limitations.some((item) => item.source === "Workload metrics"),
    ).toBe(false);
  });

  it("notes a partial pod set in the summary and keeps the cap on the card", () => {
    const projection = project([
      tool(
        "diag",
        "diagnose",
        {
          resource: deployment,
          pods: 55,
          metrics: vitals({ pods: 50, partial: true, omittedPods: 5 }),
        },
        { summary: JSON.stringify(args) },
      ),
    ]);
    const [cpu] = groupsOf(projection.groups, "metrics");
    expect(cpu.latest.summary).toBe(
      "first 50 of 55 pods · 60m window · 1m2s step · partial pod set",
    );
    const data = cpu.latest.data;
    if (data.type !== "metrics") throw new Error("expected metrics");
    expect(data.pods).toBe(50);
    expect(data.partial).toBe(true);
  });

  it("keeps a neighbour's vitals broader with the neighbour as subject", () => {
    const worker = {
      ...deployment,
      metadata: { namespace: "shop", name: "worker" },
    };
    const projection = project([
      tool(
        "diag-worker",
        "diagnose",
        { resource: worker, pods: 2, metrics: vitals() },
        {
          summary: JSON.stringify({
            kind: "Deployment",
            namespace: "shop",
            name: "worker",
          }),
        },
      ),
    ]);
    const groups = groupsOf(projection.groups, "metrics");
    expect(groups).toHaveLength(3);
    for (const group of groups) {
      expect(group.latest.relevance).toBe("broader");
      expect(group.latest.tier).toBe("context");
      const data = group.latest.data;
      if (data.type !== "metrics") throw new Error("expected metrics");
      expect(data.subject).toEqual({
        kind: "Deployment",
        group: "apps",
        namespace: "shop",
        name: "worker",
      });
    }
  });

  it("rejects a malformed metrics field instead of charting it", () => {
    const malformed: Record<string, unknown>[] = [
      { window: { start: window.start }, pods: "two", series: [] },
      { window: { ...window, end: "not a date" }, pods: 2, series: [] },
      { window: { ...window, end: window.start }, pods: 2, series: [] },
      { window, pods: -1, series: [] },
      { window, pods: 1.5, series: [] },
      { window, pods: 2, omittedPods: -3, series: [] },
    ];
    for (const metrics of malformed) {
      const projection = project([
        tool(
          "diag",
          "diagnose",
          { resource: deployment, pods: 2, metrics },
          { summary: JSON.stringify(args) },
        ),
      ]);
      expect(groupsOf(projection.groups, "metrics")).toHaveLength(0);
      expect(
        projection.limitations.some(
          (item) =>
            item.source === "Workload metrics" && item.kind === "unknown",
        ),
      ).toBe(true);
    }
  });

  it("keeps same-named workloads in different API groups on separate charts", () => {
    const rollout = {
      ...deployment,
      apiVersion: "argoproj.io/v1alpha1",
      kind: "Rollout",
    };
    const projection = project([
      tool(
        "diag-deployment",
        "diagnose",
        { resource: deployment, pods: 2, metrics: vitals() },
        { summary: JSON.stringify(args) },
      ),
      tool(
        "diag-rollout",
        "diagnose",
        { resource: rollout, pods: 2, metrics: vitals() },
        { summary: JSON.stringify(args) },
      ),
    ]);
    const groups = groupsOf(projection.groups, "metrics");
    expect(groups).toHaveLength(6);
    expect(groups.every((group) => group.observations.length === 1)).toBe(true);
    expect(new Set(groups.map((group) => group.identity)).size).toBe(6);
  });

  it("rejects inherited object keys posing as categories", () => {
    for (const category of ["__proto__", "constructor", "toString"]) {
      const projection = project([
        tool(
          "diag",
          "diagnose",
          {
            resource: deployment,
            pods: 2,
            metrics: vitals({
              series: [{ ...vitals().series[0], category }],
            }),
          },
          { summary: JSON.stringify(args) },
        ),
      ]);
      expect(groupsOf(projection.groups, "metrics")).toHaveLength(0);
      expect(
        projection.limitations.some(
          (item) =>
            item.source === "Workload metrics" && item.kind === "unknown",
        ),
      ).toBe(true);
    }
  });

  it("rejects a category outside the contract or a series without a unit", () => {
    const projection = project([
      tool(
        "diag",
        "diagnose",
        {
          resource: deployment,
          pods: 2,
          metrics: vitals({
            series: [
              { ...vitals().series[0], category: "latency" },
              { ...vitals().series[1], unit: undefined },
              vitals().series[2],
            ],
          }),
        },
        { summary: JSON.stringify(args) },
      ),
    ]);
    const groups = groupsOf(projection.groups, "metrics");
    expect(groups.map((group) => group.latest.title)).toEqual([
      "Restarts · Deployment shop/api",
    ]);
    expect(
      projection.limitations.some(
        (item) => item.source === "Workload metrics" && item.kind === "unknown",
      ),
    ).toBe(true);
  });
});

describe("live-run follow-ups", () => {
  it("reads the producer's nil-slice null as an empty events or changes result", () => {
    const projection = project([
      tool(
        "events-none",
        "get_events",
        { events: null },
        {
          summary: JSON.stringify({
            kind: "Pod",
            namespace: "shop",
            name: "api",
          }),
        },
      ),
      tool(
        "changes-none",
        "get_changes",
        { changes: null },
        {
          summary: JSON.stringify({
            kind: "Deployment",
            namespace: "shop",
            name: "api",
            since: "24h",
          }),
        },
      ),
    ]);
    expect(projection.groups).toHaveLength(0);
    expect(
      projection.limitations.map((limitation) => [
        limitation.source,
        limitation.kind,
      ]),
    ).toEqual([
      ["Events", "unknown"],
      ["Recent changes", "unknown"],
    ]);
    expect(projection.limitations[0].message).toContain(
      "No events were returned",
    );
  });

  it("keeps repeated change reads of one resource on one card and names the window", () => {
    const change = {
      kind: "Deployment",
      apiVersion: "apps/v1",
      namespace: "shop",
      name: "api",
      changeType: "update",
      timestamp: "2026-09-02T09:00:00Z",
    };
    const older = { ...change, timestamp: "2026-09-01T09:00:00Z" };
    const read = (
      id: string,
      args: Record<string, unknown>,
      changes: unknown[],
    ) =>
      tool(id, "get_changes", { changes }, { summary: JSON.stringify(args) });
    const projection = project([
      read(
        "changes-24h",
        { namespace: "shop", name: "api", since: "24h", kind: "Deployment" },
        [change],
      ),
      read(
        "changes-48h",
        {
          kind: "Deployment",
          namespace: "shop",
          name: "api",
          since: "48h",
          limit: 30,
        },
        [change, older],
      ),
      read("changes-ns", { namespace: "shop", since: "24h" }, [change]),
    ]);
    const changes = groupsOf(projection.groups, "changes");
    expect(changes.map((group) => group.identity)).toEqual([
      "changes:Deployment shop/api",
      "changes:namespace shop",
    ]);
    expect(changes[0].observations).toHaveLength(2);
    expect(changes[0].observations.map((o) => o.title)).toEqual([
      "Recent changes · last 24h",
      "Recent changes · last 48h",
    ]);
    expect(changes[0].latest.title).toBe("Recent changes · last 48h");
    expect(changes[0].latest.summary).toBe("2 changes · Deployment shop/api");
    expect(changes[1].latest.title).toBe("Recent changes · last 24h");
  });
});
