import { describe, expect, it } from "vitest";
import {
  evidenceDisplaySnapshot,
  groupEvidenceCoverage,
} from "./investigationEvidencePresentation";
import {
  evidenceSemanticSnapshot,
  type InvestigationEvidenceData,
  type InvestigationEvidenceLimitation,
} from "./investigationEvidence";

function observation(data: InvestigationEvidenceData) {
  return {
    data,
    title: "Evidence",
    tone: "warning" as const,
    summary: "Finding",
  };
}

const resource = {
  apiVersion: "apps/v1",
  kind: "Deployment",
  metadata: {
    name: "api",
    namespace: "dev",
    uid: "same-object",
    resourceVersion: "1",
  },
  spec: { replicas: 1 },
  status: {
    readyReplicas: 0,
    conditions: [
      {
        type: "Available",
        status: "False",
        reason: "MinimumReplicasUnavailable",
        lastTransitionTime: "2026-09-07T08:00:00Z",
      },
    ],
  },
};
const resourceData = (value = resource): InvestigationEvidenceData => ({
  type: "resource",
  resource: value,
  warnings: [],
});
const crash = {
  pods: ["api-123"],
  container: "api",
  state: "down",
  reason: "Error",
  exitCode: 1,
  logLine: "2026-09-07T08:00:00.000000000Z Authentication failed",
  logSource: "previous",
  logLineSelection: "fatal_pattern",
};
const event = {
  type: "Warning",
  reason: "BackOff",
  message: "Back-off restarting failed container",
  count: 1071,
  lastTimestamp: "2026-09-07T08:00:00Z",
};

describe("evidence display significance (not record identity)", () => {
  it("retains context-only status and issue changes", () => {
    const first = observation({
      type: "resource",
      resource,
      warnings: [],
      resourceContext: {
        tier: "basic",
        statusSummary: { phase: "Pending" },
        issueSummary: { count: 1, topReason: "MissingSecret" },
      },
    });
    for (const patch of [
      { statusSummary: { phase: "Running" } },
      { issueSummary: { count: 1, topReason: "ImagePullBackOff" } },
    ]) {
      if (first.data.type !== "resource") throw new Error("Expected resource");
      const next = observation({
        ...first.data,
        resourceContext: { ...first.data.resourceContext!, ...patch },
      });
      expect(evidenceDisplaySnapshot(first)).not.toBe(
        evidenceDisplaySnapshot(next),
      );
    }
  });
  it("ignores elapsed days in condition warnings", () => {
    const snapshot = (duration: string, age: string) =>
      evidenceDisplaySnapshot(
        observation({
          type: "resource",
          resource,
          warnings: [
            `Condition ` +
              "`Available=False`" +
              ` for ~${duration} (resource age: ${age}).`,
          ],
        }),
      );
    expect(snapshot("1d", "45d")).toBe(snapshot("2d", "46d"));
  });
  it("retains context-only readiness and GitOps changes when the raw object is unchanged", () => {
    const first = observation({
      type: "resource",
      resource,
      warnings: [],
      resourceContext: {
        tier: "basic",
        workloadSummary: { replicas: { desired: 1, ready: 0 } },
      },
    });
    const next = observation({
      type: "resource",
      resource,
      warnings: [],
      resourceContext: {
        tier: "basic",
        workloadSummary: { replicas: { desired: 1, ready: 1 } },
      },
    });
    expect(evidenceDisplaySnapshot(first)).not.toBe(
      evidenceDisplaySnapshot(next),
    );
    const synced = observation({
      type: "resource",
      resource,
      warnings: [],
      gitOpsDiagnosis: { tool: "argocd", sync: "Synced" },
    });
    const outOfSync = observation({
      type: "resource",
      resource,
      warnings: [],
      gitOpsDiagnosis: { tool: "argocd", sync: "OutOfSync" },
    });
    expect(evidenceDisplaySnapshot(synced)).not.toBe(
      evidenceDisplaySnapshot(outOfSync),
    );
    const healthy = observation({
      type: "resource",
      resource: { ...resource, summaryContext: { health: "Healthy" } },
      warnings: [],
    });
    const degraded = observation({
      type: "resource",
      resource: { ...resource, summaryContext: { health: "Degraded" } },
      warnings: [],
    });
    expect(evidenceDisplaySnapshot(healthy)).not.toBe(
      evidenceDisplaySnapshot(degraded),
    );
  });
  it("ignores only elapsed time in the producer's condition warning", () => {
    const first = observation({
      type: "resource",
      resource,
      warnings: ["Condition `Available=False` for ~1m6s (resource age: 46d)."],
    });
    const later = observation({
      type: "resource",
      resource,
      warnings: ["Condition `Available=False` for ~1m7s (resource age: 46d)."],
    });
    const different = observation({
      type: "resource",
      resource,
      warnings: ["A required Secret is missing."],
    });
    expect(evidenceDisplaySnapshot(first)).toBe(evidenceDisplaySnapshot(later));
    expect(evidenceDisplaySnapshot(first)).not.toBe(
      evidenceDisplaySnapshot(different),
    );
  });
  it("tolerates unstructured conditions without deleting their contents", () => {
    const value = {
      ...resource,
      status: { conditions: [null, "unrecognized", { status: "False" }] },
    };
    const snapshot = evidenceDisplaySnapshot(
      observation({ type: "resource", resource: value, warnings: [] }),
    );
    expect(snapshot).toContain('null,"unrecognized"');
  });
  it("ignores bookkeeping and elapsed summaries without changing exact snapshots", () => {
    const first = observation(resourceData());
    const next = observation(
      resourceData({
        ...resource,
        metadata: { ...resource.metadata, resourceVersion: "2" },
      }),
    );
    next.summary = "Unavailable for 1m7s instead of 1m6s";
    expect(evidenceDisplaySnapshot(first)).toBe(evidenceDisplaySnapshot(next));
    expect(evidenceSemanticSnapshot(first)).not.toBe(
      evidenceSemanticSnapshot(next),
    );
    expect(resource.metadata.resourceVersion).toBe("1");
  });
  it("ignores condition collection times, not condition state", () => {
    const first = observation(resourceData());
    const nextResource = {
      ...resource,
      status: {
        ...resource.status,
        conditions: [
          {
            ...resource.status.conditions[0],
            lastTransitionTime: "2026-09-07T08:01:00Z",
            lastUpdateTime: "2026-09-07T08:01:00Z",
          },
        ],
      },
    };
    expect(evidenceDisplaySnapshot(first)).toBe(
      evidenceDisplaySnapshot(observation(resourceData(nextResource))),
    );
    nextResource.status.conditions[0].status = "True";
    expect(evidenceDisplaySnapshot(first)).not.toBe(
      evidenceDisplaySnapshot(observation(resourceData(nextResource))),
    );
  });
  it("retains readiness, configuration, object identity and failure-detail changes", () => {
    for (const next of [
      { ...resource, status: { ...resource.status, readyReplicas: 1 } },
      { ...resource, spec: { replicas: 2 } },
      { ...resource, metadata: { ...resource.metadata, uid: "replacement" } },
      {
        ...resource,
        status: {
          ...resource.status,
          conditions: [
            { ...resource.status.conditions[0], reason: "DifferentFailure" },
          ],
        },
      },
    ])
      expect(evidenceDisplaySnapshot(observation(resourceData()))).not.toBe(
        evidenceDisplaySnapshot(observation(resourceData(next))),
      );
  });
  it("does not strip timestamp-shaped configuration keys", () => {
    const first = observation({
      type: "resource",
      warnings: [],
      resource: {
        ...resource,
        kind: "ConfigMap",
        data: { lastTransitionTime: "before" },
      },
    });
    const next = observation({
      type: "resource",
      warnings: [],
      resource: {
        ...resource,
        kind: "ConfigMap",
        data: { lastTransitionTime: "after" },
      },
    });
    expect(evidenceDisplaySnapshot(first)).not.toBe(
      evidenceDisplaySnapshot(next),
    );
  });
  it("folds recurring crash text across timestamps/instances but preserves raw provenance", () => {
    const first = observation({ type: "crash", namespace: "dev", crash });
    const next = observation({
      type: "crash",
      namespace: "dev",
      crash: {
        ...crash,
        logLine: "2026-09-07T08:06:00.000000000Z Authentication failed",
        logSource: "current",
      },
    });
    expect(evidenceDisplaySnapshot(first)).toBe(evidenceDisplaySnapshot(next));
    expect(evidenceSemanticSnapshot(first)).not.toBe(
      evidenceSemanticSnapshot(next),
    );
    expect(crash.logSource).toBe("previous");
  });
  it("retains a new crash error, exit code, or affected pod", () => {
    const first = observation({ type: "crash", namespace: "dev", crash });
    for (const patch of [
      { logLine: "Connection refused" },
      { exitCode: 137 },
      { pods: ["api-456"] },
    ]) {
      expect(evidenceDisplaySnapshot(first)).not.toBe(
        evidenceDisplaySnapshot(
          observation({
            type: "crash",
            namespace: "dev",
            crash: { ...crash, ...patch },
          }),
        ),
      );
    }
  });
  it("folds event repetition counts, timestamps and ordering", () => {
    const second = { ...event, reason: "Pulling", message: "Pulling image" };
    const first = observation({
      type: "events",
      scope: "dev/api",
      events: [event, second],
    });
    const next = observation({
      type: "events",
      scope: "dev/api",
      events: [
        second,
        { ...event, count: 1074, lastTimestamp: "2026-09-07T08:06:00Z" },
      ],
    });
    expect(evidenceDisplaySnapshot(first)).toBe(evidenceDisplaySnapshot(next));
    expect(evidenceSemanticSnapshot(first)).not.toBe(
      evidenceSemanticSnapshot(next),
    );
  });
  it("retains new event reasons, details, scope and severity", () => {
    const first = observation({
      type: "events",
      scope: "dev/api",
      events: [event],
    });
    for (const patch of [
      { reason: "FailedMount" },
      { message: "New failure detail" },
      { type: "Normal" },
    ]) {
      expect(evidenceDisplaySnapshot(first)).not.toBe(
        evidenceDisplaySnapshot(
          observation({
            type: "events",
            scope: "dev/api",
            events: [{ ...event, ...patch }],
          }),
        ),
      );
    }
    expect(evidenceDisplaySnapshot(first)).not.toBe(
      evidenceDisplaySnapshot(
        observation({ type: "events", scope: "prod/api", events: [event] }),
      ),
    );
  });
});

function limitation(
  source: string,
  message: string,
  kind: InvestigationEvidenceLimitation["kind"],
  history = false,
): InvestigationEvidenceLimitation {
  return {
    source,
    message,
    kind,
    ...(history ? { presentation: "history" as const } : {}),
    firstOrder: 0,
    sources: [],
  };
}
describe("coverage presentation", () => {
  it.each(["Recent changes", "Container logs", "Issue change correlation"])(
    "does not disguise unsummarizable %s as ordinary sampling",
    (label) => {
      const message =
        "Radar couldn't summarize this investigation step. Review it in Activity.";
      for (const notes of [
        [limitation(label, message, "unknown")],
        [
          limitation(label, "Result limit reached", "truncated"),
          limitation(label, message, "unknown"),
        ],
      ])
        expect(groupEvidenceCoverage(notes)[0].summary).toContain(message);
    },
  );
  it("turns the six overlapping notes into three impact groups, preserving every detail", () => {
    const notes = [
      limitation("Recent changes", "Result limit reached", "truncated"),
      limitation("Recent changes", "Query narrowed", "truncated"),
      limitation(
        "Recent changes",
        "Secret namespace history incomplete",
        "unknown",
        true,
      ),
      limitation(
        "Issue change correlation",
        "Not evaluated for every issue",
        "truncated",
      ),
      limitation(
        "Recent changes",
        "Secret object history incomplete",
        "unknown",
        true,
      ),
      limitation("Container logs", "Log query narrowed", "truncated"),
    ];
    const groups = groupEvidenceCoverage(notes);
    expect(groups.map((g) => [g.label, g.summary])).toEqual([
      [
        "Recent changes",
        "Some changes may be missing; change history is incomplete.",
      ],
      [
        "Issue change correlation",
        "Not all issues were checked against recent changes.",
      ],
      ["Container logs", "Only part of the logs was checked."],
    ]);
    expect(groups.flatMap((g) => g.limitations)).toHaveLength(6);
    expect(groups[0].limitations[0]).toBe(notes[0]);
  });
  it("keeps failed reads explicit and orders them ahead of ordinary limits", () => {
    const groups = groupEvidenceCoverage([
      limitation("Recent changes", "Query narrowed", "truncated"),
      limitation("Container logs", "Logs sampled", "truncated"),
      limitation("Container logs", "Forbidden: cannot read pod logs", "error"),
      limitation("Container logs", "Connection timed out", "error"),
    ]);
    expect(groups[0].hasError).toBe(true);
    expect(groups[0].summary).toContain("Forbidden");
    expect(groups[0].summary).toContain("Connection timed out");
    expect(groups[0].limitations).toHaveLength(3);
  });
  it("keeps history-only neutral and unknown limitations verbatim", () => {
    expect(
      groupEvidenceCoverage([
        limitation("Recent changes", "No durable history", "unknown", true),
      ])[0].historyOnly,
    ).toBe(true);
    expect(
      groupEvidenceCoverage([
        limitation("New producer", "No reliable coverage", "unknown"),
      ])[0].summary,
    ).toBe("No reliable coverage");
    expect(groupEvidenceCoverage([])).toEqual([]);
  });
});
