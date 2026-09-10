import { describe, expect, it } from "vitest";

import { describePodCoverage } from "./PrometheusChartsView";
import type { PrometheusResourceMetricsResult } from "../../types";

function result(
  patch: Partial<PrometheusResourceMetricsResult>,
): PrometheusResourceMetricsResult {
  return { series: [], ...patch } as PrometheusResourceMetricsResult;
}

describe("describePodCoverage", () => {
  it("names the basis a chart was drawn on", () => {
    expect(
      describePodCoverage(result({ coverage: "ksm_history", observedPods: 6 })),
    ).toBe(
      "6 pods attributed to this workload in the window (kube-state-metrics)",
    );
    expect(
      describePodCoverage(result({ coverage: "ksm_history", observedPods: 1 })),
    ).toBe(
      "1 pod attributed to this workload in the window (kube-state-metrics)",
    );
    expect(describePodCoverage(result({ coverage: "ksm_history" }))).toBe(
      "Pods attributed to this workload in the window (kube-state-metrics)",
    );
  });

  it("says what the current-pod fallback cannot see", () => {
    // The caveat is the point: a pod replaced inside the window is missing,
    // and without saying so an empty chart reads as "nothing happened".
    expect(
      describePodCoverage(
        result({ coverage: "current_pods", pods: 3, podsTotal: 3 }),
      ),
    ).toBe("3 current pods; pods replaced during the window are not included");
    expect(
      describePodCoverage(
        result({ coverage: "current_pods", pods: 50, podsTotal: 120 }),
      ),
    ).toBe(
      "first 50 of 120 current pods; pods replaced during the window are not included",
    );
  });

  it("does not call an unattributed workload empty", () => {
    expect(describePodCoverage(result({ coverage: "none" }))).toBe(
      "No pods could be attributed to this workload",
    );
  });

  it("carries the reason ownership history was unavailable", () => {
    expect(
      describePodCoverage(
        result({
          coverage: "current_pods",
          pods: 2,
          podsTotal: 2,
          scopeError: "probe timed out",
        }),
      ),
    ).toBe(
      "2 current pods; pods replaced during the window are not included Radar could not read this workload's ownership history (probe timed out).",
    );
    // A scope error with no coverage still has to reach the reader.
    expect(describePodCoverage(result({ scopeError: "probe timed out" }))).toBe(
      "Radar could not read this workload's ownership history (probe timed out).",
    );
  });

  it("says nothing when there is nothing to say", () => {
    expect(describePodCoverage(result({}))).toBeUndefined();
  });
});
