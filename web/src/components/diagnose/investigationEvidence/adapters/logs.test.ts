import { describe, expect, it } from "vitest";
import {
  investigationEvidenceSubjectRef,
  projectInvestigationEvidence,
  resolveInvestigationRootCauseEvidence,
} from "../index";
import {
  evidenceRef,
  groupsOf,
  project,
  tool,
  workloadLogsArgs,
  workloadLogsPayload,
} from "../evidenceFixtures";

describe("pod logs container identity", () => {
  const payload = {
    lines: ["ERROR missing configuration"],
    totalLines: 1,
    matchedLines: 1,
    fallback: false,
  };

  it.each([
    ["resolved identity", { ...payload, container: "app" }, {}, "app"],
    [
      "producer identity wins",
      { ...payload, container: "app" },
      { container: "proxy" },
      "app",
    ],
    ["saved explicit request", payload, { container: "app" }, "app"],
    ["saved unspecified request", payload, {}, undefined],
  ])("uses %s", (_name, result, args, expected) => {
    const projection = project([
      tool("logs", "get_pod_logs", result, {
        summary: JSON.stringify({ namespace: "shop", name: "api", ...args }),
      }),
    ]);
    const observation = groupsOf(projection.groups, "logs")[0].latest;
    expect(observation.data).toMatchObject({
      type: "logs",
      container: expected,
    });
    expect(observation.title).toContain(expected ?? "container unknown");
  });

  it("does not merge separate unknown streams or current and previous instances", () => {
    const projection = project([
      tool("unknown-1", "get_pod_logs", payload),
      tool("unknown-2", "get_pod_logs", payload),
      tool("current", "get_pod_logs", { ...payload, container: "app" }),
      tool(
        "previous",
        "get_pod_logs",
        { ...payload, container: "app" },
        {
          summary: JSON.stringify({
            namespace: "shop",
            name: "api",
            previous: true,
          }),
        },
      ),
    ]);
    const groups = groupsOf(projection.groups, "logs");
    expect(groups).toHaveLength(4);
    expect(new Set(groups.map((group) => group.identity)).size).toBe(4);
    expect(groups.slice(2).map((group) => group.identity)).toEqual([
      "logs:current:api:app",
      "logs:previous:api:app",
    ]);
  });
});

// The get_workload_logs response shape, with the stream rows fetchPodLogs
// emits per pod and container.

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
    expect(malformed.limitations[2].message).toContain(
      "Radar found 3 pods but got no logs from them",
    );
  });
});
