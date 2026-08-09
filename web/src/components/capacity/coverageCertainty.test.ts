import { describe, expect, it } from "vitest";
import type { CapacitySourceCoverage } from "@skyhook-io/k8s-ui";
import {
  autoscalerDetectionFailure,
  autoscalerNotPublishedDetail,
  coverageCertainty,
  nodeGroupsCertainty,
} from "./CapacityOverview";

function cov(
  status: CapacitySourceCoverage["status"],
  scope: CapacitySourceCoverage["scope"] = "cluster",
): CapacitySourceCoverage {
  return { status, scope, impactFields: [] };
}

function reasoned(
  status: CapacitySourceCoverage["status"],
  reasonCode: string,
): CapacitySourceCoverage {
  return { status, reasonCode, scope: "cluster", impactFields: [] };
}

describe("coverageCertainty", () => {
  it("is exact only for an observed, cluster-scoped source", () => {
    expect(coverageCertainty(cov("available"))).toBe("exact");
  });

  it("is a lower bound when observed but namespace-scoped or partial", () => {
    expect(coverageCertainty(cov("available", "explicit_namespaces"))).toBe(
      "lower_bound",
    );
    expect(coverageCertainty(cov("partial"))).toBe("lower_bound");
  });

  it("is unknown — never a false exact — when the source was not observed", () => {
    // The trust thesis: syncing / unavailable / error / absent must not read
    // as "=" (exact) on the KPI glyphs.
    for (const status of [
      "denied",
      "syncing",
      "unavailable",
      "error",
    ] as const) {
      expect(coverageCertainty(cov(status))).toBe("unknown");
    }
    expect(coverageCertainty(undefined)).toBe("unknown");
  });
});

describe("nodeGroupsCertainty", () => {
  it("is exact only when nodes and NodePools were both observed", () => {
    expect(
      nodeGroupsCertainty(
        { nodes: cov("available"), nodePools: cov("available") },
        "available",
      ).certainty,
    ).toBe("exact");
  });

  it("is a lower bound when NodePools are hidden — zero-node groups vanish", () => {
    for (const status of [
      "denied",
      "unavailable",
      "error",
      "syncing",
    ] as const) {
      const result = nodeGroupsCertainty(
        { nodes: cov("available"), nodePools: cov(status) },
        "denied",
      );
      expect(result.certainty).toBe("lower_bound");
      expect(result.title).toContain("lower bound");
    }
  });

  it("stays unknown when nodes themselves were not observed", () => {
    expect(
      nodeGroupsCertainty(
        { nodes: cov("denied"), nodePools: cov("denied") },
        "denied",
      ).certainty,
    ).toBe("unknown");
  });

  it("does not hedge a cluster that simply has no NodePool source", () => {
    expect(
      nodeGroupsCertainty({ nodes: cov("available") }, "not_detected")
        .certainty,
    ).toBe("exact");
  });

  it("does not hedge when Karpenter is absent — nothing is hidden", () => {
    // The server reports unavailable NodePool coverage on a Karpenter-less
    // cluster, but there are no NodePools to conceal scale-to-zero groups.
    const result = nodeGroupsCertainty(
      { nodes: cov("available"), nodePools: cov("unavailable") },
      "not_detected",
    );
    expect(result.certainty).toBe("exact");
    expect(result.title).not.toContain("lower bound");
  });
});

describe("autoscalerDetectionFailure", () => {
  it("reports every unreadable source as a detection failure", () => {
    expect(autoscalerDetectionFailure(cov("denied"))).toContain("permissions");
    for (const reasonCode of [
      "autoscaler_status_cache_scope",
      "autoscaler_status_cache_unavailable",
      "autoscaler_status_read_failed",
      "autoscaler_status_parse_failed",
    ]) {
      expect(
        autoscalerDetectionFailure(reasoned("unavailable", reasonCode)),
      ).toBeTruthy();
    }
    expect(autoscalerDetectionFailure(cov("error"))).toBeTruthy();
  });

  it("treats a published-nothing reading as an observation, not a failure", () => {
    // This is the ONLY reading that may render as "None detected".
    expect(
      autoscalerDetectionFailure(
        reasoned("unavailable", "autoscaler_status_not_published"),
      ),
    ).toBeNull();
    expect(autoscalerDetectionFailure(cov("available"))).toBeNull();
    expect(autoscalerDetectionFailure(undefined)).toBeNull();
  });
});

describe("autoscalerNotPublishedDetail", () => {
  it("scopes the claim to the namespace the server probed", () => {
    // Unscoped, "None detected" reads as "this cluster has no autoscaler" when
    // it only means "no status ConfigMap in the one place it can live".
    expect(
      autoscalerNotPublishedDetail({
        ...reasoned("unavailable", "autoscaler_status_not_published"),
        namespaces: ["kube-system"],
      }),
    ).toBe("No cluster-autoscaler-status ConfigMap in kube-system.");
  });

  it("names kube-system when the server reported no namespace list", () => {
    expect(
      autoscalerNotPublishedDetail(
        reasoned("unavailable", "autoscaler_status_not_published"),
      ),
    ).toContain("kube-system");
  });

  it("says nothing for any other reading", () => {
    expect(
      autoscalerNotPublishedDetail(
        reasoned("unavailable", "autoscaler_status_read_failed"),
      ),
    ).toBeNull();
    expect(autoscalerNotPublishedDetail(cov("available"))).toBeNull();
    expect(autoscalerNotPublishedDetail(undefined)).toBeNull();
  });
});
