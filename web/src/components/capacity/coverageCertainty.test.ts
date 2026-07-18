import { describe, expect, it } from "vitest";
import type { CapacitySourceCoverage } from "@skyhook-io/k8s-ui";
import { coverageCertainty } from "./CapacityOverview";

function cov(
  status: CapacitySourceCoverage["status"],
  scope: CapacitySourceCoverage["scope"] = "cluster",
): CapacitySourceCoverage {
  return { status, scope, impactFields: [] };
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
