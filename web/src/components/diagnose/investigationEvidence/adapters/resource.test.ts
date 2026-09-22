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
