import { describe, expect, it } from "vitest";
import { projectInvestigationEvidence } from "./index";
import {
  completeLogProof,
  criticalIssue,
  deployment,
  evidenceRef,
  groupsOf,
  project,
  target,
  tool,
  warningEvent,
} from "./evidenceFixtures";

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

  it("attributes evidence to saved Hub runs with a plural resource kind", () => {
    const result = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool("deployment", "get_resource", deployment),
            tool("wrong-group", "get_resource", {
              ...deployment,
              apiVersion: "example.com/v1",
            }),
            tool(
              "plural-events",
              "get_events",
              { events: [warningEvent] },
              {
                summary: JSON.stringify({
                  kind: "deployments",
                  namespace: "shop",
                  name: "api",
                }),
              },
            ),
            tool("plural-issue", "issues", {
              issues: [{ ...criticalIssue, kind: "deployments" }],
              total: 1,
              total_matched: 1,
            }),
          ],
        },
      ],
      { ...target, kind: "deployments" },
    );

    const resources = groupsOf(result.groups, "resource");
    expect(resources.map((group) => group.latest.relevance)).toEqual([
      "target",
      "broader",
    ]);
    expect(groupsOf(result.groups, "events")[0].latest.relevance).toBe(
      "target",
    );
    expect(groupsOf(result.groups, "issue")[0].latest.relevance).toBe("target");
  });

  it("keeps colliding non-core plurals on their own API group", () => {
    const podMetrics = {
      apiVersion: "metrics.k8s.io/v1beta1",
      kind: "PodMetrics",
      metadata: { namespace: "shop", name: "api" },
    };
    const result = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool("metrics-pod", "get_resource", podMetrics),
            tool("core-pod", "get_resource", {
              ...podMetrics,
              apiVersion: "v1",
              kind: "Pod",
            }),
          ],
        },
      ],
      { ...target, kind: "pods", group: "metrics.k8s.io" },
    );
    expect(
      groupsOf(result.groups, "resource").map(
        (group) => group.latest.relevance,
      ),
    ).toEqual(["target", "broader"]);
  });

  it("matches an undiscovered irregular CRD without inventing its Kind", () => {
    const result = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool("database", "get_resource", {
              apiVersion: "postgresql.cnpg.io/v1",
              kind: "Database",
              metadata: { namespace: "shop", name: "api" },
            }),
          ],
        },
      ],
      { ...target, kind: "databases", group: "postgresql.cnpg.io" },
    );
    expect(groupsOf(result.groups, "resource")[0].latest.relevance).toBe(
      "target",
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
    // events producer marks a denied namespace explicitly, so its empty result
    // is a receipt; the changes producer still has the ambiguity. The targeted
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
      expect.arrayContaining(["Recent changes", "Resource inventory"]),
    );
    expect(result.limitations.map((item) => item.source)).not.toContain(
      "Events",
    );
    expect(result.coverage).toEqual({
      attempted: 5,
      projected: 3,
      limited: 2,
      checked: 1,
    });
    expect(
      groupsOf(result.groups, "receipt").find(
        (group) =>
          group.latest.data.type === "receipt" &&
          group.latest.data.checked === "events",
      )?.latest,
    ).toMatchObject({
      // The title and the scope beside it are the whole answer here; a body
      // would only restate them.
      title: "No warning events",
      data: { message: undefined },
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
    // A complete, successful events query that returned nothing is a checked
    // receipt for its scope; the producer marks a denied namespace separately.
    expect(
      projection.groups.map((group) => [
        group.latest.data.type,
        group.latest.title,
      ]),
    ).toEqual([["receipt", "No events in this window"]]);
    expect(
      projection.limitations.map((limitation) => [
        limitation.source,
        limitation.kind,
      ]),
    ).toEqual([["Recent changes", "unknown"]]);
  });

  it("keeps one identity for a blocker whether one pod holds it or several", () => {
    const blocker = (name: string) => ({
      kind: "Pod",
      name,
      reason: "Unschedulable",
      severity: "critical",
      message: "1 node(s) no free host ports",
    });
    const bundle = (pods: string[]) => ({
      resource: {
        apiVersion: "apps/v1",
        kind: "DaemonSet",
        metadata: { namespace: "opencost", name: "node-exporter" },
      },
      resourceContext: { tier: "basic" },
      pods: pods.length,
      startupBlockers: pods.map(blocker),
    });
    // Two pods recover to one between diagnoses. The blocker is the same
    // finding, so it must update its card rather than open a second one
    // beside it saying the same thing.
    const projection = projectInvestigationEvidence(
      [
        {
          status: "done",
          timeline: [tool("first", "diagnose", bundle(["a", "b"]))],
        },
        {
          status: "done",
          verify: true,
          timeline: [tool("second", "diagnose", bundle(["a"]))],
        },
      ],
      {
        kind: "DaemonSet",
        group: "apps",
        namespace: "opencost",
        name: "node-exporter",
      },
    );
    const startup = projection.groups.filter(
      (group) => group.latest.data.type === "startup",
    );
    expect(startup).toHaveLength(1);
    expect(startup[0].observations).toHaveLength(2);
    expect(startup[0].latest.summary).toBe("1 node(s) no free host ports");
  });

  it("merges identical startup blockers across pods into one card with the pod list", () => {
    const blocker = (
      name: string,
      message = "1 node(s) no free host ports",
    ) => ({
      kind: "Pod",
      name,
      reason: "Unschedulable",
      severity: "critical",
      message,
    });
    const projection = project([
      tool("diagnose", "diagnose", {
        resource: {
          apiVersion: "apps/v1",
          kind: "DaemonSet",
          metadata: { namespace: "opencost", name: "node-exporter" },
        },
        resourceContext: { tier: "basic" },
        pods: 3,
        startupBlockers: [
          blocker("node-exporter-a"),
          blocker("node-exporter-b"),
          blocker("node-exporter-c", "0/10 nodes: insufficient memory"),
        ],
      }),
    ]);
    const startup = projection.groups.filter(
      (group) => group.latest.data.type === "startup",
    );
    expect(
      startup.map((group) => [
        group.latest.summary,
        group.latest.data.type === "startup" ? group.latest.data.pods : null,
      ]),
    ).toEqual([
      [
        "2 pods · 1 node(s) no free host ports",
        ["node-exporter-a", "node-exporter-b"],
      ],
      // A blocker only one pod holds still carries that pod, so the pods a
      // later Prometheus query is proved against do not depend on how many
      // happened to share a reason.
      ["0/10 nodes: insufficient memory", ["node-exporter-c"]],
    ]);
    expect(startup[1].id).not.toBe(startup[0].id);
  });

  it("folds pods' startup blocker into the classified issue that repeats it", () => {
    const blocker = (name: string) => ({
      kind: "Pod",
      name,
      reason: "Unschedulable",
      severity: "critical",
      message: "1 node(s) no free host ports",
    });
    const projection = project([
      tool("diagnose", "diagnose", {
        resource: {
          apiVersion: "apps/v1",
          kind: "DaemonSet",
          metadata: { namespace: "opencost", name: "node-exporter" },
        },
        resourceContext: { tier: "basic" },
        pods: 2,
        relatedIssues: [
          {
            id: "issue-unsched",
            severity: "critical",
            source: "scheduling",
            category: "unschedulable",
            category_group: "startup",
            grouping_scope: "workload",
            kind: "DaemonSet",
            namespace: "opencost",
            name: "node-exporter",
            reason: "Unschedulable",
            message: "1 node(s) no free host ports",
          },
        ],
        startupBlockers: [
          blocker("node-exporter-a"),
          blocker("node-exporter-b"),
        ],
      }),
    ]);
    expect(
      projection.groups.filter((group) => group.latest.data.type === "startup"),
    ).toHaveLength(0);
    const [issue] = projection.groups.filter(
      (group) => group.latest.data.type === "issue",
    );
    expect(
      issue.latest.data.type === "issue" && issue.latest.data.pods,
    ).toEqual(["node-exporter-a", "node-exporter-b"]);
  });

  it("files a denied events namespace as an access limitation, not a receipt", () => {
    const projection = project([
      tool(
        "events-denied",
        "get_events",
        { events: [], accessDenied: true },
        {
          summary: JSON.stringify({ namespace: "locked", kind: "Pod" }),
        },
      ),
    ]);
    expect(projection.groups).toHaveLength(0);
    expect(
      projection.limitations.map((limitation) => [
        limitation.source,
        limitation.kind,
      ]),
    ).toEqual([["Events", "error"]]);
    expect(projection.limitations[0].message).toContain(
      "not readable with your permissions",
    );
  });

  it("keeps a cluster-wide events read that was narrowed to readable namespaces from reading as a clean cluster", () => {
    const empty = project([
      tool(
        "events-partial-empty",
        "get_events",
        { events: [], partialScope: true, scopeNamespaces: ["alpha", "beta"] },
        { summary: JSON.stringify({}) },
      ),
    ]);
    const [receipt] = groupsOf(empty.groups, "receipt");
    expect(receipt.latest.title).toBe(
      "No events in the namespaces you can read",
    );
    const narrowedBody =
      receipt.latest.data.type === "receipt" ? receipt.latest.data.message : "";
    expect(narrowedBody).toContain("alpha, beta");
    // The load-bearing half: an empty answer from the namespaces a reader can
    // see must never read as an answer about the cluster.
    expect(narrowedBody).toContain("does not clear the cluster");
    expect(empty.limitations).toHaveLength(0);

    const found = project([
      tool(
        "events-partial",
        "get_events",
        {
          events: [
            {
              type: "Warning",
              reason: "BackOff",
              message: "back-off restarting",
              count: 3,
              lastTimestamp: "2026-09-02T09:00:00Z",
              involvedObject: { kind: "Pod", namespace: "alpha", name: "api" },
            },
          ],
          partialScope: true,
          scopeNamespaces: ["alpha", "beta"],
        },
        { summary: JSON.stringify({}) },
      ),
    ]);
    expect(
      found.limitations.map((limitation) => [
        limitation.source,
        limitation.kind,
      ]),
    ).toEqual([["Events", "unknown"]]);
    expect(found.limitations[0].message).toContain("alpha, beta");
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

describe("namespace listings", () => {
  it("renders a list_namespaces result as an inventory card of Namespaces", () => {
    const projection = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool(
              "namespaces",
              "list_namespaces",
              [
                {
                  name: "capi-system",
                  status: "Terminating",
                  labels: { a: "b" },
                },
                { name: "shop", status: "Active" },
              ],
              { summary: "{}" },
            ),
          ],
        },
      ],
      { kind: "Deployment", group: "apps", namespace: "shop", name: "api" },
    );
    const card = projection.groups.find((group) => group.kind === "inventory");
    expect(card?.latest.title).toBe("Namespaces");
    expect(card?.latest.summary).toBe("2 returned");
    expect(
      card?.latest.data.type === "inventory"
        ? card.latest.data.resources.map((r) => r.name)
        : [],
    ).toEqual(["capi-system", "shop"]);
  });
});
