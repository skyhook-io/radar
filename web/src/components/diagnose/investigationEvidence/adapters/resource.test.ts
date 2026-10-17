import { describe, expect, it } from "vitest";
import { groupsOf, project, tool } from "../evidenceFixtures";

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

  it("states a workload's readiness from the resource when the read carried no context", () => {
    const result = project([
      tool(
        "res",
        "get_resource",
        {
          resource: {
            apiVersion: "apps/v1",
            kind: "Deployment",
            metadata: { namespace: "shop", name: "api" },
            spec: { replicas: 1 },
            status: { replicas: 1, readyReplicas: 0 },
          },
        },
        {
          summary: JSON.stringify({
            kind: "deployment",
            namespace: "shop",
            name: "api",
          }),
        },
      ),
    ]);
    const card = groupsOf(result.groups, "resource")[0].latest;
    expect(card.summary).toBe("0/1 replicas ready");
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

describe("resource cards with Pod/template differences", () => {
  it("shows the first difference as a card warning, not a finding", () => {
    const result = project([
      tool("workload", "diagnose", {
        resource: {
          apiVersion: "apps/v1",
          kind: "Deployment",
          metadata: { namespace: "shop", name: "api" },
        },
        resourceContext: {
          tier: "diagnostic",
          templateDivergence: {
            summary: "1 of 3 current Pods differ.",
            podsCompared: 3,
            podsDiffering: 1,
            examplePod: "api-2",
            baseline: "ReplicaSet api-1",
            differences: [
              "containers[api].resources.limits.memory is 16Mi; template specifies 256Mi",
              "containers[api].resources.requests.memory is 16Mi; template specifies 128Mi",
            ],
          },
        },
      }),
    ]);
    const card = groupsOf(result.groups, "resource")[0].latest;
    expect(card.tone).toBe("neutral");
    if (card.data.type === "resource") {
      expect(card.data.warnings).toEqual([
        "1 of 3 Pods differ from their template: containers[api].resources.limits.memory is 16Mi; template specifies 256Mi (and more)",
      ]);
    }
    expect(groupsOf(result.groups, "issue")).toHaveLength(0);
  });
});
