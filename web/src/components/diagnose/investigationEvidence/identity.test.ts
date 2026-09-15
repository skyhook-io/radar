import { describe, expect, it } from "vitest";
import {
  investigationActivitySourceDomId,
  investigationEvidenceSourceDomId,
  investigationEvidenceSourceId,
  investigationEvidenceStepIdsByTurn,
  investigationEvidenceSubjectRef,
  resolveInvestigationRootCauseEvidence,
  type InvestigationEvidenceGroup,
} from "./index";
import {
  criticalIssue,
  deployment,
  evidenceRef,
  project,
  tool,
  warningEvent,
} from "./evidenceFixtures";

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

  it("fails closed for malformed, unmatched, later-turn, failed, or partial refs", () => {
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

    // A result read in an earlier turn of the same run is citable — a revised
    // assessment may rest on it — and resolves to that earlier read.
    expect(
      resolveInvestigationRootCauseEvidence(
        projection,
        { status: "linked", refs: [priorRef] },
        1,
      ),
    ).toMatchObject({ status: "linked", links: [{ source: { stepId: "old" } }] });
    // A result read AFTER the assessment turn is not: the assessment could not
    // have seen it.
    expect(
      resolveInvestigationRootCauseEvidence(
        projection,
        { status: "linked", refs: [currentRef] },
        0,
      ),
    ).toEqual({ status: "invalid", links: [] });

    for (const evidence of [
      { status: "linked" as const, refs: [evidenceRef("a", "z")] },
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

  it("counts duplicate refs across every turn up to the assessment", () => {
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
    // A server-issued ref names one payload in one scope; the same ref in two
    // turns can only be a replay, so run-wide uniqueness is the rule — the
    // same rule the server binds by.
    expect(
      resolveInvestigationRootCauseEvidence(
        priorDuplicateOnly,
        { status: "linked", refs: [ref] },
        1,
      ),
    ).toEqual({ status: "invalid", links: [] });
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
