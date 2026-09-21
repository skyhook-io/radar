import { describe, expect, it } from "vitest";
import { investigationEvidenceSubjectRef } from "../index";
import {
  criticalIssue,
  deployment,
  groupsOf,
  project,
  tool,
  warningEvent,
} from "../evidenceFixtures";

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
    expect(limitationText).toContain("part of the crash-cause candidates");
    expect(limitationText).toContain("received 1 of 3 event groups");
    expect(limitationText).toContain("rbac denied");
    expect(limitationText).toContain("referenced-by relationships");
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
      "Restarts in trailing 1h · Deployment shop/api",
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
    expect(restarts.latest.title).toContain("Restarts in trailing 1h");
    expect(restarts.latest.data).toMatchObject({
      type: "metrics",
      label: "Restarts in trailing 1h",
      unit: "count",
    });
    expect(cpu.latest.summary).toBe("2 current pods · 60m window · 1m2s step");
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
      "first 50 of 55 current pods · 60m window · 1m2s step · partial pod set",
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
      "Restarts in trailing 1h · Deployment shop/api",
    ]);
    expect(
      projection.limitations.some(
        (item) => item.source === "Workload metrics" && item.kind === "unknown",
      ),
    ).toBe(true);
  });
});
