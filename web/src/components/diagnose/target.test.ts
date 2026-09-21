import { describe, expect, it } from "vitest";

import { formatInvestigationTarget, runTargetKey } from "./target";

describe("investigation target identity", () => {
  it("uses one running key for singular Kinds and plural resource names", () => {
    expect(runTargetKey("Deployment", "prod", "api", "apps")).toBe(
      runTargetKey("deployments", "prod", "api", "apps"),
    );
  });

  it("keeps same-named resources in different API groups distinct", () => {
    expect(runTargetKey("Service", "prod", "api", "")).not.toBe(
      runTargetKey("services", "prod", "api", "serving.knative.dev"),
    );
  });

  it("formats a non-core target with its Kubernetes-qualified Kind", () => {
    expect(
      formatInvestigationTarget({
        kind: "Rollout",
        group: "argoproj.io",
        namespace: "prod",
        name: "checkout",
      }),
    ).toBe("Rollout.argoproj.io prod/checkout");
  });

  it("formats saved plural targets as Kubernetes Kinds", () => {
    expect(
      formatInvestigationTarget({
        kind: "deployments",
        group: "apps",
        namespace: "prod",
        name: "api",
      }),
    ).toBe("Deployment.apps prod/api");
    expect(
      formatInvestigationTarget({
        kind: "pods",
        group: "metrics.k8s.io",
        namespace: "prod",
        name: "api",
      }),
    ).toBe("PodMetrics.metrics.k8s.io prod/api");
  });

  it("keeps the core target label compact", () => {
    expect(
      formatInvestigationTarget({
        kind: "Service",
        group: "",
        namespace: "prod",
        name: "api",
      }),
    ).toBe("Service prod/api");
  });
});
