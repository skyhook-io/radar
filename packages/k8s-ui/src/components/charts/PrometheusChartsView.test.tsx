import { describe, expect, it } from "vitest";

import { describePodCoverage } from "./PrometheusChartsView";
import type { PrometheusResourceMetricsResult } from "./PrometheusChartsView";

function result(
  patch: Partial<PrometheusResourceMetricsResult>,
): PrometheusResourceMetricsResult {
  return { unit: "cores", ...patch } as PrometheusResourceMetricsResult;
}

describe("describePodCoverage", () => {
  it("names the pods a chart covers and what it therefore misses", () => {
    expect(describePodCoverage(result({ pods: 3, podsTotal: 3 }))).toBe(
      "3 current pods; pods replaced during the window are not included",
    );
    expect(describePodCoverage(result({ pods: 1, podsTotal: 1 }))).toBe(
      "1 current pod; pods replaced during the window are not included",
    );
  });

  it("says when the cap cut the list, so a short count is not read as the workload", () => {
    expect(describePodCoverage(result({ pods: 50, podsTotal: 120 }))).toBe(
      "first 50 of 120 current pods; pods replaced during the window are not included",
    );
  });

  it("distinguishes a workload with no pods from a kind that has none to report", () => {
    expect(describePodCoverage(result({ pods: 0 }))).toBe(
      "No pods could be attributed to this workload",
    );
    // A Node chart carries no pod count at all; it gets no caption rather
    // than a claim about pods.
    expect(describePodCoverage(result({}))).toBeUndefined();
  });
});
